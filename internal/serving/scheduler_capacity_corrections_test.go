package serving

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"
)

func TestSchedulerReadyExpansionCancellationPreservesFrozenState(t *testing.T) {
	settings, snapshot, _ := capacityE1Fixture()
	old := snapshot.Instances[0]
	added := snapshot.Options[len(snapshot.Options)-1].Instance
	added.ID, added.WorkloadUID = "ready-addition", old.WorkloadUID
	settings.Controls.Routing = true
	capacityLoad(settings, &snapshot, snapshot.Workloads[0], 40)
	snapshot.Routing = map[string]map[string]int32{old.WorkloadUID: {old.ID: schedulerRouteUnits}}
	target := SchedulerCandidate{ID: "reserved-expansion", Nodes: snapshot.Nodes, Instances: []SchedulerInstance{added}, Routing: map[string]map[string]int32{old.WorkloadUID: {added.ID: schedulerRouteUnits}}}
	reserveTargetForTest(&snapshot, &target)
	before, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	weights, admitted := SchedulerReadyExpansion(ctx, settings, snapshot, old.WorkloadUID, []SchedulerInstance{old}, []SchedulerInstance{added})
	if admitted || weights != nil || time.Since(started) > time.Second {
		t.Fatalf("canceled expansion: admitted=%v weights=%v elapsed=%v", admitted, weights, time.Since(started))
	}
	after, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || snapshot.Reservations[0].Target != &target {
		t.Fatal("canceled expansion changed physical state or frozen reservation")
	}
}

func TestCapacityUnknownPotentialDoesNotBeatMeasuredPotential(t *testing.T) {
	planner := capacityDeltaTiny([]float64{10}, 1)
	potential := .5
	known := capacityResult{joules: 10, feedback: []FeedbackWorkloadScore{{UID: "w", Population: 4, OnTime: 2, FailureFraction: .5, PotentialExcess: &potential}}}
	unknown := capacityResult{joules: 1, feedback: []FeedbackWorkloadScore{{UID: "w", Population: 4, OnTime: 2, FailureFraction: .5}}}
	planner.rankFeedback(&known)
	planner.rankFeedback(&unknown)
	if !known.better(&unknown) || unknown.better(&known) {
		t.Fatal("missing conditional potential won an outcome tie")
	}
	unknown.feedback[0].FailureFraction = .25
	unknown.feedback[0].OnTime = 3
	unknown.excesses = nil
	planner.rankFeedback(&unknown)
	if !unknown.better(&known) {
		t.Fatal("missing conditional potential erased known failure improvement")
	}
}

func TestReplayInvalidDefaultOperationCostTerminates(t *testing.T) {
	action := &schedulerReplayAction{action: Action{Kind: schedulerActionPower}}
	state := schedulerReplayState{rng: rand.New(rand.NewPCG(1, 0))}
	cost := &OperationCost{EnergyScope: schedulerWholeWall, Trials: []OperationTrial{{Duration: 0}}}
	if state.sampleOperation(action, cost) {
		t.Fatal("invalid operation estimate was executable")
	}
	if len(state.out.reasons) == 0 {
		t.Fatal("invalid operation estimate lost its rejection reason")
	}
}

func TestReplayRouteRetainsUnboundPreparingOccupancy(t *testing.T) {
	settings, snapshot := schedulerFixture()
	unbound := snapshot.Instances[0]
	unbound.ID, unbound.WorkloadUID, unbound.Ready = "preparing-occupancy", "", false
	snapshot.Instances = append(snapshot.Instances, unbound)
	target := SchedulerCandidate{Instances: snapshot.Instances, Routing: map[string]map[string]int32{
		snapshot.Workloads[0].UID: {snapshot.Instances[0].ID: schedulerRouteUnits},
	}}
	state := schedulerReplayState{settings: settings, snap: snapshot, c: target, routeCredits: map[string][]int64{}}
	state.initializeInstances()
	if !state.applyRoute(target) {
		t.Fatalf("unbound occupancy blocked an independent route: %v", state.out.reasons)
	}
	retained := state.byID[unbound.ID]
	if retained == nil || !retained.loaded || retained.ready || retained.config.Allocation != unbound.Allocation {
		t.Fatal("route changed retained preparing occupancy")
	}
}

func TestReadyExpansionFiltersSlowerIntegerRoute(t *testing.T) {
	for _, binding := range []string{"bound", "unbound"} {
		t.Run(binding, func(t *testing.T) {
			settings, snapshot := schedulerFixture()
			settings.ObservationWindow, settings.ComparisonPeriod = time.Second, time.Second
			settings.MinimumDemand = map[string]float64{snapshot.Workloads[0].UID: 10}
			snapshot.Workloads[0].SLO.TargetLatency = 50 * time.Millisecond
			old := snapshot.Instances[0]
			node := poolsAddNode(&snapshot, "slow-ready", map[string]float64{"fixed": 10})
			added := poolsInstance(old, "slow-ready", node, "fixed", old.WorkloadUID)
			actual := added
			if binding == "unbound" {
				actual.WorkloadUID = ""
			}
			snapshot.Instances = append(snapshot.Instances, actual)
			snapshot.Routing = map[string]map[string]int32{old.WorkloadUID: {old.ID: schedulerRouteUnits}}
			capacityServiceRatesForTest(&snapshot, map[string]float64{old.Profile.ID: 1 / .03, added.Profile.ID: 1 / .07})
			old.Profile, added.Profile = snapshot.Instances[0].Profile, snapshot.Instances[1].Profile
			if weights, admitted := SchedulerReadyExpansion(context.Background(), settings, snapshot, old.WorkloadUID, []SchedulerInstance{old}, []SchedulerInstance{added}); !admitted || weights[old.ID] <= 0 {
				t.Fatalf("admission-filtered expansion must retain the feasible 30ms executor: %v", weights)
			}
		})
	}
}
