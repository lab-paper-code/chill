package serving

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"slices"
	"testing"
	"time"
)

func TestCapacityLeximinAC40(t *testing.T) {
	keep := capacityResult{worst: math.Inf(1), excesses: []float64{math.Inf(1), math.Inf(1), math.Inf(1)}, keep: true}
	improved := capacityResult{worst: math.Inf(1), excesses: []float64{math.Inf(1), 2, -0.5}, joules: 100}
	if !improved.better(&keep) || keep.better(&improved) {
		t.Fatal("one impossible Workload hid the other Workloads' improvement")
	}
	second := improved
	second.excesses = []float64{math.Inf(1), 2, -0.6}
	second.joules = 200
	if !second.better(&improved) {
		t.Fatal("third exceedance did not break the worst/second-worst tie")
	}
	second.excesses = slices.Clone(improved.excesses)
	if second.better(&improved) {
		t.Fatal("equal exceedances did not prefer lower energy")
	}
}


type rev76Fixture struct {
	Settings SchedulerSettings `json:"settings"`
	Cold     SchedulerSnapshot `json:"cold"`
	Post     SchedulerSnapshot `json:"post"`
	Profiles []*Profile        `json:"profiles"`
	Rows     []CapacityRow     `json:"rows"`
}

func loadRev76Fixture(t *testing.T) rev76Fixture {
	t.Helper()
	data, err := os.ReadFile("testdata/rev76-bootstrap.json")
	if err != nil {
		t.Fatal(err)
	}
	var f rev76Fixture
	if err = json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	// Read historical raw only to prove its controlled-execution rows cannot
	// enter the shared planner; never rewrite its row keys into current keys.
	estimates := schedulerEstimateSettingsForTest()
	f.Settings.DefaultOperationCost, f.Settings.DefaultIdlePower = estimates.DefaultOperationCost, estimates.DefaultIdlePower
	margin := f.Settings.CompletionMarginMS
	f.Settings, err = (SchedulerSettingsInput{SchedulerSettings: f.Settings, CompletionMarginMS: &margin}).Settings()
	if err != nil {
		t.Fatal(err)
	}
	profiles := map[string]*Profile{}
	for _, profile := range f.Profiles {
		profiles[profile.ID] = profile
	}
	f.Post.Workloads, f.Post.Baselines, f.Post.Costs = f.Cold.Workloads, f.Cold.Baselines, f.Cold.Costs
	for _, snapshot := range []*SchedulerSnapshot{&f.Cold, &f.Post} {
		for k := range snapshot.Options {
			i := &snapshot.Options[k].Instance
			i.Profile = profiles[i.ProfileID]
			if i.Profile == nil {
				t.Fatalf("missing raw profile %s", i.ProfileID)
			}
		}
		for k := range snapshot.Instances {
			i := &snapshot.Instances[k]
			i.Profile = profiles[i.ProfileID]
		}
	}
	return f
}

func TestControlledRawReplayCapacityDoesNotCrossSharedCutover(t *testing.T) {
	fixture := loadRev76Fixture(t)
	settings, snapshot := fixture.Settings, fixture.Cold
	decision := capacityDecideForTest(t, settings, snapshot, fixture.Rows)
	if decision.Status != CapacityPending || decision.Target != nil {
		t.Fatalf("controlled raw rows were relabeled as current shared capacity: status=%s reasons=%v", decision.Status, decision.Reasons)
	}
}

func sharedBootstrapFixture() (SchedulerSettings, SchedulerSnapshot, []CapacityRow) {
	settings, snapshot := schedulerFixture()
	base := snapshot.Instances[0]
	settings.MaxReplicas, settings.MaxInstances = 3, 3
	settings.AutonomousWorkloads = []string{"w1", "w2", "w3"}
	settings.MinimumDemand = map[string]float64{"w1": 20, "w2": 20, "w3": 20}
	poolsAddWorkload(&snapshot, "w2")
	poolsAddWorkload(&snapshot, "w3")
	for _, uid := range []string{"n2", "n3"} {
		node := poolsAddNode(&snapshot, uid, map[string]float64{"fixed": 10})
		option := poolsInstance(base, "option-"+uid, node, "fixed", "")
		snapshot.Options = append(snapshot.Options, SchedulerOption{Instance: option})
	}
	for index := range snapshot.Options {
		option := &snapshot.Options[index]
		option.CompatibleUIDs = slices.Clone(settings.AutonomousWorkloads)
		snapshot.Costs = append(snapshot.Costs, poolsCost(schedulerActionPrepare, option.Instance.Node, "unloaded", schedulerState(option.Instance), "fixed"))
	}
	snapshot.Instances = nil
	snapshot.Routing = map[string]map[string]int32{}
	return settings, snapshot, capacityRowsForTest(settings, snapshot, nil)
}

func TestSharedStartupUsesPreparedCapacityPerUIDAC41(t *testing.T) {
	settings, snapshot, rows := sharedBootstrapFixture()
	pending := capacityDecideForTest(t, settings, snapshot, nil)
	if pending.Status != CapacityPending || pending.Target != nil || len(pending.Keep.Actions) != 0 {
		t.Fatalf("missing capacity committed startup: %+v", pending)
	}
	partial := capacityDecideForTest(t, settings, snapshot, rows[:1])
	if partial.Target == nil || partial.Target.Keep {
		t.Fatalf("prepared UID did not progress independently: %+v", partial)
	}
	decision := capacityDecideForTest(t, settings, snapshot, rows)
	if decision.Target == nil || decision.Target.Keep {
		t.Fatalf("complete shared rows did not release bootstrap: status=%s reasons=%v", decision.Status, decision.Reasons)
	}
	for _, uid := range settings.AutonomousWorkloads {
		if !slices.ContainsFunc(decision.Capacity.Workloads, func(workload SchedulerCapacityWorkload) bool {
			return workload.UID == uid && workload.KappaNew > 0 && len(workload.Weights) > 0
		}) {
			t.Fatalf("joint bootstrap omitted %s: %+v", uid, decision.Capacity.Workloads)
		}
	}
	if prepares := slices.DeleteFunc(slices.Clone(decision.Target.Actions), func(action Action) bool { return action.Kind != schedulerActionPrepare }); len(prepares) != 3 {
		t.Fatalf("joint bootstrap did not prepare all three Workloads: %+v", decision.Target.Actions)
	}
}

func TestSharedBootstrapReservationAllowsIndependentProgressAC40(t *testing.T) {
	settings, snapshot, rows := sharedBootstrapFixture()
	reserved := snapshot.Options[len(snapshot.Options)-1].Instance
	reserved.WorkloadUID = "w3"
	target := SchedulerCandidate{ID: "in-flight-owner", Instances: []SchedulerInstance{reserved}, Routing: map[string]map[string]int32{"w3": {reserved.ID: 1000}}}
	reserveTargetForTest(&snapshot, &target)
	decision := capacityDecideForTest(t, settings, snapshot, rows)
	if decision.Target == nil || decision.Target.Keep {
		t.Fatalf("reservation restarted empty-pool barrier or blocked independent progress: status=%s reasons=%v", decision.Status, decision.Reasons)
	}
	if !slices.ContainsFunc(decision.Target.Actions, func(action Action) bool { return action.Kind == schedulerActionPrepare && action.Node != reserved.Node }) {
		t.Fatal("independent scopes did not prepare")
	}
	if !schedulerReservedNode(snapshot, reserved.Node) || snapshot.Reservations[0].Target.Instances[0].Node != reserved.Node {
		t.Fatal("independent planning returned the in-flight owner's physical scope")
	}
}

func TestStartupCapacityNeverUsesElapsedTimeFallback(t *testing.T) {
	s, snapshot := schedulerFixture()
	snapshot.Instances = nil
	snapshot.Routing = map[string]map[string]int32{}
	for _, elapsed := range []time.Duration{0, time.Hour} {
		snapshot.At = snapshot.ObservationEnd.Add(elapsed)
		for k := range snapshot.Nodes {
			snapshot.Nodes[k].ObservedAt = snapshot.At
		}
		got := capacityDecideForTest(t, s, snapshot, nil)
		if got.Status != CapacityPending || got.Target != nil {
			t.Fatalf("missing rows gained capacity after %v: %+v", elapsed, got)
		}
	}
	rows := capacityRowsForTest(s, snapshot, nil)
	for k := range rows {
		rows[k].Status, rows[k].Kappa = CapacityRejected, nil
	}
	got := capacityDecideForTest(t, s, snapshot, rows)
	if got.Target != nil || !slices.Contains(got.Reasons, CapacityRejected+":w1") {
		t.Fatalf("rejected capacity was used: %+v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EvaluateScheduler(ctx, s, snapshot, nil); err == nil {
		t.Fatal("canceled evaluation succeeded")
	}
}
