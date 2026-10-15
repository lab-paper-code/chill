package serving

import (
	"math"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func TestReplayCompletionSubtractsOnlyActualConsumedSender(t *testing.T) {
	for _, phase := range []string{phaseQueued, phaseRunning} {
		for _, senderMS := range []float64{400, 500} {
			s, snap := schedulerFixture()
			s.Controls.Enabled = false
			snap.Arrivals = nil
			d := snap.Workloads[0]
			d.SLO.TargetLatency = 607 * time.Millisecond
			snap.Instances[0] = schedulerMeasuredInstance(snap.Instances[0], d, 407*time.Millisecond)
			snap.Instances[0].Profile.Condition.Firstfloor.DeadlinePolicy = firstfloor.DeadlineDrop
			snap.Instances[0].Profile.Batches[0].DurationNanos = []float64{float64(100 * time.Millisecond)}
			r := SchedulerRequest{ID: "consumed", Definition: d, ReceivedAt: snap.At.Add(-time.Duration(senderMS) * time.Millisecond), Elapsed: time.Duration(senderMS) * time.Millisecond, Phase: phase, Instance: "i1", BatchID: "actual", NativeSenderElapsedMS: &senderMS}
			want := time.Duration(senderMS)*time.Millisecond + 100*time.Millisecond + time.Duration(math.Max(0, 407-senderMS))*time.Millisecond
			if phase == phaseRunning {
				r.RunningElapsed = 20 * time.Millisecond
				r.Elapsed += r.RunningElapsed
				r.ReceivedAt = r.ReceivedAt.Add(-r.RunningElapsed)
			}
			snap.Outstanding = []SchedulerRequest{r}
			decision := EvaluateScheduler(s, snap)
			e := schedulerRequireEstimate(t, decision.Keep, d.UID)
			if e.ExpectedNanos == nil || *e.ExpectedNanos != float64(want) || e.PredictedOutcomes["timeout"] != 0 {
				t.Fatalf("%s sender=%g must consume predispatch only once: want %s got %+v reasons=%v", phase, senderMS, want, e, decision.Keep.Reasons)
			}
			snap.Outstanding[0].NativeSenderElapsedMS = nil
			missing := EvaluateScheduler(s, snap)
			schedulerRequireReason(t, missing.Keep.Reasons, "MissingNativeSenderContext")
		}
	}
}

func TestSchedulerReplayDeadlineModesPreserveTimeoutPopulation(t *testing.T) {
	for _, mode := range []string{"", firstfloor.DeadlineKeep, firstfloor.DeadlineDrop, firstfloor.DeadlineDefer} {
		t.Run("mode="+mode, func(t *testing.T) {
			s, snap := schedulerFixture()
			s.Controls.Enabled = false
			d := snap.Workloads[0]
			d.SLO.TargetLatency = time.Millisecond
			snap.Instances[0].Profile.Condition.Firstfloor.DeadlinePolicy = mode
			zero := 0.0
			snap.Outstanding = []SchedulerRequest{{ID: "infeasible", Definition: d, ReceivedAt: snap.At, Phase: phaseQueued, Instance: "i1", NativeSenderElapsedMS: &zero}}
			decision := EvaluateScheduler(s, snap)
			e := schedulerRequireEstimate(t, decision.Keep, d.UID)
			if decision.Keep.ExpectedJoules == nil || decision.Target == nil || decision.Status != schedulerStatusOverload {
				t.Fatalf("supported mode must be evaluable, not missing evidence or caller expiry: %+v", decision)
			}
			if mode == firstfloor.DeadlineDrop {
				if e.Samples != 0 || e.ExpectedNanos != nil || e.PredictedOutcomes["timeout"] != s.Replays || e.PredictedOutcomes["completed"] != 0 || math.Abs(*decision.Keep.ExpectedJoules-1.2) > 1e-9 {
					t.Fatalf("drop must not execute or contribute successful latency: %+v", decision.Keep)
				}
				schedulerRequireReason(t, decision.Keep.Reasons, "SLODeadlineDrops")
			} else if e.Samples != s.Replays || e.ExpectedNanos == nil || *e.ExpectedNanos != float64(10*time.Millisecond) || e.PredictedOutcomes["timeout"] != 0 {
				t.Fatalf("keep/defer must execute despite SLO, without expiring caller: %+v", e)
			}
		})
	}
}

func TestSchedulerReplayDropsAtSLOBoundaryWhileBatchRuns(t *testing.T) {
	s, snap := schedulerFixture()
	d := snap.Workloads[0]
	queued := *d
	queued.UID = "queued"
	queued.SLO.TargetLatency = 5 * time.Millisecond
	snap.Instances[0].Profile.Condition.Firstfloor.DeadlinePolicy = firstfloor.DeadlineDrop
	snap.Instances[0].Profile.Batches[0].DurationNanos = []float64{float64(100 * time.Millisecond)}
	zero := 0.0
	snap.Outstanding = []SchedulerRequest{{ID: "active", Definition: d, ReceivedAt: snap.At, Phase: phaseRunning, Instance: "i1", BatchID: "actual", NativeSenderElapsedMS: &zero}, {ID: "queued", Definition: &queued, ReceivedAt: snap.At, Phase: phaseQueued, Instance: "i1", NativeSenderElapsedMS: &zero}}
	state := schedulerReplayState{settings: s, snap: snap, c: SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances}, rng: rand.New(rand.NewPCG(1, 0)), horizon: float64(s.Horizon), out: schedulerReplayResult{samples: map[string][]float64{}, definitions: map[string]*Definition{}, outcomes: map[string]map[string]int{}}}
	state.initializeInstances()
	state.initializeOutstanding()
	if !state.initializeRunning() || !state.chooseBatches() {
		t.Fatalf("active batch initialization failed: %+v", state.out)
	}
	next, remaining := state.nextEvent()
	if !remaining || next != float64(5*time.Millisecond) || len(state.instances[0].queue) != 1 {
		t.Fatalf("infeasible prediction must not drop while active; actual expiry must be next: next=%g state=%+v", next, state.instances[0])
	}
	state.now = next
	if !state.chooseBatches() || len(state.instances[0].queue) != 0 || state.instances[0].outstanding != 1 || len(state.instances[0].batch) != 1 || state.out.outcomes[queued.Identity()]["timeout"] != 1 || len(state.out.samples[queued.Identity()]) != 0 {
		t.Fatalf("deadline boundary must release only queued request: %+v %+v", state.instances[0], state.out)
	}
	state.now = float64(100 * time.Millisecond)
	state.completeExecution()
	if state.out.outcomes[d.Identity()]["completed"] != 1 || state.instances[0].outstanding != 0 {
		t.Fatalf("active execution must finish nonpreemptively: %+v", state.out)
	}
}

func TestSchedulerReplayDeferredFIFOAndNormalEDF(t *testing.T) {
	s, snap := schedulerFixture()
	d := snap.Workloads[0]
	other := *d
	other.UID = "other"
	normal := *d
	normal.UID = "normal"
	normal.SLO.TargetLatency = 20 * time.Millisecond
	snap.Instances[0].Profile.Condition.Firstfloor.DeadlinePolicy = firstfloor.DeadlineDefer
	snap.Instances[0].MaxBatch = 2
	snap.Instances[0].Profile.Batches = append(snap.Instances[0].Profile.Batches, BatchProfile{Batch: 2, DurationNanos: []float64{float64(5 * time.Millisecond)}, EnergyPerInputJoules: 1, MemoryBytes: 100})
	zero := 0.0
	snap.Outstanding = []SchedulerRequest{
		{ID: "deferred-a", QueueOrder: 0, Definition: d, ReceivedAt: snap.At, Phase: phaseDeferred, Instance: "i1", NativeSenderElapsedMS: &zero},
		{ID: "deferred-other", QueueOrder: 1, Definition: &other, ReceivedAt: snap.At, Phase: phaseDeferred, Instance: "i1", NativeSenderElapsedMS: &zero},
		{ID: "deferred-c", QueueOrder: 2, Definition: d, ReceivedAt: snap.At, Phase: phaseDeferred, Instance: "i1", NativeSenderElapsedMS: &zero},
		{ID: "normal", QueueOrder: 3, Definition: &normal, ReceivedAt: snap.At, Phase: phaseQueued, Instance: "i1", NativeSenderElapsedMS: &zero},
	}
	result := schedulerReplayForTest(s, snap, SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances, Actions: []Action{{ID: "drain", Kind: schedulerActionDrain, Instance: "i1"}}})
	if len(result.reasons) > 0 || !reflect.DeepEqual(result.samples[normal.Identity()], []float64{float64(10 * time.Millisecond)}) || !reflect.DeepEqual(result.samples[d.Identity()], []float64{float64(20 * time.Millisecond), float64(40 * time.Millisecond)}) || !reflect.DeepEqual(result.samples[other.Identity()], []float64{float64(30 * time.Millisecond)}) {
		t.Fatalf("normal precedes deferred; deferred cannot batch across incompatible FIFO entry or evade drain: %+v", result)
	}
	for j := range snap.Outstanding {
		snap.Outstanding[j].Phase = phaseQueued
	}
	result = schedulerReplayForTest(s, snap, SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances})
	if len(result.reasons) > 0 || !reflect.DeepEqual(result.samples[normal.Identity()], []float64{float64(10 * time.Millisecond)}) || !reflect.DeepEqual(result.samples[d.Identity()], []float64{float64(15 * time.Millisecond), float64(15 * time.Millisecond)}) {
		t.Fatalf("normal EDF compatible batch extraction must match shared selector: %+v", result)
	}
}

func TestSchedulerReplayDeferTransitionRunsAfterNormalWork(t *testing.T) {
	s, snap := schedulerFixture()
	d := snap.Workloads[0]
	expired := *d
	expired.UID = "expired"
	expired.SLO.TargetLatency = time.Millisecond
	snap.Instances[0].Profile.Condition.Firstfloor.DeadlinePolicy = firstfloor.DeadlineDefer
	zero, one := 0.0, 1.0
	snap.Outstanding = []SchedulerRequest{{ID: "expired", QueueOrder: 0, Definition: &expired, ReceivedAt: snap.At, Phase: phaseQueued, Instance: "i1", NativeSenderElapsedMS: &one}, {ID: "normal", QueueOrder: 1, Definition: d, ReceivedAt: snap.At, Phase: phaseQueued, Instance: "i1", NativeSenderElapsedMS: &zero}}
	result := schedulerReplayForTest(s, snap, SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances})
	if len(result.reasons) > 0 || !reflect.DeepEqual(result.samples[d.Identity()], []float64{float64(10 * time.Millisecond)}) || !reflect.DeepEqual(result.samples[expired.Identity()], []float64{float64(20 * time.Millisecond)}) || result.outcomes[expired.Identity()]["timeout"] != 0 {
		t.Fatalf("defer must demote once, preserve caller and execute after normal: %+v", result)
	}
}

func TestSchedulerReplayKeepOverdueUsesMinimumServiceCost(t *testing.T) {
	s, snap := schedulerFixture()
	d := snap.Workloads[0]
	d.SLO.TargetLatency = time.Millisecond
	snap.Instances[0].MaxBatch = 2
	snap.Instances[0].Profile.Batches = append(snap.Instances[0].Profile.Batches, BatchProfile{Batch: 2, DurationNanos: []float64{float64(5 * time.Millisecond)}, EnergyPerInputJoules: 5, MemoryBytes: 100})
	zero := 0.0
	for _, id := range []string{"a", "b", "c"} {
		snap.Outstanding = append(snap.Outstanding, SchedulerRequest{ID: id, Definition: d, ReceivedAt: snap.At, Phase: phaseQueued, Instance: "i1", NativeSenderElapsedMS: &zero})
	}
	result := schedulerReplayForTest(s, snap, SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances})
	if len(result.reasons) > 0 || !reflect.DeepEqual(result.samples[d.Identity()], []float64{float64(5 * time.Millisecond), float64(5 * time.Millisecond), float64(15 * time.Millisecond)}) {
		t.Fatalf("overdue keep must run fastest fillable batch, even when higher energy: %+v", result)
	}
}

func TestSchedulerReplayObservedDropReturningNeverBecomesSuccess(t *testing.T) {
	s, snap := schedulerFixture()
	d := snap.Workloads[0]
	snap.Instances[0] = schedulerMeasuredInstance(snap.Instances[0], d, 5*time.Millisecond)
	life := time.Millisecond
	residual := time.Duration(0)
	snap.Outstanding = []SchedulerRequest{{ID: "drop-returning", Definition: d, ReceivedAt: snap.At, Phase: phaseReturning, Instance: "i1", NativeOutcome: "deadline_drop", RemainingLifetime: &life, ExchangeResidual: &residual}}
	result := schedulerReplayForTest(s, snap, SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances})
	if len(result.reasons) > 0 || len(result.samples[d.Identity()]) != 0 || result.outcomes[d.Identity()]["timeout"] != 1 || result.outcomes[d.Identity()]["completed"] != 0 || result.outcomes[d.Identity()]["expired-returning"] != 0 {
		t.Fatalf("observed SLO drop must preserve outcome despite return/caller lifetime: %+v", result)
	}
}

func TestSchedulerOutstandingExtendsDeferredNativeWitness(t *testing.T) {
	_, snap := schedulerFixture()
	d := snap.Workloads[0]
	remaining := 30.0
	native := firstfloor.RequestState{Context: firstfloor.RequestContext{RequestID: "deferred", WorkloadUID: d.UID, Definition: d.Identity(), SenderElapsedMS: 7, LifetimeMS: 50}, Phase: phaseDeferred, QueueMS: 3, RemainingLifetimeMS: &remaining}
	call := &Call{receipt: Receipt{ID: "deferred", UID: d.UID, ReceivedAt: snap.At}, definition: d, identity: d.Identity(), assignment: &Assignment{Instance: "i1"}}
	runtime := &Runtime{calls: map[string]*Call{"deferred": call}, instances: map[string]*runtimeInstance{"i1": {ReadyInstance: ReadyInstance{State: InstanceState{ID: "i1", ObservedAt: snap.At, Native: &firstfloor.State{Requests: []firstfloor.RequestState{native}}}}}}}
	requests := runtime.SchedulerOutstanding(snap.At.Add(2 * time.Millisecond))
	if len(requests) != 1 || requests[0].Phase != phaseDeferred || requests[0].QueueAge != 5*time.Millisecond || requests[0].NativeSenderElapsedMS == nil || *requests[0].NativeSenderElapsedMS != 7 || requests[0].RemainingLifetime == nil || *requests[0].RemainingLifetime != 28*time.Millisecond {
		t.Fatalf("deferred witness must retain native sender, FIFO and caller lifetime: %+v", requests)
	}
	call.backendDone = true
	call.exchangeFinishedAt = snap.At
	call.progress = &BackendProgress{Outcome: "deadline_drop"}
	requests = runtime.SchedulerOutstanding(snap.At)
	if requests[0].Phase != phaseReturning || requests[0].NativeOutcome != "deadline_drop" {
		t.Fatalf("returning drop must retain native outcome: %+v", requests)
	}
}

func TestSchedulerReplayCannotSwitchFrozenDeadlineMode(t *testing.T) {
	_, snap := schedulerFixture()
	state := schedulerReplayState{snap: snap, c: SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances}}
	state.initializeInstances()
	target := snap.Instances[0]
	profile := *target.Profile
	settings := *profile.Condition.Firstfloor
	settings.DeadlinePolicy = firstfloor.DeadlineDrop
	profile.Condition.Firstfloor = &settings
	target.Profile = &profile
	state.goal.Instances = []SchedulerInstance{target}
	if state.applyRoute() {
		t.Fatal("in-place policy update reinterpreted frozen keep as drop")
	}
	schedulerRequireReason(t, state.out.reasons, "ImmutableNativeDeadlinePolicyMismatch")
}

func TestSchedulerReplayOverheadUpdatePreservesCapturedSender(t *testing.T) {
	_, snap := schedulerFixture()
	d := snap.Workloads[0]
	snap.Outstanding = []SchedulerRequest{{ID: "pending", Definition: d, ReceivedAt: snap.At.Add(-20 * time.Millisecond), Elapsed: 20 * time.Millisecond, Phase: phasePending}}
	state := schedulerReplayState{snap: snap, c: SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances}, routingApplied: true, routeCredits: map[schedulerRouteKey][]int64{}, weights: make([]int32, 1), available: make([]bool, 1), out: schedulerReplayResult{definitions: map[string]*Definition{}, outcomes: map[string]map[string]int{}}}
	state.initializeInstances()
	state.initializeOutstanding()
	state.routeRequests()
	state.receiveNative()
	instance := state.instances[0]
	before := state.nativeRequestState(instance, state.requests[0])
	target := snap.Instances[0]
	target = schedulerMeasuredInstance(target, d, 50*time.Millisecond)
	state.goal.Instances = []SchedulerInstance{target}
	if !state.applyRoute() {
		t.Fatalf("permitted envelope update failed: %+v", state.out)
	}
	after := state.nativeRequestState(instance, state.requests[0])
	if before.Context.SenderElapsedMS != 20 || after.Context.SenderElapsedMS != 20 || firstfloor.RemainingSLOMS(before) != firstfloor.RemainingSLOMS(after) {
		t.Fatalf("overhead update changed already admitted native context: before=%+v after=%+v", before, after)
	}
}
