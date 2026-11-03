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

func TestResultMeasurementFactsMissingReasons(t *testing.T) {
	campaign, _, record := campaignResultFixture(t)
	missing := "RetainedEvidenceMissing"
	published, err := BuildBatchEnergyOptimizationResult(BatchEnergyOptimizationResultSnapshot{
		Campaign: *campaign, Result: record, StatisticsReason: &missing,
	})
	if err != nil || published.MeasurementFacts != nil || published.MeasurementFactsReason == nil || *published.MeasurementFactsReason != missing {
		t.Fatalf("retained evidence absence changed: %+v error=%v", published, err)
	}
	campaign.Status.Result = nil
	unpublished, err := BuildBatchEnergyOptimizationResult(BatchEnergyOptimizationResultSnapshot{Campaign: *campaign})
	if err != nil || unpublished.MeasurementFacts != nil || unpublished.MeasurementFactsReason == nil ||
		*unpublished.MeasurementFactsReason != "NoPublishedResult" || unpublished.Result != nil || len(unpublished.ResultRows) != 0 {
		t.Fatalf("unpublished result became observed zero: %+v error=%v", unpublished, err)
	}
}

func TestRetainedResultSeparatesReuseConfirmationAndSourceReference(t *testing.T) {
	campaign, _, record := campaignResultFixture(t)
	campaign.Annotations = map[string]string{"chill.dacs.io/profile-reuse": `{"sourceProfileID":"namespace/source/source-uid","maximumRelativeDifference":0.05}`}
	total := 90.0
	facts := &profilederivation.MeasurementFacts{
		Condition: profilederivation.CurvePlanSpec{NodeUID: "confirmation-node"},
		Batches:   []profilederivation.MeasurementBatchFacts{{Batch: 1, TotalJoulesPerItem: &total}},
	}
	view, err := BuildBatchEnergyOptimizationResult(BatchEnergyOptimizationResultSnapshot{Campaign: *campaign, Result: record, MeasurementFacts: facts})
	if err != nil || len(view.ResultRows) != 2 {
		t.Fatalf("reuse rows=%+v error=%v", view.ResultRows, err)
	}
	confirmation, source := view.ResultRows[0], view.ResultRows[1]
	if confirmation.RowKind != ResultRowConfirmation || confirmation.TotalJoulesPerItem == nil || *confirmation.TotalJoulesPerItem != total ||
		source.RowKind != ResultRowSourceReference || source.ProfileID != "namespace/source/source-uid" || source.TotalJoulesPerItem != nil ||
		source.ActiveWatts != nil || source.Confidence != nil || source.Assessment.Status != MeasurementUnknown {
		t.Fatalf("independent confirmation/source reference mixed: %+v", view.ResultRows)
	}
	condition, ok := source.Condition.(map[string]string)
	if !ok || len(condition) != 1 || condition["sourceProfileId"] != source.ProfileID {
		t.Fatalf("source reference forged target physical condition: %+v", source.Condition)
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
