package serving

import (
	"context"
	"testing"
	"time"
)

func TestFeedbackConsumerUnroutedCohortPreservesSharedActorInterference(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	fixed := *snapshot.Workloads[0]
	fixed.UID = "fixed-unrouted"
	snapshot.Workloads = append(snapshot.Workloads, &fixed)
	snapshot.Arrivals = append(snapshot.Arrivals, SchedulerArrival{ID: "fixed-arrival", At: snapshot.Arrivals[0].At, Definition: &fixed, Accepted: true})
	zero, native := 0.0, 20e6
	elapsed := int64(20e6)
	snapshot.Outstanding = []SchedulerRequest{{ID: "fixed-running", Definition: &fixed, Phase: phaseRunning, Instance: "i1", BatchID: "actual", Elapsed: 20 * time.Millisecond, CompletionMarginMS: &zero, NativeSenderElapsedMS: &zero, NativeElapsedNanos: &native, ExchangeElapsedNanos: &elapsed}}
	result, err := EvaluateFeedbackCandidate(context.Background(), settings, snapshot, candidate, 1)
	if err != nil || len(result.InvalidReasons) != 0 || len(result.Workloads) != 2 {
		t.Fatalf("unrouted cohort must not invalidate an otherwise executable replay: %+v %v", result, err)
	}
	for _, score := range result.Workloads {
		if score.UID == fixed.UID {
			if score.Population != 2 || score.OnTime+score.Late != 1 || score.Unfinished != 1 || score.FailureFraction != .5 || score.PotentialExcess != nil || len(score.PotentialUnavailableReasons) == 0 {
				t.Fatalf("fixed request population or actual running work disappeared: %+v", score)
			}
		} else if score.OnTime+score.Late != 2 || score.SuccessQuantileNanos == nil || *score.SuccessQuantileNanos != 80e6 {
			t.Fatalf("controlled work must wait sampled10ms fixed residual before batch2 service70ms: %+v", score)
		}
	}
}

func TestFeedbackConsumerUnresolvedPhysicalPrerequisiteRemainsInvalid(t *testing.T) {
	for _, blocked := range []string{"native-queue", "route-dependency"} {
		t.Run(blocked, func(t *testing.T) {
			settings, snapshot, candidate := feedbackFixture()
			snapshot.Arrivals = nil
			zero := 0.0
			request := SchedulerRequest{ID: "blocked", Definition: snapshot.Workloads[0], Phase: phasePending, CompletionMarginMS: &zero}
			if blocked == "native-queue" {
				snapshot.Instances[0].Ready = false
				request.Phase, request.Instance = phaseQueued, "i1"
				request.NativeSenderElapsedMS, request.NativeElapsedNanos = &zero, &zero
			} else {
				snapshot.Routing = map[string]map[string]int32{request.Definition.UID: {}}
				candidate.Keep = false
				candidate.Actions = []Action{{ID: "route", Kind: schedulerActionRoute, DependsOn: []string{"unresolved"}}}
			}
			snapshot.Outstanding = []SchedulerRequest{request}
			result, err := EvaluateFeedbackCandidate(context.Background(), settings, snapshot, candidate, 1)
			if err != nil || len(result.InvalidReasons) == 0 || len(result.Workloads) != 1 || result.Workloads[0].Unfinished != 1 {
				t.Fatalf("unresolved native ownership or action dependency must stay invalid: %+v %v", result, err)
			}
		})
	}
}
