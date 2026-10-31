package serving

import (
	"math/rand/v2"
	"slices"
	"testing"
)

func capacityBridgeFixture(t *testing.T) (SchedulerSettings, SchedulerSnapshot, []CapacityRow, SchedulerInstance, SchedulerInstance) {
	t.Helper()
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ComparisonPeriod = 6*s.ControlInterval, 600*s.ControlInterval
	s.MinimumDemand = map[string]float64{"w1": .52, "w2": 20}
	s.AutonomousWorkloads = append(s.AutonomousWorkloads, "w2")
	snap.Workloads[0].SLO.TargetLatency = 7 * s.ControlInterval
	other := poolsAddWorkload(&snap, "w2")
	other.SLO.TargetLatency = s.ControlInterval
	other.Model.Name = "bridge-model"
	other.Model.Paths = slices.Clone(other.Model.Paths)
	other.Model.Paths[0].Artifact = "bridge-model"
	blocked := poolsAddNode(&snap, "blocked", map[string]float64{"fixed": 10})
	spare := poolsAddNode(&snap, "spare", map[string]float64{"fixed": 10})
	blocker := poolsInstance(snap.Instances[0], "blocker", blocked, "fixed", "w2")
	profile := *blocker.Profile
	profile.ID, profile.Condition.LogicalModel, profile.Condition.Artifact.Name = "bridge-profile", other.Model.Name, other.Model.Paths[0].Artifact
	blocker.Profile = &profile
	bridge := poolsInstance(blocker, "bridge", spare, "fixed", "w2")
	wanted := poolsInstance(snap.Instances[0], "recovery", blocked, "fixed", "w1")
	snap.Instances = append(snap.Instances, blocker)
	snap.Options = append(snap.Options, poolsOption(blocker), poolsOption(bridge), poolsOption(wanted))
	snap.Routing = map[string]map[string]int32{"w1": {snap.Instances[0].ID: 1000}, "w2": {blocker.ID: 1000}}
	snap.Costs = []OperationCost{
		poolsCost(schedulerActionRemove, blocked, schedulerState(blocker), "unloaded", "fixed"),
		poolsCost(schedulerActionPrepare, blocked, "unloaded", schedulerState(wanted), "fixed"),
		poolsCost(schedulerActionPrepare, spare, "unloaded", schedulerState(bridge), "fixed"),
	}
	for index := range 2 {
		snap.Outstanding = append(snap.Outstanding, SchedulerRequest{ID: string(rune('a' + index)), Definition: other, ReceivedAt: snap.At, Phase: phasePending})
	}
	rows := capacityRowsForTest(s, snap, capacityKappaByProfile(map[string]float64{
		snap.Instances[0].Profile.ID: .372, wanted.Profile.ID: .752,
	}))
	return s, snap, rows, blocker, bridge
}

func TestCapacityStagedBridgePreparesBeforeRoutedBlockerStopsAC45(t *testing.T) {
	s, snap, rows, blocker, bridge := capacityBridgeFixture(t)
	decision := capacityDecideForTest(t, s, snap, rows)
	if decision.Target == nil || decision.Target.Keep {
		t.Fatalf("safe prepared bridge was not selected: status=%s reasons=%v", decision.Status, decision.Reasons)
	}
	actions := decision.Target.Actions
	stage := slices.IndexFunc(actions, func(a Action) bool { return a.Kind == schedulerActionRoute && a.From == "handoff-bridge" })
	if stage < 0 {
		t.Fatalf("resource replacement omitted its ACK handoff: %+v", actions)
	}
	prepare := slices.IndexFunc(actions, func(a Action) bool { return a.Kind == schedulerActionPrepare && a.Node == bridge.Node })
	stop := slices.IndexFunc(actions, func(a Action) bool { return a.Kind == schedulerActionStop && a.Instance == blocker.ID })
	if prepare < 0 || stop < 0 || !slices.Contains(actions[stage].DependsOn, actions[prepare].ID) || !slices.Contains(actions[stop].DependsOn, actions[stage].ID) {
		t.Fatalf("bridge prepare/ACK/stop dependency missing: %+v", actions)
	}
	final := slices.IndexFunc(actions, func(a Action) bool { return a.Kind == schedulerActionRoute && a.From == "" })
	if final < 0 || final <= stage {
		t.Fatal("intermediate handoff replaced the frozen final route")
	}
}

func TestCapacityBridgePreservesTerminatedRecoveryRouteSCH51(t *testing.T) {
	s, snap, rows, blocker, _ := capacityBridgeFixture(t)
	snap.Instances[0].Ready, snap.Instances[0].Terminated = false, true
	decision := capacityDecideForTest(t, s, snap, rows)
	if decision.Target == nil {
		t.Fatalf("terminal recovery lost its target: %v", decision.Reasons)
	}
	actions := decision.Target.Actions
	stage := slices.IndexFunc(actions, func(a Action) bool { return a.RouteTarget != nil })
	stop := slices.IndexFunc(actions, func(a Action) bool { return a.Kind == schedulerActionStop && a.Instance == blocker.ID })
	if stage < 0 || stop < 0 || !slices.Contains(actions[stop].DependsOn, actions[stage].ID) {
		t.Fatalf("terminal recovery lost executable bridge handoff: %+v", actions)
	}
	if actions[stage].RouteTarget.Routing["w1"][snap.Instances[0].ID] != snap.Routing["w1"][snap.Instances[0].ID] {
		t.Fatal("bridge changed the preserved terminal UID declaration")
	}
}

func TestCapacityBridgeQueuedIngressReplayAC45(t *testing.T) {
	s, snap, rows, _, bridge := capacityBridgeFixture(t)
	decision := capacityDecideForTest(t, s, snap, rows)
	if decision.Target == nil {
		t.Fatal("bridge fixture lost its target")
	}
	actions := decision.Target.Actions
	stage := slices.IndexFunc(actions, func(a Action) bool { return a.RouteTarget != nil })
	prepare := slices.IndexFunc(actions, func(a Action) bool { return a.Kind == schedulerActionPrepare && a.Node == bridge.Node })
	if stage < 0 || prepare < 0 {
		t.Fatal("bridge fixture lost its staged preparation")
	}
	if len(decision.Capacity.BridgeStages) != 1 || actions[stage].RouteTarget == nil || len(actions[stage].RouteTarget.Actions) != 0 {
		t.Fatal("bridge lost its immutable, acyclic capacity witness")
	}
	refs := observationReferences{}
	stored := refs.candidate(*decision.Target)
	if stored.Actions[stage].RouteTarget.Instances[0].Profile != nil || actions[stage].RouteTarget.Instances[0].Profile == nil {
		t.Fatal("nested bridge catalog projection leaked or mutated live profiles")
	}
	state := schedulerReplayState{settings: s, snap: snap, c: *decision.Target, routeCredits: map[string][]int64{}, rng: rand.New(rand.NewPCG(1, 0)), out: schedulerReplayResult{definitions: map[string]*Definition{}}}
	state.initializeInstances()
	state.initializeActions()
	state.initializeOutstanding()
	state.admitArrivals()
	state.weights = make([]int32, len(state.instances))
	state.available = make([]bool, len(state.instances))
	for range len(actions) * 2 {
		if !state.completeActions() {
			t.Fatalf("staged replay rejected its executable DAG: %v", state.out.reasons)
		}
		for range len(actions) {
			if !state.startActions() {
				t.Fatalf("staged replay rejected its executable DAG: %v", state.out.reasons)
			}
		}
		if state.routingApplied && state.applied.ID == actions[stage].RouteTarget.ID {
			state.routeRequests()
			if state.byID[actions[prepare].Instance].outstanding != 2 {
				t.Fatal("queued gateway ingress did not move to the prepared bridge")
			}
		}
		if state.routingApplied && state.applied.ID == decision.Target.ID {
			return
		}
		next := float64(1 << 62)
		for _, action := range state.actions {
			if action.started && !action.done && action.finish > state.now {
				next = min(next, action.finish)
			}
		}
		if next == float64(1<<62) {
			t.Fatalf("bridge replay deadlocked: %+v", state.actions)
		}
		state.now = next
	}
	t.Fatal("staged replay did not reach the frozen final route")
}

func TestCapacityBridgePreservesSupplyReserveAndPhysicalLimitsAC45(t *testing.T) {
	for _, constraint := range []string{"steady bridge supply", "physical slots", "bridge replica budget", "reserve", "routing disabled", "missing bridge cost"} {
		t.Run(constraint, func(t *testing.T) {
			s, snap, rows, _, bridge := capacityBridgeFixture(t)
			switch constraint {
			case "steady bridge supply":
				s.MinimumDemand["w2"] = 101
			case "physical slots":
				s.MaxInstances = 3
			case "bridge replica budget":
				s.MaxReplicas = 1
			case "reserve":
				s.TransitionReserveFraction = .1
			case "routing disabled":
				s.Controls.Routing = false
			case "missing bridge cost":
				snap.Costs = slices.DeleteFunc(snap.Costs, func(cost OperationCost) bool { return cost.Node == bridge.Node })
			}
			got := capacityDecideForTest(t, s, snap, rows)
			if got.Target == nil {
				t.Fatalf("constraint lost the safe fallback: %s", got.Status)
			}
			for _, action := range got.Target.Actions {
				if action.RouteTarget != nil {
					t.Fatalf("%s admitted an unproved intermediate route: %+v", constraint, action)
				}
			}
		})
	}
}
