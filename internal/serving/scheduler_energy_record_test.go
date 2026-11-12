package serving

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSchedulerEnergyRecordScopesPredictionsAndDeduplicatesCells(t *testing.T) {
	settings, snapshot := schedulerFixture()
	settings.ComparisonPeriod = 600 * time.Second
	snapshot.Nodes[0].PowerMode = schedulerTestEcoMode
	snapshot.Instances[0].Profile.Condition.PowerMode = schedulerTestEcoMode
	snapshot.Instances[0].IdleScope = energyTestUnknownIdle
	node := poolsAddNode(&snapshot, "cheap", map[string]float64{energyTestFixedMode: 1})
	snapshot.Nodes[len(snapshot.Nodes)-1].PowerMode = energyTestFixedMode
	wanted := poolsInstance(snapshot.Instances[0], "cheap", node, energyTestFixedMode, "w1")
	wanted.IdleScope = schedulerNodeWallScope
	wanted.Profile.IdleBaselineWatts = 1
	wanted.Profile.Batches = append([]BatchProfile(nil), wanted.Profile.Batches...)
	wanted.Profile.Batches[0].EnergyPerInputJoules = .01
	snapshot.Options = append(snapshot.Options, poolsOption(wanted))
	snapshot.Costs = append(snapshot.Costs, poolsCost("prepare", node, "unloaded", schedulerState(wanted), energyTestFixedMode), poolsCost("remove", snapshot.Instances[0].Node, schedulerState(snapshot.Instances[0]), "unloaded", schedulerTestEcoMode))
	capacityLoad(settings, &snapshot, snapshot.Workloads[0], 10)
	decision := capacityDecideForTest(t, settings, snapshot)
	raw, err := json.Marshal(decision.Capacity)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err = json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	cells, ok := record["missingCells"].([]any)
	if !ok || len(cells) == 0 {
		t.Fatalf("decision-level missing-cell table absent: %s", raw)
	}
	candidates := record["undecidableCandidates"].([]any)
	references := 0
	for _, value := range candidates {
		candidate := value.(map[string]any)
		if _, inline := candidate["missingCells"]; inline {
			t.Fatalf("candidate duplicated full missing cells: %+v", candidate)
		}
		refs, ok := candidate["missingCellRefs"].([]any)
		if !ok || len(refs) == 0 {
			t.Fatalf("candidate missing cell references: %+v", candidate)
		}
		references += len(refs)
		for _, ref := range refs {
			if int(ref.(float64)) >= len(cells) {
				t.Fatalf("invalid cell reference: %v", ref)
			}
		}
	}
	if references <= len(cells) {
		t.Fatalf("fixture did not share cells across candidates: refs=%d cells=%d", references, len(cells))
	}
	selected := map[string]string{}
	for _, instance := range decision.Target.Instances {
		selected[instance.Node.UID] = planKey([]SchedulerInstance{instance})
	}
	stripped := 0
	for _, value := range record["plans"].([]any) {
		plan := value.(map[string]any)
		uid := plan["node"].(map[string]any)["uid"].(string)
		if plan["key"] == capacitySearchStartKeep || plan["key"] == selected[uid] {
			continue
		}
		stripped++
		if phases, ok := plan["idleCosts"].(map[string]any); ok {
			for _, value := range phases {
				phase := value.(map[string]any)
				for _, field := range []string{"powerMode", "state", "requestWatts", "requestReferences"} {
					if _, present := phase[field]; present {
						t.Fatalf("unselected plan kept new phase field %s: %+v", field, plan)
					}
				}
			}
		}
		if actions, ok := plan["actionCosts"].([]any); ok {
			for _, value := range actions {
				action := value.(map[string]any)
				for _, field := range []string{"seconds", "joules"} {
					if _, present := action[field]; present {
						t.Fatalf("unselected plan kept predicted action field %s: %+v", field, plan)
					}
				}
			}
		}
	}
	if stripped == 0 || len(candidates) < 2 {
		t.Fatalf("fixture lacks inventory or shared missing candidates: inventory=%d candidates=%d", stripped, len(candidates))
	}
}
