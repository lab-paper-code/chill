package serving

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestFeedbackConsumerAssignedQueueMembershipCountsOnce(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	snapshot.Arrivals = nil
	snapshot.Workloads[0].SLO.TargetLatency = 10 * time.Millisecond
	snapshot.Workloads[0].Admission.Policy = AdmissionPolicyReject
	snapshot.Instances[0].Profile.Batches = []BatchProfile{
		schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{60e6}),
		schedulerTestBatchProfile(BatchProfile{Batch: 2}, []float64{1e6}),
	}
	zero := 0.0
	snapshot.Outstanding = []SchedulerRequest{{ID: "assigned", Definition: snapshot.Workloads[0], Phase: phaseAssigned, Instance: "i1", CompletionMarginMS: &zero, NativeSenderElapsedMS: &zero}}
	result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
	score := result.Workloads[0]
	if score.DeadlineDropped != 1 || score.Completed != 0 || score.PotentialExcess == nil || *score.PotentialExcess != 5 {
		t.Fatalf("one assigned request cannot assemble batch2 after native receipt: %+v", result)
	}
}

func TestFeedbackConsumerFailurePotentialIncludesBusyResidual(t *testing.T) {
	for _, phase := range []string{phaseQueued, phasePending} {
		t.Run(phase, func(t *testing.T) {
			settings, snapshot, candidate := feedbackFixture()
			snapshot.Arrivals = nil
			snapshot.Instances[0].MaxBatch = 1
			snapshot.Instances[0].Capacity = 1
			snapshot.Workloads[0].SLO.TargetLatency = 10 * time.Millisecond
			zero, native := 0.0, 20e6
			elapsed := int64(20e6)
			life := time.Millisecond
			waiting := SchedulerRequest{ID: "waiting", Definition: snapshot.Workloads[0], Phase: phase, CompletionMarginMS: &zero, NativeSenderElapsedMS: &zero, NativeElapsedNanos: &zero, RemainingLifetime: &life}
			if phase == phaseQueued {
				waiting.Instance = "i1"
				snapshot.Instances[0].Capacity = 2
			}
			snapshot.Outstanding = []SchedulerRequest{
				{ID: "running", Definition: snapshot.Workloads[0], Phase: phaseRunning, Instance: "i1", BatchID: "actual", Elapsed: 20 * time.Millisecond, CompletionMarginMS: &zero, NativeSenderElapsedMS: &zero, NativeElapsedNanos: &native, ExchangeElapsedNanos: &elapsed},
				waiting,
			}
			result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
			score := result.Workloads[0]
			if score.LifetimeExpired != 1 || score.Completed != 1 || score.PotentialExcess == nil || math.Abs(*score.PotentialExcess-3) > 1e-12 {
				t.Fatalf("1ms age + sampled 9ms busy residual + 30ms service must retain 40ms potential: %+v", result)
			}
		})
	}
}

func TestFeedbackConsumerUnsampledPotentialRemainsDiagnostic(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	settings.DefaultOperationCost[schedulerActionPrepare] = OperationCostValue{Seconds: .05, Joules: 1}
	target := snapshot.Instances[0]
	target.ID, target.MaxBatch = "prepared", 1
	snapshot.Instances = nil
	snapshot.Arrivals = snapshot.Arrivals[:1]
	zero, life := 0.0, time.Duration(0)
	snapshot.Outstanding = []SchedulerRequest{{ID: "expired-before-prepare", Definition: snapshot.Workloads[0], Phase: phasePending, CompletionMarginMS: &zero, RemainingLifetime: &life}}
	candidate.Keep, candidate.Instances = false, []SchedulerInstance{target}
	candidate.Actions = []Action{
		{ID: "prepare", Kind: schedulerActionPrepare, Instance: target.ID, Node: target.Node, From: "unloaded", To: schedulerState(target)},
		{ID: "route", Kind: schedulerActionRoute, DependsOn: []string{"prepare"}},
	}
	result, err := EvaluateFeedbackCandidate(context.Background(), settings, snapshot, candidate, 1)
	if err != nil || len(result.InvalidReasons) != 0 || len(result.Workloads) != 1 {
		t.Fatalf("missing optional potential must not erase known outcome axes: %+v %v", result, err)
	}
	score := result.Workloads[0]
	if score.Population != 2 || score.LifetimeExpired != 1 || score.Completed != 1 || score.FailureFraction != .5 || score.PotentialExcess != nil || len(score.PotentialUnavailableReasons) == 0 {
		t.Fatalf("unsampled prepare must leave potential unknown and retain known finite outcomes: %+v", result)
	}
}
