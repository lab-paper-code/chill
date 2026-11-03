package queryapi_test

import (
	"reflect"
	"testing"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/queryapi"
)

func TestCoverageProblemWithoutFailedRequestHasNextAction(t *testing.T) {
	out := queryapi.MeasurementCoverageResponse{}
	cell := queryapi.MeasurementCoverageCell{
		ID: "conflict",
		Assessment: queryapi.MeasurementAssessment{
			DisplayStatus: queryapi.MeasurementProblem,
			Reasons: []queryapi.MeasurementReason{{
				Code: "CurrentDeclarationConflict", Message: "현재 선언과 측정이 다릅니다",
			}},
		},
	}
	queryapi.AddCoverageActions(&out, cell, map[string]int{})
	if len(out.NextActions) != 1 || out.NextActions[0].ReasonCode != "CurrentDeclarationConflict" ||
		out.NextActions[0].CellID != cell.ID {
		t.Fatalf("problem must remain actionable without a failed request: %#v", out.NextActions)
	}
}
func TestCoverageFailedRequestHistoryUsesCurrentIdentityNotCellStatus(t *testing.T) {
	current, historical := 0, 0
	summary := queryapi.MeasurementCoverageSummary{
		CurrentFailedRequestCount: &current, HistoricalFailedRequestCount: &historical,
	}
	cells := map[string]queryapi.MeasurementCoverageCell{
		"nano-int8": {
			CurrentMeasurement: &profilederivation.MeasurementSelection{ID: "latest-partial"},
			Assessment:         queryapi.MeasurementAssessment{DisplayStatus: queryapi.MeasurementInsufficient},
		},
	}
	requests := []queryapi.MeasurementEntity{}
	for _, id := range []string{"old-failure-1", "old-failure-2", "old-failure-3"} {
		requests = append(requests, queryapi.MeasurementEntity{ID: id, Failed: true, CellIDs: []string{"nano-int8"}})
	}
	for _, id := range []string{"current-failure-1", "current-failure-2", "current-failure-3", "current-failure-4"} {
		cells[id] = queryapi.MeasurementCoverageCell{
			CurrentMeasurement: &profilederivation.MeasurementSelection{ID: id},
			Assessment:         queryapi.MeasurementAssessment{DisplayStatus: queryapi.MeasurementProblem},
		}
		requests = append(requests, queryapi.MeasurementEntity{ID: id, Failed: true, CellIDs: []string{id}})
	}
	queryapi.CountCoverageFailedRequests(&summary, requests, cells)
	if current != 4 || historical != 3 {
		t.Fatalf("latest partial changed older failures into current failures: current=%d historical=%d", current, historical)
	}
}
func TestR18CompactChecklistRetainsDifferentUnknownReasons(t *testing.T) {
	variant := queryapi.MeasurementCoverageVariant{
		BatchProgress: queryapi.MeasurementBatchProgress{Target: []int{1, 2}},
		ResultRows: []queryapi.ResultRow{
			{
				ID: "unset", Batch: 1,
				Assessment: queryapi.MeasurementAssessment{
					Checklist: queryapi.MeasurementChecklist{Checks: []queryapi.MeasurementCheck{{
						Code: "FrozenPowerWindowPolicy", Result: "unknown", Reason: "PolicyUnset", Message: "policy",
					}}},
				},
			},
			{
				ID: "unknown", Batch: 2,
				Assessment: queryapi.MeasurementAssessment{
					Checklist: queryapi.MeasurementChecklist{Checks: []queryapi.MeasurementCheck{{
						Code: "FrozenPowerWindowPolicy", Result: "unknown", Message: "policy",
					}}},
				},
			},
		},
	}
	checks := queryapi.CoverageVariantChecklist(&variant)
	if len(checks) != 2 || checks[0].Reason != "PolicyUnset" || checks[1].Reason != "" ||
		!reflect.DeepEqual(checks[0].SourceIDs, []string{"unset"}) ||
		!reflect.DeepEqual(checks[1].SourceIDs, []string{"unknown"}) {
		t.Fatalf("different unknown causes merged: %+v", checks)
	}
}
