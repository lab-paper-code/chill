package httpapi

import (
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestProjectActionNeutralNotSubmittedPreservesOtherTerminalStates(t *testing.T) {
	at := time.Now().UTC()
	for _, tc := range []struct {
		status, failure, phase string
		completed              bool
	}{
		{"NOT_SUBMITTED", "", "completed", true},
		{"NOT_SUBMITTED", "ActualPreflightError", "failed", true},
		{"NOT_SUBMITTED", "", "started", false},
		{"complete", "", "completed", true},
		{"complete", "ExistingError", "completed", true},
		{"partial", "", "failed", true},
		{"failed", "ActualError", "failed", true},
		{mutationAccepted, "", "completed", true},
		{mutationAccepted, "CreateError", "failed", true},
		{mutationAccepted, "", "started", false},
		{mutationDeletionRequested, "", "completed", true},
		{mutationDeletionRequested, "DeleteError", "failed", true},
		{mutationDeletionRequested, "", "started", false},
	} {
		t.Run(tc.status+"/"+tc.failure+"/"+tc.phase, func(t *testing.T) {
			observation := serving.SchedulerActionObservation{
				ID: "preflight", Kind: "measurement-post", Phase: tc.status, Error: tc.failure, StartedAt: at,
			}
			if tc.completed {
				observation.CompletedAt = &at
			}
			event, err := ProjectAction(observation)
			if err != nil {
				t.Fatal(err)
			}
			if event.Phase != tc.phase || event.Status != tc.status || (event.CompletedAt != nil) != tc.completed {
				t.Fatalf("terminal semantics changed: %+v", event)
			}
			if tc.completed && (event.Result == nil || event.Result.Status != tc.status || event.Result.Error != tc.failure) {
				t.Fatalf("terminal result lost original outcome: %+v", event.Result)
			}
		})
	}
}
