package serving

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"
)

// Replay the projected terminal receipt, not a new live power action.
func TestPowerRebootDefect9ReceiptReplay(t *testing.T) {
	raw, err := os.ReadFile("testdata/power-reboot-defect9.json")
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Status, Error  string
		StartedAt      time.Time
		Before, Actual struct {
			Node      NodeReference
			PowerMode string
			Instances []InstanceState
			States    []string
		}
		Target SchedulerCandidate
	}
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Status != "partial" || evidence.Error != "power transition changed actual loaded state" ||
		len(evidence.Before.Instances) != 1 || len(evidence.Before.States) != 1 || len(evidence.Actual.States) != 0 || evidence.Before.PowerMode == evidence.Actual.PowerMode {
		t.Fatal("fixture no longer represents defect 9's successful mode change and lost actor")
	}
	old := evidence.Before.Instances[0]
	source := SchedulerInstance{ID: old.ID, PodUID: old.PodUID, Node: old.Node, Allocation: old.Allocation,
		Profile: &Profile{ID: old.Profile}, Ready: true, Stopped: true}
	node := evidence.Target.Nodes[0]
	node.PowerMode = evidence.Before.PowerMode
	snapshot := SchedulerSnapshot{At: evidence.StartedAt, Nodes: []SchedulerNode{node}, Instances: []SchedulerInstance{source}}
	settings := schedulerEstimateSettingsForTest()
	for _, known := range []bool{false, true} {
		if known {
			snapshot.Nodes[0].Reboots = []PowerReboot{{From: evidence.Before.PowerMode, To: evidence.Actual.PowerMode}}
		}
		target := evidence.Target
		target.Actions = schedulerPlan(settings, snapshot, target)
		power := requireTransitionAction(t, target.Actions, schedulerActionPower)
		remove := requireTransitionAction(t, target.Actions, schedulerActionRemove)
		prepare := requireTransitionAction(t, target.Actions, schedulerActionPrepare)
		if known != slices.Contains(power.DependsOn, remove.ID) || known == slices.Contains(remove.DependsOn, power.ID) {
			t.Fatalf("known=%v: wrong replay ordering: %+v", known, target.Actions)
		}
		if known && !slices.Contains(prepare.DependsOn, power.ID) {
			t.Fatal("replacement can prepare before reboot power settles")
		}
		if result := schedulerReplayForTest(settings, snapshot, target); len(result.reasons) != 0 {
			t.Fatalf("known=%v: replay failed: %v", known, result.reasons)
		}
		t.Logf("defect-9 %s -> %s known=%v: power=%s remove=%s prepare=%s; replay complete", evidence.Before.PowerMode, evidence.Actual.PowerMode, known, power.ID, remove.ID, prepare.ID)
	}
}
