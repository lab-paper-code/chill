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
	if score.Satisfied || score.Completed != 3 || score.FailureFraction != 0 || score.SuccessQuantileNanos == nil || *score.SuccessQuantileNanos != 200e6 {
		t.Fatalf("old 100ms obligation must retain 200ms violation despite current 1s target: %+v", result)
	}
	for _, estimate := range result.SLO {
		if estimate.Definition == frozen.Identity() && (estimate.ExpectedNanos == nil || *estimate.ExpectedNanos != 200e6 || estimate.NormalizedExceedance != 1) {
			t.Fatalf("frozen definition estimate lost its own SLO: %+v", estimate)
		}
	}
}
