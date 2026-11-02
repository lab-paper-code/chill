package queryapi

import (
	"encoding/json"
	"testing"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

func TestCoverageCellCurrentFailureNeverFallsBackToOldSuccess(t *testing.T) {
	for _, test := range []struct {
		name  string
		input MeasurementAssessmentInput
		want  string
	}{
		{"failed", MeasurementAssessmentInput{
			Execution: &MeasurementExecution{Phase: "Failed", Generation: 1, ObservedGeneration: 1},
		}, MeasurementProblem},
		{"partial", MeasurementAssessmentInput{
			Accepted: true, Available: true, MissingBatches: []int{2},
		}, MeasurementInsufficient},
	} {
		t.Run(test.name, func(t *testing.T) {
			cell := MeasurementCoverageCell{
				Support: MeasurementSupport{Verdict: MeasurementSupportCompatible},
				History: []MeasurementHistory{{RequestID: "old"}, {RequestID: "current"}},
				Variants: []MeasurementCoverageVariant{
					{
						ID: "old-condition", RequestIDs: []string{"old"}, Condition: map[string]any{"runtimeImage": "old"},
						Assessment: AssessMeasurement(MeasurementAssessmentInput{Accepted: true, Available: true}),
					},
					{
						ID: "current-condition", RequestIDs: []string{"current"},
						Condition:  map[string]any{"runtimeImage": "recorded"},
						Assessment: AssessMeasurement(test.input),
					},
				},
			}
			before, _ := json.Marshal(cell)
			got := AssessMeasurementCoverageCell(cell, &profilederivation.MeasurementSelection{ID: "current"}, true)
			if got.Assessment.DisplayStatus != test.want || got.VariantCount != 1 ||
				got.Variants[0].ID != "current-condition" || len(got.History) != 2 ||
				got.CurrentMeasurement == nil || got.CurrentMeasurement.ID != "current" {
				t.Fatalf("current problem hidden by old success: %+v", got)
			}
			after, _ := json.Marshal(cell)
			if string(before) != string(after) {
				t.Fatal("pure cell assessment mutated retained evidence")
			}
		})
	}
}

func TestCoverageCellCurrentRepresentativeAndSupport(t *testing.T) {
	cell := MeasurementCoverageCell{
		Support: MeasurementSupport{Verdict: MeasurementSupportUnknown},
		Variants: []MeasurementCoverageVariant{{
			ID: "condition", RequestIDs: []string{"current"},
			Assessment:    AssessMeasurement(MeasurementAssessmentInput{Accepted: true, Available: true}),
			BatchProgress: MeasurementBatchProgress{Target: []int{1}, Measured: []int{1}},
			ResultRows:    []ResultRow{{RowKind: ResultRowSourceReference, Batch: 8}, {Batch: 1}},
		}},
	}
	selected := &profilederivation.MeasurementSelection{ID: "current"}
	got := AssessMeasurementCoverageCell(cell, selected, false)
	if got.Assessment.DisplayStatus != MeasurementUsable || got.Support.Verdict != MeasurementSupportUnknown ||
		got.BatchProgress == nil || got.Representative == nil || got.Representative.Batch != 1 {
		t.Fatalf("unread declarations replaced accepted current evidence: %+v", got)
	}
	cell.Support.Verdict = MeasurementSupportIncompatible
	got = AssessMeasurementCoverageCell(cell, selected, true)
	if got.Assessment.DisplayStatus != MeasurementProblem || got.Assessment.Reasons[0].Code != "CurrentDeclarationConflict" {
		t.Fatal("current declaration conflicts must remain visible")
	}
	cell.Support.Verdict = MeasurementSupportCompatible
	got = AssessMeasurementCoverageCell(cell, nil, false)
	if got.Assessment.DisplayStatus != MeasurementUnknown || got.VariantCount != 0 ||
		got.BatchProgress != nil || got.Representative != nil || got.CurrentMeasurement != nil {
		t.Fatal("unread sources must not imply an attempted current measurement")
	}
}

func TestCoverageCurrentMeasurementSurvivesSummaryProjection(t *testing.T) {
	diagnostic := "Last entry selected."
	selected := &profilederivation.MeasurementSelection{
		ID: "ns/latest/uid", Model: "model", DeviceClass: "class", ExecutionPath: "cpu", PowerMode: "fixed",
	}
	payload := MeasurementCoverageResponse{Cells: []MeasurementCoverageCell{
		{
			ID: "current", CurrentMeasurement: selected,
			Support: MeasurementSupport{Verdict: MeasurementSupportCompatible, Diagnostics: []string{diagnostic}},
		},
		{ID: "empty"},
	}}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	projected := assertSummaryRoundTrip(t, raw, false)
	cells := restoreSummary(t, projected)["cells"].([]any)
	current := cells[0].(map[string]any)["currentMeasurement"].(map[string]any)
	if current["id"] != selected.ID || current["model"] != selected.Model ||
		current["measuredAt"] != "0001-01-01T00:00:00Z" ||
		cells[1].(map[string]any)["currentMeasurement"] != nil {
		t.Fatal("current measurement identity, time or null changed in summary")
	}
	support := cells[0].(map[string]any)["support"].(map[string]any)
	if support["diagnostics"].([]any)[0] != diagnostic || support["verdict"] != MeasurementSupportCompatible {
		t.Fatal("summary changed non-blocking compatibility diagnostics")
	}
}

func TestCompatibilityDiagnosticsDoNotChangeVerdict(t *testing.T) {
	diagnostic := "Last entry selected."
	view := BuildModelCompatibility(ModelCompatibilitySnapshot{
		Verdict: MeasurementSupportCompatible, Diagnostics: []string{diagnostic},
	})
	if view.Verdict != MeasurementSupportCompatible || len(view.Reasons) != 0 ||
		len(view.Diagnostics) != 1 || view.Diagnostics[0] != diagnostic {
		t.Fatalf("diagnostic changed static support verdict: %+v", view)
	}
	empty := BuildModelCompatibility(ModelCompatibilitySnapshot{})
	if empty.Diagnostics == nil {
		t.Fatal("compatibility diagnostics must serialize as an empty list, not null")
	}
}
