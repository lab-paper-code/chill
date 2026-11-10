package serving

import (
	"testing"
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
	if result.Workloads[0].Population != 2 || result.Workloads[0].Completed != 2 {
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
			if result.Workloads[0].Population != 1 || result.Workloads[0].Unfinished != 1 || result.Workloads[0].Completed != 0 {
				t.Fatalf("unrouted pending request must remain unfinished without invalidating replay: %+v", result.Workloads)
			}
		})
	}
}
