package live

import (
	"encoding/json"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

// ProjectAction projects the committed observation identically for live and history.
func ProjectAction(action serving.SchedulerActionObservation) (*ActionEvent, error) {
	var operation servingkube.Operation
	if len(action.Expected) != 0 {
		if err := json.Unmarshal(action.Expected, &operation); err != nil {
			return nil, err
		}
	}
	uid := operation.WorkloadUID
	if uid == "" && operation.Prepare != nil && operation.Prepare.Owner != nil {
		uid = operation.Prepare.Owner.UID
	}
	projected := &ActionEvent{Action: Action{
		ID: action.ID, Kind: action.Kind, Node: NodeRef{UID: action.Node.UID, Name: action.Node.Name},
		WorkloadUID: uid, Status: action.Phase, StartedAt: action.StartedAt.UTC().Format(time.RFC3339Nano), ExpectedSeconds: action.ExpectedSeconds,
	}, Phase: "started"}
	if source := action.ExpectedSource; source != nil {
		reference, err := json.Marshal(source.Reference)
		if err != nil {
			return nil, err
		}
		projected.ExpectedSource = &ExpectedSource{CostID: source.CostID, Reference: reference, Statistic: source.Statistic, TrialCount: source.TrialCount}
	}
	if action.CompletedAt != nil {
		completed := action.CompletedAt.UTC().Format(time.RFC3339Nano)
		projected.CompletedAt = &completed
		projected.Phase = "completed"
		if action.Phase != "complete" && (action.Phase != "NOT_SUBMITTED" || action.Error != "") {
			projected.Phase = "failed"
		}
		projected.Result = &ActionResult{Status: action.Phase, Error: action.Error}
	}
	return projected, nil
}
