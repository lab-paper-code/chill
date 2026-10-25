package serving

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestActionExpectedDuration(t *testing.T) {
	node := NodeReference{Name: "node", UID: "uid"}
	cost := OperationCost{ID: "measured", Node: node, Kind: schedulerActionPrepare, To: "profile", PowerMode: "low", Meter: "wall", Scope: schedulerNodeWallScope, EnergyScope: schedulerWholeWall,
		Trials: []OperationTrial{{Duration: time.Second, Joules: 2}, {Duration: 3 * time.Second, Joules: 6}}}
	cost.Reference.Store, cost.Reference.Key, cost.Reference.Size = "evidence", "cost/trials", 100
	snapshot := SchedulerSnapshot{Nodes: []SchedulerNode{{Node: node, PowerMode: "low"}}, Costs: []OperationCost{cost}}
	action := Action{Kind: schedulerActionPrepare, Node: node, To: "profile", Status: schedulerActionPlanned}
	got := EstimateAction(snapshot, action)
	if got.ExpectedSeconds == nil || *got.ExpectedSeconds != 2 || got.ExpectedSource == nil || got.ExpectedSource.CostID != cost.ID || got.ExpectedSource.Statistic != "mean" || got.ExpectedSource.TrialCount != 2 || got.ExpectedSource.Reference != cost.Reference {
		t.Fatalf("duration and provenance: %+v", got)
	}
	got.Status, got.Elapsed = phaseRunning, 5*time.Second
	snapshot.Costs[0].Trials = []OperationTrial{{Duration: 9 * time.Second, Joules: 18}}
	frozen := EstimateAction(snapshot, got)
	if frozen.ExpectedSeconds != got.ExpectedSeconds || *frozen.ExpectedSeconds != 2 || frozen.ExpectedSource != got.ExpectedSource || frozen.Elapsed != 5*time.Second {
		t.Fatalf("running estimate changed: %+v", frozen)
	}
	snapshot.Actions = []Action{got}
	plan := schedulerPlanState{snap: snapshot, nodeTail: map[string]string{}}
	plan.add(got.Kind, got.Node, got.Instance, got.From, got.To, nil)
	plan.selectCosts()
	if continued := plan.actions[0]; continued.ExpectedSeconds != got.ExpectedSeconds || continued.ExpectedSource != got.ExpectedSource || continued.Elapsed != got.Elapsed {
		t.Fatalf("replanning replaced running evidence: %+v", continued)
	}
	for _, kind := range []string{schedulerActionPrepare, schedulerActionRoute, schedulerActionStop, schedulerActionDrain} {
		action.Kind = kind
		action.To = "unmeasured"
		missing := EstimateAction(snapshot, action)
		if missing.ExpectedSeconds != nil || missing.ExpectedSource != nil {
			t.Fatalf("unmeasured action acquired estimate: %+v", missing)
		}
		encoded, err := json.Marshal(missing)
		if err != nil || !strings.Contains(string(encoded), `"expectedSeconds":null`) || !strings.Contains(string(encoded), `"expectedSource":null`) {
			t.Fatalf("missing estimates must remain explicit null: %s %v", encoded, err)
		}
	}
	action.Kind, action.To = schedulerActionPrepare, "profile"
	snapshot.Costs[0].PowerMode = "other"
	if mismatch := EstimateAction(snapshot, action); mismatch.ExpectedSeconds != nil {
		t.Fatalf("wrong power condition supplied duration: %+v", mismatch)
	}
	snapshot.Costs[0].PowerMode = "low"
	snapshot.Costs[0].Trials[0].Duration = 0
	if invalid := EstimateAction(snapshot, action); invalid.ExpectedSeconds != nil {
		t.Fatalf("invalid trial supplied duration: %+v", invalid)
	}
}
