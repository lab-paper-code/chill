package kube

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

func TestContinueWithoutRouteSkipsDemandWindow(t *testing.T) {
	controller := schedulerEmptyPoolController(t)
	at := time.Now().UTC()
	if err := controller.Continue(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, err := controller.observationLog.SchedulerObservationPage(context.Background(), at, time.Now().Add(time.Second), 10, 0, observation.SchedulerObservationSelection{})
	if err != nil || len(rows) != 1 || rows[0].Control == nil {
		t.Fatalf("control rows: %+v %v", rows, err)
	}
	if rows[0].Control.Window != 0 {
		t.Fatalf("continuation without a route loaded demand evidence: %s", rows[0].Control.Window)
	}
}

func TestContinuationWindowFollowsUnfinishedOwnerRoutes(t *testing.T) {
	controller := schedulerEmptyPoolController(t)
	target := &serving.SchedulerCandidate{ID: "goal", Actions: []serving.Action{{ID: "route", Kind: servingstate.OperationRoute}}}
	installTestTransition(controller, target)
	parent := controller.runs[transitionRunKind+"/"+target.ID]
	if !controller.continuationNeedsWindowLocked() {
		t.Fatal("unfinished route lost current demand for Ready expansion")
	}
	controller.runs["route"] = &actionRun{owner: parent.owner,
		action: serving.Action{Kind: servingstate.OperationRoute, Status: servingstate.NativeOperationComplete},
		result: &servingstate.OperationResult{Status: servingstate.NativeOperationComplete}}
	if controller.continuationNeedsWindowLocked() {
		t.Fatal("ACKed route cleanup rereads request history")
	}
	controller.runs["route"].owner = "another-owner"
	if !controller.continuationNeedsWindowLocked() {
		t.Fatal("another owner's route completion suppressed demand refresh")
	}
	parent.action.Status, parent.reserved = servingstate.NativeOperationFailed, true
	if controller.continuationNeedsWindowLocked() {
		t.Fatal("failed owner cleanup loaded route expansion demand")
	}
}

func TestContinuationCaptureOmitsHistoricalEvidence(t *testing.T) {
	controller := schedulerEmptyPoolController(t)
	ctx := context.Background()
	at := time.Now().UTC()
	for i := range 1000 {
		id := fmt.Sprint(i)
		for _, kind := range []string{"received", "response_finished"} {
			if err := controller.observationLog.RequestEvent(serving.RequestEvent{Kind: kind, RequestID: id, At: at, Outcome: "completed"}); err != nil {
				t.Fatal(err)
			}
		}
		if err := controller.observationLog.RequestClosed(id, serving.RequestTimings{}); err != nil {
			t.Fatal(err)
		}
	}
	var historical, physical []time.Duration
	for i := range 22 {
		for _, demand := range []bool{true, false} {
			timing := &observation.SchedulerControlObservation{}
			started := time.Now()
			snapshot, err := controller.captureTimed(ctx, started, controller.config, timing, demand)
			elapsed := time.Since(started)
			if err != nil || demand && len(snapshot.Arrivals) != 1000 || !demand && (len(snapshot.Arrivals) != 0 || timing.Window != 0) {
				t.Fatalf("capture evidence selection: arrivals=%d timing=%+v error=%v", len(snapshot.Arrivals), timing, err)
			}
			if i > 0 && demand {
				historical = append(historical, elapsed)
			} else if i > 0 {
				physical = append(physical, elapsed)
			}
		}
	}
	slices.Sort(historical)
	slices.Sort(physical)
	t.Logf("capture p50 with 1000 request rows: historical=%s physical-only=%s", historical[len(historical)/2], physical[len(physical)/2])
}
