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
	"gonum.org/v1/gonum/stat/distuv"
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

	refusedBatchUnmeasured        = "BatchUnmeasured"
	refusedWorkloadMissing        = "WorkloadMissing"
	refusedWorkloadAutonomous     = "WorkloadAutonomous"
	refusedWorkloadServing        = "WorkloadServing"
	occupancyTooFewBatches        = "TooFewBatches"
	occupancyNotSaturated         = "NotSaturated"
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
	Batches                int             `json:"batches"`
	Throughput             float64         `json:"throughput"`
	PerRequestNanos        int64           `json:"perRequestNanos"`
	IdleFraction           float64         `json:"idleFraction"`
	Cycles                 int             `json:"cycles"`
	CycleMeanNanos         float64         `json:"cycleMeanNanos"`
	CycleSDNanos           float64         `json:"cycleSDNanos"`
	CycleRelativeHalfWidth *float64        `json:"cycleRelativeHalfWidth,omitempty"`
	PrecisionTargetMet     *bool           `json:"precisionTargetMet,omitempty"`
	IdleUpperNanos         int64           `json:"idleUpperNanos"`
	IdleNanosPerRequest    float64         `json:"idleNanosPerRequest"`
	ActorWaitNanos         int64           `json:"actorWaitNanos"`
	UnknownRows            int             `json:"unknownRows"`
	Reasons                []string        `json:"reasons,omitempty"`
	IdleIdentified         bool            `json:"idleIdentified"`
	DrainError             string          `json:"drainError,omitempty"`
	DrainState             *OperationState `json:"drainState,omitempty"`
	// Conditions is the exact immutable CR, including calculation provenance.
	Conditions edge.DispatchOccupancyMeasurementSpec `json:"conditions"`
	LoadJob    struct {
		Name  string `json:"name"`
		Image string `json:"image"`
		State string `json:"state"`
		Error string `json:"error,omitempty"`
	} `json:"loadJob"`
	Isolation  *MeasurementIsolation `json:"isolation,omitempty"`
	Verdict    string                `json:"verdict"`
	Reason     string                `json:"reason,omitempty"`
	MeasuredAt time.Time             `json:"measuredAt"`
}

// occupancyBatch retains only physical batch evidence, not HTTP/cohort samples.
type occupancyBatch struct {
	instance                           string
	id                                 uint64
	size                               int
	start, finished, ready             time.Time
	members, queued, running, executed int
	invalid, failed                    bool
}

// occupancyInputs projects only the inputs needed by control; the complete
// immutable derivation JSON is retained in Conditions.
type occupancyInputs struct {
	Computed struct {
		FramedDecodedRequestUpperBytes int64 `json:"framedDecodedRequestUpperBytes"`
	} `json:"computed"`
	Coefficients struct {
		edge.OccupancyPlanningFloors
		TimeMargin        float64 `json:"timeMargin"`
		RelativePrecision float64 `json:"relativeXPlanningPrecision"`
		Confidence        float64 `json:"planningConfidence"`
		IdleBase          float64 `json:"maximumIdleBaseFraction"`
		IdleErrorSeconds  float64 `json:"idleDErrorSecondsPerRequest"`
	} `json:"coefficients"`
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

func validateOccupancyProfile(s edge.DispatchOccupancyMeasurementSpec, profile *serving.Profile, w *edge.Workload, preparation PrepareSpec) error {
	var inputs occupancyInputs
	if err := json.Unmarshal([]byte(s.Conditions.InputsJSON), &inputs); err != nil {
		return err
	}
	p, c := inputs.Profile, inputs.Coefficients
	for _, v := range []float64{c.TimeMargin, c.RelativePrecision, c.Confidence, c.IdleBase, c.IdleErrorSeconds, c.WindowFloorSeconds} {
		if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("invalid frozen planning coefficients")
		}
	}
	if c.Confidence >= 1 || c.RelativePrecision >= 1 || c.IdleBase > 1 || p.ProfileID != profile.ID ||
		p.RuntimeImage != profile.Condition.RuntimeImage || p.Workload.Name != w.Name || p.Workload.UID != string(w.UID) ||
		p.Workload.Generation != w.Generation || p.Workload.SLO.Percentile != w.Spec.SLO.Percentile ||
		p.Workload.SLO.TargetLatencyNanos != w.Spec.SLO.TargetLatency.Duration.Nanoseconds() {
		return errors.New("frozen profile/runtime or Workload identity changed")
	}
	waiting := int64(s.Batch) * int64(s.Conditions.WaitingBatches)
	bytes := inputs.Computed.FramedDecodedRequestUpperBytes
	if bytes <= 0 || waiting > int64(preparation.Config.MaxQueueRequests) || waiting > int64(preparation.Config.MaxQueueBytes)/bytes {
		return errors.New("fixed outstanding supply exceeds current native queue capacity")
	}
	mean, _ := profile.MeanNanos(int(s.Batch))
	idle, _ := strconv.ParseFloat(s.MaximumIdleFraction, 64)
	idleBound := math.Min(c.IdleBase, c.IdleErrorSeconds/(c.TimeMargin*mean/1e9/float64(s.Batch)+c.IdleErrorSeconds))
	if float64(s.DurationSeconds) < math.Ceil(math.Max(c.WindowFloorSeconds, c.TimeMargin*float64(s.MinimumBatches)*mean/1e9)) ||
		idle > idleBound+1e-12 || float64(s.Conditions.MaximumIdleNanosPerRequest)/1e9 > c.IdleErrorSeconds {
		return errors.New("measurement window or idle bound violates frozen profile calculation")
	}
	return nil
}

type occupancyMember struct {
	instance                  string
	id                        uint64
	size                      int
	queued, running, finished *time.Time
	complete, final, failed   bool
}

type occupancyBatchKey struct {
	instance string
	id       uint64
}

type occupancyAccumulator struct {
	batches map[occupancyBatchKey]*occupancyBatch
	pending map[string]occupancyMember
	unknown int
}

func (a *occupancyAccumulator) add(r serving.RequestRow) {
	if a.pending == nil {
		a.pending = map[string]occupancyMember{}
	}
	m := occupancyMember{instance: r.Instance, id: r.BatchID, size: r.BatchSize, queued: r.NativeQueuedAt,
		running: r.NativeRunningAt, finished: r.NativeFinishedAt, complete: r.NativeExecutionComplete, final: r.Kind == "final",
		failed: r.NativeError != "" || r.NativeOutcome != "" && r.NativeOutcome != "completed"}
	if r.Kind == serving.RequestRowNative {
		prior, ok := a.pending[r.RequestID]
		if !ok {
			return
		} // A repeated log line cannot add a physical member.
		if m.id != 0 {
			prior.id, prior.size = m.id, m.size
		}
		if m.queued != nil {
			prior.queued = m.queued
		}
		if m.running != nil {
			prior.running = m.running
		}
		if m.finished != nil {
			prior.finished = m.finished
		}
		prior.complete = prior.complete || m.complete
		prior.failed = prior.failed || m.failed
		m = prior
	}
	if m.id == 0 || m.queued == nil || m.running == nil || m.finished == nil || !m.complete {
		a.pending[r.RequestID] = m
		return
	}
	delete(a.pending, r.RequestID)
	a.addMember(m)
}

func (a *occupancyAccumulator) addMember(m occupancyMember) {
	if a.batches == nil {
		a.batches = map[occupancyBatchKey]*occupancyBatch{}
	}
	if m.id == 0 {
		a.unknown++
		return
	}
	key := occupancyBatchKey{m.instance, m.id}
	b := a.batches[key]
	if b == nil {
		b = &occupancyBatch{instance: m.instance, id: m.id}
		a.batches[key] = b
	}
	if b.size != 0 && b.size != m.size {
		b.invalid = true
	}
	b.size = m.size
	b.members++
	if m.queued != nil {
		b.queued++
	}
	if m.running != nil {
		b.running++
	}
	if m.finished != nil && m.complete {
		b.executed++
	}
	if !m.final || m.size <= 0 || m.instance == "" {
		b.invalid = true
	}
	b.failed = b.failed || m.failed
	if at := m.running; at != nil {
		if !b.start.IsZero() && !b.start.Equal(*at) {
			b.invalid = true
		}
		b.start = *at
	}
	if at := m.finished; at != nil && at.After(b.finished) {
		b.finished = *at
	}
	// B>1 cannot be ready until every member has entered the actor queue.
	if at := m.queued; at != nil && at.After(b.ready) {
		b.ready = *at
	}
}

func occupancyReason(record *DispatchOccupancyRecord, reason string) {
	if !slices.Contains(record.Reasons, reason) {
		record.Reasons = append(record.Reasons, reason)
	}
}

// measure uses actual actor starts for full d, including loaded runner slowdown.
// A finished-to-next-ready gap is an idle upper bound: actor release is not
// observed in the frozen runtime. Missing evidence is never a zero-idle pair.
func (a *occupancyAccumulator) measure(record *DispatchOccupancyRecord, warmup, duration time.Duration) {
	for _, m := range a.pending {
		a.addMember(m)
	}
	batches := make([]*occupancyBatch, 0, len(a.batches))
	for _, b := range a.batches {
		if !b.start.IsZero() {
			batches = append(batches, b)
		} else {
			a.unknown++
		}
	}
	slices.SortFunc(batches, func(a, b *occupancyBatch) int { return a.start.Compare(b.start) })
	record.UnknownRows = a.unknown
	if len(batches) == 0 {
		occupancyReason(record, "MissingPhysicalEvidence")
		return
	}
	record.Window.Start = batches[0].start.Add(warmup).UTC()
	record.Window.End = record.Window.Start.Add(duration)
	w := record.Window
	var selected []*occupancyBatch
	left, right := false, false
	for _, b := range batches {
		if !b.start.After(w.Start) {
			left = true
		}
		if !b.start.Before(w.End) {
			right = true
		}
		if !b.start.Before(w.Start) && !b.start.After(w.End) {
			selected = append(selected, b)
		}
	}
	record.Batches = len(selected)
	if len(selected) >= 2 {
		first, last := selected[0], selected[len(selected)-1]
		record.Cycles = len(selected) - 1
		span := last.start.Sub(first.start).Seconds()
		if span > 0 && last.id > first.id && last.instance == first.instance {
			record.Throughput = float64(last.id-first.id) * float64(record.Batch) / span
			record.PerRequestNanos = int64(math.Round(1e9/record.Throughput - record.ProfileMeanNanos/float64(record.Batch)))
		}
	}
	if !left || !right {
		occupancyReason(record, "MissingWindowBoundary")
	}
	measureOccupancyContinuity(record, batches, duration)
	measureOccupancyPrecision(record, selected)
}

func measureOccupancyContinuity(record *DispatchOccupancyRecord, batches []*occupancyBatch, duration time.Duration) {
	w := record.Window
	var cycleIdle time.Duration
	for i, b := range batches {
		// Include both endpoints and the last batch's tail, not only starts
		// selected for throughput.
		if i+1 == len(batches) {
			break
		}
		next := batches[i+1]
		if !next.start.After(w.Start) || !b.start.Before(w.End) {
			continue
		}
		valid := func(b *occupancyBatch) bool {
			return !b.invalid && b.size == record.Batch && b.members == record.Batch && b.queued == record.Batch && b.running == record.Batch &&
				b.executed == record.Batch && !b.finished.Before(b.start) && !b.ready.After(b.start)
		}
		if !valid(b) || !valid(next) || b.instance != next.instance || b.id+1 != next.id || b.finished.After(next.start) {
			occupancyReason(record, "UnknownPhysicalContinuity")
		}
		if b.failed || next.failed {
			occupancyReason(record, "NativeExecutionError")
		}
		if b.start.After(b.ready) && !b.ready.IsZero() {
			record.ActorWaitNanos += b.start.Sub(b.ready).Nanoseconds()
		}
		record.IdleUpperNanos += occupancySupplyIdle(b.finished, next.ready, w.Start, w.End).Nanoseconds()
		if !b.start.Before(w.Start) && !next.start.After(w.End) {
			cycleIdle += occupancySupplyIdle(b.finished, next.ready, b.start, next.start)
		}
	}
	if record.UnknownRows > 0 {
		occupancyReason(record, "UnknownRequestExecution")
	}
	record.IdleIdentified = len(record.Reasons) == 0
	record.IdleFraction = float64(record.IdleUpperNanos) / float64(duration)
	if record.Cycles > 0 {
		record.IdleNanosPerRequest = float64(cycleIdle) / float64(record.Cycles*record.Batch)
	}
}

// occupancySupplyIdle clips the observed finished-to-readiness upper bound to
// the same physical interval used for its denominator.
func occupancySupplyIdle(finished, ready, start, end time.Time) time.Duration {
	if finished.IsZero() || ready.IsZero() {
		return 0
	}
	if finished.Before(start) {
		finished = start
	}
	if ready.After(end) {
		ready = end
	}
	if ready.After(finished) {
		return ready.Sub(finished)
	}
	return 0
}

func measureOccupancyPrecision(record *DispatchOccupancyRecord, selected []*occupancyBatch) {
	if record.IdleIdentified && record.Cycles >= 2 {
		var sumSquares float64
		for i := 1; i < len(selected); i++ {
			value := float64(selected[i].start.Sub(selected[i-1].start))
			delta := value - record.CycleMeanNanos
			record.CycleMeanNanos += delta / float64(i)
			sumSquares += delta * (value - record.CycleMeanNanos)
		}
		record.CycleSDNanos = math.Sqrt(sumSquares / float64(record.Cycles-1))
		var inputs occupancyInputs
		if json.Unmarshal([]byte(record.Conditions.Conditions.InputsJSON), &inputs) == nil &&
			inputs.Coefficients.Confidence > 0 && inputs.Coefficients.Confidence < 1 && record.CycleMeanNanos > 0 {
			// Empirical stationary-iid t approximation, not a coverage guarantee.
			t := distuv.StudentsT{Mu: 0, Sigma: 1, Nu: float64(record.Cycles - 1)}.Quantile((1 + inputs.Coefficients.Confidence) / 2)
			relative := t * record.CycleSDNanos / math.Sqrt(float64(record.Cycles)) / record.CycleMeanNanos
			record.CycleRelativeHalfWidth = &relative
			record.PrecisionTargetMet = ptr.To(relative <= inputs.Coefficients.RelativePrecision)
		}
	}
}

func judgeOccupancy(record *DispatchOccupancyRecord, spec edge.DispatchOccupancyMeasurementSpec) {
	maxIdle, _ := strconv.ParseFloat(spec.MaximumIdleFraction, 64)
	if record.Batches < int(spec.MinimumBatches) || record.Throughput == 0 {
		occupancyReason(record, fmt.Sprintf("%s %d", occupancyTooFewBatches, record.Batches))
	}
	if record.IdleFraction > maxIdle || spec.Conditions.MaximumIdleNanosPerRequest > 0 &&
		record.IdleNanosPerRequest > float64(spec.Conditions.MaximumIdleNanosPerRequest) {
		occupancyReason(record, fmt.Sprintf("%s %.6f", occupancyNotSaturated, record.IdleFraction))
	}
	record.Verdict = measurementAccepted
	if len(record.Reasons) > 0 {
		record.Verdict, record.Reason = measurementInsufficient, strings.Join(record.Reasons, "; ")
	}
}

func measureOccupancy(record *DispatchOccupancyRecord, rows []serving.RequestRow, warmup, duration time.Duration) {
	var a occupancyAccumulator
	for _, r := range rows {
		a.add(r)
	}
	a.measure(record, warmup, duration)
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
	plan.powered = readBackMode(node, nodeClass(node, in.classes)) == mode
	maxActuation, ok := nodePowerTargets(node, in.classes, in.config.PowerPolicies)[mode]
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
	_, generation, err := c.measurement.powerModeStart(ctx, ref)
	if err != nil {
		return fail(err)
	}
	spec := config.Preparations[profile.ID]
	spec.Owner = &serving.WorkloadReference{Name: plan.workload.Name, UID: string(plan.workload.UID)}
	spec.Config.Policy.FixedBatchSize, spec.Instance.FixedBatchSize = b, b
	prepareCtx := ctx
	if m.Spec.PreparationSeconds > 0 {
		var cancel context.CancelFunc
		prepareCtx, cancel = context.WithTimeout(ctx, time.Duration(m.Spec.PreparationSeconds+m.Spec.JobSetupSeconds)*time.Second)
		defer cancel()
	}
	if err := c.measurement.PrepareInstance(prepareCtx, spec); err != nil {
		return fail(err)
	}
	mean, _ := profile.MeanNanos(b)
	var rate float64
	if m.Spec.SupplyMode != occupancyClosedLoop {
		factor, err := strconv.ParseFloat(m.Spec.RateFactor, 64)
		if err != nil {
			return fail(err)
		}
		rate = factor * float64(b) / (mean / 1e9)
	}
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
	loadErr := errors.Join(err, c.deleteLoadJobs(ctx, ref, config.LoadGenerator))
	drainState, drainErr := c.waitOccupancyDrain(ctx, string(plan.workload.UID), ref, time.Duration(m.Spec.DrainSeconds)*time.Second)
	var aggregate occupancyAccumulator
	gap, scanErr := c.observationLog.ScanOccupancyRows(string(plan.workload.UID), ref.UID, started, time.Now(), aggregate.add)
	if scanErr != nil {
		return fail(scanErr)
	}
	record := DispatchOccupancyRecord{SchemaVersion: DispatchOccupancySchema, Node: ref, PowerMode: profile.Condition.PowerMode,
		RuntimeImage: profile.Condition.RuntimeImage, Model: profile.Condition.LogicalModel, ProfileID: profile.ID, Batch: b,
		ProfileMeanNanos: mean, OfferedRate: rate, Conditions: m.Spec, Verdict: measurementAccepted, MeasuredAt: time.Now().UTC()}
	aggregate.measure(&record, time.Duration(m.Spec.WarmupSeconds)*time.Second, time.Duration(m.Spec.DurationSeconds)*time.Second)
	if gap != nil {
		occupancyReason(&record, "ObservationRetentionGap")
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
	witness, witnessErr := c.measurement.measurementIsolation(isolationCtx, ref, map[string]bool{profile.Condition.LogicalModel: true}, generation, 0)
	cancelIsolation()
	record.Isolation = &witness
	if witnessErr != nil {
		witness.Error = witnessErr.Error()
		occupancyReason(&record, powerChangedReason)
	}
	judgeOccupancy(&record, m.Spec)
	raw, err := json.Marshal(record)
	if err != nil {
		return fail(err)
	}
	identity := evidencestore.ObjectIdentity{Store: config.EvidenceStore, Key: DispatchOccupancyPrefix + ref.UID + "/" + string(m.UID) + ".json", Size: int64(len(raw))}
	if _, err = config.Evidence.PutImmutable(finalCtx, identity, bytes.NewReader(raw)); err != nil {
		return fail(err)
	}
	return c.patchMeasurementStatus(finalCtx, m, func() {
		m.Status.Phase, m.Status.Reason = edge.MeasurementPhaseComplete, record.Reason
		if record.Verdict != measurementAccepted {
			m.Status.Phase = edge.MeasurementPhaseInsufficient
		}
		m.Status.PerRequestNanos = ptr.To(record.PerRequestNanos)
		m.Status.Throughput = strconv.FormatFloat(record.Throughput, 'f', 4, 64)
		m.Status.Result = &edge.ProfilingRunEvidenceReference{Store: identity.Store, Key: identity.Key, SchemaVersion: DispatchOccupancySchema, Size: identity.Size}
	})
}

func (c *SchedulerController) waitOccupancyDrain(ctx context.Context, workloadUID string, ref serving.NodeReference, duration time.Duration) (OperationState, error) {
	var observed OperationState
	if duration <= 0 {
		return observed, nil
	}
	drainCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	err := c.operations.wait(drainCtx, func() (bool, error) {
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
func loadJob(m *edge.DispatchOccupancyMeasurement, ref serving.NodeReference, plan occupancyPlan, rate float64, config LoadGeneratorConfig) *batch.Job {
	sum := sha256.Sum256([]byte(m.UID))
	seconds := float64(m.Spec.WarmupSeconds) + float64(m.Spec.DurationSeconds)
	if m.Spec.SupplyMode == occupancyClosedLoop {
		// The physical window starts after generator startup and first-request delivery.
		seconds += float64(m.Spec.RequestLifetimeSeconds) + float64(m.Spec.DeliveryMarginMillis)/1000
	}
	data := core.VolumeMount{Name: "data", MountPath: "/data"}
	deadline := int64(seconds) + 120
	args := []string{"replay", "--endpoint", config.Endpoint, "--token-file", "/var/run/secrets/kubernetes.io/serviceaccount/token",
		"--ca-file", "/ca/tls.crt", "--workload", plan.workload.Name, "--dataset-dir", "/data", "--fields", strings.Join(plan.fields, ","),
		"--duration", strconv.FormatFloat(seconds, 'f', 3, 64), "--seed", "0", "--workers", strconv.Itoa(int(m.Spec.Workers))}
	if m.Spec.SupplyMode == occupancyClosedLoop {
		args = append(args, "--arrivals", occupancyClosedLoop, "--request-lifetime", strconv.Itoa(int(m.Spec.RequestLifetimeSeconds)),
			"--delivery-margin", strconv.FormatFloat(float64(m.Spec.DeliveryMarginMillis)/1000, 'f', 3, 64))
		deadline = int64(m.Spec.JobSetupSeconds) + int64(math.Ceil(seconds)) + int64(m.Spec.DrainSeconds) + int64(m.Spec.CollectionSeconds)
	} else {
		args = append(args, "--rate", strconv.FormatFloat(rate, 'f', 4, 64), "--arrivals", "poisson")
	}
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
		if err = eIfNotFound(c.kube.BatchV1().Jobs(job.Namespace).Delete(ctx, job.Name,
			meta.DeleteOptions{PropagationPolicy: ptr.To(meta.DeletePropagationBackground)})); err != nil {
			return err
		}
	}
	return nil
}
