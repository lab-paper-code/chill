package serving

import (
	"context"
	"errors"
	"fmt"
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
	schedulerTestNewInstance = "new"
	schedulerTestOther       = "other"
	schedulerTestEcoMode     = "eco"
)

func schedulerFixture() (SchedulerSettings, SchedulerSnapshot) {
	at := time.Unix(100, 0)
	s := SchedulerSettings{Algorithm: SchedulerAlgorithmFixedHorizon, ObservationWindow: 100 * time.Millisecond, ControlInterval: time.Second, Horizon: 100 * time.Millisecond, MaxObservationAge: time.Second, Seed: 42, Replays: 3, MaxCandidates: 30, MaxReplicas: 2, MaxInstances: 4, MaxReplayRequests: 100, MaxCPUs: 4, Controls: SchedulerControls{Enabled: true, Placement: true, Resources: true, Replicas: true, Sharing: true, Routing: true, Batching: true, Power: true, Membership: true}}
	node := SchedulerNode{Node: NodeReference{Name: "node1", UID: "n1"}, Pool: "pool", PowerMode: "fixed", Ready: true, CPUs: []int{0, 1, 2, 3}, MemoryBytes: 10000, ObservedAt: at}
	image := "registry/model@sha256:" + strings.Repeat("a", 64)
	settings := &modelcompat.FirstfloorSettings{MaxBatchSize: 1, MaxQueueSize: 100}
	path := ModelPath{Name: "cpu", Artifact: "model", Files: []string{"/model"}, Format: "onnx", Image: image, RuntimeFamily: "firstfloor", Backend: "ort-cpu", Firstfloor: settings}
	d := &Definition{Name: "workload", UID: "w1", Generation: 1, Model: Model{Name: "model", UID: "m", Generation: 1, Paths: []ModelPath{path}}, Preprocessor: Preprocessor{Name: "pre", Input: []RawField{{Name: "x", Format: "UTF8", MaxBytes: 16}}, Output: []RawField{{Name: "y", Format: "UTF8"}}}, Dataset: &DatasetReference{Name: "data", UID: "data-uid"}, SLO: SLO{Percentile: 100, TargetLatency: 100 * time.Millisecond}}
	p := &Profile{ID: "profile", NodeName: node.Node.Name, Condition: derivation.CurvePlanSpec{LogicalModel: "model", NodeUID: node.Node.UID, DeviceClass: "cpu", RuntimeFamily: "firstfloor", Backend: "ort-cpu", RuntimeImage: image, Firstfloor: settings, PowerMode: "fixed", CPU: derivation.CurveCPUContract{Policy: "quota-affinity", PolicyVersion: "v1", Request: "1", Limit: "1", ORTIntraOpThreads: 1, ORTInterOpThreads: 1, AffinityCPUs: []int{0}}, PlannedBatchSizes: []int{1}, Preprocessor: derivation.CurvePreprocessorContract{Name: "pre", Inputs: []string{"x"}, Outputs: []string{"y"}}, ActiveProtocol: derivation.CurveProtocolContract{Dataset: &derivation.CurveDatasetContract{Name: "data", UID: "data-uid"}}, Artifact: derivation.CurveArtifactContract{Name: "model", ExecutionPath: "cpu", Files: []string{"/model"}, Image: image}}, IdleBaselineWatts: 12, MeasuredAt: at, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "curve", Size: 1}, Evidence: []evidencestore.ObjectIdentity{{Store: "raw", Key: "profile", Size: 1}}, Batches: []BatchProfile{{Batch: 1, DurationNanos: []float64{float64(10 * time.Millisecond)}, EnergyPerInputJoules: 1, MemoryBytes: 100}}}
	p.RuntimeMetadata = &firstfloor.RuntimeMetadata{Runtime: "onnxruntime", RuntimeVersion: "1.19.0", ModelFiles: []string{"/model/model"}, Inputs: []string{"x"}, Outputs: []string{"y"}, Providers: []string{"CPUExecutionProvider"}, Device: "cpu", CPUAffinity: []int{0}, IntraOpThreads: 1, InterOpThreads: 1, InitializedBatches: []int{1}, WarmupIterations: 1}
	i := SchedulerInstance{ID: "i1", Node: node.Node, Pool: node.Pool, Profile: p, WorkloadUIDs: []string{d.UID}, Allocation: SchedulerAllocation{CPUs: []int{0}, Threads: 1, MemoryBytes: 1000}, Ready: true, Capacity: 100, Weight: 1, MaxBatch: 1, EnvelopeSource: "measured transport", CompletionObserved: true, ObservedAt: at, IdleMeter: "wall-n1", IdleScope: schedulerNodeWallScope}
	i = schedulerMeasuredInstance(i, d, 0)
	b := NodeBaseline{ID: "unloaded-n1", Node: node.Node, PowerMode: "fixed", Watts: 10, Meter: "wall-n1", Scope: schedulerNodeWallScope, MeasuredAt: at, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "idle"}}
	b.Reference.Size = 1
	p.Evidence[0].Size = 1
	snap := SchedulerSnapshot{At: at, ObservationEnd: at, Workloads: []*Definition{d}, Nodes: []SchedulerNode{node}, Instances: []SchedulerInstance{i}, Options: []SchedulerOption{{Instance: i, CompatibleUIDs: []string{d.UID}}}, Baselines: []NodeBaseline{b}}
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
func schedulerEvaluateForTest(t testing.TB, settings SchedulerSettings, snapshot SchedulerSnapshot) SchedulerDecision {
	t.Helper()
	decision, err := EvaluateScheduler(context.Background(), settings, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return decision
}

func schedulerEnumerateForTest(t testing.TB, settings SchedulerSettings, snapshot SchedulerSnapshot) ([]SchedulerCandidate, SchedulerSearch) {
	t.Helper()
	candidates, search, err := EnumerateScheduler(context.Background(), settings, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return candidates, search
}

func schedulerReplayForTest(s SchedulerSettings, snap SchedulerSnapshot, c SchedulerCandidate) schedulerReplayResult {
	result, err := schedulerReplay(context.Background(), s, snap, c, rand.New(rand.NewPCG(uint64(s.Seed), 0)))
	if err != nil {
		panic(err)
	}
	return result
}

// schedulerMeasuredInstance supplies a new completed native trial for an
// explicitly measured fixture condition; it never rebinds an old typed proof.
func schedulerMeasuredInstance(i SchedulerInstance, d *Definition, completion time.Duration) SchedulerInstance {
	at := i.ObservedAt
	exchange := time.Millisecond + completion
	fact := &overheadFact{}
	fact.add(&RequestEvent{Kind: eventBackendExchangeFinished, WorkloadUID: d.UID, Definition: d.Identity(), Assignment: &Assignment{Instance: i.ID, Node: i.Node, Profile: i.Profile.ID, Artifact: i.Profile.Condition.Artifact.Name, ExecutionPath: i.Profile.Condition.Artifact.ExecutionPath}, NativeProgress: &BackendProgress{ProfileID: i.Profile.ID, Phase: phaseFinished, ExecutionComplete: true, NativeElapsedMS: 1}, ClientSpanNanos: int64(time.Millisecond), ElapsedNanos: int64(exchange)})
	fact.add(&RequestEvent{Kind: eventResponseFinished, ElapsedNanos: int64(exchange)})
	measurement := overheadProjection(map[string]*overheadFact{"fixture-trial": fact}, map[string]*Definition{d.Identity(): d}, at.Add(-time.Second), at, nil)[i.ID]
	i.CompletionMeasurement = &measurement
	i.CompletionOverhead, i.EnvelopeSource, i.CompletionObserved = measurement.Completion, measurement.Source, measurement.Valid()
	return i
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
	for _, d := range i.CompletionMeasurement.Definitions {
		return schedulerMeasuredInstance(i, d, i.CompletionOverhead)
	}
	return i
}

func TestSchedulerWindowPopulationAndZeroSample(t *testing.T) {
	s, snap := schedulerFixture()
	s.Controls.Enabled = false
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
	decision := schedulerEvaluateForTest(t, s, snap)
	if decision.Search.Received != 2 || decision.Search.Accepted != 1 || decision.Search.ComputeInputs != 1 {
		t.Fatalf("wrong population %+v", decision.Search)
	}
	e := schedulerRequireEstimate(t, decision.Keep, d.UID)
	if e.Samples != 3*s.Replays || e.ExpectedNanos == nil || *e.ExpectedNanos < float64(time.Second) {
		t.Fatalf("outstanding frozen timing lost: %+v", e)
	}
	snap.Arrivals = nil
	snap.Outstanding = nil
	empty := schedulerEvaluateForTest(t, s, snap)
	e = schedulerRequireEstimate(t, empty.Keep, d.UID)
	if e.Samples != 0 || e.ExpectedNanos != nil {
		t.Fatalf("empty percentile must be null: %+v", e)
	}
	if empty.Keep.ExpectedJoules == nil || math.Abs(*empty.Keep.ExpectedJoules-1.2) > 1e-9 {
		t.Fatalf("empty serving still consumes physical idle: %+v", empty.Keep)
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
			decision := schedulerEvaluateForTest(t, settings, snapshot)
			schedulerRequireReason(t, decision.Keep.Reasons, reason)
			if decision.Target != nil || decision.Keep.ExpectedJoules != nil {
				t.Fatalf("unknown, future or stale observation became actionable: %+v", decision)
			}
		})
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
	snap.Outstanding[0].NativeSenderElapsedMS = &zero
	snap.Outstanding[1].NativeSenderElapsedMS = &zero
	decision := schedulerEvaluateForTest(t, s, snap)
	e := schedulerRequireEstimate(t, decision.Keep, b.UID)
	if e.ExpectedNanos == nil || *e.ExpectedNanos != float64(160*time.Millisecond) || e.NormalizedExceedance == 0 {
		t.Fatalf("cross UID running batch must block B for conditioned 60ms then service 100ms: %+v, reasons=%v", e, decision.Keep.Reasons)
	}
	if got := schedulerRequireEstimate(t, decision.Keep, a.UID); got.ExpectedNanos == nil || *got.ExpectedNanos != float64(100*time.Millisecond) {
		t.Fatalf("frozen running elapsed lost %+v", got)
	}
	snap.Outstanding[0].RunningElapsed = 100 * time.Millisecond
	pending := schedulerEvaluateForTest(t, s, snap)
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
	decision := schedulerEvaluateForTest(t, s, snap)
	if decision.Keep.ExpectedJoules == nil || math.Abs(*decision.Keep.ExpectedJoules-3.2) > 1e-9 {
		t.Fatalf("shared model once + whole inactive node once: %+v", decision.Keep)
	}
	// A measured coexistence baseline replaces, rather than adds to, derived idle.
	b = snap.Baselines[0]
	b.ID = "matched"
	b.State = []string{SchedulerInstanceState(snap.Instances[0])}
	b.Watts = 15
	snap.Baselines = append(snap.Baselines, b)
	decision = schedulerEvaluateForTest(t, s, snap)
	if decision.Keep.ExpectedJoules == nil || math.Abs(*decision.Keep.ExpectedJoules-3.5) > 1e-9 {
		t.Fatalf("matching whole state must win: %+v", decision.Keep)
	}
	snap.Baselines[1].Meter = "wall-n1"
	pending := schedulerEvaluateForTest(t, s, snap)
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
	target = schedulerMeasuredInstance(target, d, 0)
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
	decision := schedulerEvaluateForTest(t, s, snap)
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

func TestSchedulerBoundsSeedUnknownOwnershipAndReturnPopulation(t *testing.T) {
	s, snap := schedulerFixture()
	s.MaxCandidates = 2
	snap.Options = append(snap.Options, snap.Options[0])
	snap.Options[1].Instance = schedulerTestCPU(snap.Options[1].Instance)
	first := schedulerEvaluateForTest(t, s, snap)
	second := schedulerEvaluateForTest(t, s, snap)
	if !reflect.DeepEqual(first, second) || len(first.Candidates) > 2 || !first.Search.Truncated || !first.Candidates[0].Keep {
		t.Fatalf("bound/seed/current reproducibility failed: %+v", first.Search)
	}
	snap.Outstanding = []SchedulerRequest{{ID: "unknown", Definition: snap.Workloads[0], ReceivedAt: snap.At, Phase: phaseAssigned, Instance: "i1"}}
	pending := schedulerEvaluateForTest(t, s, snap)
	if pending.Target != nil {
		t.Fatal("assignment is not native execution proof")
	}
	schedulerRequireReason(t, pending.Keep.Reasons, "NativeOwnershipPending")
	s.Controls.Enabled = false
	snap.Instances[0] = schedulerMeasuredInstance(snap.Instances[0], snap.Workloads[0], 5*time.Millisecond)
	call := &Call{
		receipt:            Receipt{ID: "returning", UID: snap.Workloads[0].UID, ReceivedAt: snap.At.Add(-10 * time.Millisecond)},
		definition:         snap.Workloads[0],
		assignment:         &Assignment{Instance: "i1"},
		backendDone:        true,
		exchangeFinishedAt: snap.At.Add(-2 * time.Millisecond),
		clientSpan:         8 * time.Millisecond,
		progress:           &BackendProgress{Phase: "finished", ExecutionComplete: true, NativeElapsedMS: 6},
	}
	runtime := &Runtime{calls: map[string]*Call{"returning": call}}
	clock := func() time.Time { return snap.At }
	current := runtime.SchedulerInputs(clock)
	snap.At, snap.Outstanding = current.At, current.Outstanding
	returned := schedulerEvaluateForTest(t, s, snap)
	e := schedulerRequireEstimate(t, returned.Keep, snap.Workloads[0].UID)
	if e.ExpectedNanos == nil || *e.ExpectedNanos != float64(11*time.Millisecond) {
		t.Fatalf("original age already includes 2ms receipt-to-exchange residual and 2ms response tail; only 1ms of joint 5ms remains: %+v", e)
	}
	// A measured zero residual is evidence, not a missing observation.
	call.progress.NativeElapsedMS = 8
	current = runtime.SchedulerInputs(clock)
	snap.At, snap.Outstanding = current.At, current.Outstanding
	zero := schedulerEvaluateForTest(t, s, snap)
	e = schedulerRequireEstimate(t, zero.Keep, snap.Workloads[0].UID)
	if e.ExpectedNanos == nil || *e.ExpectedNanos != float64(13*time.Millisecond) {
		t.Fatalf("zero exchange residual must retain the remaining response tail: %+v", e)
	}
	call.progress.NativeElapsedMS = 6
	for _, clientSpan := range []time.Duration{0, 5 * time.Millisecond} {
		call.clientSpan = clientSpan
		current = runtime.SchedulerInputs(clock)
		snap.At, snap.Outstanding = current.At, current.Outstanding
		missing := schedulerEvaluateForTest(t, s, snap)
		if missing.Target != nil || snap.Outstanding[0].ExchangeResidual != nil {
			t.Fatalf("missing or negative exchange residual must not become completion evidence: %+v", missing)
		}
		schedulerRequireReason(t, missing.Keep.Reasons, "MissingReturningCompletionEvidence")
	}
}

func TestSchedulerRejectedComputeAndUnknownCPUQuota(t *testing.T) {
	s, snap := schedulerFixture()
	s.Controls.Enabled = false
	snap.Arrivals = []SchedulerArrival{{ID: "capacity-rejected", At: snap.At.Add(-s.ObservationWindow), Definition: snap.Workloads[0], Compute: true, Outcome: "QueueFull"}}
	decision := schedulerEvaluateForTest(t, s, snap)
	e := schedulerRequireEstimate(t, decision.Keep, snap.Workloads[0].UID)
	if decision.Search.Accepted != 0 || decision.Search.ComputeInputs != 1 || e.ExpectedNanos == nil || *e.ExpectedNanos != float64(10*time.Millisecond) {
		t.Fatalf("valid rejected demand must be replayed: %+v %+v", decision.Search, e)
	}
	snap.Nodes[0].NonServing = []SchedulerAllocation{{CPUCount: 1, MemoryBytes: 500}}
	unknown := schedulerEvaluateForTest(t, s, snap)
	schedulerRequireReason(t, unknown.Keep.Reasons, "UnknownExclusiveCPUAffinity")
	snap.Nodes[0].Reserved.CPUs = []int{3}
	snap.Nodes[0].NonServing[0].CPUReserved = true
	covered := schedulerEvaluateForTest(t, s, snap)
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
	target = schedulerMeasuredInstance(target, &updated, 0)
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
	target = schedulerMeasuredInstance(target, snap.Workloads[0], 0)
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
	decision := schedulerEvaluateForTest(t, s, snap)
	e := schedulerRequireEstimate(t, decision.Keep, d.UID)
	if decision.Status != schedulerStatusLifetime || decision.Target != nil || e.ExpectedNanos != nil || e.Samples != 0 || e.PredictedOutcomes["expired-running"] != s.Replays || e.PredictedOutcomes["expired-queued"] != s.Replays || decision.Keep.ExpectedJoules == nil || math.Abs(*decision.Keep.ExpectedJoules-2.2) > 1e-9 {
		t.Fatalf("expired running work stays physical; queued expires; no successful fake zero percentile: %+v", decision)
	}
	snap.Outstanding = nil
	snap.Arrivals = []SchedulerArrival{{ID: "trace", At: snap.At.Add(-s.ObservationWindow), Definition: d, Compute: true, Lifetime: life}}
	projected := schedulerEvaluateForTest(t, s, snap)
	pe := schedulerRequireEstimate(t, projected.Keep, d.UID)
	if pe.PredictedOutcomes["expired-running"] != s.Replays {
		t.Fatalf("arrival lifetime must freeze into repeated demand: %+v", pe)
	}
	snap.Arrivals[0].Lifetime = 0
	d.SLO.TargetLatency = time.Millisecond
	unlimited := schedulerEvaluateForTest(t, s, snap)
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
	decision := schedulerEvaluateForTest(t, s, snap)
	e := schedulerRequireEstimate(t, decision.Keep, d.UID)
	if decision.Keep.ExpectedJoules == nil || math.Abs(*decision.Keep.ExpectedJoules-1.7) > 1e-9 || e.ExpectedNanos == nil || *e.ExpectedNanos != float64(30*time.Millisecond) {
		t.Fatalf("only half first batch overlaps H; later SLO completions still included: %+v", decision.Keep)
	}
	snap.Arrivals = nil
	s.Horizon = time.Millisecond
	zeroSender := 0.0
	snap.Outstanding = []SchedulerRequest{{ID: phaseRunning, Definition: d, ReceivedAt: snap.At.Add(-7 * time.Millisecond), Elapsed: 7 * time.Millisecond, RunningElapsed: 7 * time.Millisecond, Phase: phaseRunning, Instance: "i1", BatchID: "actual"}}
	snap.Outstanding[0].NativeSenderElapsedMS = &zeroSender
	running := schedulerEvaluateForTest(t, s, snap)
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
	decision := schedulerEvaluateForTest(t, s, snap)
	ae := schedulerRequireEstimate(t, decision.Keep, a.UID)
	be := schedulerRequireEstimate(t, decision.Keep, b.UID)
	if ae.ExpectedNanos == nil || be.ExpectedNanos == nil || *ae.ExpectedNanos != float64(100*time.Millisecond) || *be.ExpectedNanos != float64(60*time.Millisecond) {
		t.Fatalf("actor policy uses real sender context, not reconstructed total age: %+v", decision.Keep)
	}
	snap.Outstanding[0].NativeSenderElapsedMS = nil
	missing := schedulerEvaluateForTest(t, s, snap)
	schedulerRequireReason(t, missing.Keep.Reasons, "MissingNativeSenderContext")
}

func TestSchedulerDerivedBaselinePreservesMeasuredSignedIdleDelta(t *testing.T) {
	s, snap := schedulerFixture()
	s.Controls.Enabled = false
	snap.Instances[0].Profile.IdleBaselineWatts = 8
	decision := schedulerEvaluateForTest(t, s, snap)
	if decision.Keep.ExpectedJoules == nil || math.Abs(*decision.Keep.ExpectedJoules-0.8) > 1e-9 {
		t.Fatalf("derived unloaded + measured signed loaded-idle delta must retain exact subtraction: %+v", decision.Keep)
	}
	snap.Instances[0].Profile.IdleBaselineWatts = 1
	other := schedulerTestCPU(snap.Instances[0])
	other.ID = schedulerTestOther
	snap.Instances = append(snap.Instances, other)
	outside := schedulerEvaluateForTest(t, s, snap)
	schedulerRequireReason(t, outside.Keep.Reasons, "DerivedNodeBaselineOutsidePhysicalRange")
}

func TestSchedulerEnergyOverflowRemainsUnknownNotJSONInfinity(t *testing.T) {
	s, snap := schedulerFixture()
	s.Controls.Enabled = false
	d := snap.Workloads[0]
	snap.Instances[0].Profile.Batches[0].EnergyPerInputJoules = math.MaxFloat64
	snap.Arrivals = []SchedulerArrival{{ID: "one", At: snap.At.Add(-s.ObservationWindow), Definition: d, Compute: true}, {ID: "two", At: snap.At.Add(-s.ObservationWindow + time.Millisecond), Definition: d, Compute: true}}
	decision := schedulerEvaluateForTest(t, s, snap)
	if decision.Keep.ExpectedJoules != nil || decision.Target != nil {
		t.Fatalf("unrepresentable expected energy must remain ineligible: %+v", decision)
	}
	schedulerRequireReason(t, decision.Keep.Reasons, "ExpectedEnergyOutsidePhysicalNumericalRange")
}

func TestSchedulerUnobservedCompletionOverheadIsNotSLOQualified(t *testing.T) {
	s, snap := schedulerFixture()
	snap.Instances[0].CompletionObserved = false
	decision := schedulerEvaluateForTest(t, s, snap)
	if decision.Keep.ExpectedJoules != nil || decision.Target != nil || decision.Keep.Status != schedulerStatusPending {
		t.Fatalf("bootstrap completion overhead must not qualify SLO evaluation: %+v", decision)
	}
	schedulerRequireReason(t, decision.Keep.Reasons, "CompletionOverheadUnobserved:i1")
	// A scalar flag without a boundary-validated typed fact is still unobserved.
	measurement := snap.Instances[0].CompletionMeasurement
	snap.Instances[0].CompletionMeasurement = nil
	snap.Instances[0].CompletionObserved = true
	decision = schedulerEvaluateForTest(t, s, snap)
	schedulerRequireReason(t, decision.Keep.Reasons, "CompletionOverheadUnobserved:i1")
	snap.Instances[0].CompletionMeasurement = measurement
	snap.Instances[0].CompletionObserved = true
	decision = schedulerEvaluateForTest(t, s, snap)
	for _, r := range decision.Keep.Reasons {
		if strings.HasPrefix(r, "CompletionOverheadUnobserved") {
			t.Fatalf("observed completion overhead still unqualified: %v", decision.Keep.Reasons)
		}
	}
	if decision.Keep.ExpectedJoules == nil {
		t.Fatalf("observed instance must be evaluable: %+v", decision.Keep.Reasons)
	}
}

func TestSchedulerCompletionProofMustRemainBoundToConsumedQuantityAndCondition(t *testing.T) {
	for _, condition := range []string{"quantity", "node", "profile", "runtime", "CPU", "threads", "GPU", "artifact", "preprocessor", "dataset"} {
		t.Run(condition, func(t *testing.T) {
			settings, snapshot := schedulerFixture()
			settings.Controls.Enabled = false
			snapshot.Options = nil
			instance := &snapshot.Instances[0]
			profile := *instance.Profile
			instance.Profile = &profile
			switch condition {
			case "quantity":
				instance.CompletionOverhead += time.Nanosecond
			case "node":
				instance.Node.UID = "replacement-node"
			case "profile":
				profile.ID = "replacement-profile"
			case "runtime":
				profile.Condition.Backend = "unmeasured-runtime"
			case "CPU":
				instance.Allocation.CPUs = []int{1}
			case "threads":
				instance.Allocation.Threads = 2
			case "GPU":
				instance.Allocation.GPUUUID = "unmeasured-GPU"
			case "artifact":
				profile.Condition.Artifact.Name = "unmeasured-artifact"
			case "preprocessor":
				profile.Condition.Preprocessor.Inputs = []string{"unmeasured-field"}
			case "dataset":
				profile.Condition.ActiveProtocol.Dataset = &derivation.CurveDatasetContract{Name: "data", UID: "replaced-data-uid"}
			}
			if !instance.CompletionObserved || !instance.CompletionMeasurement.Valid() {
				t.Fatal("mutation regression must retain the original proof and scalar flag")
			}
			decision := schedulerEvaluateForTest(t, settings, snapshot)
			if condition != "node" {
				schedulerRequireReason(t, decision.Keep.Reasons, "CompletionOverheadUnobserved:i1")
			}
			if decision.Keep.Status != schedulerStatusPending || decision.Keep.ExpectedJoules != nil || decision.Target != nil {
				t.Fatalf("mutated consumed measurement condition must remain Pending: %+v", decision.Keep)
			}
		})
	}
}

func TestSchedulerQuarantinedUnusedNodeDoesNotBlockKnownCPU(t *testing.T) {
	settings, snapshot := schedulerFixture()
	settings.Controls.Enabled = false
	snapshot.Options = nil
	quarantined := snapshot.Nodes[0]
	quarantined.Node = NodeReference{Name: "quarantined-gpu", UID: "gpu-node"}
	quarantined.Ready, quarantined.Reason = false, "non-serving physical GPU ownership unobserved"
	snapshot.Nodes = append(snapshot.Nodes, quarantined)
	baseline := snapshot.Baselines[0]
	baseline.ID, baseline.Node, baseline.Meter, baseline.Watts = "gpu-idle", quarantined.Node, "wall-gpu-node", 20
	baseline.Reference.Key = baseline.ID
	snapshot.Baselines = append(snapshot.Baselines, baseline)
	known := schedulerEvaluateForTest(t, settings, snapshot)
	if known.Keep.Status == schedulerStatusPending || known.Keep.ExpectedJoules == nil ||
		math.Abs(*known.Keep.ExpectedJoules-3.2) > 1e-9 {
		t.Fatalf("unused quarantined node blocked known CPU or lost whole-POOL energy: %+v", known.Keep)
	}
	if known.Keep.Nodes[1].Ready || known.Keep.Nodes[1].Reason == "" {
		t.Fatal("evaluation hid the quarantined Node's actual eligibility")
	}
	instance := snapshot.Instances[0]
	profile := *instance.Profile
	profile.ID, profile.NodeName, profile.Condition.NodeUID = "gpu-node-profile", quarantined.Node.Name, quarantined.Node.UID
	instance.Node, instance.Profile, instance.IdleMeter = quarantined.Node, &profile, baseline.Meter
	snapshot.Instances[0] = schedulerMeasuredInstance(instance, snapshot.Workloads[0], 0)
	selected := schedulerEvaluateForTest(t, settings, snapshot)
	if selected.Keep.Status != schedulerStatusPending || selected.Keep.ExpectedJoules != nil {
		t.Fatalf("candidate using the quarantined Node became evaluable: %+v", selected.Keep)
	}
}

func TestSchedulerCanceledComputationReturnsContextError(t *testing.T) {
	settings, snapshot, source, target := schedulerProjectionFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if decision, err := EvaluateScheduler(ctx, settings, snapshot); !errors.Is(err, context.Canceled) || decision.Target != nil {
		t.Fatalf("canceled evaluation returned decision=%+v err=%v", decision, err)
	}
	if projection, err := ProjectSchedulerTransition(ctx, settings, snapshot, source, target); !errors.Is(err, context.Canceled) || projection.ExpectedJoules != nil {
		t.Fatalf("canceled projection returned projection=%+v err=%v", projection, err)
	}
	if kernels, err := ProjectSchedulerWorkKernels(ctx, settings, snapshot, map[string]SchedulerCandidate{"source": source}, map[string]float64{"source": 0}); !errors.Is(err, context.Canceled) || kernels != nil {
		t.Fatalf("canceled kernels returned kernels=%+v err=%v", kernels, err)
	}
}

func TestSchedulerCPUComputationHonorsDeadline(t *testing.T) {
	for _, stage := range []string{"evaluation", "projection", "kernels"} {
		t.Run(stage, func(t *testing.T) {
			settings, snapshot, source, _ := schedulerProjectionFixture()
			settings.Replays = 1 << 30
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			var err error
			switch stage {
			case "evaluation":
				_, err = EvaluateScheduler(ctx, settings, snapshot)
			case "projection":
				_, err = ProjectSchedulerTransition(ctx, settings, snapshot, source, source)
			case "kernels":
				var kernels map[string]map[string]SchedulerTransitionProjection
				kernels, err = ProjectSchedulerWorkKernels(ctx, settings, snapshot, map[string]SchedulerCandidate{"source": source}, map[string]float64{"source": 0})
				if kernels != nil {
					t.Fatal("canceled kernel computation exposed partial history inputs")
				}
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("CPU %s did not propagate deadline: %v", stage, err)
			}
		})
	}
}

func TestSchedulerEnumerationHonorsInFlightDeadline(t *testing.T) {
	settings, snapshot := schedulerFixture()
	settings.MaxCandidates = 1 << 30
	settings.MaxReplicas, settings.MaxInstances = 1, 21
	for index := range 20 {
		definition := *snapshot.Workloads[0]
		definition.Name, definition.UID = fmt.Sprintf("workload-%02d", index), fmt.Sprintf("uid-%02d", index)
		snapshot.Workloads = append(snapshot.Workloads, &definition)
		snapshot.Options[0].CompatibleUIDs = append(snapshot.Options[0].CompatibleUIDs, definition.UID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := ctx.Err(); err != nil {
		t.Fatalf("deadline expired before consumer started: %v", err)
	}
	decision, err := EvaluateScheduler(ctx, settings, snapshot)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("recursive candidate enumeration ignored the consumer deadline: %v", err)
	}
	if decision.Target != nil || len(decision.Candidates) != 0 || decision.Search.Examined != 0 {
		t.Fatalf("canceled enumeration leaked a partial decision: %+v", decision)
	}
}
