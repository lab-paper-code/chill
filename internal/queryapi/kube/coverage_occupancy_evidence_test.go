package kube

import (
	"context"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
)

func TestCoverageOccupancyInvalidEvidenceReason(t *testing.T) {
	for _, payload := range []string{"not-json", "{}"} {
		t.Run(payload, func(t *testing.T) {
			base, selected, task := coverageOccupancyFixture(t)
			task.Status.Phase = api.MeasurementPhaseComplete
			task.Status.Result = &api.ProfilingRunEvidenceReference{Store: "evidence", Key: "invalid.json"}
			projection, err := ProjectDispatchOccupancy(
				context.Background(),
				&occupancyEvidenceReader{payload: payload},
				1<<20,
				task,
				decodeOccupancyEvidenceFixture,
			)
			if err != nil {
				t.Fatal(err)
			}
			base.OccupancyResults[string(task.UID)] = projection
			base.Occupancies.Items = []api.DispatchOccupancyMeasurement{task}
			entry := findCoverageOccupancy(t, base, selected, task)
			if entry.State != "failed" || entry.Reason != "EvidenceInvalid" {
				t.Fatalf("invalid evidence became %+v", entry)
			}
		})
	}
}
