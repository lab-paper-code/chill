package serving

import (
	"math"
	"math/rand/v2"
	"reflect"
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
	schedulerTestWideInput   = "wide"
	schedulerTestNewInstance = "new"
	schedulerTestOther       = "other"
	schedulerTestEcoMode     = "eco"
)

func schedulerFixture() (SchedulerSettings, SchedulerSnapshot) {
	at := time.Unix(100, 0)
	s := SchedulerSettings{ObservationWindow: 100 * time.Millisecond, ControlInterval: time.Second, Horizon: 100 * time.Millisecond, MaxObservationAge: time.Second, Seed: 42, Replays: 3, MaxCandidates: 30, MaxReplicas: 2, MaxInstances: 4, MaxReplayRequests: 100, MaxCPUs: 4, Controls: SchedulerControls{Enabled: true, Placement: true, Resources: true, Replicas: true, Sharing: true, Routing: true, Batching: true, Power: true, Membership: true}}
	node := SchedulerNode{Node: NodeReference{Name: "node1", UID: "n1"}, Pool: "pool", PowerMode: "fixed", Ready: true, CPUs: []int{0, 1, 2, 3}, MemoryBytes: 10000, ObservedAt: at}
	image := "registry/model@sha256:" + strings.Repeat("a", 64)
	inputs := []modelcompat.InputTensor{{Name: "x", Datatype: "FP32", SampleShape: []int32{1}}}
	settings := &modelcompat.FirstfloorSettings{MaxBatchSize: 1, MaxQueueSize: 100}
	path := ModelPath{Name: "cpu", Artifact: "model", Path: "/model", Format: "onnx", Image: image, RuntimeFamily: "firstfloor", Backend: "ort-cpu", Tensors: inputs, Firstfloor: settings}
	d := &Definition{Name: "workload", UID: "w1", Generation: 1, Model: Model{Name: "model", UID: "m", Generation: 1, Paths: []ModelPath{path}}, SLO: SLO{Percentile: 100, TargetLatency: 100 * time.Millisecond}}
	p := &Profile{ID: "profile", NodeName: node.Node.Name, Condition: derivation.CurvePlanSpec{LogicalModel: "model", NodeUID: node.Node.UID, DeviceClass: "cpu", RuntimeFamily: "firstfloor", Backend: "ort-cpu", RuntimeImage: image, Firstfloor: settings, PowerMode: "fixed", CPU: derivation.CurveCPUContract{Policy: "quota-affinity", PolicyVersion: "v1", Request: "1", Limit: "1", ORTIntraOpThreads: 1, ORTInterOpThreads: 1, AffinityCPUs: []int{0}}, PlannedBatchSizes: []int{1}, Tensor: derivation.CurveTensorContract{Tensors: inputs}, Artifact: derivation.CurveArtifactContract{Name: "model", ExecutionPath: "cpu", Path: "/model", Image: image}}, IdleBaselineWatts: 12, MeasuredAt: at, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "curve", Size: 1}, Evidence: []evidencestore.ObjectIdentity{{Store: "raw", Key: "profile", Size: 1}}, Batches: []BatchProfile{{Batch: 1, DurationNanos: []float64{float64(10 * time.Millisecond)}, EnergyPerInputJoules: 1, MemoryBytes: 100}}}
	p.RuntimeMetadata = &firstfloor.RuntimeMetadata{Runtime: "onnxruntime", RuntimeVersion: "1.19.0", ModelPath: "/model", Inputs: []firstfloor.InputSpec{{Name: "x", Datatype: "FP32", Shape: []int{-1, 1}}}, Outputs: []firstfloor.InputSpec{{Name: "y", Datatype: "FP32", Shape: []int{-1, 1}}}, Providers: []string{"CPUExecutionProvider"}, Device: "cpu", CPUAffinity: []int{0}, IntraOpThreads: 1, InterOpThreads: 1, InitializedBatches: []int{1}, WarmupIterations: 1}
	i := SchedulerInstance{ID: "i1", Node: node.Node, Pool: node.Pool, Profile: p, WorkloadUIDs: []string{d.UID}, Allocation: SchedulerAllocation{CPUs: []int{0}, Threads: 1, MemoryBytes: 1000}, Ready: true, Capacity: 100, Weight: 1, MaxBatch: 1, EnvelopeSource: "measured transport", ObservedAt: at, IdleMeter: "wall-n1", IdleScope: schedulerNodeWallScope}
	b := NodeBaseline{ID: "unloaded-n1", Node: node.Node, PowerMode: "fixed", Watts: 10, Meter: "wall-n1", Scope: schedulerNodeWallScope, MeasuredAt: at, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "idle"}}
	b.Reference.Size = 1
	p.Evidence[0].Size = 1
	snap := SchedulerSnapshot{At: at, Workloads: []*Definition{d}, Nodes: []SchedulerNode{node}, Instances: []SchedulerInstance{i}, Options: []SchedulerOption{{Instance: i, CompatibleUIDs: []string{d.UID}}}, Baselines: []NodeBaseline{b}}
	return s, snap
}
func schedulerRequireEstimate(t *testing.T, c SchedulerCandidate, uid string) SchedulerEstimate {
	t.Helper()
	for _, e := range c.SLO {
		if e.UID == uid {
			return e
		}
	}
	t.Fatalf("missing UID %s in %+v", uid, c)
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
func schedulerReplayForTest(s SchedulerSettings, snap SchedulerSnapshot, c SchedulerCandidate) schedulerReplayResult {
	return schedulerReplay(s, snap, c, rand.New(rand.NewPCG(uint64(s.Seed), 0)))
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
	s.Controls.Enabled = false
	d := snap.Workloads[0]
	start := snap.At.Add(-s.ObservationWindow)
	snap.Arrivals = []SchedulerArrival{{At: start.Add(-time.Nanosecond), Definition: d, Accepted: true}, {At: start, Definition: d, Accepted: true}, {At: start.Add(time.Millisecond), Outcome: "malformed"}, {At: snap.At, Definition: d, Accepted: true}}
	snap.Outstanding = []SchedulerRequest{{ID: "old", Definition: d, ReceivedAt: start.Add(-time.Second), Elapsed: time.Second, Phase: "pending"}}
	decision := EvaluateScheduler(s, snap)
	if decision.Search.Received != 2 || decision.Search.Accepted != 1 || decision.Search.ComputeInputs != 1 {
		t.Fatalf("wrong population %+v", decision.Search)
	}
	e := schedulerRequireEstimate(t, decision.Keep, d.UID)
	if e.Samples != 2*s.Replays || e.ExpectedNanos == nil || *e.ExpectedNanos < float64(time.Second) {
		t.Fatalf("outstanding frozen timing lost: %+v", e)
	}
	snap.Arrivals = nil
	snap.Outstanding = nil
	empty := EvaluateScheduler(s, snap)
	e = schedulerRequireEstimate(t, empty.Keep, d.UID)
	if e.Samples != 0 || e.ExpectedNanos != nil {
		t.Fatalf("empty percentile must be null: %+v", e)
	}
	if empty.Keep.ExpectedJoules == nil || math.Abs(*empty.Keep.ExpectedJoules-1.2) > 1e-9 {
		t.Fatalf("empty serving still consumes physical idle: %+v", empty.Keep)
	}
}

func TestSchedulerSharedUIDNonpreemptiveBlockingAndResidual(t *testing.T) {
	s, snap := schedulerFixture()
	s.Controls.Enabled = false
	s.Horizon = 200 * time.Millisecond
	a := snap.Workloads[0]
	b := *a
	b.UID = "w2"
	b.Name = schedulerTestOther
	b.SLO.TargetLatency = 30 * time.Millisecond
	snap.Workloads = append(snap.Workloads, &b)
	snap.Instances[0].WorkloadUIDs = append(snap.Instances[0].WorkloadUIDs, b.UID)
	snap.Instances[0].Profile.Batches[0].DurationNanos = []float64{float64(100 * time.Millisecond)}
	snap.Outstanding = []SchedulerRequest{{ID: "running-a", Definition: a, ReceivedAt: snap.At.Add(-40 * time.Millisecond), Elapsed: 40 * time.Millisecond, Phase: phaseRunning, Instance: "i1", BatchID: "native-7", RunningElapsed: 40 * time.Millisecond}, {ID: "queued-b", Definition: &b, ReceivedAt: snap.At, Phase: "queued", Instance: "i1"}}
	zero := 0.0
	snap.Outstanding[1].NativeSenderElapsedMS = &zero
	decision := EvaluateScheduler(s, snap)
	e := schedulerRequireEstimate(t, decision.Keep, b.UID)
	if e.ExpectedNanos == nil || *e.ExpectedNanos != float64(160*time.Millisecond) || e.NormalizedExceedance == 0 {
		t.Fatalf("cross UID running batch must block B for conditioned 60ms then service 100ms: %+v, reasons=%v", e, decision.Keep.Reasons)
	}
	if got := schedulerRequireEstimate(t, decision.Keep, a.UID); got.ExpectedNanos == nil || *got.ExpectedNanos != float64(100*time.Millisecond) {
		t.Fatalf("frozen running elapsed lost %+v", got)
	}
	snap.Outstanding[0].RunningElapsed = 100 * time.Millisecond
	pending := EvaluateScheduler(s, snap)
	if pending.Target != nil {
		t.Fatalf("out-of-range elapsed cannot be zero residual: %+v", pending.Target)
	}
	schedulerRequireReason(t, pending.Keep.Reasons, "RunningElapsedOutsideProfileRange")
}

func TestSchedulerEnergyCountsPhysicalSharedIdleAndInactiveNodesOnce(t *testing.T) {
	s, snap := schedulerFixture()
	s.Controls.Enabled = false
	other := *snap.Workloads[0]
	other.UID = "w2"
	snap.Workloads = append(snap.Workloads, &other)
	snap.Instances[0].WorkloadUIDs = append(snap.Instances[0].WorkloadUIDs, other.UID)
	n2 := snap.Nodes[0]
	n2.Node = NodeReference{Name: "node2", UID: "n2"}
	snap.Nodes = append(snap.Nodes, n2)
	b := snap.Baselines[0]
	b.ID = "inactive-idle"
	b.Node = n2.Node
	b.Meter = schedulerTestSecondMeter
	b.Watts = 20
	snap.Baselines = append(snap.Baselines, b)
	decision := EvaluateScheduler(s, snap)
	if decision.Keep.ExpectedJoules == nil || math.Abs(*decision.Keep.ExpectedJoules-3.2) > 1e-9 {
		t.Fatalf("shared model once + whole inactive node once: %+v", decision.Keep)
	}
	// A measured coexistence baseline replaces, rather than adds to, derived idle.
	b = snap.Baselines[0]
	b.ID = "matched"
	b.State = []string{SchedulerInstanceState(snap.Instances[0])}
	b.Watts = 15
	snap.Baselines = append(snap.Baselines, b)
	decision = EvaluateScheduler(s, snap)
	if decision.Keep.ExpectedJoules == nil || math.Abs(*decision.Keep.ExpectedJoules-3.5) > 1e-9 {
		t.Fatalf("matching whole state must win: %+v", decision.Keep)
	}
	snap.Baselines[1].Meter = "wall-n1"
	pending := EvaluateScheduler(s, snap)
	schedulerRequireReason(t, pending.Keep.Reasons, "NonDistinctNodeMeter")
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

func TestSchedulerSLOFeasibleBeforeEnergyIncludingTransition(t *testing.T) {
	s, snap := schedulerFixture()
	s.Horizon = 200 * time.Millisecond
	d := snap.Workloads[0]
	d.SLO.TargetLatency = 50 * time.Millisecond
	snap.Instances[0].Profile.Batches[0].DurationNanos = []float64{float64(100 * time.Millisecond)}
	snap.Instances[0].Profile.Batches[0].EnergyPerInputJoules = 0.1
	snap.Arrivals = []SchedulerArrival{{At: snap.At.Add(-s.ObservationWindow + 20*time.Millisecond), Definition: d, Accepted: true}}
	node := snap.Nodes[0]
	node.Node = NodeReference{Name: "node2", UID: "n2"}
	snap.Nodes = append(snap.Nodes, node)
	p := *snap.Instances[0].Profile
	p.ID = "fast"
	p.NodeName = node.Node.Name
	p.Condition.NodeUID = node.Node.UID
	p.Batches = []BatchProfile{{Batch: 1, DurationNanos: []float64{float64(time.Millisecond)}, EnergyPerInputJoules: 5, MemoryBytes: 100}}
	target := snap.Instances[0]
	target.ID = "fast-target"
	target.Node = node.Node
	target.Profile = &p
	target.IdleMeter = schedulerTestSecondMeter
	snap.Options = []SchedulerOption{{Instance: target, CompatibleUIDs: []string{d.UID}}}
	b := snap.Baselines[0]
	b.Node = node.Node
	b.ID = "unloaded-n2"
	b.Meter = schedulerTestSecondMeter
	snap.Baselines = append(snap.Baselines, b)
	snap.Costs = []OperationCost{{ID: "prep-fast", Node: node.Node, Kind: schedulerActionPrepare, From: "unloaded", To: SchedulerInstanceState(target), Trials: []OperationTrial{{Duration: 10 * time.Millisecond, Joules: 0.5}}, EnergyScope: "whole-wall", Meter: schedulerTestSecondMeter, Scope: schedulerNodeWallScope, PowerMode: "fixed"}, {ID: "remove-old", Node: snap.Nodes[0].Node, Kind: schedulerActionRemove, From: SchedulerInstanceState(snap.Instances[0]), To: "unloaded", Trials: []OperationTrial{{Duration: time.Millisecond, Joules: 0.2}}, EnergyScope: "whole-wall", Meter: "wall-n1", Scope: schedulerNodeWallScope, PowerMode: "fixed"}}
	for j := range snap.Costs {
		snap.Costs[j].Reference = evidencestore.ObjectIdentity{Store: "raw", Key: snap.Costs[j].ID, Size: 1}
	}
	snap.Costs[1].BeforeState = []string{SchedulerInstanceState(snap.Instances[0])}
	decision := EvaluateScheduler(s, snap)
	if decision.Target == nil || decision.Target.Keep || decision.Target.WorstExceedance != 0 {
		t.Fatalf("SLO must beat cheap keep %+v", decision)
	}
	if decision.Keep.ExpectedJoules == nil || decision.Target.ExpectedJoules == nil || *decision.Target.ExpectedJoules <= *decision.Keep.ExpectedJoules {
		t.Fatalf("fixture must compare higher-energy feasible target %+v", decision)
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
	// Final resources fit, but old+new overlap while preparing does not.
	snap.Nodes[0].Reserved.CPUs = nil
	c = SchedulerCandidate{ID: "resize", Nodes: snap.Nodes, Instances: []SchedulerInstance{old}}
	c.Instances[0].ID = "resize-new"
	c.Actions = schedulerPlan(snap, c)
	snap.Costs = []OperationCost{{ID: "prep", Node: old.Node, Kind: schedulerActionPrepare, From: "unloaded", To: SchedulerInstanceState(old), Trials: []OperationTrial{{Duration: time.Millisecond, Joules: 1}}, EnergyScope: "whole-wall", Meter: "wall-n1", Scope: schedulerNodeWallScope, PowerMode: "fixed"}}
	c.Actions = schedulerPlan(snap, c)
	result := schedulerReplayForTest(s, snap, c)
	schedulerRequireReason(t, result.reasons, "TemporaryReadinessResourceInfeasible")
}

func TestSchedulerBoundsSeedUnknownOwnershipAndReturnPopulation(t *testing.T) {
	s, snap := schedulerFixture()
	s.MaxCandidates = 2
	snap.Options = append(snap.Options, snap.Options[0])
	snap.Options[1].Instance = schedulerTestCPU(snap.Options[1].Instance)
	first := EvaluateScheduler(s, snap)
	second := EvaluateScheduler(s, snap)
	if !reflect.DeepEqual(first, second) || len(first.Candidates) > 2 || !first.Search.Truncated || !first.Candidates[0].Keep {
		t.Fatalf("bound/seed/current reproducibility failed: %+v", first.Search)
	}
	snap.Outstanding = []SchedulerRequest{{ID: "unknown", Definition: snap.Workloads[0], ReceivedAt: snap.At, Phase: phaseAssigned, Instance: "i1"}}
	pending := EvaluateScheduler(s, snap)
	if pending.Target != nil {
		t.Fatal("assignment is not native execution proof")
	}
	schedulerRequireReason(t, pending.Keep.Reasons, "NativeOwnershipPending")
	s.Controls.Enabled = false
	snap.Instances[0].Return = 5 * time.Millisecond
	snap.Outstanding[0].Phase = "returning"
	snap.Outstanding[0].Elapsed = 12 * time.Millisecond
	snap.Outstanding[0].ReturnElapsed = 2 * time.Millisecond
	returned := EvaluateScheduler(s, snap)
	e := schedulerRequireEstimate(t, returned.Keep, snap.Workloads[0].UID)
	if e.ExpectedNanos == nil || *e.ExpectedNanos != float64(15*time.Millisecond) {
		t.Fatalf("return phase kept original age and remaining delivery: %+v", e)
	}
}

func TestSchedulerRejectedComputeAndUnknownCPUQuota(t *testing.T) {
	s, snap := schedulerFixture()
	s.Controls.Enabled = false
	snap.Arrivals = []SchedulerArrival{{ID: "capacity-rejected", At: snap.At.Add(-s.ObservationWindow), Definition: snap.Workloads[0], Compute: true, Outcome: "QueueFull"}}
	decision := EvaluateScheduler(s, snap)
	e := schedulerRequireEstimate(t, decision.Keep, snap.Workloads[0].UID)
	if decision.Search.Accepted != 0 || decision.Search.ComputeInputs != 1 || e.ExpectedNanos == nil || *e.ExpectedNanos != float64(10*time.Millisecond) {
		t.Fatalf("valid rejected demand must be replayed: %+v %+v", decision.Search, e)
	}
	snap.Nodes[0].NonServing = []SchedulerAllocation{{CPUCount: 1, MemoryBytes: 500}}
	unknown := EvaluateScheduler(s, snap)
	schedulerRequireReason(t, unknown.Keep.Reasons, "UnknownExclusiveCPUAffinity")
	snap.Nodes[0].Reserved.CPUs = []int{3}
	snap.Nodes[0].NonServing[0].CPUReserved = true
	covered := EvaluateScheduler(s, snap)
	if covered.Keep.ExpectedJoules == nil {
		t.Fatalf("explicit reserved coverage should restore evaluability %v", covered.Keep.Reasons)
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
	snap.Options = []SchedulerOption{{Instance: target, CompatibleUIDs: old.WorkloadUIDs}}
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
	s.Controls.Enabled = false
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

func TestSchedulerRoutingKeepsInputPathsSeparate(t *testing.T) {
	s, snap := schedulerFixture()
	s.Controls.Enabled = false
	d := snap.Workloads[0]
	path := d.Model.Paths[0]
	path.Name = schedulerTestWideInput
	path.Artifact = schedulerTestWideInput
	path.Tensors = []modelcompat.InputTensor{{Name: "x", Datatype: "FP32", SampleShape: []int32{2}}}
	d.Model.Paths = append(d.Model.Paths, path)
	wide := schedulerTestCPU(snap.Instances[0])
	wide.ID = schedulerTestWideInput
	wide.Profile.Condition.Artifact.Name = schedulerTestWideInput
	wide.Profile.Condition.Artifact.ExecutionPath = schedulerTestWideInput
	wide.Profile.Condition.Tensor.Tensors = path.Tensors
	wide.Profile.Batches = append([]BatchProfile(nil), wide.Profile.Batches...)
	wide.Profile.Batches[0].DurationNanos = []float64{float64(30 * time.Millisecond)}
	snap.Instances = append(snap.Instances, wide)
	snap.RoutingCredits = map[string]map[int]map[string]int64{d.UID: {0: {"i1": 100, schedulerTestWideInput: -100}, 1: {"i1": 100, schedulerTestWideInput: -100}}}
	snap.Outstanding = []SchedulerRequest{{ID: "narrow", QueueOrder: 0, Definition: d, InputPath: 0, ReceivedAt: snap.At, Phase: "pending"}, {ID: schedulerTestWideInput, QueueOrder: 1, Definition: d, InputPath: 1, ReceivedAt: snap.At, Phase: "pending"}}
	result := schedulerReplayForTest(s, snap, SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances})
	if len(result.reasons) > 0 || !reflect.DeepEqual(result.samples[d.Identity()], []float64{float64(10 * time.Millisecond), float64(30 * time.Millisecond)}) {
		t.Fatalf("credit cannot override actual tensor compatibility: %+v", result)
	}
	snap.Outstanding[1].InputPath = -1
	decision := EvaluateScheduler(s, snap)
	schedulerRequireReason(t, decision.Keep.Reasons, "UnknownInputRoute")
}

func TestSchedulerNativeQueueOrderSurvivesReceiptOrderAndUIDSort(t *testing.T) {
	s, snap := schedulerFixture()
	s.Controls.Enabled = false
	d := snap.Workloads[0]
	later := *d
	later.UID = "a"
	later.Name = schedulerTestOther
	snap.Workloads = append(snap.Workloads, &later)
	snap.Instances[0].WorkloadUIDs = append(snap.Instances[0].WorkloadUIDs, later.UID)
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
	s.Controls.Enabled = false
	d := snap.Workloads[0]
	life := 5 * time.Millisecond
	snap.Outstanding = []SchedulerRequest{{ID: "first", QueueOrder: 0, RemainingLifetime: &life, Definition: d, ReceivedAt: snap.At, Phase: "queued", Instance: "i1"}, {ID: "second", QueueOrder: 1, RemainingLifetime: &life, Definition: d, ReceivedAt: snap.At, Phase: "queued", Instance: "i1"}}
	zero := 0.0
	for j := range snap.Outstanding {
		snap.Outstanding[j].NativeSenderElapsedMS = &zero
	}
	decision := EvaluateScheduler(s, snap)
	e := schedulerRequireEstimate(t, decision.Keep, d.UID)
	if decision.Status != schedulerStatusLifetime || decision.Target != nil || e.ExpectedNanos != nil || e.Samples != 0 || e.PredictedOutcomes["expired-running"] != s.Replays || e.PredictedOutcomes["expired-queued"] != s.Replays || decision.Keep.ExpectedJoules == nil || math.Abs(*decision.Keep.ExpectedJoules-2.2) > 1e-9 {
		t.Fatalf("expired running work stays physical; queued expires; no successful fake zero percentile: %+v", decision)
	}
	snap.Outstanding = nil
	snap.Arrivals = []SchedulerArrival{{ID: "trace", At: snap.At.Add(-s.ObservationWindow), Definition: d, Compute: true, Lifetime: life}}
	projected := EvaluateScheduler(s, snap)
	pe := schedulerRequireEstimate(t, projected.Keep, d.UID)
	if pe.PredictedOutcomes["expired-running"] != s.Replays {
		t.Fatalf("arrival lifetime must freeze into repeated demand: %+v", pe)
	}
	snap.Arrivals[0].Lifetime = 0
	d.SLO.TargetLatency = time.Millisecond
	unlimited := EvaluateScheduler(s, snap)
	ue := schedulerRequireEstimate(t, unlimited.Keep, d.UID)
	if unlimited.Status != schedulerStatusOverload || ue.ExpectedNanos == nil || ue.Samples != s.Replays || ue.PredictedOutcomes["expired-running"] != 0 {
		t.Fatalf("SLO is never a request lifetime: %+v", unlimited)
	}
}

func TestSchedulerFiniteHorizonEnergyClipsExecutionAndOperation(t *testing.T) {
	s, snap := schedulerFixture()
	s.Controls.Enabled = false
	d := snap.Workloads[0]
	for j := 0; j < 3; j++ {
		snap.Arrivals = append(snap.Arrivals, SchedulerArrival{ID: strings.Repeat("x", j+1), At: snap.At.Add(-s.ObservationWindow + 95*time.Millisecond), Definition: d, Accepted: true})
	}
	decision := EvaluateScheduler(s, snap)
	e := schedulerRequireEstimate(t, decision.Keep, d.UID)
	if decision.Keep.ExpectedJoules == nil || math.Abs(*decision.Keep.ExpectedJoules-1.7) > 1e-9 || e.ExpectedNanos == nil || *e.ExpectedNanos != float64(30*time.Millisecond) {
		t.Fatalf("only half first batch overlaps H; later SLO completions still included: %+v", decision.Keep)
	}
	snap.Arrivals = nil
	s.Horizon = time.Millisecond
	snap.Outstanding = []SchedulerRequest{{ID: phaseRunning, Definition: d, ReceivedAt: snap.At.Add(-7 * time.Millisecond), Elapsed: 7 * time.Millisecond, RunningElapsed: 7 * time.Millisecond, Phase: phaseRunning, Instance: "i1", BatchID: "actual"}}
	running := EvaluateScheduler(s, snap)
	if running.Keep.ExpectedJoules == nil || math.Abs(*running.Keep.ExpectedJoules-0.112) > 1e-9 {
		t.Fatalf("running execution overlap is 1/10 batch plus 1ms baseline: %+v", running.Keep)
	}
	snap.Outstanding = nil
	s.Horizon = 5 * time.Millisecond
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
	old.Status = phaseRunning
	old.Elapsed = time.Millisecond
	snap.Actions = []Action{old}
	continued := schedulerPlan(snap, c)
	found := false
	for _, a := range continued {
		if a.Kind == schedulerActionPrepare {
			found = true
			if a.ID != old.ID || a.Elapsed != old.Elapsed || a.Status != phaseRunning {
				t.Fatalf("actual active action must retain identity/progress: %+v", continued)
			}
		}
	}
	if !found {
		t.Fatal("missing continuation")
	}
}

func TestSchedulerCurrentBatchPolicyUsesFrozenNativeSenderContext(t *testing.T) {
	s, snap := schedulerFixture()
	s.Controls.Enabled = false
	a := snap.Workloads[0]
	b := *a
	b.UID = "b"
	b.Name = schedulerTestOther
	snap.Workloads = append(snap.Workloads, &b)
	snap.Instances[0].WorkloadUIDs = append(snap.Instances[0].WorkloadUIDs, b.UID)
	zero, forty := 0.0, 40.0
	snap.Outstanding = []SchedulerRequest{{ID: "a", Definition: a, ReceivedAt: snap.At.Add(-80 * time.Millisecond), Elapsed: 80 * time.Millisecond, QueueAge: 10 * time.Millisecond, QueueOrder: 0, Phase: "queued", Instance: "i1", NativeSenderElapsedMS: &zero}, {ID: "b", Definition: &b, ReceivedAt: snap.At.Add(-50 * time.Millisecond), Elapsed: 50 * time.Millisecond, QueueOrder: 1, Phase: "queued", Instance: "i1", NativeSenderElapsedMS: &forty}}
	decision := EvaluateScheduler(s, snap)
	ae := schedulerRequireEstimate(t, decision.Keep, a.UID)
	be := schedulerRequireEstimate(t, decision.Keep, b.UID)
	if ae.ExpectedNanos == nil || be.ExpectedNanos == nil || *ae.ExpectedNanos != float64(100*time.Millisecond) || *be.ExpectedNanos != float64(60*time.Millisecond) {
		t.Fatalf("actor policy uses real sender context, not reconstructed total age: %+v", decision.Keep)
	}
	snap.Outstanding[0].NativeSenderElapsedMS = nil
	missing := EvaluateScheduler(s, snap)
	schedulerRequireReason(t, missing.Keep.Reasons, "MissingNativeSenderContext")
}

func TestSchedulerDerivedBaselinePreservesMeasuredSignedIdleDelta(t *testing.T) {
	s, snap := schedulerFixture()
	s.Controls.Enabled = false
	snap.Instances[0].Profile.IdleBaselineWatts = 8
	decision := EvaluateScheduler(s, snap)
	if decision.Keep.ExpectedJoules == nil || math.Abs(*decision.Keep.ExpectedJoules-0.8) > 1e-9 {
		t.Fatalf("derived unloaded + measured signed loaded-idle delta must retain exact subtraction: %+v", decision.Keep)
	}
	snap.Instances[0].Profile.IdleBaselineWatts = 1
	other := schedulerTestCPU(snap.Instances[0])
	other.ID = schedulerTestOther
	snap.Instances = append(snap.Instances, other)
	outside := EvaluateScheduler(s, snap)
	schedulerRequireReason(t, outside.Keep.Reasons, "DerivedNodeBaselineOutsidePhysicalRange")
}

func TestSchedulerEnergyOverflowRemainsUnknownNotJSONInfinity(t *testing.T) {
	s, snap := schedulerFixture()
	s.Controls.Enabled = false
	d := snap.Workloads[0]
	snap.Instances[0].Profile.Batches[0].EnergyPerInputJoules = math.MaxFloat64
	snap.Arrivals = []SchedulerArrival{{ID: "one", At: snap.At.Add(-s.ObservationWindow), Definition: d, Compute: true}, {ID: "two", At: snap.At.Add(-s.ObservationWindow + time.Millisecond), Definition: d, Compute: true}}
	decision := EvaluateScheduler(s, snap)
	if decision.Keep.ExpectedJoules != nil || decision.Target != nil {
		t.Fatalf("unrepresentable expected energy must remain ineligible: %+v", decision)
	}
	schedulerRequireReason(t, decision.Keep.Reasons, "ExpectedEnergyOutsidePhysicalNumericalRange")
}
