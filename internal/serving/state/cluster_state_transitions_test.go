package state

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestStartTransitionRetainsWholeLaunchCriticalSection(t *testing.T) {
	state := &ReservationState{runs: map[string]*ActionRun{}}
	target := serving.SchedulerCandidate{ID: "target", Actions: []serving.Action{{Kind: OperationPrepare, Node: serving.NodeReference{Name: "node", UID: "node-uid"}}}}
	var order []string
	inside := func(stage string) {
		if state.mu.TryLock() {
			state.mu.Unlock()
			t.Fatal("launch callback escaped the original reservation critical section")
		}
		order = append(order, stage)
	}
	state.StartTransition(&target, serving.SchedulerControls{}, func() {
		inside("reconcile")
	}, func(serving.Action) bool {
		inside("eligible")
		return true
	}, func() {
		inside("dispatch")
		if run := state.runs[TransitionRunKind+"/"+target.ID]; run == nil || run.result.Operation.Target != &target {
			t.Fatal("dispatch did not see the same admitted target")
		}
	})
	if !reflect.DeepEqual(order, []string{"reconcile", "eligible", "dispatch"}) {
		t.Fatal(order)
	}
	if !state.mu.TryLock() {
		t.Fatal("launch retained its mutex after returning")
	}
	state.mu.Unlock()
}

func TestCompleteRunPublishesReceiptAndActionAtomically(t *testing.T) {
	state := &ReservationState{}
	before := &OperationResult{}
	run := &ActionRun{reserved: true, result: before, action: serving.Action{Status: NativeOperationRunning}}
	result := OperationResult{Status: NativeOperationPartial, StartedAt: time.Unix(1, 0), EndedAt: time.Unix(2, 0)}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	state.mu.Lock()
	done := make(chan struct{})
	go func() {
		state.CompleteRun(run, &result, false, errors.New("physical failure"), ctx)
		close(done)
	}()
	if run.result != before || run.action.Status != NativeOperationRunning || len(state.state.Results) != 0 {
		t.Fatal("completion was visible before the original reservation mutex was released")
	}
	state.mu.Unlock()
	<-done
	if run.result != &result || run.reserved || run.action.Status != NativeOperationFailed || run.action.Elapsed != time.Second || len(state.state.Results) != 1 {
		t.Fatal("receipt, reservation and terminal action were not published together")
	}
	if run.action.Reason != serving.ActionDeadlineExceeded+": physical failure" {
		t.Fatal(run.action.Reason)
	}
}
