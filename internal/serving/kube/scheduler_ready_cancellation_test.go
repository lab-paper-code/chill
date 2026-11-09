package kube

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

func TestContinueReadyExpansionUsesCallerCancellation(t *testing.T) {
	controller, prepared := tickReadySubsetController(t, nil)
	prepared.Store(true)
	lifetime, stop := context.WithCancel(context.Background())
	controller.ctx = lifetime
	defer stop()
	controller.config.Settings.Controls = serving.SchedulerControls{Routing: true}
	snapshot := handoffSnapshot(t, controller)
	uid := snapshot.Workloads[0].UID
	controller.config.Settings.MinimumDemand = map[string]float64{uid: 100000}
	var added serving.SchedulerInstance
	for _, instance := range snapshot.Instances {
		if instance.Node.Name == "b" {
			added = instance
			added.WorkloadUID = uid
		}
	}
	if added.ID == "" {
		t.Fatal("missing independently Ready fixture executor")
	}
	late := added
	late.ID, late.Node = "late-executor", serving.NodeReference{Name: "late", UID: "late"}
	goal := &serving.SchedulerCandidate{ID: "cancel-ready-replay", Instances: []serving.SchedulerInstance{added, late}, Routing: map[string]map[string]int32{uid: {added.ID: 800, late.ID: 200}}, Actions: []serving.Action{
		{ID: "late-prepare", Kind: servingstate.OperationPrepare, Instance: late.ID, Node: late.Node},
		{ID: "late-route", Kind: servingstate.OperationRoute, DependsOn: []string{"late-prepare"}},
	}}
	installTestTransition(controller, goal)
	parent := controller.runs[transitionRunKind+"/"+goal.ID]
	parent.admittedControls = controller.config.Settings.Controls
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- controller.Continue(ctx, time.Now()) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("caller cancellation lost: %v", err)
		}
	case <-time.After(time.Second):
		stop()
		<-done
		t.Fatal("Ready replay retained reservation lock after caller cancellation")
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if lifetime.Err() != nil || !parent.reserved || parent.result.Operation.Target != goal {
		t.Fatal("caller cancellation changed controller lifetime or frozen reservation")
	}
}
