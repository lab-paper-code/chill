package kube

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	occupancyKind = "dispatch-occupancy-measurement"
	// DispatchOccupancyPrefix holds the dispatch occupancy records.
	DispatchOccupancyPrefix = "serving/dispatch-occupancy/"
	DispatchOccupancySchema = "chill.dacs.io/dispatch-occupancy-measurement.v1alpha1"
	// loadNodeLabel names the measured Node UID on a load Job.
	loadNodeLabel = "chill.dacs.io/measurement-load"

	refusedBatchUnmeasured    = "BatchUnmeasured"
	refusedWorkloadMissing    = "WorkloadMissing"
	refusedWorkloadAutonomous = "WorkloadAutonomous"
	refusedWorkloadServing    = "WorkloadServing"
	occupancyTooFewBatches    = "TooFewBatches"
	occupancyNotSaturated     = "NotSaturated"
	occupancyLoadFailed       = "LoadFailed"
)

// LoadGeneratorConfig runs the load Job of a dispatch-occupancy measurement
// on the ChillServer Node against the serving Service.
type LoadGeneratorConfig struct {
	Image, ServiceAccount, Namespace string
	// Endpoint is the HTTPS origin of the serving Service by its .svc name.
	Endpoint string
	// CASecret holds the serving certificate; the Job mounts its tls.crt only.
	CASecret string
	// NodeName is the ChillServer Node, outside every measured Node.
	NodeName string
}

// DispatchOccupancyRecord is the immutable record of one occupancy measurement.
type DispatchOccupancyRecord struct {
	SchemaVersion    string                `json:"schemaVersion"`
	Node             serving.NodeReference `json:"node"`
	PowerMode        string                `json:"powerMode"`
	RuntimeImage     string                `json:"runtimeImage"`
	Model            string                `json:"model"`
	ProfileID        string                `json:"profileID"`
	Batch            int                   `json:"batch"`
	ProfileMeanNanos float64               `json:"profileMeanNanos"`
	OfferedRate      float64               `json:"offeredRate"`
	Window           struct {
		Start time.Time `json:"start"`
		End   time.Time `json:"end"`
	} `json:"window"`
	Batches         int     `json:"batches"`
	Throughput      float64 `json:"throughput"`
	PerRequestNanos int64   `json:"perRequestNanos"`
	IdleFraction    float64 `json:"idleFraction"`
	LoadJob         struct {
		Name  string `json:"name"`
		Image string `json:"image"`
	} `json:"loadJob"`
	Isolation  *MeasurementIsolation `json:"isolation,omitempty"`
	Verdict    string                `json:"verdict"`
	Reason     string                `json:"reason,omitempty"`
	MeasuredAt time.Time             `json:"measuredAt"`
}

// measureOccupancy fills the window of record from the final rows of one
// fixed-batch instance: X from the batch ID span over the start span of the
// window batches, d = 1/X - profile mean/B, and the fraction of the window
// the instance waited for its next request after a batch ended.
func measureOccupancy(record *DispatchOccupancyRecord, rows []serving.RequestRow, warmup, duration time.Duration) {
	type batchRows struct {
		start, executed, queued time.Time
	}
	batches := map[uint64]*batchRows{}
	for _, r := range rows {
		if r.Outcome != "completed" || r.RunningNanos == nil {
			continue
		}
		start := r.ReceivedAt.Add(time.Duration(*r.RunningNanos))
		row := batches[r.BatchID]
		if row == nil {
			row = &batchRows{start: start}
			batches[r.BatchID] = row
		}
		if start.Before(row.start) {
			row.start = start
		}
		if r.ExecutedNanos != nil {
			if at := r.ReceivedAt.Add(time.Duration(*r.ExecutedNanos)); at.After(row.executed) {
				row.executed = at
			}
		}
		if r.QueuedNanos != nil {
			if at := r.ReceivedAt.Add(time.Duration(*r.QueuedNanos)); row.queued.IsZero() || at.Before(row.queued) {
				row.queued = at
			}
		}
	}
	ids := make([]uint64, 0, len(batches))
	var first time.Time
	for id, row := range batches {
		ids = append(ids, id)
		if first.IsZero() || row.start.Before(first) {
			first = row.start
		}
	}
	slices.Sort(ids)
	w := &record.Window
	w.Start = first.Add(warmup).UTC()
	w.End = w.Start.Add(duration)
	var inWindow []uint64
	for _, id := range ids {
		if start := batches[id].start; !start.Before(w.Start) && !start.After(w.End) {
			inWindow = append(inWindow, id)
		}
	}
	record.Batches = len(inWindow)
	if record.Batches < 2 {
		return
	}
	low, high := inWindow[0], inWindow[len(inWindow)-1]
	if span := batches[high].start.Sub(batches[low].start).Seconds(); span > 0 {
		record.Throughput = float64(high-low) * float64(record.Batch) / span
		record.PerRequestNanos = int64(math.Round(1e9/record.Throughput - record.ProfileMeanNanos/float64(record.Batch)))
	}
	var idle time.Duration
	for _, id := range inWindow {
		this, next := batches[id], batches[id+1]
		if next == nil || this.executed.IsZero() || next.queued.IsZero() {
			continue
		}
		// The instance waited from its batch's end until the next request arrived.
		if arrived := minTime(next.queued, w.End); arrived.After(this.executed) {
			idle += arrived.Sub(this.executed)
		}
	}
	record.IdleFraction = idle.Seconds() / duration.Seconds()
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// occupancyPlan is what admission resolved for a granted occupancy measurement.
type occupancyPlan struct {
	profile  *serving.Profile
	workload *edge.Workload
	dataset  *edge.Dataset
	fields   []string
	// powered is set when the Node already reads back the profile's mode.
	powered bool
	target  edge.NodeActuationSpec
}

// occupancyRefusal judges a dispatch-occupancy measurement before its grant.
func (c *SchedulerController) occupancyRefusal(ctx context.Context, m *edge.DispatchOccupancyMeasurement, node *core.Node, in admissionInputs) (plan occupancyPlan, reason, detail string, wait bool) {
	if m.Namespace != c.observer.config.ActuationNamespace {
		return plan, refusedActuationNamespace, m.Namespace, false
	}
	if plan.profile, reason, detail, wait = measurementProfile(m.Namespace, m.Spec.BatchEnergyOptimization, m.Spec.Node, in); reason != "" || wait {
		return plan, reason, detail, wait
	}
	if _, ok := plan.profile.MeanNanos(int(m.Spec.Batch)); !ok {
		return plan, refusedBatchUnmeasured, strconv.Itoa(int(m.Spec.Batch)), false
	}
	var workload edge.Workload
	if err := c.store.client.Get(ctx, client.ObjectKey{Name: m.Spec.Workload}, &workload); err != nil || workload.Spec.DatasetRef == nil {
		return plan, refusedWorkloadMissing, m.Spec.Workload, false
	}
	plan.workload = &workload
	if slices.Contains(in.config.Settings.AutonomousWorkloads, string(workload.UID)) {
		return plan, refusedWorkloadAutonomous, m.Spec.Workload, false
	}
	// The Pods of this cycle: Admit runs before Tick refreshes the snapshot.
	if pod := workloadPod(in.pods, string(workload.UID)); pod != "" {
		return plan, refusedWorkloadServing, pod, false
	}
	var dataset edge.Dataset
	var preprocessor edge.Preprocessor
	if err := c.store.client.Get(ctx, client.ObjectKey{Name: workload.Spec.DatasetRef.Name}, &dataset); err != nil {
		return plan, refusedWorkloadMissing, workload.Spec.DatasetRef.Name, false
	}
	if err := c.store.client.Get(ctx, client.ObjectKey{Name: workload.Spec.PreprocessorRef.Name}, &preprocessor); err != nil {
		return plan, refusedWorkloadMissing, workload.Spec.PreprocessorRef.Name, false
	}
	plan.dataset = &dataset
	for _, f := range preprocessor.Spec.Input.Fields {
		plan.fields = append(plan.fields, f.Name)
	}
	mode := plan.profile.Condition.PowerMode
	plan.powered = readBackMode(node, nodeClass(node, in.classes), in.acts) == mode
	target, ok := nodePowerTargets(node, in.classes, in.config.PowerPolicies)[mode]
	if !plan.powered && !ok {
		return plan, refusedPowerModeUnknown, mode, false
	}
	plan.target = target
	return plan, "", "", false
}

// workloadPod names a non-terminal serving Pod that the Workload owns or routes to.
func workloadPod(pods []core.Pod, uid string) string {
	for i := range pods {
		p := &pods[i]
		if p.Status.Phase == core.PodSucceeded || p.Status.Phase == core.PodFailed {
			continue
		}
		var routing map[string]int32
		_ = json.Unmarshal([]byte(p.Annotations[ServingRoutingAnnotation]), &routing)
		if workloadOwner(p.OwnerReferences) == uid || routing[uid] > 0 {
			return p.Namespace + "/" + p.Name
		}
	}
	return ""
}

// startOccupancy registers the run of a granted occupancy measurement.
func (c *SchedulerController) startOccupancy(node *core.Node, m *edge.DispatchOccupancyMeasurement, plan occupancyPlan, config SchedulerControllerConfig) {
	ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	root := &measurementRoot{object: m, node: node.Name}
	operation := Operation{ID: occupancyKind + ":" + node.Name + ":" + string(m.UID), Kind: occupancyKind, Node: ref}
	c.startNodeRun(operation, config, func(ctx context.Context, result *OperationResult) error {
		err := c.runOccupancy(ctx, m, ref, plan, config)
		return errors.Join(err, c.handBack(ctx, ref, string(m.UID), root, false, result))
	})
}

// runOccupancy saturates one fixed-batch instance with the load Job and
// records the dispatch occupancy of its window.
func (c *SchedulerController) runOccupancy(ctx context.Context, m *edge.DispatchOccupancyMeasurement, ref serving.NodeReference, plan occupancyPlan, config SchedulerControllerConfig) error {
	profile, b := plan.profile, int(m.Spec.Batch)
	if err := c.patchMeasurementStatus(ctx, m, func() {
		m.Status.Phase, m.Status.PowerMode = edge.MeasurementPhaseMeasuring, profile.Condition.PowerMode
	}); err != nil {
		return err
	}
	fail := func(err error) error { return errors.Join(err, c.failMeasurement(ctx, m, firstLine(err.Error()))) }
	if !plan.powered {
		act := powerRequest(plan.target, time.Now())
		act.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "DispatchOccupancyMeasurement", Name: m.Name, UID: m.UID}}
		if err := c.measurement.ChangePower(ctx, ref, act); err != nil {
			return fail(err)
		}
	}
	since := time.Now()
	spec := config.Preparations[profile.ID]
	spec.Owner = &serving.WorkloadReference{Name: plan.workload.Name, UID: string(plan.workload.UID)}
	spec.Config.Policy.FixedBatchSize, spec.Instance.FixedBatchSize = b, b
	if err := c.measurement.PrepareInstance(ctx, spec); err != nil {
		return fail(err)
	}
	mean, _ := profile.MeanNanos(b)
	factor, err := strconv.ParseFloat(m.Spec.RateFactor, 64)
	if err != nil {
		return fail(err)
	}
	rate := factor * float64(b) / (mean / 1e9)
	job := loadJob(m, ref, plan, rate, config.LoadGenerator)
	started := time.Now()
	if _, err = c.kube.BatchV1().Jobs(job.Namespace).Create(ctx, job, meta.CreateOptions{}); err != nil {
		return fail(err)
	}
	var state string
	err = c.operations.wait(ctx, func() (bool, error) {
		current, err := c.kube.BatchV1().Jobs(job.Namespace).Get(ctx, job.Name, meta.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, condition := range current.Status.Conditions {
			if (condition.Type == batch.JobComplete || condition.Type == batch.JobFailed) && condition.Status == core.ConditionTrue {
				state = string(condition.Type) + " " + condition.Reason
			}
		}
		return state != "", nil
	})
	if err = errors.Join(err, c.deleteLoadJobs(ctx, ref, config.LoadGenerator)); err != nil {
		return fail(err)
	}
	if !strings.HasPrefix(state, string(batch.JobComplete)) {
		return fail(errors.New(occupancyLoadFailed + " " + strings.TrimSpace(state)))
	}
	observed, err := c.observationLog.Query(string(plan.workload.UID), started, time.Now(), nil, nil, 0, "")
	if err != nil {
		return fail(err)
	}
	rows := slices.DeleteFunc(observed.Requests, func(r serving.RequestRow) bool { return r.Node == nil || r.Node.UID != ref.UID })
	record := DispatchOccupancyRecord{SchemaVersion: DispatchOccupancySchema, Node: ref, PowerMode: profile.Condition.PowerMode,
		RuntimeImage: profile.Condition.RuntimeImage, Model: profile.Condition.LogicalModel, ProfileID: profile.ID, Batch: b,
		ProfileMeanNanos: mean, OfferedRate: rate, Verdict: measurementAccepted, MeasuredAt: time.Now().UTC()}
	measureOccupancy(&record, rows, time.Duration(m.Spec.WarmupSeconds)*time.Second, time.Duration(m.Spec.DurationSeconds)*time.Second)
	record.LoadJob.Name, record.LoadJob.Image = job.Name, config.LoadGenerator.Image
	maxIdle, _ := strconv.ParseFloat(m.Spec.MaximumIdleFraction, 64)
	witness, witnessErr := c.measurement.measurementIsolation(ctx, ref, map[string]bool{profile.Condition.LogicalModel: true}, since, "")
	record.Isolation = &witness
	switch {
	case witnessErr != nil:
		witness.Error = witnessErr.Error()
		record.Verdict, record.Reason = measurementInsufficient, powerChangedReason
	case record.Batches < int(m.Spec.MinimumBatches) || record.Throughput == 0:
		record.Verdict, record.Reason = measurementInsufficient, fmt.Sprintf("%s %d", occupancyTooFewBatches, record.Batches)
	case record.IdleFraction > maxIdle:
		record.Verdict, record.Reason = measurementInsufficient, fmt.Sprintf("%s %.4f", occupancyNotSaturated, record.IdleFraction)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return fail(err)
	}
	identity := evidencestore.ObjectIdentity{Store: config.EvidenceStore, Key: DispatchOccupancyPrefix + ref.UID + "/" + string(m.UID) + ".json", Size: int64(len(raw))}
	if _, err = config.Evidence.PutImmutable(ctx, identity, bytes.NewReader(raw)); err != nil {
		return fail(err)
	}
	return c.patchMeasurementStatus(ctx, m, func() {
		m.Status.Phase, m.Status.Reason = edge.MeasurementPhaseComplete, record.Reason
		if record.Verdict != measurementAccepted {
			m.Status.Phase = edge.MeasurementPhaseInsufficient
		}
		m.Status.PerRequestNanos = ptr.To(record.PerRequestNanos)
		m.Status.Throughput = strconv.FormatFloat(record.Throughput, 'f', 4, 64)
		m.Status.Result = &edge.ProfilingRunEvidenceReference{Store: identity.Store, Key: identity.Key, SchemaVersion: DispatchOccupancySchema, Size: identity.Size}
	})
}

// loadJob is the load generator Job of one occupancy measurement.
func loadJob(m *edge.DispatchOccupancyMeasurement, ref serving.NodeReference, plan occupancyPlan, rate float64, config LoadGeneratorConfig) *batch.Job {
	sum := sha256.Sum256([]byte(m.UID))
	seconds := m.Spec.WarmupSeconds + m.Spec.DurationSeconds
	data := core.VolumeMount{Name: "data", MountPath: "/data"}
	return &batch.Job{
		ObjectMeta: meta.ObjectMeta{Name: "occupancy-" + hex.EncodeToString(sum[:10]), Namespace: config.Namespace, Labels: map[string]string{loadNodeLabel: ref.UID}},
		Spec: batch.JobSpec{
			BackoffLimit: ptr.To[int32](0), ActiveDeadlineSeconds: ptr.To(int64(seconds) + 120),
			Template: core.PodTemplateSpec{
				ObjectMeta: meta.ObjectMeta{Labels: map[string]string{loadNodeLabel: ref.UID}},
				Spec: core.PodSpec{
					RestartPolicy: core.RestartPolicyNever, NodeName: config.NodeName, ServiceAccountName: config.ServiceAccount,
					InitContainers: []core.Container{{Name: "dataset", Image: plan.dataset.Spec.Source.Image, VolumeMounts: []core.VolumeMount{data},
						Command: []string{"sh", "-ec", `cp -R "$1"/. /data/`, "--", path.Join(plan.dataset.Spec.Source.Path, "evaluation")}}},
					Containers: []core.Container{{Name: "load", Image: config.Image, VolumeMounts: []core.VolumeMount{data, {Name: "ca", MountPath: "/ca", ReadOnly: true}},
						Args: []string{"replay", "--endpoint", config.Endpoint, "--token-file", "/var/run/secrets/kubernetes.io/serviceaccount/token",
							"--ca-file", "/ca/tls.crt", "--workload", plan.workload.Name, "--dataset-dir", "/data", "--fields", strings.Join(plan.fields, ","),
							"--rate", strconv.FormatFloat(rate, 'f', 4, 64), "--duration", strconv.Itoa(int(seconds)), "--arrivals", "poisson", "--seed", "0",
							"--workers", strconv.Itoa(int(m.Spec.Workers))}}},
					Volumes: []core.Volume{{Name: "data", VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{}}},
						{Name: "ca", VolumeSource: core.VolumeSource{Projected: &core.ProjectedVolumeSource{Sources: []core.VolumeProjection{{Secret: &core.SecretProjection{
							LocalObjectReference: core.LocalObjectReference{Name: config.CASecret}, Items: []core.KeyToPath{{Key: "tls.crt", Path: "tls.crt"}}}}}}}}},
				},
			},
		},
	}
}

// deleteLoadJobs removes the load Jobs of the Node with their Pods.
func (c *SchedulerController) deleteLoadJobs(ctx context.Context, ref serving.NodeReference, config LoadGeneratorConfig) error {
	jobs, err := c.kube.BatchV1().Jobs(config.Namespace).List(ctx, meta.ListOptions{LabelSelector: loadNodeLabel + "=" + ref.UID})
	if err != nil {
		return err
	}
	for _, job := range jobs.Items {
		// Background propagation removes the Pods; the default orphans them and the load goes on.
		if err = eIfNotFound(c.kube.BatchV1().Jobs(job.Namespace).Delete(ctx, job.Name,
			meta.DeleteOptions{PropagationPolicy: ptr.To(meta.DeletePropagationBackground)})); err != nil {
			return err
		}
	}
	return nil
}
