package kube

import (
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestReplacementCompletionHistoryIndependentOfDemandWindow(t *testing.T) {
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	node := serving.NodeReference{Name: "n", UID: "node-uid"}
	instance := serving.SchedulerInstance{ID: "new", Node: node, WorkloadUID: "w", Profile: &serving.Profile{ID: "new-profile"}, Ready: true}
	replacement := serving.SchedulerReplacement{NodeUID: node.UID, WorkloadUID: "w", ProfileID: instance.Profile.ID}
	target := serving.SchedulerCandidate{ID: "replacement", Instances: []serving.SchedulerInstance{instance}}
	parent := &actionRun{owner: target.ID, action: serving.Action{Kind: transitionRunKind, Status: nativeOperationRunning}, result: &OperationResult{Operation: Operation{Target: &target}}, replacements: []serving.SchedulerReplacement{replacement}}
	c := &SchedulerController{runs: map[string]*actionRun{"parent": parent}, config: SchedulerControllerConfig{Settings: serving.SchedulerSettings{ControlInterval: 10 * time.Second, MaxObservationAge: time.Minute}}}
	snapshot := serving.SchedulerSnapshot{Instances: target.Instances, ScaleDown: map[string]serving.SchedulerDownState{"w": {Allowed: true}}}
	got, _ := c.reconcileSnapshot(snapshot, now, time.Second)
	if len(got.Replacements) != 1 || !got.Replacements[0].CompletedAt.Equal(now) || !got.ScaleDown["w"].Allowed {
		t.Fatalf("completed replacement/demand state lost: %+v", got)
	}
	// Action receipts expire with the arrival window, but replacement history does not.
	got, _ = c.reconcileSnapshot(snapshot, now.Add(17*time.Second), time.Second)
	if len(got.Replacements) != 1 || !got.Replacements[0].CompletedAt.Equal(now) {
		t.Fatalf("history reset by demand window: %+v", got.Replacements)
	}
	got, _ = c.reconcileSnapshot(snapshot, now.Add(2*time.Minute), time.Second)
	if len(got.Replacements) != 0 {
		t.Fatal("observation gap retained replacement history")
	}
	c.replacements = []serving.SchedulerReplacement{replacement}
	snapshot.Instances[0].Node.UID = "new-node-uid"
	got, _ = c.reconcileSnapshot(snapshot, now.Add(2*time.Minute+time.Second), time.Second)
	if len(got.Replacements) != 0 {
		t.Fatal("node UID change retained replacement history")
	}
}
