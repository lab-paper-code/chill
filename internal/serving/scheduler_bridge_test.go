package serving

import (
	"context"
	"math/rand/v2"
	"slices"
	"testing"
)

func capacityBridgeFixture(t *testing.T) (SchedulerSettings, SchedulerSnapshot, SchedulerInstance, SchedulerInstance) {
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
	snap.Nodes[slices.IndexFunc(snap.Nodes, func(node SchedulerNode) bool { return node.Node == blocked })].MemoryBytes = blocker.Allocation.MemoryBytes
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
	capacityServiceRatesForTest(&snap, map[string]float64{
		snap.Instances[0].Profile.ID: .372, wanted.Profile.ID: .752,
	})
	return s, snap, blocker, bridge
}

func TestCapacityStagedBridgePreparesBeforeRoutedBlockerStopsAC45(t *testing.T) {
	s, snap, blocker, bridge := capacityBridgeFixture(t)
	decision := capacityDecideForTest(t, s, snap)
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
	s, snap, blocker, _ := capacityBridgeFixture(t)
	snap.Instances[0].Ready, snap.Instances[0].Terminated = false, true
	decision := capacityDecideForTest(t, s, snap)
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
	s, snap, _, bridge := capacityBridgeFixture(t)
	decision := capacityDecideForTest(t, s, snap)
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
	for _, constraint := range []string{"physical slots", "bridge replica budget", "reserve", "routing disabled"} {
		t.Run(constraint, func(t *testing.T) {
			s, snap, _, _ := capacityBridgeFixture(t)
			switch constraint {
			case "physical slots":
				s.MaxInstances = 3
			case "bridge replica budget":
				s.MaxReplicas = 1
			case "reserve":
				s.TransitionReserveFraction = .1
			case "routing disabled":
				s.Controls.Routing = false
			}
			got := capacityDecideForTest(t, s, snap)
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

func TestCapacityBridgeMissingCostUsesLabeledEstimate(t *testing.T) {
	s, snap, _, bridge := capacityBridgeFixture(t)
	snap.Costs = slices.DeleteFunc(snap.Costs, func(cost OperationCost) bool { return cost.Node == bridge.Node })
	got := capacityDecideForTest(t, s, snap)
	if got.Target == nil || !slices.ContainsFunc(got.Target.Actions, func(a Action) bool { return a.RouteTarget != nil }) {
		t.Fatal("missing measurement blocked the safe intermediate route")
	}
	index := slices.IndexFunc(got.Target.Actions, func(a Action) bool { return a.Kind == schedulerActionPrepare && a.Node == bridge.Node })
	if index < 0 {
		t.Fatal("bridge preparation missing")
	}
	prepare := got.Target.Actions[index]
	if prepare.CostProvenance == nil || prepare.CostProvenance.Source == operationSourceExact {
		t.Fatalf("transferred/default bridge estimate lacks provenance: %+v", prepare)
	}
}

func TestBridgeExcludesIndependentlyRetiringIdleDestination(t *testing.T) {
	settings, snapshot, _, bridge := capacityBridgeFixture(t)
	decision := capacityDecideForTest(t, settings, snapshot)
	if decision.Target == nil || len(decision.Capacity.BridgeStages) == 0 {
		t.Fatal("fixture needs a staged bridge")
	}
	idleNode := poolsAddNode(&snapshot, "obsolete-idle", map[string]float64{"fixed": 10})
	idle := poolsInstance(bridge, "obsolete-idle", idleNode, "fixed", "w2")
	snapshot.Instances = append(snapshot.Instances, idle)
	snapshot.Options = append(snapshot.Options, poolsOption(idle))
	capacityServiceRatesForTest(&snapshot, map[string]float64{
		snapshot.Instances[0].Profile.ID: .372,
	})
	keep := SchedulerCandidate{Nodes: snapshot.Nodes, Instances: snapshot.Instances, Routing: snapshot.Routing}
	planner := newCapacityPlanner(context.Background(), settings, snapshot, keep, decision)
	planner.plans = make([][]*capacityPlan, len(snapshot.Nodes))
	for n, node := range snapshot.Nodes {
		plan := &capacityPlan{mode: node.PowerMode}
		for _, instance := range decision.Target.Instances {
			if instance.Node == node.Node {
				plan.instances = append(plan.instances, instance)
			}
		}
		planner.plans[n] = []*capacityPlan{plan}
	}
	compiler := schedulerBridgeCompiler{p: planner, choice: make([]int, len(snapshot.Nodes)),
		goal:   SchedulerCandidate{Nodes: slices.Clone(snapshot.Nodes), Routing: snapshot.Routing},
		out:    &schedulerBridgeSchedule{instances: map[string]SchedulerInstance{}},
		before: map[string]bool{}, ends: map[string]float64{}, blocked: map[string]string{}, retiring: map[string]bool{},
		unavailable: map[string]bool{}, prepares: map[string]string{}, routing: map[string]map[string]int32{}}
	if _, reason := compiler.initialize(); reason != "" {
		t.Fatal(reason)
	}
	compiler.resolve()
	if !compiler.handoff(planner.ws[planner.index["w2"]]) {
		t.Fatal("safe bridge destination was lost")
	}
	for _, action := range compiler.out.actions {
		if action.RouteTarget != nil && action.RouteTarget.Routing["w2"][idle.ID] > 0 {
			t.Fatal("bridge routed to an obsolete idle executor whose independent stop can already run")
		}
	}
}
