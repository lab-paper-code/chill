package serving

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

const (
	schedulerTestNewModel    = "new-model"
	schedulerTestSecondMeter = "wall-n2"
	schedulerTestNewInstance = "new"
	schedulerTestOther       = "other"
	schedulerTestEcoMode     = "eco"
)

func schedulerFixture() (SchedulerSettings, SchedulerSnapshot) {
	at := time.Unix(100, 0)
	s := SchedulerSettings{ObservationWindow: 100 * time.Millisecond, ControlInterval: time.Second, ComparisonPeriod: 100 * time.Millisecond, MaxObservationAge: time.Second, MaxReplicas: 2, MaxInstances: 4, MaxCPUs: 4, AutonomousWorkloads: []string{"w1"}, CapacityWindow: time.Second, CapacityWindows: 3, CapacityPassingWindows: 2, CapacityMinimumRequests: 50, PlanSearchWorkers: 2, Controls: SchedulerControls{Placement: true, Resources: true, Replicas: true, Routing: true, Batching: true, Power: true, Membership: true}}
	node := SchedulerNode{Node: NodeReference{Name: "node1", UID: "n1"}, PowerMode: "fixed", Ready: true, CPUs: []int{0, 1, 2, 3}, ModeCPUs: map[string][]int{"fixed": {0, 1, 2, 3}, schedulerTestEcoMode: {0, 1, 2, 3}}, MemoryBytes: 10000, ObservedAt: at}
	image := "registry/model@sha256:" + strings.Repeat("a", 64)
	settings := &modelcompat.FirstfloorSettings{MaxBatchSize: 1, MaxQueueSize: 100}
	path := ModelPath{Name: "cpu", Artifact: "model", Files: []string{"/model"}, Format: "onnx", Image: image, RuntimeFamily: "firstfloor", Backend: "ort-cpu", Firstfloor: settings}
	d := &Definition{Name: "workload", UID: "w1", Generation: 1, Model: Model{Name: "model", UID: "m", Generation: 1, Paths: []ModelPath{path}}, Preprocessor: Preprocessor{Name: "pre", Input: []RawField{{Name: "x", Format: "UTF8", MaxBytes: 16}}, Output: []RawField{{Name: "y", Format: "UTF8"}}}, Dataset: &DatasetReference{Name: "data", UID: "data-uid"}, SLO: SLO{Percentile: 100, TargetLatency: 100 * time.Millisecond}}
	p := &Profile{ID: "profile", NodeName: node.Node.Name, Condition: derivation.CurvePlanSpec{LogicalModel: "model", NodeUID: node.Node.UID, DeviceClass: "cpu", RuntimeFamily: "firstfloor", Backend: "ort-cpu", RuntimeImage: image, Firstfloor: settings, PowerMode: "fixed", CPU: derivation.CurveCPUContract{Policy: "quota-affinity", PolicyVersion: "v1", Request: "1", Limit: "1", ORTIntraOpThreads: 1, ORTInterOpThreads: 1, AffinityCPUs: []int{0}}, PlannedBatchSizes: []int{1}, Preprocessor: derivation.CurvePreprocessorContract{Name: "pre", Inputs: []string{"x"}, Outputs: []string{"y"}}, ActiveProtocol: derivation.CurveProtocolContract{Dataset: &derivation.CurveDatasetContract{Name: "data", UID: "data-uid"}}, Artifact: derivation.CurveArtifactContract{Name: "model", ExecutionPath: "cpu", Files: []string{"/model"}, Image: image}}, IdleBaselineWatts: 12, MeasuredAt: at, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "curve", Size: 1}, Evidence: []evidencestore.ObjectIdentity{{Store: "raw", Key: "profile", Size: 1}}, Batches: []BatchProfile{{Batch: 1, DurationNanos: []float64{float64(10 * time.Millisecond)}, EnergyPerInputJoules: 1, MemoryBytes: 100}}}
	p.RuntimeMetadata = &firstfloor.RuntimeMetadata{Runtime: "onnxruntime", RuntimeVersion: "1.19.0", ModelFiles: []string{"/model/model"}, Inputs: []string{"x"}, Outputs: []string{"y"}, Providers: []string{"CPUExecutionProvider"}, Device: "cpu", CPUAffinity: []int{0}, IntraOpThreads: 1, InterOpThreads: 1, InitializedBatches: []int{1}, WarmupIterations: 1}
	i := SchedulerInstance{ID: "i1", Node: node.Node, Profile: p, WorkloadUID: d.UID, Allocation: SchedulerAllocation{CPUs: []int{0}, Threads: 1, MemoryBytes: 1000}, Ready: true, Routable: map[string]bool{d.UID: true}, Capacity: 100, Weight: 1, MaxBatch: 1, ObservedAt: at, IdleMeter: "wall-n1", IdleScope: schedulerNodeWallScope}
	b := NodeBaseline{ID: "unloaded-n1", Node: node.Node, PowerMode: "fixed", Watts: 10, Meter: "wall-n1", Scope: schedulerNodeWallScope, MeasuredAt: at, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "idle"}}
	b.Reference.Size = 1
	p.Evidence[0].Size = 1
	snap := SchedulerSnapshot{At: at, ObservationEnd: at, Workloads: []*Definition{d}, Nodes: []SchedulerNode{node}, Instances: []SchedulerInstance{i}, Options: []SchedulerOption{{Instance: i, CompatibleUIDs: []string{d.UID}}}, Baselines: []NodeBaseline{b}}
	return s, snap
}
func schedulerRequireEstimate(t *testing.T, c SchedulerCandidate, uid string) SchedulerEstimate {
	t.Helper()
	for _, e := range c.SLO {
		if e.UID == uid || e.Definition == uid {
			return e
		}
	}
	t.Fatalf("missing UID or definition %s in %+v", uid, c)
	return SchedulerEstimate{}
}
func schedulerRequireReason(t *testing.T, reasons []string, prefix string) {
	t.Helper()
	for _, r := range reasons {
		if strings.HasPrefix(r, prefix) {
			return
		}
	}
	t.Fatalf("missing %s in %v", prefix, reasons)
}

// schedulerTestSeed seeds every replay in these tests.
const schedulerTestSeed = 42

// schedulerKeepForTest replays the snapshot's keep candidate (continuing an
// in-progress target) once and returns it with its SLO estimates.
func schedulerKeepForTest(t testing.TB, settings SchedulerSettings, snapshot SchedulerSnapshot) SchedulerCandidate {
	t.Helper()
	keep := SchedulerCandidate{ID: "keep", Keep: true, Nodes: snapshot.Nodes, Instances: snapshot.Instances, Routing: schedulerInstanceRouting(context.Background(), snapshot.Workloads, snapshot.Instances)}
	for uid, routing := range snapshot.Routing {
		keep.Routing[uid] = routing
	}
	keep, err := schedulerReplayEstimate(context.Background(), settings, snapshot, keep, schedulerTestSeed, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return keep
}

func schedulerReplayForTest(s SchedulerSettings, snap SchedulerSnapshot, c SchedulerCandidate) schedulerReplayResult {
	result, err := schedulerReplay(context.Background(), s, snap, c, rand.New(rand.NewPCG(schedulerTestSeed, 0)), nil, nil)
	if err != nil {
		panic(err)
	}
	return result
}

func schedulerTestCPU(i SchedulerInstance) SchedulerInstance {
	p := *i.Profile
	p.ID += "-other-cpu"
	p.Condition.CPU.AffinityCPUs = []int{1}
	if p.RuntimeMetadata != nil {
		metadata := *p.RuntimeMetadata
		metadata.CPUAffinity = []int{1}
		p.RuntimeMetadata = &metadata
	}
	i.Profile = &p
	i.Allocation.CPUs = []int{1}
	return i
}

func TestSchedulerWindowPopulationAndZeroSample(t *testing.T) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads = nil
	d := snap.Workloads[0]
	start := snap.ObservationEnd.Add(-s.ObservationWindow)
	snap.Arrivals = []SchedulerArrival{{At: start.Add(-time.Nanosecond), Definition: d, Accepted: true}, {At: start, Definition: d, Accepted: true}, {At: start.Add(time.Millisecond), Outcome: "malformed"}, {At: snap.ObservationEnd, Definition: d, Accepted: true}}
	snap.At = snap.At.Add(20 * time.Millisecond)
	late := snap.ObservationEnd.Add(time.Millisecond)
	snap.Outstanding = []SchedulerRequest{
		{ID: "old", Definition: d, ReceivedAt: start.Add(-time.Second),
			Elapsed: snap.At.Sub(start.Add(-time.Second)), Phase: phasePending},
		{ID: "after-cutoff", Definition: d, QueueOrder: 1, ReceivedAt: late,
			Elapsed: snap.At.Sub(late), Phase: phasePending},
	}
	search := schedulerSearchPopulation(context.Background(), s, snap)
	if search.Received != 2 || search.Accepted != 1 || search.ComputeInputs != 1 {
		t.Fatalf("wrong population %+v", search)
	}
	e := schedulerRequireEstimate(t, schedulerKeepForTest(t, s, snap), d.UID)
	if e.Samples != 3 || e.ExpectedNanos == nil || *e.ExpectedNanos < float64(time.Second) {
		t.Fatalf("outstanding frozen timing lost: %+v", e)
	}
	snap.Arrivals = nil
	snap.Outstanding = nil
	empty := schedulerKeepForTest(t, s, snap)
	e = schedulerRequireEstimate(t, empty, d.UID)
	if e.Samples != 0 || e.ExpectedNanos != nil {
		t.Fatalf("empty percentile must be null: %+v", e)
	}
	if joules := *empty.ExpectedJoules; math.Abs(joules-1.2) > 1e-9 {
		t.Fatalf("empty serving still consumes physical idle: %+v", empty)
	}
}

func TestSchedulerObservationTimeBounds(t *testing.T) {
	for _, source := range []string{
		"missing cutoff", "future cutoff", "future node", "stale node", "future instance", "stale instance",
	} {
		t.Run(source, func(t *testing.T) {
			settings, snapshot := schedulerFixture()
			reason := "UnknownOrFutureObservationWindow"
			switch source {
			case "missing cutoff":
				snapshot.ObservationEnd = time.Time{}
			case "future cutoff":
				snapshot.ObservationEnd = snapshot.At.Add(time.Nanosecond)
			case "future node", "stale node":
				snapshot.Nodes[0].ObservedAt = snapshot.At.Add(time.Nanosecond)
				if source == "stale node" {
					snapshot.Nodes[0].ObservedAt = snapshot.At.Add(-settings.MaxObservationAge - time.Nanosecond)
				}
				reason = "UnknownOrStaleNode:" + snapshot.Nodes[0].Node.Name
			case "future instance", "stale instance":
				snapshot.Instances[0].ObservedAt = snapshot.At.Add(time.Nanosecond)
				if source == "stale instance" {
					snapshot.Instances[0].ObservedAt = snapshot.At.Add(-settings.MaxObservationAge - time.Nanosecond)
				}
				reason = "UnknownOrStaleInstance:" + snapshot.Instances[0].ID
			}
			decision, err := EvaluateScheduler(context.Background(), settings, snapshot, nil)
			if err != nil {
				t.Fatal(err)
			}
			schedulerRequireReason(t, decision.Keep.Reasons, reason)
			if decision.Target != nil || decision.Keep.ExpectedJoules != nil {
				t.Fatalf("unknown, future or stale observation became actionable: %+v", decision)
			}
		})
	}
}

// A running batch blocks a queued request of a tighter definition version on
// the same Workload instance: the conditioned residual, then its own service.
func TestSchedulerNonpreemptiveBlockingAndResidual(t *testing.T) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads = nil
	s.ComparisonPeriod = 200 * time.Millisecond
	a := snap.Workloads[0]
	b := *a
	b.Generation++
	b.SLO.TargetLatency = 30 * time.Millisecond
	snap.Workloads = []*Definition{&b}
	snap.Instances[0].Profile.Batches[0].DurationNanos = []float64{float64(100 * time.Millisecond)}
	snap.Outstanding = []SchedulerRequest{{ID: "running-a", Definition: a, ReceivedAt: snap.At.Add(-40 * time.Millisecond), Elapsed: 40 * time.Millisecond, Phase: phaseRunning, Instance: "i1", BatchID: "native-7", RunningElapsed: 40 * time.Millisecond}, {ID: "queued-b", Definition: &b, ReceivedAt: snap.At, Phase: "queued", Instance: "i1"}}
	zero := 0.0
	snap.Outstanding[0].NativeSenderElapsedMS = &zero
	snap.Outstanding[1].NativeSenderElapsedMS = &zero
	keep := schedulerKeepForTest(t, s, snap)
	e := schedulerRequireEstimate(t, keep, b.Identity())
	if e.ExpectedNanos == nil || *e.ExpectedNanos != float64(160*time.Millisecond) || e.NormalizedExceedance == 0 {
		t.Fatalf("running batch must block B for conditioned 60ms then service 100ms: %+v, reasons=%v", e, keep.Reasons)
	}
	if got := schedulerRequireEstimate(t, keep, a.Identity()); got.ExpectedNanos == nil || *got.ExpectedNanos != float64(100*time.Millisecond) {
		t.Fatalf("frozen running elapsed lost %+v", got)
	}
	snap.Outstanding[0].RunningElapsed = 100 * time.Millisecond
	pending := schedulerKeepForTest(t, s, snap)
	if pending.ExpectedJoules != nil {
		t.Fatalf("out-of-range elapsed cannot be zero residual: %+v", pending)
	}
	schedulerRequireReason(t, pending.Reasons, "RunningElapsedOutsideProfileRange")
}

func TestSchedulerEnergyCountsPhysicalIdleAndInactiveNodesOnce(t *testing.T) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads = nil
	n2 := snap.Nodes[0]
	n2.Node = NodeReference{Name: "node2", UID: "n2"}
	snap.Nodes = append(snap.Nodes, n2)
	b := snap.Baselines[0]
	b.ID = "inactive-idle"
	b.Node = n2.Node
	b.Meter = schedulerTestSecondMeter
	b.Watts = 20
	snap.Baselines = append(snap.Baselines, b)
	if joules := schedulerReplayForTest(s, snap, SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances}).energy; math.Abs(joules-3.2) > 1e-9 {
		t.Fatalf("loaded idle once + whole inactive node once: %g", joules)
	}
	// A measured coexistence baseline replaces, rather than adds to, derived idle.
	b = snap.Baselines[0]
	b.ID = "matched"
	b.State = []string{SchedulerInstanceState(snap.Instances[0])}
	b.Watts = 15
	snap.Baselines = append(snap.Baselines, b)
	if joules := schedulerReplayForTest(s, snap, SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances}).energy; math.Abs(joules-3.5) > 1e-9 {
		t.Fatalf("matching whole state must win: %g", joules)
	}
	snap.Baselines[1].Meter = "wall-n1"
	pending := schedulerReplayForTest(s, snap, SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances})
	schedulerRequireReason(t, pending.reasons, "NonDistinctNodeMeter")
}

func TestSchedulerTransitionWallReplacesBaselineAndReadinessGatesRouting(t *testing.T) {
	s, snap := schedulerFixture()
	target := snap.Instances[0]
	target.ID = schedulerTestNewInstance
	snap.Instances = nil
	snap.Outstanding = nil
	snap.Arrivals = []SchedulerArrival{{At: snap.At.Add(-s.ObservationWindow), Definition: snap.Workloads[0], Accepted: true}}
	cost := OperationCost{ID: schedulerActionPrepare, Node: target.Node, Kind: schedulerActionPrepare, From: "unloaded", To: SchedulerInstanceState(target), Trials: []OperationTrial{{Duration: 20 * time.Millisecond, Joules: 4}}, EnergyScope: "whole-wall", Meter: "wall-n1", Scope: schedulerNodeWallScope, PowerMode: "fixed"}
	snap.Costs = []OperationCost{cost}
	snap.Costs[0].Reference = evidencestore.ObjectIdentity{Store: "raw", Key: schedulerActionPrepare, Size: 1}
	c := SchedulerCandidate{ID: schedulerTestNewInstance, Nodes: snap.Nodes, Instances: []SchedulerInstance{target}}
	c.Actions = schedulerPlan(snap, c)
	result := schedulerReplayForTest(s, snap, c)
	if len(result.reasons) > 0 {
		t.Fatalf("replay %v", result.reasons)
	}
	if got := result.samples[snap.Workloads[0].Identity()]; len(got) != 1 || got[0] != float64(30*time.Millisecond) {
		t.Fatalf("route only after 20ms ready, then 10ms service: %v", got)
	}
	// 4J prep REPLACES the 20ms unloaded baseline, 0.96J loaded idle + 1J service.
	if math.Abs(result.energy-5.96) > 1e-9 {
		t.Fatalf("double-counted transition baseline: %g", result.energy)
	}
	snap.Costs = nil
	missing := schedulerReplayForTest(s, snap, c)
	schedulerRequireReason(t, missing.reasons, "MissingOperationCost")
}

func TestSchedulerPlanSelectsCostByReplayKey(t *testing.T) {
	_, snap := schedulerFixture()
	target := snap.Instances[0]
	target.ID = schedulerTestNewInstance
	current := snap.Instances[0]
	prepare := OperationCost{Node: target.Node, Kind: schedulerActionPrepare, From: "unloaded", To: SchedulerInstanceState(target)}
	power := OperationCost{Node: target.Node, Kind: schedulerActionPower, From: "fixed", To: schedulerTestEcoMode}
	snap.Costs = nil
	for _, c := range []struct {
		id, mode string
		state    []string
	}{{"prepare-eco", schedulerTestEcoMode, nil}, {"prepare-fixed", "fixed", nil}} {
		cost := prepare
		cost.ID, cost.PowerMode = c.id, c.mode
		snap.Costs = append(snap.Costs, cost)
	}
	for _, c := range []struct {
		id    string
		state []string
	}{{"power-other", []string{"other"}}, {"power-loaded", []string{SchedulerInstanceState(current)}}} {
		cost := power
		cost.ID, cost.BeforeState = c.id, c.state
		snap.Costs = append(snap.Costs, cost)
	}
	costOf := func(actions []Action, kind string) string {
		for _, a := range actions {
			if a.Kind == kind {
				return a.CostID
			}
		}
		return ""
	}
	added := schedulerPlan(snap, SchedulerCandidate{Nodes: snap.Nodes, Instances: []SchedulerInstance{current, target}})
	if got := costOf(added, schedulerActionPrepare); got != "prepare-fixed" {
		t.Fatalf("prepare cost ignored the Node power mode: %s", got)
	}
	nodes := append([]SchedulerNode(nil), snap.Nodes...)
	nodes[0].PowerMode = schedulerTestEcoMode
	lowered := schedulerPlan(snap, SchedulerCandidate{Nodes: nodes, Instances: []SchedulerInstance{current}})
	if got := costOf(lowered, schedulerActionPower); got != "power-loaded" {
		t.Fatalf("power cost ignored the loaded state: %s", got)
	}
}

func TestSchedulerResourcesAndNodeWidePowerPrerequisites(t *testing.T) {
	s, snap := schedulerFixture()
	old := snap.Instances[0]
	other := schedulerTestCPU(old)
	other.ID = schedulerTestOther
	snap.Instances = append(snap.Instances, other)
	target := old
	target.ID = "new-mode"
	p := *old.Profile
	p.ID = "new-profile"
	p.Condition.PowerMode = schedulerTestEcoMode
	target.Profile = &p
	c := SchedulerCandidate{ID: schedulerTestEcoMode, Nodes: append([]SchedulerNode(nil), snap.Nodes...), Instances: []SchedulerInstance{target}}
	c.Nodes[0].PowerMode = schedulerTestEcoMode
	actions := schedulerPlan(snap, c)
	drained := map[string]bool{}
	var power Action
	for _, a := range actions {
		if a.Kind == schedulerActionDrain {
			drained[a.ID] = true
		}
		if a.Kind == schedulerActionPower {
			power = a
		}
	}
	if len(power.DependsOn) < 2 {
		t.Fatalf("node action must stop/drain all runners while retaining loaded witnesses: %+v", actions)
	}
	for _, dep := range power.DependsOn {
		if !drained[dep] {
			t.Fatalf("power prereq not node-wide drain: %+v", power)
		}
	}
	snap.Nodes[0].Reserved.CPUs = []int{0}
	reasons := schedulerValidateResources(s, snap.Nodes, snap.Instances)
	schedulerRequireReason(t, reasons, "ExclusiveCPUInfeasible")
	// An overlapping replacement needs the measured removal before preparation.
	snap.Nodes[0].Reserved.CPUs = nil
	c = SchedulerCandidate{ID: "resize", Nodes: snap.Nodes, Instances: []SchedulerInstance{old}}
	c.Instances[0].ID = "resize-new"
	c.Actions = schedulerPlan(snap, c)
	snap.Costs = []OperationCost{{ID: "prep", Node: old.Node, Kind: schedulerActionPrepare, From: "unloaded", To: SchedulerInstanceState(old), Trials: []OperationTrial{{Duration: time.Millisecond, Joules: 1}}, EnergyScope: "whole-wall", Meter: "wall-n1", Scope: schedulerNodeWallScope, PowerMode: "fixed"}}
	c.Actions = schedulerPlan(snap, c)
	result := schedulerReplayForTest(s, snap, c)
	schedulerRequireReason(t, result.reasons, "MissingOperationCost")
}

func TestSchedulerInFlightAndReturnPopulation(t *testing.T) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads = nil
	s.CompletionMarginMS = 5
	// E1: dispatched after the native observation, so the native queue does not list it yet.
	snap.Outstanding = []SchedulerRequest{{ID: "in-flight", Definition: snap.Workloads[0], ReceivedAt: snap.At.Add(-5 * time.Millisecond), Elapsed: 5 * time.Millisecond, Phase: phaseAssigned, Instance: "i1"}}
	inFlight := schedulerKeepForTest(t, s, snap)
	e := schedulerRequireEstimate(t, inFlight, snap.Workloads[0].UID)
	if len(inFlight.Reasons) > 0 || e.ExpectedNanos == nil || *e.ExpectedNanos != float64(20*time.Millisecond) {
		t.Fatalf("5ms age + 10ms native service + 5ms margin on its assigned instance: %v %+v", inFlight.Reasons, e)
	}
	call := &Call{
		receipt:            Receipt{ID: "returning", UID: snap.Workloads[0].UID, ReceivedAt: snap.At.Add(-10 * time.Millisecond)},
		definition:         snap.Workloads[0],
		assignment:         &Assignment{Instance: "i1"},
		backendDone:        true,
		exchangeFinishedAt: snap.At.Add(-2 * time.Millisecond),
		progress:           &BackendProgress{Phase: "finished", ExecutionComplete: true, NativeElapsedMS: 6},
	}
	runtime := &Runtime{calls: map[string]*Call{"returning": call}}
	current := runtime.SchedulerInputs(func() time.Time { return snap.At })
	snap.At, snap.Outstanding = current.At, current.Outstanding
	e = schedulerRequireEstimate(t, schedulerKeepForTest(t, s, snap), snap.Workloads[0].UID)
	if e.ExpectedNanos == nil || *e.ExpectedNanos != float64(13*time.Millisecond) {
		t.Fatalf("original 10ms age includes the 2ms response tail; 3ms of the 5ms margin remains: %+v", e)
	}
}

func TestSchedulerRejectedComputeAndUnknownCPUQuota(t *testing.T) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads = nil
	snap.Arrivals = []SchedulerArrival{{ID: "capacity-rejected", At: snap.At.Add(-s.ObservationWindow), Definition: snap.Workloads[0], Compute: true, Outcome: "QueueFull"}}
	keep := schedulerKeepForTest(t, s, snap)
	e := schedulerRequireEstimate(t, keep, snap.Workloads[0].UID)
	search := schedulerSearchPopulation(context.Background(), s, snap)
	if search.Accepted != 0 || search.ComputeInputs != 1 || e.ExpectedNanos == nil || *e.ExpectedNanos != float64(10*time.Millisecond) {
		t.Fatalf("valid rejected demand must be replayed: %+v %+v", search, e)
	}
	snap.Nodes[0].NonServing = []SchedulerAllocation{{CPUCount: 1, MemoryBytes: 500}}
	unknown := schedulerKeepForTest(t, s, snap)
	schedulerRequireReason(t, unknown.Reasons, "UnknownExclusiveCPUAffinity")
	snap.Nodes[0].Reserved.CPUs = []int{3}
	snap.Nodes[0].NonServing[0].CPUReserved = true
	covered := schedulerKeepForTest(t, s, snap)
	if covered.ExpectedJoules == nil {
		t.Fatalf("explicit reserved coverage should restore evaluability %v", covered.Reasons)
	}
}

func TestSchedulerInProgressPairedContinuationAndCompletedPrerequisite(t *testing.T) {
	s, snap := schedulerFixture()
	target := snap.Instances[0]
	target.ID = schedulerTestNewInstance
	snap.Instances = nil
	snap.Actions = []Action{{ID: "actual-finished-prereq", Kind: schedulerActionStop, Status: schedulerActionComplete}}
	snap.Costs = []OperationCost{{ID: "prep", Node: target.Node, Kind: schedulerActionPrepare, From: "unloaded", To: SchedulerInstanceState(target), PowerMode: "fixed", Trials: []OperationTrial{{Duration: 20 * time.Millisecond, Joules: 4}}, EnergyScope: "whole-wall", Meter: "wall-n1", Scope: schedulerNodeWallScope, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "prep", Size: 1}}}
	c := SchedulerCandidate{ID: "continue", Nodes: snap.Nodes, Instances: []SchedulerInstance{target}, Actions: []Action{{ID: "actual-running", Kind: schedulerActionPrepare, Node: target.Node, Instance: target.ID, From: "unloaded", To: SchedulerInstanceState(target), CostID: "prep", Status: phaseRunning, Elapsed: 10 * time.Millisecond, DependsOn: []string{"actual-finished-prereq"}}}}
	result := schedulerReplayForTest(s, snap, c)
	if len(result.reasons) > 0 || math.Abs(result.energy-3.08) > 1e-9 {
		t.Fatalf("remaining 10ms 2J + ready 90ms 1.08J, not repeated full action: %+v", result)
	}
	c.Actions[0].Elapsed = 20 * time.Millisecond
	outside := schedulerReplayForTest(s, snap, c)
	schedulerRequireReason(t, outside.reasons, "OperationElapsedOutsideProfileRange")
}

func TestSchedulerFrozenOldDefinitionPendingSurvivesModelCutover(t *testing.T) {
	s, snap := schedulerFixture()
	old := snap.Workloads[0]
	updated := *old
	updated.Generation++
	updated.Model = old.Model
	updated.Model.Paths = append([]ModelPath(nil), old.Model.Paths...)
	updated.Model.Paths[0].Artifact = schedulerTestNewModel
	snap.Workloads = []*Definition{&updated}
	snap.Outstanding = []SchedulerRequest{{ID: "accepted-before-update", Definition: old, ReceivedAt: snap.At.Add(-5 * time.Millisecond), Elapsed: 5 * time.Millisecond, Phase: "pending"}}
	target := schedulerTestCPU(snap.Instances[0])
	target.ID = schedulerTestNewModel
	p := *target.Profile
	p.ID = "new-profile"
	p.Condition.Artifact.Name = schedulerTestNewModel
	target.Profile = &p
	before := 10.0
	snap.Costs = []OperationCost{{ID: schedulerActionPrepare, Node: target.Node, Kind: schedulerActionPrepare, From: "unloaded", To: SchedulerInstanceState(target), PowerMode: "fixed", BeforeBaselineWatts: &before, Trials: []OperationTrial{{Duration: 5 * time.Millisecond, Joules: 0.2}}, EnergyScope: "whole-wall", Meter: "wall-n1", Scope: schedulerNodeWallScope, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: schedulerActionPrepare, Size: 1}}, {ID: schedulerActionRemove, Node: target.Node, Kind: schedulerActionRemove, From: SchedulerInstanceState(snap.Instances[0]), To: "unloaded", PowerMode: "fixed", BeforeBaselineWatts: &before, Trials: []OperationTrial{{Duration: time.Millisecond, Joules: 0.1}}, EnergyScope: "whole-wall", Meter: "wall-n1", Scope: schedulerNodeWallScope, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: schedulerActionRemove, Size: 1}}}
	c := SchedulerCandidate{ID: "model-cutover", Nodes: snap.Nodes, Instances: []SchedulerInstance{target}}
	c.Actions = schedulerPlan(snap, c)
	result := schedulerReplayForTest(s, snap, c)
	samples := result.samples[old.Identity()]
	if len(result.reasons) > 0 || len(samples) != 1 || samples[0] != float64(15*time.Millisecond) {
		t.Fatalf("accepted old definition must execute before its retiring runner stops: %+v", result)
	}
}

func TestSchedulerLoadedPowerWitnessAndNewBundleReadiness(t *testing.T) {
	s, snap := schedulerFixture()
	old := snap.Instances[0]
	target := old
	target.ID = "eco-new"
	p := *old.Profile
	p.ID = "eco-profile"
	p.Condition.PowerMode = schedulerTestEcoMode
	p.IdleBaselineWatts = 8
	target.Profile = &p
	snap.Options = []SchedulerOption{{Instance: target, CompatibleUIDs: []string{old.WorkloadUID}}}
	unloaded := snap.Baselines[0]
	unloaded.ID = "eco-idle"
	unloaded.PowerMode = schedulerTestEcoMode
	unloaded.Watts = 6
	snap.Baselines = append(snap.Baselines, unloaded)
	snap.Arrivals = []SchedulerArrival{{ID: "arriving", At: snap.At.Add(-s.ObservationWindow), Definition: snap.Workloads[0], Accepted: true}}
	witness := []string{SchedulerInstanceState(old)}
	snap.Costs = []OperationCost{{ID: "power-loaded", Node: old.Node, Kind: schedulerActionPower, From: "fixed", To: schedulerTestEcoMode, BeforeState: witness, TargetState: witness, Trials: []OperationTrial{{Duration: 20 * time.Millisecond, Joules: 2}}, EnergyScope: "whole-wall", Meter: "wall-n1", Scope: schedulerNodeWallScope, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: schedulerActionPower, Size: 1}}, {ID: "remove-old", Node: old.Node, Kind: schedulerActionRemove, From: SchedulerInstanceState(old), To: "unloaded", PowerMode: schedulerTestEcoMode, BeforeState: witness, Trials: []OperationTrial{{Duration: time.Millisecond, Joules: 0.2}}, EnergyScope: "whole-wall", Meter: "wall-n1", Scope: schedulerNodeWallScope, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: schedulerActionRemove, Size: 1}}, {ID: "prepare-eco", Node: old.Node, Kind: schedulerActionPrepare, From: "unloaded", To: SchedulerInstanceState(target), PowerMode: schedulerTestEcoMode, Trials: []OperationTrial{{Duration: 5 * time.Millisecond, Joules: 1}}, EnergyScope: "whole-wall", Meter: "wall-n1", Scope: schedulerNodeWallScope, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: schedulerActionPrepare, Size: 1}}}
	c := SchedulerCandidate{ID: schedulerTestEcoMode, Nodes: append([]SchedulerNode(nil), snap.Nodes...), Instances: []SchedulerInstance{target}}
	c.Nodes[0].PowerMode = schedulerTestEcoMode
	c.Actions = schedulerPlan(snap, c)
	result := schedulerReplayForTest(s, snap, c)
	samples := result.samples[snap.Workloads[0].Identity()]
	if len(result.reasons) > 0 || len(samples) != 1 || samples[0] != float64(36*time.Millisecond) {
		t.Fatalf("loaded power 20ms -> remove 1ms -> NEW immutable bundle ready 5ms -> service 10ms: %+v", result)
	}
	snap.Costs[0].BeforeState = nil
	wrong := schedulerReplayForTest(s, snap, c)
	schedulerRequireReason(t, wrong.reasons, "PowerLoadedWitnessMismatch")
}

func TestSchedulerRoutingSharesUIDCreditsAndSortsPhysicalIDs(t *testing.T) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads = nil
	d := snap.Workloads[0]
	newer := *d
	newer.Generation++
	slow := snap.Instances[0]
	slow.ID = "z"
	slow.Profile.Batches[0].DurationNanos = []float64{float64(20 * time.Millisecond)}
	fast := schedulerTestCPU(slow)
	fast.ID = "a"
	fast.Profile.Batches = append([]BatchProfile(nil), fast.Profile.Batches...)
	fast.Profile.Batches[0].DurationNanos = []float64{float64(5 * time.Millisecond)}
	snap.Instances = []SchedulerInstance{slow, fast}
	snap.Workloads = append(snap.Workloads, &newer)
	snap.Outstanding = []SchedulerRequest{{ID: "first", QueueOrder: 0, Definition: d, ReceivedAt: snap.At, Phase: "pending"}, {ID: "second", QueueOrder: 1, Definition: &newer, ReceivedAt: snap.At, Phase: "pending"}, {ID: "third", QueueOrder: 2, Definition: d, ReceivedAt: snap.At, Phase: "pending"}}
	result := schedulerReplayForTest(s, snap, SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances})
	if len(result.reasons) > 0 || !reflect.DeepEqual(result.samples[newer.Identity()], []float64{float64(20 * time.Millisecond)}) || !reflect.DeepEqual(result.samples[d.Identity()], []float64{float64(5 * time.Millisecond), float64(10 * time.Millisecond)}) {
		t.Fatalf("UID credit must cross SLO versions and tie must choose physical a: %+v", result)
	}
}

func TestSchedulerNativeQueueOrderSurvivesReceiptOrder(t *testing.T) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads = nil
	d := snap.Workloads[0]
	later := *d
	later.Generation++
	snap.Workloads = append(snap.Workloads, &later)
	snap.Outstanding = []SchedulerRequest{{ID: "later-queue", QueueOrder: 1, Definition: &later, ReceivedAt: snap.At.Add(-time.Millisecond), Phase: "queued", Instance: "i1"}, {ID: "first-queue", QueueOrder: 0, Definition: d, ReceivedAt: snap.At, Phase: "queued", Instance: "i1"}}
	zero := 0.0
	for j := range snap.Outstanding {
		snap.Outstanding[j].NativeSenderElapsedMS = &zero
	}
	result := schedulerReplayForTest(s, snap, SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances})
	if len(result.reasons) > 0 || !reflect.DeepEqual(result.samples[d.Identity()], []float64{float64(10 * time.Millisecond)}) || !reflect.DeepEqual(result.samples[later.Identity()], []float64{float64(20 * time.Millisecond)}) {
		t.Fatalf("actual actor queue order must survive capture sort: %+v", result)
	}
}

func TestSchedulerExpiredLifetimesDoNotBecomeCheapSuccessfulSamples(t *testing.T) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads = nil
	d := snap.Workloads[0]
	life := 5 * time.Millisecond
	snap.Outstanding = []SchedulerRequest{{ID: "first", QueueOrder: 0, RemainingLifetime: &life, Definition: d, ReceivedAt: snap.At, Phase: "queued", Instance: "i1"}, {ID: "second", QueueOrder: 1, RemainingLifetime: &life, Definition: d, ReceivedAt: snap.At, Phase: "queued", Instance: "i1"}}
	zero := 0.0
	for j := range snap.Outstanding {
		snap.Outstanding[j].NativeSenderElapsedMS = &zero
	}
	keep := schedulerKeepForTest(t, s, snap)
	e := schedulerRequireEstimate(t, keep, d.UID)
	if keep.Status != schedulerStatusLifetime || e.ExpectedNanos != nil || e.Samples != 0 || e.PredictedOutcomes["expired-running"] != 1 || e.PredictedOutcomes["expired-queued"] != 1 || math.Abs(*keep.ExpectedJoules-2.2) > 1e-9 {
		t.Fatalf("expired running work stays physical; queued expires; no successful fake zero percentile: %+v", keep)
	}
	snap.Outstanding = nil
	snap.Arrivals = []SchedulerArrival{{ID: "trace", At: snap.At.Add(-s.ObservationWindow), Definition: d, Compute: true, Lifetime: life}}
	pe := schedulerRequireEstimate(t, schedulerKeepForTest(t, s, snap), d.UID)
	if pe.PredictedOutcomes["expired-running"] != 1 {
		t.Fatalf("arrival lifetime must freeze into repeated demand: %+v", pe)
	}
	snap.Arrivals[0].Lifetime = 0
	d.SLO.TargetLatency = time.Millisecond
	unlimited := schedulerKeepForTest(t, s, snap)
	ue := schedulerRequireEstimate(t, unlimited, d.UID)
	if unlimited.Status != SchedulerStatusOverload || ue.ExpectedNanos == nil || ue.Samples != 1 || ue.PredictedOutcomes["expired-running"] != 0 {
		t.Fatalf("SLO is never a request lifetime: %+v", unlimited)
	}
}

func TestSchedulerFiniteHorizonEnergyClipsExecutionAndOperation(t *testing.T) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads = nil
	d := snap.Workloads[0]
	for j := 0; j < 3; j++ {
		snap.Arrivals = append(snap.Arrivals, SchedulerArrival{ID: strings.Repeat("x", j+1), At: snap.At.Add(-s.ObservationWindow + 95*time.Millisecond), Definition: d, Accepted: true})
	}
	keep := schedulerKeepForTest(t, s, snap)
	e := schedulerRequireEstimate(t, keep, d.UID)
	if joules := *keep.ExpectedJoules; math.Abs(joules-1.7) > 1e-9 || e.ExpectedNanos == nil || *e.ExpectedNanos != float64(30*time.Millisecond) {
		t.Fatalf("only half first batch overlaps H; later SLO completions still included: %+v", keep)
	}
	snap.Arrivals = nil
	s.ComparisonPeriod = time.Millisecond
	zeroSender := 0.0
	snap.Outstanding = []SchedulerRequest{{ID: phaseRunning, Definition: d, ReceivedAt: snap.At.Add(-7 * time.Millisecond), Elapsed: 7 * time.Millisecond, RunningElapsed: 7 * time.Millisecond, Phase: phaseRunning, Instance: "i1", BatchID: "actual"}}
	snap.Outstanding[0].NativeSenderElapsedMS = &zeroSender
	if joules := *schedulerKeepForTest(t, s, snap).ExpectedJoules; math.Abs(joules-0.112) > 1e-9 {
		t.Fatalf("running execution overlap is 1/10 batch plus 1ms baseline: %g", joules)
	}
	snap.Outstanding = nil
	s.ComparisonPeriod = 5 * time.Millisecond
	target := snap.Instances[0]
	target.ID = schedulerTestNewInstance
	snap.Instances = nil
	snap.Costs = []OperationCost{{ID: schedulerActionPrepare, Node: target.Node, Kind: schedulerActionPrepare, From: "unloaded", To: SchedulerInstanceState(target), PowerMode: "fixed", Trials: []OperationTrial{{Duration: 20 * time.Millisecond, Joules: 4}}, EnergyScope: "whole-wall", Meter: "wall-n1", Scope: schedulerNodeWallScope, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: schedulerActionPrepare, Size: 1}}}
	c := SchedulerCandidate{Nodes: snap.Nodes, Instances: []SchedulerInstance{target}, Actions: []Action{{ID: schedulerActionPrepare, Kind: schedulerActionPrepare, Node: target.Node, Instance: target.ID, From: "unloaded", To: SchedulerInstanceState(target), CostID: schedulerActionPrepare}}}
	result := schedulerReplayForTest(s, snap, c)
	if len(result.reasons) > 0 || math.Abs(result.energy-1) > 1e-9 {
		t.Fatalf("partial operation replaces wall baseline over H: %+v", result)
	}
}

func TestSchedulerTerminalPrepareDoesNotReuseAbsentEffect(t *testing.T) {
	_, snap := schedulerFixture()
	target := snap.Instances[0]
	target.ID = "reusable-slot"
	snap.Instances = nil
	c := SchedulerCandidate{Nodes: snap.Nodes, Instances: []SchedulerInstance{target}}
	initial := schedulerPlan(snap, c)
	var old Action
	for _, a := range initial {
		if a.Kind == schedulerActionPrepare {
			old = a
		}
	}
	if old.ID == "" {
		t.Fatal("missing prepare")
	}
	old.Status = schedulerActionComplete
	snap.Actions = []Action{old, {ID: "removed", Kind: schedulerActionRemove, Instance: target.ID, Status: schedulerActionComplete}}
	snap.At = snap.At.Add(time.Second)
	again := schedulerPlan(snap, c)
	for _, a := range again {
		if a.Kind == schedulerActionPrepare {
			if a.Status != schedulerActionPlanned || a.ID == old.ID {
				t.Fatalf("absent effect requires fresh executable prepare: %+v", again)
			}
		}
	}
	old.Status = schedulerActionFailed
	snap.Actions = []Action{old}
	retry := schedulerPlan(snap, c)
	for _, a := range retry {
		if a.Kind == schedulerActionPrepare && (a.Status != schedulerActionPlanned || a.ID == old.ID) {
			t.Fatalf("failed prepare must not poison retry: %+v", retry)
		}
	}
	old.Status, old.Elapsed = phaseRunning, time.Millisecond
	snap.Actions = []Action{old}
	c.Routing = map[string]map[string]int32{"w1": {target.ID: 1}}
	c.Actions = initial
	reserveTargetForTest(&snap, &c)
	independent := schedulerPlan(snap, c)
	if slices.ContainsFunc(independent, func(a Action) bool { return a.Kind == schedulerActionPrepare }) {
		t.Fatalf("new plan duplicated the owner's preparation: %+v", independent)
	}
	if snap.Actions[0].ID != old.ID || snap.Actions[0].Elapsed != old.Elapsed || snap.Actions[0].Status != phaseRunning {
		t.Fatal("independent planning changed the actual running receipt")
	}
}

func TestSchedulerCurrentBatchPolicyUsesFrozenNativeSenderContext(t *testing.T) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads = nil
	a := snap.Workloads[0]
	b := *a
	b.Generation++
	snap.Workloads = append(snap.Workloads, &b)
	zero, forty := 0.0, 40.0
	snap.Outstanding = []SchedulerRequest{{ID: "a", Definition: a, ReceivedAt: snap.At.Add(-80 * time.Millisecond), Elapsed: 80 * time.Millisecond, QueueAge: 10 * time.Millisecond, QueueOrder: 0, Phase: "queued", Instance: "i1", NativeSenderElapsedMS: &zero}, {ID: "b", Definition: &b, ReceivedAt: snap.At.Add(-50 * time.Millisecond), Elapsed: 50 * time.Millisecond, QueueOrder: 1, Phase: "queued", Instance: "i1", NativeSenderElapsedMS: &forty}}
	keep := schedulerKeepForTest(t, s, snap)
	ae := schedulerRequireEstimate(t, keep, a.Identity())
	be := schedulerRequireEstimate(t, keep, b.Identity())
	if ae.ExpectedNanos == nil || be.ExpectedNanos == nil || *ae.ExpectedNanos != float64(100*time.Millisecond) || *be.ExpectedNanos != float64(60*time.Millisecond) {
		t.Fatalf("actor policy uses real sender context, not reconstructed total age: %+v", keep)
	}
	snap.Outstanding[0].NativeSenderElapsedMS = nil
	schedulerRequireReason(t, schedulerKeepForTest(t, s, snap).Reasons, "MissingNativeSenderContext")
}

func TestSchedulerDerivedBaselinePreservesMeasuredSignedIdleDelta(t *testing.T) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads = nil
	snap.Instances[0].Profile.IdleBaselineWatts = 8
	keep := SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances}
	if joules := schedulerReplayForTest(s, snap, keep).energy; math.Abs(joules-0.8) > 1e-9 {
		t.Fatalf("derived unloaded + measured signed loaded-idle delta must retain exact subtraction: %g", joules)
	}
	snap.Instances[0].Profile.IdleBaselineWatts = 1
	other := schedulerTestCPU(snap.Instances[0])
	other.ID = schedulerTestOther
	snap.Instances = append(snap.Instances, other)
	keep.Instances = snap.Instances
	schedulerRequireReason(t, schedulerReplayForTest(s, snap, keep).reasons, "DerivedNodeBaselineOutsidePhysicalRange")
}

func TestSchedulerEnergyOverflowRemainsUnknownNotJSONInfinity(t *testing.T) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads = nil
	d := snap.Workloads[0]
	snap.Instances[0].Profile.Batches[0].EnergyPerInputJoules = math.MaxFloat64
	snap.Arrivals = []SchedulerArrival{{ID: "one", At: snap.At.Add(-s.ObservationWindow), Definition: d, Compute: true}, {ID: "two", At: snap.At.Add(-s.ObservationWindow + time.Millisecond), Definition: d, Compute: true}}
	result := schedulerReplayForTest(s, snap, SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances})
	schedulerRequireReason(t, result.reasons, "ExpectedEnergyOutsidePhysicalNumericalRange")
}

func TestSchedulerWithoutCompletionObservationsIsNotPending(t *testing.T) {
	for _, margin := range []float64{0, 5} {
		s, snap := schedulerFixture()
		s.CompletionMarginMS = margin
		keep := schedulerKeepForTest(t, s, snap)
		if keep.Status == schedulerStatusPending || keep.ExpectedJoules == nil {
			t.Fatalf("margin %g without completion observations must stay evaluable: %+v", margin, keep)
		}
	}
}

func TestSchedulerQuarantinedUnusedNodeDoesNotBlockKnownCPU(t *testing.T) {
	settings, snapshot := schedulerFixture()
	settings.AutonomousWorkloads = nil
	snapshot.Options = nil
	quarantined := snapshot.Nodes[0]
	quarantined.Node = NodeReference{Name: "quarantined-gpu", UID: "gpu-node"}
	quarantined.Ready, quarantined.Reasons = false, []string{NodePlacementBudgetUnavailable}
	snapshot.Nodes = append(snapshot.Nodes, quarantined)
	baseline := snapshot.Baselines[0]
	baseline.ID, baseline.Node, baseline.Meter, baseline.Watts = "gpu-idle", quarantined.Node, "wall-gpu-node", 20
	baseline.Reference.Key = baseline.ID
	snapshot.Baselines = append(snapshot.Baselines, baseline)
	known := schedulerKeepForTest(t, settings, snapshot)
	if known.Status == schedulerStatusPending || known.ExpectedJoules == nil || math.Abs(*known.ExpectedJoules-3.2) > 1e-9 {
		t.Fatalf("unused quarantined node blocked known CPU or lost whole-POOL energy: %+v", known)
	}
	if known.Nodes[1].Ready || len(known.Nodes[1].Reasons) == 0 {
		t.Fatal("evaluation hid the quarantined Node's actual eligibility")
	}
	instance := snapshot.Instances[0]
	profile := *instance.Profile
	profile.ID, profile.NodeName, profile.Condition.NodeUID = "gpu-node-profile", quarantined.Node.Name, quarantined.Node.UID
	instance.Node, instance.Profile, instance.IdleMeter = quarantined.Node, &profile, baseline.Meter
	snapshot.Instances[0] = instance
	selected := schedulerKeepForTest(t, settings, snapshot)
	if selected.Status != schedulerStatusPending || selected.ExpectedJoules != nil {
		t.Fatalf("an instance on the quarantined Node became evaluable: %+v", selected)
	}
}

func TestSchedulerCanceledComputationReturnsContextError(t *testing.T) {
	settings, snapshot := schedulerFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if decision, err := EvaluateScheduler(ctx, settings, snapshot, nil); !errors.Is(err, context.Canceled) || decision.Target != nil {
		t.Fatalf("canceled evaluation returned decision=%+v err=%v", decision, err)
	}
}

func reserveTargetForTest(snapshot *SchedulerSnapshot, target *SchedulerCandidate) {
	reservation := SchedulerReservation{Owner: target.ID, Target: target}
	for uid := range target.Routing {
		reservation.WorkloadUIDs = append(reservation.WorkloadUIDs, uid)
	}
	for _, instance := range append(slices.Clone(snapshot.Instances), target.Instances...) {
		if slices.Contains(reservation.WorkloadUIDs, instance.WorkloadUID) || slices.Contains(reservation.WorkloadUIDs, instance.OwnerWorkloadUID) {
			reservation.Nodes = append(reservation.Nodes, instance.Node)
			reservation.Instances = append(reservation.Instances, instance.ID)
		}
	}
	for _, action := range target.Actions {
		if action.Node.UID != "" {
			reservation.Nodes = append(reservation.Nodes, action.Node)
		}
	}
	snapshot.Reservations = append(snapshot.Reservations, reservation)
}
