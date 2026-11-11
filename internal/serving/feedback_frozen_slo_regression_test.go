package serving

import (
	"context"
	"testing"
	"time"
)

func TestFeedbackConsumerFrozenDefinitionSLOSurvivesCurrentRelaxation(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	snapshot.Instances[0].MaxBatch = 1
	snapshot.Instances[0].Profile.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{200e6})}
	definition := snapshot.Workloads[0]
	frozen := *definition
	frozen.SLO.TargetLatency = 100 * time.Millisecond
	frozen.SLO.Percentile = 100
	frozen.Admission.Policy = AdmissionPolicyDelay
	definition.Generation++
	definition.SLO.TargetLatency = time.Second
	definition.SLO.Percentile = 1
	post, sender, native := 0.0, 0.0, 0.0
	snapshot.Outstanding = []SchedulerRequest{{ID: "frozen", Definition: &frozen, Phase: phaseQueued, Instance: "i1", CompletionMarginMS: &post, NativeSenderElapsedMS: &sender, NativeElapsedNanos: &native}}
	result, err := EvaluateFeedbackCandidate(context.Background(), settings, snapshot, candidate, 1)
	if err != nil || len(result.InvalidReasons) != 0 || len(result.Workloads) != 1 {
		t.Fatalf("mixed frozen/current evaluation: %+v %v", result, err)
	}
	score := result.Workloads[0]
	if score.Satisfied || score.OnTime+score.Late != 3 || score.Late != 1 || score.FailureFraction != 0 || score.SuccessQuantileNanos == nil || *score.SuccessQuantileNanos != 600e6 {
		t.Fatalf("deferred old 100ms obligation must retain 600ms violation after normal work: %+v", result)
	}
	for _, estimate := range result.SLO {
		if estimate.Definition == frozen.Identity() && (estimate.ExpectedNanos == nil || *estimate.ExpectedNanos != 600e6 || estimate.NormalizedExceedance != 5) {
			t.Fatalf("frozen definition estimate lost its own SLO: %+v", estimate)
		}
	}
}

func TestFeedbackConsumerFrozenDefinitionPercentileSurvivesCurrentChange(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	snapshot.Arrivals = nil
	snapshot.Instances[0].MaxBatch = 1
	definition := snapshot.Workloads[0]
	definition.SLO.TargetLatency = 50 * time.Millisecond
	frozen := *definition
	frozen.SLO.Percentile = 100
	frozen.Admission.Policy = AdmissionPolicyDelay
	definition.Generation++
	definition.SLO.Percentile = 1
	zero := 0.0
	snapshot.Outstanding = []SchedulerRequest{
		{ID: "old-a", Definition: &frozen, Phase: phaseQueued, Instance: "i1", CompletionMarginMS: &zero, NativeSenderElapsedMS: &zero, NativeElapsedNanos: &zero},
		{ID: "old-b", Definition: &frozen, Phase: phaseQueued, Instance: "i1", QueueOrder: 1, CompletionMarginMS: &zero, NativeSenderElapsedMS: &zero, NativeElapsedNanos: &zero},
	}
	result, err := EvaluateFeedbackCandidate(context.Background(), settings, snapshot, candidate, 1)
	if err != nil || len(result.InvalidReasons) != 0 || len(result.Workloads) != 1 {
		t.Fatalf("frozen percentile evaluation: %+v %v", result, err)
	}
	score := result.Workloads[0]
	if score.Satisfied || score.OnTime != 1 || score.Late != 1 || score.SuccessQuantileNanos == nil || *score.SuccessQuantileNanos != 60e6 {
		t.Fatalf("old p100 must judge 30/60ms at 60ms despite current p1: %+v", result)
	}
}
