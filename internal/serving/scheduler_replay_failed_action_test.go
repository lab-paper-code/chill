package serving

import (
	"context"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestReplayFailedPreparePreservesObservedStateAndBlocksDependentRoute(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	failed := snapshot.Instances[0]
	failed.ID, failed.WorkloadUID, failed.Weight = "failed-instance", "", 0
	failed.Ready, failed.Stopped = false, true
	snapshot.Instances = append(snapshot.Instances, failed)
	uid := snapshot.Workloads[0].UID
	snapshot.Routing = map[string]map[string]int32{uid: {snapshot.Instances[0].ID: 1000}}
	candidate.ID, candidate.Instances, candidate.Routing = "keep", snapshot.Instances, snapshot.Routing
	snapshot.Actions = []Action{
		{ID: "failed-prepare", Kind: schedulerActionPrepare, Node: failed.Node, Instance: failed.ID,
			From: "unloaded", To: schedulerState(failed), Status: schedulerActionFailed, Reason: "preparation failed"},
		{ID: "dependent-route", Kind: schedulerActionRoute, DependsOn: []string{"failed-prepare"},
			RouteTarget: &SchedulerCandidate{ID: "blocked-route", Routing: map[string]map[string]int32{uid: {}}}},
	}

	state := schedulerReplayState{settings: settings, snap: snapshot, c: candidate}
	state.initializeInstances()
	state.initializeActions()
	if !state.startActions() || !state.completeActions() || state.unlockedActions() {
		t.Fatalf("failed prepare must not abort or keep replay at the same timestamp: %+v", state.out)
	}
	prepare, route := state.actionByID["failed-prepare"], state.actionByID["dependent-route"]
	if prepare.started || prepare.done || route.started || route.done || state.applied.ID != candidate.ID {
		t.Fatalf("failed prepare must neither execute nor satisfy the route dependency: prepare=%+v route=%+v applied=%s", prepare, route, state.applied.ID)
	}
	occupant := state.byID[failed.ID]
	if !occupant.loaded || occupant.ready || !occupant.stopped || occupant.config.Allocation != failed.Allocation {
		t.Fatalf("failed prepare changed observed occupancy or readiness: %+v", occupant)
	}

	result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
	if result.Workloads[0].Population != 2 || result.Workloads[0].OnTime+result.Workloads[0].Late != 2 {
		t.Fatalf("failed prepare or its dependent route interrupted healthy observed supply: %+v", result.Workloads)
	}
}

func TestReplayFailedPrepareLeavesUnroutedRequestsUnfinished(t *testing.T) {
	for _, chain := range []bool{false, true} {
		name := "failed-only"
		if chain {
			name = "reverse-ordered-dependent-chain"
		}
		t.Run(name, func(t *testing.T) {
			settings, snapshot, candidate := feedbackFixture()
			snapshot.Arrivals = nil
			snapshot.Instances[0].Ready = false
			uid := snapshot.Workloads[0].UID
			snapshot.Routing = map[string]map[string]int32{uid: {}}
			candidate.Routing = snapshot.Routing
			zero := 0.0
			snapshot.Outstanding = []SchedulerRequest{{
				ID: "pending", Definition: snapshot.Workloads[0], Phase: phasePending, CompletionMarginMS: &zero,
			}}
			snapshot.Actions = []Action{{
				ID: "failed-prepare", Kind: schedulerActionPrepare, Instance: snapshot.Instances[0].ID, Status: schedulerActionFailed,
			}}
			if chain {
				snapshot.Actions = append([]Action{
					{ID: "dependent-route", Kind: schedulerActionRoute, DependsOn: []string{"dependent-drain"}},
					{ID: "dependent-drain", Kind: schedulerActionDrain, DependsOn: []string{"failed-prepare"}},
				}, snapshot.Actions...)
			}

			state := schedulerReplayState{settings: settings, snap: snapshot, c: candidate}
			state.initializeInstances()
			state.initializeActions()
			if !state.startActions() || state.blockedReplayPrerequisite() || state.unlockedActions() {
				t.Fatalf("failed chain must not remain an unresolved replay prerequisite: %+v", state.out)
			}
			for _, action := range state.actions {
				if !action.blocked || action.started || action.done {
					t.Fatalf("failed chain must stay distinct from successful completion: %+v", action)
				}
			}
			result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
			if result.Workloads[0].Population != 1 || result.Workloads[0].Unfinished != 1 || result.Workloads[0].OnTime+result.Workloads[0].Late != 0 {
				t.Fatalf("unrouted pending request must remain unfinished without invalidating replay: %+v", result.Workloads)
			}
		})
	}
}

// control379 retained a partial IC prepare after its physical target disappeared.
// API observations omit catalog profile bodies, so this fixture retains its
// action/ownership shape with the paired synthetic profile used by replay tests.
func TestReplayControl379PartialPrepareUsesCommonPhysicalState(t *testing.T) {
	settings, snapshot, keep := feedbackFixture()
	node := poolsAddNode(&snapshot, "failed-node", map[string]float64{"fixed": 1})
	snapshot.Actions = []Action{{
		ID: "partial-ic-prepare", Kind: schedulerActionPrepare, Node: node, Instance: "missing-ic-target",
		From: "unloaded", To: "missing-profile::1000", Status: "partial", Reason: "profile: Error (exit 1)",
	}}
	keep.Nodes = snapshot.Nodes
	changed := keep
	changed.Keep = false
	changed.Actions = []Action{{ID: "independent-route", Kind: schedulerActionRoute}}
	first := feedbackEvaluateForTest(t, settings, snapshot, keep)
	second := feedbackEvaluateForTest(t, settings, snapshot, changed)
	if !reflect.DeepEqual(first.Workloads, second.Workloads) || !reflect.DeepEqual(first.SLO, second.SLO) {
		t.Fatalf("candidate flag changed the initial physical world: keep=%+v changed=%+v", first, second)
	}
}

func TestSchedulerControl379PartialPrepareKeepsEvaluableWithoutReapply(t *testing.T) {
	settings, snapshot := schedulerFixture()
	snapshot.Instances[0].Weight = schedulerRouteUnits
	node := poolsAddNode(&snapshot, "failed-node", map[string]float64{"fixed": 1})
	snapshot.Actions = []Action{{
		ID: "partial-ic-prepare", Kind: schedulerActionPrepare, Node: node, Instance: "missing-ic-target",
		From: "unloaded", To: "missing-profile::1000", Status: "partial", Reason: "profile: Error (exit 1)",
	}}
	before := slices.Clone(snapshot.Actions)
	decision := capacityDecideForTest(t, settings, snapshot)
	if decision.Keep.Status == schedulerStatusPending || decision.Target == nil || len(decision.Target.Actions) != 0 {
		t.Fatalf("partial prepare must not force an equivalent route: keep=%+v target=%+v", decision.Keep, decision.Target)
	}
	if !slices.ContainsFunc(decision.Reasons, func(reason string) bool {
		return strings.Contains(reason, "partial-ic-prepare") && strings.Contains(reason, "missing-ic-target")
	}) {
		t.Fatalf("decision omitted excluded target and reason: %v", decision.Reasons)
	}
	if !reflect.DeepEqual(snapshot.Actions, before) {
		t.Fatal("evaluation rewrote the physical owner ledger")
	}
}

func TestSchedulerPlanIdenticalPhysicalPolicyRoutingDoesNotReapply(t *testing.T) {
	settings, snapshot := schedulerFixture()
	snapshot.Routing = schedulerInstanceRouting(context.Background(), snapshot.Workloads, snapshot.Instances)
	target := SchedulerCandidate{Nodes: snapshot.Nodes, Instances: slices.Clone(snapshot.Instances), Routing: snapshot.Routing}
	if actions := schedulerPlan(settings, snapshot, target); len(actions) != 0 {
		t.Fatalf("identical target must not emit actions: %+v", actions)
	}
	target.Instances[0].MaxWait++
	if actions := schedulerPlan(settings, snapshot, target); !slices.ContainsFunc(actions, func(action Action) bool {
		return action.Kind == schedulerActionRoute
	}) {
		t.Fatalf("a changed batching policy still needs a route: %+v", actions)
	}
}

func TestReplayCommonScopeRetainsPhysicalOccupancyAndOutstanding(t *testing.T) {
	settings, snapshot, keep := feedbackFixture()
	node := poolsAddNode(&snapshot, "unknown", map[string]float64{"fixed": 1})
	unknown := poolsInstance(snapshot.Instances[0], "unknown-executor", node, "fixed", "")
	unknown.Profile = nil
	snapshot.Instances = append(snapshot.Instances, unknown)
	uid := snapshot.Workloads[0].UID
	snapshot.Routing = map[string]map[string]int32{uid: {snapshot.Instances[0].ID: schedulerRouteUnits}}
	keep.Nodes, keep.Instances, keep.Routing = snapshot.Nodes, snapshot.Instances, snapshot.Routing
	zero := 0.0
	lifetime := time.Nanosecond
	snapshot.Outstanding = []SchedulerRequest{{
		ID: "unknown-queued", Definition: snapshot.Workloads[0], ReceivedAt: snapshot.At,
		Phase: phaseQueued, Instance: unknown.ID, NativeSenderElapsedMS: &zero, NativeElapsedNanos: &zero,
		CompletionMarginMS: &zero, RemainingLifetime: &lifetime,
	}}
	snapshot.Actions = []Action{
		{ID: "unknown-power", Kind: schedulerActionPower, Node: node, From: "fixed", To: "eco", Status: schedulerActionPlanned},
		{ID: "dependent-remove", Kind: schedulerActionRemove, Node: node, Instance: unknown.ID, DependsOn: []string{"unknown-power"}},
	}
	state := schedulerReplayState{settings: settings, snap: snapshot, c: keep, out: schedulerReplayResult{
		definitions: map[string]*Definition{}, outcomes: map[string]map[string]int{},
	}}
	state.initializeInstances()
	state.initializeActions()
	state.initializeOutstanding()
	occupant := state.byID[unknown.ID]
	if !occupant.loaded || !occupant.ready || !occupant.excluded || occupant.config.Allocation != unknown.Allocation ||
		occupant.outstanding != 1 || len(occupant.queue) != 1 || replayHasCapacity(occupant) {
		t.Fatalf("exclusion changed physical state or supplied capacity: %+v", occupant)
	}
	if !state.startActions() || state.actionByID["unknown-power"].started || !state.actionByID["dependent-remove"].blocked {
		t.Fatal("unknown co-resident allowed node-wide transition or cleanup")
	}
	state.now = 2
	state.expireRequests()
	state.completeExecution()
	if occupant.outstanding != 1 || len(occupant.queue) != 1 {
		t.Fatal("client timeout released unknown physical work")
	}
	for _, candidateKeep := range []bool{true, false} {
		candidate := keep
		candidate.Keep = candidateKeep
		if !candidateKeep {
			candidate.Actions = []Action{{ID: "healthy-route", Kind: schedulerActionRoute}}
		}
		result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
		if result.Workloads[0].Population != 3 || result.Workloads[0].OnTime != 2 || result.Workloads[0].Timeout != 1 {
			t.Fatalf("exclusion lost requests or independent supply: %+v", result)
		}
	}
}

func TestReplayCommonUncreatedPreparationUsesOwnerTarget(t *testing.T) {
	settings, snapshot, keep := feedbackFixture()
	node := poolsAddNode(&snapshot, "preparing", map[string]float64{"fixed": 1})
	future := poolsInstance(snapshot.Instances[0], "authorized-slot", node, "fixed", snapshot.Workloads[0].UID)
	action := Action{ID: "authorized-prepare", Kind: schedulerActionPrepare, Node: node, Instance: future.ID,
		From: "unloaded", To: schedulerState(future), Status: phaseRunning}
	owner := SchedulerCandidate{ID: "owner", Nodes: snapshot.Nodes, Instances: []SchedulerInstance{future}, Actions: []Action{action}}
	snapshot.Reservations = []SchedulerReservation{{Owner: owner.ID, Nodes: []NodeReference{node}, Target: &owner}}
	snapshot.Actions = []Action{action}
	for _, candidateKeep := range []bool{true, false} {
		candidate := keep
		candidate.Keep = candidateKeep
		state := schedulerReplayState{settings: settings, snap: snapshot, c: candidate, rng: rand.New(rand.NewPCG(1, 0))}
		state.initializeInstances()
		state.initializeActions()
		if state.byID[future.ID] == nil || state.byID[future.ID].loaded || !state.startActions() ||
			!state.actionByID[action.ID].started || state.actionByID[action.ID].blocked {
			t.Fatalf("authorized preparation vanished from candidate keep=%v: %+v", candidateKeep, state.out.reasons)
		}
	}
}

func TestReplaySnapshotRouteUsesFrozenOwnerInsteadOfCandidateGoal(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	owner := candidate
	owner.ID = "frozen-owner"
	action := Action{ID: "owner-route", Kind: schedulerActionRoute, Status: schedulerActionPlanned}
	owner.Actions = []Action{action}
	action.RouteTarget = &owner // same owner-controlled projection as the snapshot writer
	snapshot.Actions = []Action{action}
	snapshot.Reservations = []SchedulerReservation{{Owner: owner.ID, Nodes: []NodeReference{snapshot.Nodes[0].Node}, Target: &owner}}
	candidate.ID, candidate.Keep = "independent-goal", false
	candidate.Instances = slices.Clone(candidate.Instances)
	candidate.Instances[0].MaxWait++
	state := schedulerReplayState{settings: settings, snap: snapshot, c: candidate, routeCredits: map[string][]int64{}}
	state.initializeInstances()
	state.initializeActions()
	if !state.startActions() || state.applied.ID != owner.ID || state.instances[0].config.MaxWait != owner.Instances[0].MaxWait {
		t.Fatalf("candidate rewrote owner route: applied=%+v reasons=%v", state.applied, state.out.reasons)
	}
}

func TestCapacityCommonScopePreservesIndependentExpansion(t *testing.T) {
	settings, snapshot, fast := capacityE1Fixture()
	blocked := snapshot.Options[1].Instance.Node
	snapshot.Actions = []Action{{ID: "partial-prepare", Kind: schedulerActionPrepare, Node: blocked,
		Instance: "missing-target", Status: "partial", Reason: "profile: Error (exit 1)"}}
	decision := capacityDecideForTest(t, settings, snapshot)
	if decision.Keep.Status == schedulerStatusPending || decision.Target == nil || !poolsOn(*decision.Target, fast) {
		t.Fatalf("local exclusion blocked independent recovery: keep=%+v target=%+v", decision.Keep, decision.Target)
	}
	if !slices.ContainsFunc(decision.Capacity.Plans, func(plan SchedulerNodePlan) bool {
		return plan.Node == blocked && !plan.Eligible && plan.Reason == "ReplayExcludedNode"
	}) {
		t.Fatal("same-node resource/power/transition dependency was not excluded")
	}
}

func TestReplayTransientRetryRetainsExecutableSupply(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	node := poolsAddNode(&snapshot, "retry-node", map[string]float64{"fixed": 10})
	future := poolsInstance(snapshot.Instances[0], "retry", node, "fixed", snapshot.Workloads[0].UID)
	future.ID = fmt.Sprintf("target-%s-%s-%d", future.WorkloadUID, future.Profile.ID, future.MaxBatch)
	snapshot.Actions = []Action{{ID: "previous-attempt", Kind: schedulerActionPrepare, Node: node, Instance: future.ID,
		Status: schedulerActionFailed, To: schedulerState(future),
		PreparationFailure: NewPreparationFailure(future, "old-pod", PreparationFailureObservation{Classification: PreparationTransient, ReasonKey: "delivery:ErrImagePull"})}}
	candidate.Keep = false
	candidate.Nodes = snapshot.Nodes
	candidate.Instances = []SchedulerInstance{future}
	candidate.Routing = map[string]map[string]int32{future.WorkloadUID: {future.ID: schedulerRouteUnits}}
	settings.DefaultOperationCost[schedulerActionPrepare] = OperationCostValue{Seconds: .001, Joules: .01}
	candidate.Actions = schedulerPlan(settings, snapshot, candidate)
	result, err := EvaluateFeedbackCandidate(context.Background(), settings, snapshot, candidate, 0)
	if err != nil || len(result.InvalidReasons) != 0 || len(result.Workloads) != 1 || result.Workloads[0].OnTime != 2 {
		t.Fatalf("fresh authorized retry supplies no service: err=%v result=%+v", err, result)
	}
}

func TestReplayExcludedDrainDoesNotInvalidateIndependentSupply(t *testing.T) {
	for _, failedPower := range []bool{false, true} {
		t.Run(fmt.Sprint("failed-power=", failedPower), func(t *testing.T) {
			settings, snapshot, candidate := feedbackFixture()
			node := poolsAddNode(&snapshot, "unknown", map[string]float64{"fixed": 1})
			unknown := poolsInstance(snapshot.Instances[0], "unknown-executor", node, "fixed", "")
			if !failedPower {
				unknown.Profile = nil
			}
			snapshot.Instances = append(snapshot.Instances, unknown)
			uid := snapshot.Workloads[0].UID
			snapshot.Routing = map[string]map[string]int32{uid: {snapshot.Instances[0].ID: schedulerRouteUnits}}
			candidate.Keep = false
			candidate.Nodes, candidate.Instances, candidate.Routing = snapshot.Nodes, snapshot.Instances, snapshot.Routing
			zero := 0.0
			lifetime := time.Nanosecond
			snapshot.Outstanding = []SchedulerRequest{{ID: "unknown-queued", Definition: snapshot.Workloads[0], ReceivedAt: snapshot.At,
				Phase: phaseQueued, Instance: unknown.ID, NativeSenderElapsedMS: &zero, NativeElapsedNanos: &zero,
				CompletionMarginMS: &zero, RemainingLifetime: &lifetime}}
			snapshot.Actions = []Action{{ID: "unknown-drain", Kind: schedulerActionDrain, Node: node, Instance: unknown.ID, Status: phaseRunning}}
			if failedPower {
				snapshot.Actions = append(snapshot.Actions, Action{ID: "failed-power", Kind: schedulerActionPower, Node: node, Status: schedulerActionFailed})
			}
			result, err := EvaluateFeedbackCandidate(context.Background(), settings, snapshot, candidate, 0)
			if err != nil || len(result.InvalidReasons) != 0 {
				t.Fatalf("unmodelable drain invalidated independent candidate: err=%v result=%+v", err, result)
			}
			if len(result.Workloads) != 1 || result.Workloads[0].Population != 3 || result.Workloads[0].OnTime != 2 || result.Workloads[0].Timeout != 1 {
				t.Fatalf("excluded drain lost requests or healthy capacity: %+v", result.Workloads)
			}
			state := schedulerReplayState{settings: settings, snap: snapshot, c: candidate, out: schedulerReplayResult{
				definitions: map[string]*Definition{}, outcomes: map[string]map[string]int{},
			}}
			state.initializeInstances()
			state.initializeActions()
			state.initializeOutstanding()
			state.now = 2
			state.expireRequests()
			state.completeExecution()
			drain, occupant := state.actionByID["unknown-drain"], state.byID[unknown.ID]
			if !drain.blocked || drain.done || drain.started || occupant.outstanding != 1 || len(occupant.queue) != 1 ||
				snapshot.Actions[0].Status != phaseRunning {
				t.Fatal("prediction exclusion completed the physical drain or released its outstanding")
			}
		})
	}
}

func TestReplayFrozenRoutePreservesIndependentWorkload(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	source := snapshot.Instances[0]
	node := poolsAddNode(&snapshot, "second", map[string]float64{"fixed": 1})
	second := poolsInstance(source, "second", node, "fixed", "second-workload")
	snapshot.Instances = append(snapshot.Instances, second)
	snapshot.Routing = map[string]map[string]int32{source.WorkloadUID: {source.ID: schedulerRouteUnits}, second.WorkloadUID: {second.ID: schedulerRouteUnits}}
	owner := SchedulerCandidate{ID: "first-owner", Nodes: snapshot.Nodes, Instances: []SchedulerInstance{source},
		Routing: map[string]map[string]int32{source.WorkloadUID: {source.ID: schedulerRouteUnits}}}
	snapshot.Actions = []Action{{ID: "owner-route", Kind: schedulerActionRoute, Status: schedulerActionPlanned, RouteTarget: &owner}}
	candidate.Keep = false
	candidate.Nodes, candidate.Instances, candidate.Routing = snapshot.Nodes, snapshot.Instances, snapshot.Routing
	state := schedulerReplayState{settings: settings, snap: snapshot, c: candidate, rng: rand.New(rand.NewPCG(1, 0)), routeCredits: map[string][]int64{}}
	state.initializeInstances()
	state.initializeActions()
	if !state.startActions() || state.applied.Routing[second.WorkloadUID][second.ID] != schedulerRouteUnits {
		t.Fatalf("owner route dropped independent workload: applied=%+v reasons=%v", state.applied.Routing, state.out.reasons)
	}
}

func TestReplayObservedSuccessfulRetryIgnoresEarlierAttemptFailure(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	actual := &snapshot.Instances[0]
	actual.PodUID, actual.TargetID = "successful-pod", "stable-target"
	snapshot.Actions = []Action{{ID: "earlier-attempt", Kind: schedulerActionPrepare, Node: actual.Node, Instance: actual.TargetID,
		Status: schedulerActionFailed, To: schedulerState(*actual),
		PreparationFailure: NewPreparationFailure(*actual, "failed-pod", PreparationFailureObservation{Classification: PreparationTransient, ReasonKey: "delivery:ErrImagePull"})}}
	result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
	if result.Workloads[0].OnTime != 2 {
		t.Fatalf("earlier attempt excluded a different successful Pod: %+v", result)
	}
}

func TestReplayDoesNotRecoverUnscopedRouteFromReservation(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	action := Action{ID: "unscoped-route", Kind: schedulerActionRoute, Status: schedulerActionPlanned}
	owner := candidate
	owner.Actions = []Action{action}
	snapshot.Actions = []Action{action}
	snapshot.Reservations = []SchedulerReservation{{Owner: "owner", Nodes: []NodeReference{snapshot.Nodes[0].Node}, Target: &owner}}
	result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
	if result.Workloads[0].OnTime != 2 || !slices.ContainsFunc(result.Assumptions, func(reason string) bool {
		return strings.Contains(reason, "unscoped-route") && strings.Contains(reason, "MissingActionRouteTarget")
	}) {
		t.Fatalf("route authority was inferred from the unscoped reservation target: %+v", result)
	}
}
