package kube

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
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
	BurstOccupancyPrefix    = "serving/dispatch-occupancy-observations/"
	BurstOccupancySchema    = "chill.dacs.io/dispatch-occupancy-measurement.v1alpha2"
	// loadNodeLabel names the measured Node UID on a load Job.
	loadNodeLabel = "chill.dacs.io/measurement-load"

	refusedBatchUnmeasured        = "BatchUnmeasured"
	refusedWorkloadMissing        = "WorkloadMissing"
	refusedWorkloadAutonomous     = "WorkloadAutonomous"
	refusedWorkloadServing        = "WorkloadServing"
	occupancyTooFewPairs          = "TooFewPairs"
	occupancyObserved             = "Observed"
	occupancyLoadFailed           = "LoadFailed"
	occupancyClosedLoop           = "closed-loop"
	occupancyConditionsInfeasible = "ConditionsInfeasible"
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

// DispatchOccupancyRecord is the legacy scheduler-consumed v1alpha1 evidence.
// Its reader and accepted objects remain unchanged until scheduler admission changes.
type DispatchOccupancyRecord struct {
	SchemaVersion    string                `json:"schemaVersion"`
	Node             serving.NodeReference `json:"node"`
	PowerMode        string                `json:"powerMode"`
	RuntimeImage     string                `json:"runtimeImage"`
	Model            string                `json:"model"`
	ExecutionPath    string                `json:"executionPath,omitempty"`
	ProfileID        string                `json:"profileID"`
	Batch            int                   `json:"batch"`
	ProfileMeanNanos float64               `json:"profileMeanNanos"`
	OfferedRate      float64               `json:"offeredRate"`
	Window           struct {
		Start time.Time `json:"start"`
		End   time.Time `json:"end"`
	} `json:"window"`
	Batches                int                                   `json:"batches"`
	Throughput             float64                               `json:"throughput"`
	PerRequestNanos        int64                                 `json:"perRequestNanos"`
	IdleFraction           float64                               `json:"idleFraction"`
	Cycles                 int                                   `json:"cycles"`
	CycleMeanNanos         float64                               `json:"cycleMeanNanos"`
	CycleSDNanos           float64                               `json:"cycleSDNanos"`
	CycleRelativeHalfWidth *float64                              `json:"cycleRelativeHalfWidth,omitempty"`
	PrecisionTargetMet     *bool                                 `json:"precisionTargetMet,omitempty"`
	IdleUpperNanos         int64                                 `json:"idleUpperNanos"`
	IdleNanosPerRequest    float64                               `json:"idleNanosPerRequest"`
	DRange                 *[2]float64                           `json:"dRange,omitempty"`
	KappaErrorBound        *float64                              `json:"kappaErrorBound,omitempty"`
	ActorWaitNanos         int64                                 `json:"actorWaitNanos"`
	UnknownRows            int                                   `json:"unknownRows"`
	Reasons                []string                              `json:"reasons,omitempty"`
	IdleIdentified         bool                                  `json:"idleIdentified"`
	DrainError             string                                `json:"drainError,omitempty"`
	DrainState             *servingstate.OperationState          `json:"drainState,omitempty"`
	Conditions             edge.DispatchOccupancyMeasurementSpec `json:"conditions"`
	LoadJob                struct {
		Name  string `json:"name"`
		Image string `json:"image"`
		State string `json:"state"`
		Error string `json:"error,omitempty"`
	} `json:"loadJob"`
	Isolation  *servingops.MeasurementIsolation `json:"isolation,omitempty"`
	Verdict    string                           `json:"verdict"`
	Reason     string                           `json:"reason,omitempty"`
	MeasuredAt time.Time                        `json:"measuredAt"`
}

// BurstOccupancyRecord is immutable factual evidence, never scheduler admission.
type BurstOccupancyRecord struct {
	SchemaVersion string                       `json:"schemaVersion"`
	Node          serving.NodeReference        `json:"node"`
	PowerMode     string                       `json:"powerMode"`
	RuntimeImage  string                       `json:"runtimeImage"`
	Model         string                       `json:"model"`
	ProfileID     string                       `json:"profileID"`
	Batch         int                          `json:"batch"`
	Estimate      *serving.OccupancyEstimate   `json:"estimate"`
	Reasons       []string                     `json:"reasons,omitempty"`
	DrainError    string                       `json:"drainError,omitempty"`
	DrainState    *servingstate.OperationState `json:"drainState,omitempty"`
	// Conditions is the exact immutable CR, including calculation provenance.
	Conditions edge.DispatchOccupancyMeasurementSpec `json:"conditions"`
	LoadJob    struct {
		Name  string `json:"name"`
		Image string `json:"image"`
		State string `json:"state"`
		Error string `json:"error,omitempty"`
	} `json:"loadJob"`
	Isolation  *servingops.MeasurementIsolation `json:"isolation,omitempty"`
	Verdict    string                           `json:"verdict"`
	Reason     string                           `json:"reason,omitempty"`
	MeasuredAt time.Time                        `json:"measuredAt"`
}

// validateOccupancyProfile rejects changed accepted identities or resource bounds.
func validateOccupancyProfile(s edge.DispatchOccupancyMeasurementSpec, profile *serving.Profile, w *edge.Workload, preparation servingstate.PrepareSpec) error {
	var inputs struct {
		Computed struct {
			FramedDecodedRequestUpperBytes int64 `json:"framedDecodedRequestUpperBytes"`
		} `json:"computed"`
		Profile struct {
			ProfileID    string `json:"profileID"`
			RuntimeImage string `json:"runtimeImage"`
			Workload     struct {
				Name       string `json:"name"`
				UID        string `json:"uid"`
				Generation int64  `json:"generation"`
				SLO        struct {
					Percentile         float64 `json:"percentile"`
					TargetLatencyNanos int64   `json:"targetLatencyNanos"`
				} `json:"slo"`
			} `json:"workload"`
		} `json:"profile"`
	}
	if err := json.Unmarshal([]byte(s.Conditions.InputsJSON), &inputs); err != nil {
		return err
	}
	p := inputs.Profile
	if p.ProfileID != profile.ID || p.RuntimeImage != profile.Condition.RuntimeImage ||
		p.Workload.Name != w.Name || p.Workload.UID != string(w.UID) ||
		p.Workload.Generation != w.Generation || p.Workload.SLO.Percentile != w.Spec.SLO.Percentile ||
		p.Workload.SLO.TargetLatencyNanos != w.Spec.SLO.TargetLatency.Duration.Nanoseconds() {
		return errors.New("frozen profile/runtime or Workload identity changed")
	}
	waiting := int64(s.Batch) * int64(s.Conditions.WaitingBatches)
	requestBytes := inputs.Computed.FramedDecodedRequestUpperBytes
	if requestBytes <= 0 || waiting > int64(preparation.Config.MaxQueueRequests) ||
		waiting > int64(preparation.Config.MaxQueueBytes)/requestBytes {
		return errors.New("configured outstanding supply exceeds current native queue capacity")
	}
	return nil
}

func occupancyReason(record *BurstOccupancyRecord, reason string) {
	if !slices.Contains(record.Reasons, reason) {
		record.Reasons = append(record.Reasons, reason)
	}
}

func judgeOccupancy(record *BurstOccupancyRecord, spec edge.DispatchOccupancyMeasurementSpec) {
	if record.Estimate == nil || record.Estimate.Pairs < int(spec.MinimumPairs) {
		occupancyReason(record, occupancyTooFewPairs)
	}
	record.Verdict = occupancyObserved
	if len(record.Reasons) > 0 {
		record.Verdict, record.Reason = servingops.MeasurementInsufficient, strings.Join(record.Reasons, "; ")
	}
}

// occupancyPlan is what admission resolved for a granted occupancy measurement.
type occupancyPlan struct {
	profile  *serving.Profile
	workload *edge.Workload
	dataset  *edge.Dataset
	fields   []string
	// powered is set when the Node already reads back the profile's mode;
	// otherwise maxActuation bounds the switch into it.
	powered      bool
	maxActuation time.Duration
}

// occupancyRefusal judges a dispatch-occupancy measurement before its grant.
func (c *SchedulerController) occupancyRefusal(ctx context.Context, m *edge.DispatchOccupancyMeasurement, node *core.Node, in admissionInputs) (plan occupancyPlan, reason, detail string, wait bool) {
	if err := m.Spec.ValidateConditions(); err != nil {
		return plan, occupancyConditionsInfeasible, err.Error(), false
	}
	if m.Spec.SupplyMode == occupancyClosedLoop &&
		(in.config.GatewayRequestTimeout <= 0 || time.Duration(m.Spec.RequestLifetimeSeconds)*time.Second > in.config.GatewayRequestTimeout) {
		return plan, occupancyConditionsInfeasible, "request lifetime exceeds the configured gateway cap", false
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
	if err := c.Authorize(servingstate.Operation{WorkloadUID: string(workload.UID)}); err != nil {
		return plan, "", "", true
	}
	if m.Spec.SupplyMode == occupancyClosedLoop && (m.Spec.Conditions.ProfileID != plan.profile.ID ||
		time.Duration(m.Spec.RequestLifetimeSeconds)*time.Second < workload.Spec.SLO.TargetLatency.Duration) {
		return plan, occupancyConditionsInfeasible, "profile identity or Workload lifetime mismatch", false
	}
	if m.Spec.SupplyMode == occupancyClosedLoop {
		if err := validateOccupancyProfile(m.Spec, plan.profile, &workload, in.config.Preparations[plan.profile.ID]); err != nil {
			return plan, occupancyConditionsInfeasible, err.Error(), false
		}
	}
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
	plan.powered = servingstate.ReadBackMode(node, servingstate.NodeClass(node, in.classes)) == mode
	maxActuation, ok := servingstate.NodePowerTargets(node, in.classes, in.config.PowerPolicies)[mode]
	if !plan.powered && !ok {
		return plan, refusedPowerModeUnknown, mode, false
	}
	plan.maxActuation = maxActuation
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
		_ = json.Unmarshal([]byte(p.Annotations[servingstate.ServingRoutingAnnotation]), &routing)
		if servingstate.WorkloadOwner(p.OwnerReferences) == uid || routing[uid] > 0 {
			return p.Namespace + "/" + p.Name
		}
	}
	return ""
}

// startOccupancy registers the run of a granted occupancy measurement.
func (c *SchedulerController) startOccupancy(node *core.Node, m *edge.DispatchOccupancyMeasurement, plan occupancyPlan, config SchedulerControllerConfig) {
	ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	root := &measurementRoot{object: m, node: node.Name}
	operation := servingstate.Operation{ID: occupancyKind + ":" + node.Name + ":" + string(m.UID), Kind: occupancyKind, Node: ref, WorkloadUID: string(plan.workload.UID), Holder: string(m.UID)}
	c.startNodeRun(operation, config, func(ctx context.Context, result *servingstate.OperationResult) error {
		err := c.runOccupancy(ctx, m, ref, plan, config)
		return errors.Join(err, c.handBack(ctx, ref, string(m.UID), root, false, result))
	})
}

// runOccupancy generates a configured fixed-batch burst and records the shared
// estimator's factual output, without changing scheduler d consumption.
func (c *SchedulerController) runOccupancy(ctx context.Context, m *edge.DispatchOccupancyMeasurement, ref serving.NodeReference, plan occupancyPlan, config SchedulerControllerConfig) error {
	profile, b := plan.profile, int(m.Spec.Batch)
	if m.Spec.PlannedElapsedSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(m.Spec.PlannedElapsedSeconds)*time.Second)
		defer cancel()
	}
	fail := func(err error) error {
		// Preserve terminal status even when the measurement watchdog expired
		// before load creation. Publication is bounded independently of execution.
		failureCtx, cancelFailure := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancelFailure()
		return errors.Join(err, c.failMeasurement(failureCtx, m, firstLine(err.Error())))
	}
	if err := c.patchMeasurementStatus(ctx, m, func() {
		m.Status.Phase, m.Status.PowerMode = edge.MeasurementPhaseMeasuring, profile.Condition.PowerMode
	}); err != nil {
		return fail(err)
	}
	if !plan.powered {
		if err := c.measurement.ChangePower(ctx, ref, string(m.UID), profile.Condition.PowerMode, plan.maxActuation); err != nil {
			return fail(err)
		}
	}
	_, generation, err := c.measurement.PowerModeStart(ctx, ref)
	if err != nil {
		return fail(err)
	}
	spec := config.Preparations[profile.ID]
	spec.Owner = &serving.WorkloadReference{Name: plan.workload.Name, UID: string(plan.workload.UID)}
	spec.Config.Policy.FixedBatchSize, spec.Instance.FixedBatchSize = b, b
	spec.Config.Policy.MaxBatchWaitMS = 0
	spec.Instance.MaxWait = 0
	prepareCtx := ctx
	if m.Spec.PreparationSeconds > 0 {
		var cancel context.CancelFunc
		prepareCtx, cancel = context.WithTimeout(ctx, time.Duration(m.Spec.PreparationSeconds+m.Spec.JobSetupSeconds)*time.Second)
		defer cancel()
	}
	if _, err := c.measurement.PrepareInstance(prepareCtx, spec); err != nil {
		return fail(err)
	}
	job := loadJob(m, ref, plan, config.LoadGenerator)
	started := time.Now()
	if _, err = c.kube.BatchV1().Jobs(job.Namespace).Create(ctx, job, meta.CreateOptions{}); err != nil {
		return fail(err)
	}
	var state string
	err = c.operations.Wait(ctx, func() (bool, error) {
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
	loadErr := errors.Join(err, c.deleteLoadJobs(ctx, ref, config.LoadGenerator))
	drainState, drainErr := c.waitOccupancyDrain(ctx, string(plan.workload.UID), ref, time.Duration(m.Spec.DrainSeconds)*time.Second)
	collected := time.Now()
	record, err := c.collectOccupancy(ctx, m, ref, plan, started, collected, &drainState)
	if err != nil {
		return fail(err)
	}
	if loadErr != nil || !strings.HasPrefix(state, string(batch.JobComplete)) {
		occupancyReason(&record, occupancyLoadFailed+" "+strings.TrimSpace(state))
	}
	if drainErr != nil {
		record.DrainError = drainErr.Error()
		occupancyReason(&record, "PhysicalDrainUnconfirmed")
	}
	if m.Spec.DrainSeconds > 0 {
		record.DrainState = &drainState
	}
	record.LoadJob.Name, record.LoadJob.Image = job.Name, config.LoadGenerator.Image
	record.LoadJob.State = state
	if loadErr != nil {
		record.LoadJob.Error = loadErr.Error()
	}
	// The elapsed plan is not permission to lose a failed run's evidence.
	finalCtx, cancelFinal := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Minute)
	defer cancelFinal()
	isolationCtx, cancelIsolation := context.WithTimeout(finalCtx, 30*time.Second)
	witness, witnessErr := c.measurement.MeasurementIsolation(isolationCtx, ref, map[string]bool{profile.Condition.LogicalModel: true}, generation, 0)
	cancelIsolation()
	record.Isolation = &witness
	if witnessErr != nil {
		witness.Error = witnessErr.Error()
		occupancyReason(&record, servingstate.PowerChangedReason)
	}
	judgeOccupancy(&record, m.Spec)
	raw, err := json.Marshal(record)
	if err != nil {
		return fail(err)
	}
	identity := evidencestore.ObjectIdentity{Store: config.EvidenceStore, Key: BurstOccupancyPrefix + ref.UID + "/" + string(m.UID) + ".json", Size: int64(len(raw))}
	if _, err = config.Evidence.PutImmutable(finalCtx, identity, bytes.NewReader(raw)); err != nil {
		return fail(err)
	}
	return c.patchMeasurementStatus(finalCtx, m, func() {
		m.Status.Phase, m.Status.Reason = edge.MeasurementPhaseComplete, record.Reason
		if record.Verdict != occupancyObserved {
			m.Status.Phase = edge.MeasurementPhaseInsufficient
		}
		m.Status.Result = &edge.ProfilingRunEvidenceReference{Store: identity.Store, Key: identity.Key, SchemaVersion: BurstOccupancySchema, Size: identity.Size}
	})
}

func (c *SchedulerController) collectOccupancy(ctx context.Context, m *edge.DispatchOccupancyMeasurement, ref serving.NodeReference, plan occupancyPlan, started, collected time.Time, drainState *servingstate.OperationState) (BurstOccupancyRecord, error) {
	profile, b := plan.profile, int(m.Spec.Batch)
	origin := serving.NewOccupancyEstimator(nil, nil, started, collected)
	gap, scanErr := c.observationLog.ScanOccupancyRows(ctx, string(plan.workload.UID), ref.UID, started, collected, origin.Add)
	if scanErr != nil {
		return BurstOccupancyRecord{}, scanErr
	}
	// Join late native supplements before selecting this Job's receipt cohort.
	firstStart := origin.FirstRunningAt(started, collected)
	record := BurstOccupancyRecord{SchemaVersion: BurstOccupancySchema, Node: ref, PowerMode: profile.Condition.PowerMode,
		RuntimeImage: profile.Condition.RuntimeImage, Model: profile.Condition.LogicalModel, ProfileID: profile.ID, Batch: b,
		Conditions: m.Spec, MeasuredAt: time.Now().UTC()}
	windowStart := firstStart.Add(time.Duration(m.Spec.WarmupSeconds) * time.Second)
	if firstStart.IsZero() {
		windowStart = started.Add(time.Duration(m.Spec.WarmupSeconds) * time.Second)
		occupancyReason(&record, "MissingPhysicalEvidence")
	}
	windowEnd := windowStart.Add(time.Duration(m.Spec.DurationSeconds) * time.Second)
	policies := make(map[string]firstfloor.Metadata, len(drainState.Native))
	for instance, state := range drainState.Native {
		policies[instance] = state.Metadata
	}
	estimate := serving.NewOccupancyEstimator(map[string]*serving.Profile{profile.ID: profile}, policies, windowStart, windowEnd)
	scanGap, scanErr := c.observationLog.ScanOccupancyRows(ctx, string(plan.workload.UID), ref.UID, started, collected, estimate.Add)
	if scanErr != nil {
		return BurstOccupancyRecord{}, scanErr
	}
	mean, _ := profile.MeanNanos(b)
	record.Estimate = &serving.OccupancyEstimate{
		Node: ref, WorkloadUID: string(plan.workload.UID), Model: profile.Condition.LogicalModel,
		PowerMode: profile.Condition.PowerMode, ExecutionPath: profile.Condition.Artifact.ExecutionPath,
		RuntimeImage: profile.Condition.RuntimeImage, Batch: b,
		Profile: serving.OccupancyProfile{ProfileID: profile.ID, Reference: profile.Reference, Evidence: profile.Evidence, MeanNanos: mean},
		Window:  serving.OccupancyWindow{Start: windowStart, End: windowEnd},
	}
	for _, cell := range estimate.Results() {
		if cell.Node == ref && cell.WorkloadUID == string(plan.workload.UID) &&
			cell.Profile.ProfileID == profile.ID && cell.Batch == b {
			record.Estimate = &cell
			break
		}
	}
	if scanGap != nil {
		occupancyReason(&record, "ObservationRetentionGap")
	}
	if gap != nil {
		occupancyReason(&record, "ObservationRetentionGap")
	}
	return record, nil
}

func (c *SchedulerController) waitOccupancyDrain(ctx context.Context, workloadUID string, ref serving.NodeReference, duration time.Duration) (servingstate.OperationState, error) {
	var observed servingstate.OperationState
	if duration <= 0 {
		return observed, nil
	}
	drainCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	err := c.operations.Wait(drainCtx, func() (bool, error) {
		if !c.runtime.Drained(workloadUID) {
			return false, nil
		}
		var err error
		observed, err = c.operations.Capture(drainCtx, ref)
		if err != nil {
			return false, err
		}
		if len(observed.Native) == 0 || len(observed.Reasons) > 0 {
			return false, nil
		}
		for _, state := range observed.Native {
			if !state.Ready || len(state.Requests) > 0 {
				return false, nil
			}
		}
		return true, nil
	})
	return observed, err
}

// loadJob is the load generator Job of one occupancy measurement.
func loadJob(m *edge.DispatchOccupancyMeasurement, ref serving.NodeReference, plan occupancyPlan, config LoadGeneratorConfig) *batch.Job {
	sum := sha256.Sum256([]byte(m.UID))
	seconds := float64(m.Spec.WarmupSeconds) + float64(m.Spec.DurationSeconds)
	// Extend supply beyond the worker-native burst window.
	seconds += float64(m.Spec.RequestLifetimeSeconds) + float64(m.Spec.DeliveryMarginMillis)/1000
	data := core.VolumeMount{Name: "data", MountPath: "/data"}
	args := []string{"replay", "--endpoint", config.Endpoint, "--token-file", "/var/run/secrets/kubernetes.io/serviceaccount/token",
		"--ca-file", "/ca/tls.crt", "--workload", plan.workload.Name, "--dataset-dir", "/data", "--fields", strings.Join(plan.fields, ","),
		"--duration", strconv.FormatFloat(seconds, 'f', 3, 64), "--seed", strconv.FormatUint(m.Spec.Seed, 10), "--workers", strconv.Itoa(int(m.Spec.Workers))}
	args = append(args, "--arrivals", occupancyClosedLoop, "--request-lifetime", strconv.Itoa(int(m.Spec.RequestLifetimeSeconds)),
		"--delivery-margin", strconv.FormatFloat(float64(m.Spec.DeliveryMarginMillis)/1000, 'f', 3, 64),
		"--timeout", strconv.FormatFloat(float64(m.Spec.RequestLifetimeSeconds)+(float64(m.Spec.DeliveryMarginMillis)+1)/1000, 'f', 3, 64))
	deadline := int64(m.Spec.JobSetupSeconds) + int64(math.Ceil(seconds)) + int64(m.Spec.DrainSeconds) + int64(m.Spec.CollectionSeconds)
	return &batch.Job{
		ObjectMeta: meta.ObjectMeta{Name: "occupancy-" + hex.EncodeToString(sum[:10]), Namespace: config.Namespace, Labels: map[string]string{loadNodeLabel: ref.UID}},
		Spec: batch.JobSpec{
			BackoffLimit: ptr.To[int32](0), ActiveDeadlineSeconds: ptr.To(deadline),
			Template: core.PodTemplateSpec{
				ObjectMeta: meta.ObjectMeta{Labels: map[string]string{loadNodeLabel: ref.UID}},
				Spec: core.PodSpec{
					RestartPolicy: core.RestartPolicyNever, NodeName: config.NodeName, ServiceAccountName: config.ServiceAccount,
					InitContainers: []core.Container{{Name: "dataset", Image: plan.dataset.Spec.Source.Image, VolumeMounts: []core.VolumeMount{data},
						Command: []string{"sh", "-ec", `cp -R "$1"/. /data/`, "--", path.Join(plan.dataset.Spec.Source.Path, "evaluation")}}},
					Containers: []core.Container{{Name: "load", Image: config.Image, VolumeMounts: []core.VolumeMount{data, {Name: "ca", MountPath: "/ca", ReadOnly: true}},
						Args: args}},
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
		if err = servingstate.IgnoreNotFound(c.kube.BatchV1().Jobs(job.Namespace).Delete(ctx, job.Name,
			meta.DeleteOptions{PropagationPolicy: ptr.To(meta.DeletePropagationBackground)})); err != nil {
			return err
		}
	}
	return nil
}
