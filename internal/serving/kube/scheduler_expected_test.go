package kube

import (
	"testing"
	"time"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestActionDurationSurvivesExecutionProjection(t *testing.T) {
	seconds := 2.5
	source := &serving.ActionExpectedSource{CostID: "cost", Statistic: "mean", TrialCount: 2}
	action := serving.Action{ID: "action", Kind: servingstate.OperationRemove, Status: servingstate.NativeOperationRunning, ExpectedSeconds: &seconds, ExpectedSource: source}
	operation := scheduledOperation(action, nil, SchedulerControllerConfig{}, nil, nil)
	if operation.ExpectedSeconds != action.ExpectedSeconds || operation.ExpectedSource != source {
		t.Fatalf("operation lost frozen estimate: %+v", operation)
	}
	start := time.Unix(100, 0)
	controller := &SchedulerController{reservationState: &reservationState{runs: map[string]*actionRun{action.ID: {action: action, started: start}}}}
	projected := controller.actionsLocked(start.Add(4 * time.Second))
	if len(projected) != 1 || projected[0].ExpectedSeconds != action.ExpectedSeconds || projected[0].ExpectedSource != source || projected[0].Elapsed != 4*time.Second {
		t.Fatalf("projection mixed measured elapsed and expected duration: %+v", projected)
	}
}
