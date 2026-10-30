package operatorconsole

import (
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"testing"
)

func TestMeasurementAssessmentEvidenceAndExecution(t *testing.T) {
	for _, test := range []struct {
		name   string
		input  MeasurementAssessmentInput
		status string
	}{
		{"Complete phase is not evidence", MeasurementAssessmentInput{Execution: &MeasurementExecution{Phase: "Complete", Generation: 2, ObservedGeneration: 2}, Attempted: true}, MeasurementUnknown},
		{"old generation failure", MeasurementAssessmentInput{Execution: &MeasurementExecution{Phase: "Failed", Generation: 2, ObservedGeneration: 1}}, MeasurementUnknown},
		{"real failure", MeasurementAssessmentInput{Execution: &MeasurementExecution{Phase: "Failed", Generation: 2, ObservedGeneration: 2}}, MeasurementFailed},
		{"accepted behind failure", MeasurementAssessmentInput{Accepted: true, Available: true, Execution: &MeasurementExecution{Phase: "Failed", Generation: 2, ObservedGeneration: 2}}, MeasurementComplete},
		{"missing goals", MeasurementAssessmentInput{Accepted: true, Available: true, MissingBatches: []int{2}}, MeasurementPartial},
		{"reporting absent", MeasurementAssessmentInput{Accepted: true, Checks: []MeasurementCheck{{Code: "CI", Result: MeasurementUnknown}}}, MeasurementComplete},
		{"reuse not criteria", MeasurementAssessmentInput{Accepted: true, Reused: true, Checks: []MeasurementCheck{{Code: "CI", Result: "pass"}}}, MeasurementComplete},
		{"actual broken evidence", MeasurementAssessmentInput{Accepted: true, Problems: []MeasurementReason{{Code: "InvalidEvidence"}}}, MeasurementWarning},
		{"empty compatible", MeasurementAssessmentInput{Support: "Compatible", SourcesComplete: true}, MeasurementUnmeasured},
		{"incomplete list", MeasurementAssessmentInput{Support: "Compatible"}, MeasurementUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := AssessMeasurement(test.input)
			if got.Status != test.status {
				t.Fatalf("%s != %s", got.Status, test.status)
			}
		})
	}
}

func TestMeasurementChecklistPreservesZeroAndTimestampFootnote(t *testing.T) {
	zero, yes := 0.0, true
	batch := profilederivation.MeasurementBatchFacts{Batch: 1, P95Milliseconds: &zero, LatencySampleCount: 2, IncrementalJoulesPerItem: &zero, Confidence: &profilederivation.PointConfidence{Level: .95, HalfWidth: 0}, ActiveTrialCount: 3, IdleTrialCount: 3, PowerSource: profilederivation.MeasurementPowerSource{Kind: "node-wall", NodeName: "node"}, Sources: []profilederivation.RawEvidenceIdentity{{Store: "evidence", Key: "raw"}}, Quality: []profilederivation.MeasurementTrialQuality{{SourceTimestampAbsent: true, PolicyPassed: &yes}}}
	if got := AssessMeasurementBatch(batch, 3, false, true); got.Status != MeasurementCriteriaMet {
		t.Fatalf("timestamp/node-wall/zero must not fail: %#v", got)
	}
	batch.ActiveTrialCount = 2
	if got := AssessMeasurementBatch(batch, 3, false, true); got.Status != MeasurementComplete {
		t.Fatalf("idle trials cannot fill active count: %#v", got)
	}
	batch.ActiveTrialCount = 3
	batch.Quality[0].PolicyPassed = nil
	if got := AssessMeasurementBatch(batch, 3, false, true); got.Checklist.CriteriaMet == nil || *got.Checklist.CriteriaMet {
		t.Fatalf("unknown frozen policy must not pass: %#v", got)
	}
}

func TestMeasurementVariantIdentityExcludesOnlyRequestGoals(t *testing.T) {
	base := map[string]any{"name": "first", "plannedBatchSizes": []int{1}, "cpu": "1", "dataset": "uid-one", "protocol": "p"}
	id := MeasurementVariantID(base)
	base["name"] = "second"
	base["plannedBatchSizes"] = []int{2}
	if MeasurementVariantID(base) != id {
		t.Fatal("request goal/name split identical frozen conditions")
	}
	base["dataset"] = "uid-two"
	if MeasurementVariantID(base) == id {
		t.Fatal("different dataset joined")
	}
}
