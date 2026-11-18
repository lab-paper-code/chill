package kube

import (
	"slices"
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

// A run with a result projects its end, the start of the next measured window.
// A running run has none yet, and neither has a cancelled one, which keeps
// running until its operation returns.
func TestActionProjectionCarriesResultEnd(t *testing.T) {
	start, ended := time.Unix(100, 0), time.Unix(103, 0)
	cancelled := serving.Action{ID: "cancel", Kind: servingstate.OperationPrepare, Status: servingstate.NativeOperationCancel}
	done := serving.Action{ID: "done", Kind: servingstate.OperationRoute, Status: servingstate.NativeOperationComplete}
	running := serving.Action{ID: "running", Kind: servingstate.OperationRemove, Status: servingstate.NativeOperationRunning}
	controller := &SchedulerController{reservationState: &reservationState{runs: map[string]*actionRun{
		cancelled.ID: {action: cancelled, started: start, result: &servingstate.OperationResult{}},
		done.ID:      {action: done, started: start, result: &servingstate.OperationResult{EndedAt: ended}},
		running.ID:   {action: running, started: start, result: &servingstate.OperationResult{}},
	}}}
	projected := controller.actionsLocked(start.Add(4 * time.Second))
	if len(projected) != 3 || projected[0].EndedAt != nil || projected[1].EndedAt == nil || !projected[1].EndedAt.Equal(ended) || projected[2].EndedAt != nil {
		t.Fatalf("projection must carry only an ended run's end: %+v", projected)
	}
}

// The measurements of the published decision come back in the next captured
// snapshot: that is the planner's only memory of a miss.
func TestPublishedMeasurementsReturnInTheNextSnapshot(t *testing.T) {
	c := &SchedulerController{reservationState: &reservationState{runs: map[string]*actionRun{}}}
	fact := serving.SchedulerMeasurement{DefinitionID: "w/1/m/1/p/1", Serving: []serving.SchedulerServingShare{{ProfileID: "profile", MaxBatch: 4}}, OnTimeRate: 18}
	at := time.Unix(1000, 0).UTC()
	published := serving.SchedulerSnapshot{At: at}
	c.mu.Lock()
	c.publishDecisionLocked(&published, &serving.SchedulerDecision{At: at, Measurements: []serving.SchedulerMeasurement{fact}})
	c.mu.Unlock()
	next := serving.SchedulerSnapshot{At: at.Add(time.Second)}
	c.annotateHistory(&next, time.Minute)
	if len(next.Measurements) != 1 || next.Measurements[0].DefinitionID != fact.DefinitionID || next.Measurements[0].OnTimeRate != fact.OnTimeRate ||
		!slices.Equal(next.Measurements[0].Serving, fact.Serving) {
		t.Fatalf("the next snapshot must carry the published measurements: %+v", next.Measurements)
	}
}

// A run that ended inside the observation window is kept, although capture
// I/O moved the snapshot time past the window's length since its end: its end
// still starts the measured window.
func TestRunRetiresOnlyBeforeTheObservationWindow(t *testing.T) {
	window, ended := time.Minute, time.Unix(1000, 0).UTC()
	done := serving.Action{ID: "done", Kind: servingstate.OperationRoute, Status: servingstate.NativeOperationComplete}
	c := &SchedulerController{reservationState: &reservationState{runs: map[string]*actionRun{
		done.ID: {action: done, started: ended.Add(-time.Second), result: &servingstate.OperationResult{EndedAt: ended}},
	}}}
	snapshot := serving.SchedulerSnapshot{ObservationEnd: ended.Add(window - time.Second), At: ended.Add(window + time.Second)}
	if next, _ := c.reconcileSnapshot(snapshot, snapshot.At, window, nil); len(next.Actions) != 1 || next.Actions[0].EndedAt == nil || !next.Actions[0].EndedAt.Equal(ended) {
		t.Fatalf("a run that ended inside the observation window must stay: %+v", next.Actions)
	}
	snapshot.ObservationEnd = ended.Add(window + time.Second)
	if next, _ := c.reconcileSnapshot(snapshot, snapshot.At, window, nil); len(next.Actions) != 0 {
		t.Fatalf("a run that ended before the observation window must retire: %+v", next.Actions)
	}
}
