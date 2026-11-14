package queryapi

import (
	"encoding/json"
	"testing"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

func TestMeasurementResultRowPreservesSignedConfidenceZeroAndUnknown(t *testing.T) {
	zero, mean, width, p95, passed := 0.0, -1.0, 2.0, 3.0, true
	facts := profilederivation.MeasurementFacts{PointPolicy: profilederivation.PointPolicy{MinimumTrials: 2}}
	batch := profilederivation.MeasurementBatchFacts{
		Batch: 1, P95Milliseconds: &p95, LatencySampleCount: 6,
		IdleWatts: &zero, ActiveWatts: &zero, TotalJoulesPerItem: &zero, IncrementalJoulesPerItem: &mean,
		Confidence: &profilederivation.PointConfidence{Level: .8, HalfWidth: width}, ActiveTrialCount: 3, IdleTrialCount: 3,
		PowerSource: profilederivation.MeasurementPowerSource{Kind: "edge-metrics", NodeName: "node"},
		Sources:     []profilederivation.RawEvidenceIdentity{{Store: "store", Key: "raw", Size: 1}},
		Quality:     []profilederivation.MeasurementTrialQuality{{PolicyPassed: &passed, SourceTimestampAbsent: true}},
	}
	row := MeasurementResultRow(facts, batch, "profile", false, true)
	if row.Confidence == nil || row.Confidence.Lower != -3 || row.Confidence.Upper != 1 || row.Confidence.Level != .8 ||
		row.TotalJoulesPerItem == nil || *row.TotalJoulesPerItem != 0 || row.Assessment.Status != MeasurementCriteriaMet || row.MissingReasons == nil {
		t.Fatalf("signed CI/zero/timestamp reporting changed: %+v", row)
	}
	batch.Confidence.HalfWidth = 0
	if row := MeasurementResultRow(facts, batch, "profile", false, true); row.Confidence.Lower != -1 || row.Assessment.Status != MeasurementCriteriaMet {
		t.Fatalf("zero-width CI was not retained: %+v", row)
	}
	batch.ActiveTrialCount = 2
	if row := MeasurementResultRow(facts, batch, "profile", false, true); row.Assessment.Status != MeasurementReview {
		t.Fatalf("idle repetitions counted as independent active trials: %+v", row)
	}
	if row := MeasurementResultRow(facts, batch, "profile", true, true); row.RowKind != ResultRowConfirmation || row.Assessment.Status != MeasurementReview {
		t.Fatalf("confirmation presented as full independent result: %+v", row)
	}
	missing := MeasurementResultRow(facts, profilederivation.MeasurementBatchFacts{Batch: 1}, "profile", false, false)
	if missing.P95Milliseconds != nil || missing.TotalJoulesPerItem != nil || missing.Confidence != nil {
		t.Fatalf("missing statistics became observed zero: %+v", missing)
	}
	if _, err := json.Marshal(missing); err != nil {
		t.Fatal(err)
	}
}

func TestR18PowerPolicyReasonPreservesUnknownAndFailure(t *testing.T) {
	failed, passed := false, true
	for _, tc := range []struct {
		name, reason, result string
		policy               *bool
	}{
		{"unset", "PolicyUnset", "unknown", nil},
		{"unknown", "", "unknown", nil},
		{"failed", "WindowGapExceeded", "fail", &failed},
		{"passed", "", "pass", &passed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := profilederivation.MeasurementBatchFacts{
				Quality: []profilederivation.MeasurementTrialQuality{{PolicyPassed: tc.policy, PolicyReason: tc.reason}},
			}
			assessment := AssessMeasurementBatch(facts, 3, false, true)
			raw, err := json.Marshal(assessment)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Checklist struct {
					Checks []map[string]any `json:"checks"`
				} `json:"checklist"`
			}
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			for _, check := range wire.Checklist.Checks {
				if check["code"] != "FrozenPowerWindowPolicy" {
					continue
				}
				reason, _ := check["reason"].(string)
				if check["result"] != tc.result || reason != tc.reason {
					t.Fatalf("policy distinction lost: %+v", check)
				}
				return
			}
			t.Fatal("power policy check missing")
		})
	}
}
