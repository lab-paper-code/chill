package queryapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const sessionResultPayload = `{
 "schemaVersion": "chill.dacs.io/batch-energy-optimization-result.v1alpha6",
 "condition": {"nodeName": "jetsono", "nodeUID": "node-uid", "deviceClass": "jetson-agx-orin-64g", "powerMode": "MAXN",
  "logicalModel": "mobilenet-v2-050", "runtimeFamily": "onnxruntime", "backend": "cpu", "runtimeImage": "registry/firstfloor:1",
  "artifact": {}, "preprocessor": {}, "actor": {}},
 "settings": {"meterLagSeconds": 3, "minimumPowerWindowSeconds": 15, "midLevelRatio": 0.25, "sparseMeanGapSeconds": 1,
  "levelDurationLimitSeconds": 300, "postLoadWindowSeconds": 60, "minimumSamples": 59, "confidenceLevel": 0.95},
 "power": {"idleWatts": {"status": "Identified", "value": 7.5, "range": [7.4, 7.6]},
  "awakeStepWatts": {"status": "NotIdentified", "range": [0, 1.2], "reason": "the hold has no breakpoint"},
  "holdSeconds": {"status": "Identified", "value": 0.2, "range": [0.1, 0.3]},
  "meterIntervalSeconds": 1, "settledAtSeconds": 4, "postLoad": [[0, 9.1], [1, 8.2], [2, 7.6]],
  "holdSegmentsSearched": 0, "holdResolutionWatts": 0},
 "batches": [{"batch": 1, "serviceNanos": [10, 20, 30], "precedingIdleNanos": [1, 2, 3],
  "dispatchOccupancyNanos": [40, 10, 30, 20], "saturationInputRate": 80,
  "joulesPerInput": {"status": "Identified", "value": 0.05, "range": [0.04, 0.06]},
  "peakMemoryBytes": 1048576, "memoryKind": "host", "prepareCostKey": "prepare-1", "removeCostKey": "remove-1",
  "levels": [{"kind": "saturation", "id": "b1-saturation", "start": "2026-11-14T00:00:00Z", "end": "2026-11-14T00:01:00Z",
   "sentInputs": 59, "completedInputs": 59, "completedBatches": 59, "endReason": "Samples", "inputRate": 80,
   "meanWatts": 12.5, "meanWattsStandardError": 0.1, "powerSamples": 57}]}],
 "stop": {"batch": 2, "class": "CapacityLimit", "reason": "batch 2 exceeds the memory envelope"},
 "meterSource": {"kind": "prometheus"}
}`

func sessionCampaign(phase, outcome, message string) edgev1alpha1.BatchEnergyOptimization {
	return edgev1alpha1.BatchEnergyOptimization{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "session", UID: "session-uid", Generation: 1},
		Spec:       edgev1alpha1.BatchEnergyOptimizationSpec{Session: &edgev1alpha1.BatchEnergyOptimizationSessionSpec{}},
		Status: edgev1alpha1.BatchEnergyOptimizationStatus{
			ObservedGeneration: 1, Phase: phase, Outcome: outcome, CompletedBatches: 1,
			Conditions: []metav1.Condition{{Type: "MeasurementComplete", Status: metav1.ConditionTrue,
				Reason: outcome, Message: message, ObservedGeneration: 1}},
		},
	}
}

func TestSessionResultDecodesIntoTheView(t *testing.T) {
	t.Parallel()
	result, err := profilederivation.DecodeSessionResult([]byte(sessionResultPayload))
	if err != nil {
		t.Fatal(err)
	}
	campaign := sessionCampaign("Complete", "CapacityLimit", "batch 2 exceeds the memory envelope")
	campaign.Status.Result = &edgev1alpha1.ProfilingRunEvidenceReference{Store: "store", Key: "batch-energy-optimizations/session-uid/result.json", Size: 10}
	view, err := BuildBatchEnergyOptimizationResult(BatchEnergyOptimizationResultSnapshot{
		ReadAt: time.Unix(0, 0), Campaign: campaign, Session: &result,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.ResultFormat != ResultFormatSession || view.Session == nil ||
		view.Campaign.CompletedBatches != 1 || view.Campaign.Reason != "batch 2 exceeds the memory envelope" ||
		view.Campaign.StoredResult == nil || !view.Assessment.Evidence.Accepted {
		t.Fatalf("view = %+v", view)
	}
	power := view.Session.Power
	if power.IdleWatts.Value == nil || *power.IdleWatts.Value != 7.5 || power.IdleWatts.Unit != "W" ||
		power.AwakeStepWatts.Status != profilederivation.ParameterNotIdentified || power.AwakeStepWatts.Value != nil ||
		power.AwakeStepWatts.Range == nil || power.HoldSeconds.Unit != "s" || power.PostLoadSampleCount != 3 {
		t.Fatalf("power = %+v", power)
	}
	batch := view.Session.Batches[0]
	if batch.ServiceRecordCount != 3 || batch.DispatchOccupancy.Count != 4 || batch.DispatchOccupancy.MedianNanos == nil ||
		*batch.DispatchOccupancy.MedianNanos != 25 || batch.JoulesPerInput.Unit != "J" || len(batch.Levels) != 1 ||
		batch.PrepareCostKey != "prepare-1" || view.Session.Stop == nil || view.Session.Stop.Class != profilederivation.StopCapacityLimit {
		t.Fatalf("batch = %+v", batch)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, large := range []string{`"serviceNanos"`, `"precedingIdleNanos"`, `"dispatchOccupancyNanos"`} {
		if strings.Contains(string(encoded), large) {
			t.Fatalf("view carries %s", large)
		}
	}
}

func TestPreviousFormatResultIsAnExplicitState(t *testing.T) {
	t.Parallel()
	campaign := sessionCampaign("Complete", "Complete", "")
	campaign.Spec.Session = nil
	campaign.Status.Result = &edgev1alpha1.ProfilingRunEvidenceReference{Store: "store", Key: "batch-energy-optimizations/old-uid/result.json", Size: 1}
	view, err := BuildBatchEnergyOptimizationResult(BatchEnergyOptimizationResultSnapshot{Campaign: campaign})
	if err != nil {
		t.Fatal(err)
	}
	if view.ResultFormat != ResultFormatPrevious || !strings.Contains(view.ResultFormatMessage, "measure again") || view.Session != nil {
		t.Fatalf("format = %q %q", view.ResultFormat, view.ResultFormatMessage)
	}
	failed := BuildBatchEnergyOptimizationResultError(BatchEnergyOptimizationResultSnapshot{Campaign: campaign}, 502, "Unavailable", "store down")
	if failed.ResultFormat != ResultFormatPrevious {
		t.Fatalf("an old request whose read failed has format %q", failed.ResultFormat)
	}
}

func TestFailedSessionReportsItsOutcomeAndSentence(t *testing.T) {
	t.Parallel()
	campaign := sessionCampaign("Failed", "SessionUnresolved", "resolve target Node: node jetsono not found")
	view, err := BuildBatchEnergyOptimizationResult(BatchEnergyOptimizationResultSnapshot{Campaign: campaign})
	if err != nil {
		t.Fatal(err)
	}
	if view.ResultFormat != "" || view.Campaign.Reason != "resolve target Node: node jetsono not found" ||
		len(view.Assessment.FailureSummary) != 1 || view.Assessment.FailureSummary[0].Code != "SessionUnresolved" ||
		view.Assessment.FailureSummary[0].Label != "준비 정보 해석 실패" ||
		view.Assessment.FailureSummary[0].Message != view.Campaign.Reason {
		t.Fatalf("view = %+v", view)
	}
	campaign.Generation = 2
	if MeasurementReasonSentence(campaign) != "" {
		t.Fatal("a condition of an earlier generation is not the current reason")
	}
}
