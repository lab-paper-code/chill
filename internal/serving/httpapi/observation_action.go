package httpapi

import (
	"encoding/json"
	"time"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	"github.com/lab-paper-code/chill/internal/serving/live"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

const (
	mutationAccepted          = "Accepted"
	mutationDeletionRequested = "DeletionRequested"
)

// ProjectAction projects the committed observation identically for live and history.
func ProjectAction(action observation.SchedulerActionObservation) (*live.ActionEvent, error) {
	var operation servingstate.Operation
	if len(action.Expected) != 0 {
		if err := json.Unmarshal(action.Expected, &operation); err != nil {
			return nil, err
		}
	}
	uid := operation.WorkloadUID
	if uid == "" && operation.Prepare != nil && operation.Prepare.Owner != nil {
		uid = operation.Prepare.Owner.UID
	}
	projected := &live.ActionEvent{Action: live.Action{
		ID: action.ID, Kind: action.Kind, Node: live.NodeRef{UID: action.Node.UID, Name: action.Node.Name},
		WorkloadUID: uid, Status: action.Phase, StartedAt: action.StartedAt.UTC().Format(time.RFC3339Nano), ExpectedSeconds: action.ExpectedSeconds,
	}, Phase: "started"}
	if source := action.ExpectedSource; source != nil {
		reference, err := json.Marshal(source.Reference)
		if err != nil {
			return nil, err
		}
		projected.ExpectedSource = &live.ExpectedSource{CostID: source.CostID, Reference: reference, Statistic: source.Statistic, TrialCount: source.TrialCount}
	}
	if action.CompletedAt != nil {
		completed := action.CompletedAt.UTC().Format(time.RFC3339Nano)
		projected.CompletedAt = &completed
		projected.Phase = "completed"
		success := action.Phase == "complete" || action.Error == "" &&
			(action.Phase == "NOT_SUBMITTED" || action.Phase == mutationAccepted || action.Phase == mutationDeletionRequested)
		if !success {
			projected.Phase = "failed"
		}
		projected.Result = &live.ActionResult{Status: action.Phase, Error: action.Error}
	}
	return projected, nil
}
