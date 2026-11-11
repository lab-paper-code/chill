package serving

import (
	"testing"
	"time"
)

func TestFeedbackConsumerReturningPOSTUsesConsumedAccountingBudget(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	snapshot.Arrivals = nil
	post, sender, native := 30.0, 30.0, 60e6
	life := 10 * time.Millisecond
	snapshot.Outstanding = []SchedulerRequest{{ID: "returning", Definition: snapshot.Workloads[0], Phase: phaseReturning, Instance: "i1", Elapsed: 115 * time.Millisecond, ReturnElapsed: 5 * time.Millisecond, CompletionMarginMS: &post, NativeSenderElapsedMS: &sender, NativeElapsedNanos: &native, RemainingLifetime: &life}}
	snapshot.PathFeedback = []PathFeedback{{WorkloadUID: snapshot.Workloads[0].UID, TotalNanos: 900e6, PostNanos: 900e6}}
	result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
	score := result.Workloads[0]
	if score.OnTime+score.Late != 1 || score.Timeout != 0 || score.SuccessQuantileNanos == nil || *score.SuccessQuantileNanos != 120e6 {
		t.Fatalf("S30+N60+POST30 leaves 5ms at server age115, completing before 10ms lifetime: %+v", result)
	}
}
