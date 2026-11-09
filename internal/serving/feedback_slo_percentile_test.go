package serving

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

// Captured terminal outcomes exercise replay accounting without random failures
// or invented completion times for requests that never returned a result.
func feedbackPercentileFixture(n, misses, late int, percentile float64, outcome string) (SchedulerSettings, SchedulerSnapshot, SchedulerCandidate) {
	settings, snapshot, candidate := feedbackFixture()
	snapshot.Arrivals = nil
	definition := snapshot.Workloads[0]
	definition.SLO = SLO{Percentile: percentile, TargetLatency: 100 * time.Millisecond}
	zero := 0.0
	for j := range n {
		native := 100e6
		request := SchedulerRequest{ID: fmt.Sprintf("r%04d", j), Definition: definition, Phase: phaseReturning, Instance: snapshot.Instances[0].ID, CompletionMarginMS: &zero, NativeSenderElapsedMS: &zero, NativeElapsedNanos: &native}
		if j < misses {
			switch outcome {
			case "expired":
				life := time.Duration(0)
				request.RemainingLifetime = &life
			case "unfinished":
				request.Phase, request.Instance = phasePending, ""
				snapshot.Instances[0].Ready = false
			default:
				request.NativeOutcome = outcome
			}
		} else if j < misses+late {
			native = 101e6
		}
		snapshot.Outstanding = append(snapshot.Outstanding, request)
	}
	return settings, snapshot, candidate
}

func TestFeedbackConsumerFullPopulationPercentile(t *testing.T) {
	for _, tc := range []struct {
		name       string
		n, misses  int
		late       int
		percentile float64
		outcome    string
		met        bool
	}{
		{"p99 one failure", 1000, 1, 0, 99, firstfloor.OutcomeBatchError, true},
		{"p99 ten failures", 1000, 10, 0, 99, firstfloor.OutcomeBatchError, true},
		{"p99 eleven failures", 1000, 11, 0, 99, firstfloor.OutcomeBatchError, false},
		{"p99 dropped tail", 1000, 10, 0, 99, firstfloor.OutcomeDeadlineDrop, true},
		{"p99 expired tail", 1000, 10, 0, 99, "expired", true},
		{"p99 unfinished tail", 1000, 10, 0, 99, "unfinished", true},
		{"p99 combined tail fits", 1000, 5, 5, 99, firstfloor.OutcomePreprocessFailed, true},
		{"p99 combined tail exceeds", 1000, 6, 5, 99, firstfloor.OutcomePreprocessFailed, false},
		{"p100 exact target", 1000, 0, 0, 100, "", true},
		{"p100 one failure", 1000, 1, 0, 100, firstfloor.OutcomeBatchError, false},
		{"p100 one late completion", 1000, 0, 1, 100, "", false},
		{"p99.9 one failure", 1000, 1, 0, 99.9, firstfloor.OutcomeBatchError, true},
		{"p99.9 two failures", 1000, 2, 0, 99.9, firstfloor.OutcomeBatchError, false},
		{"above p99.9 one failure", 1000, 1, 0, math.Nextafter(99.9, 100), firstfloor.OutcomeBatchError, false},
		{"no successful completion", 1, 1, 0, 99, firstfloor.OutcomeBatchError, false},
		{"empty", 0, 0, 0, 99, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings, snapshot, candidate := feedbackPercentileFixture(tc.n, tc.misses, tc.late, tc.percentile, tc.outcome)
			result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
			score, estimate := result.Workloads[0], result.SLO[0]
			if score.Satisfied != tc.met || score.Population != tc.n || score.Completed != tc.n-tc.misses || score.DeadlineDropped+score.Failed+score.LifetimeExpired+score.Unfinished != tc.misses {
				t.Fatalf("full request population verdict: want met=%v, got %+v", tc.met, result)
			}
			wantFraction := 0.0
			if tc.n > 0 {
				wantFraction = float64(tc.misses) / float64(tc.n)
			}
			if score.FailureFraction != wantFraction || estimate.Samples != score.Completed {
				t.Fatalf("failure count or success-only diagnostic changed: %+v", result)
			}
			if score.Completed == 0 {
				if score.SuccessQuantileNanos != nil || estimate.ExpectedNanos != nil {
					t.Fatalf("absent completions acquired fake latency: %+v", result)
				}
			} else if score.SuccessQuantileNanos == nil {
				t.Fatalf("successful completions lost diagnostic quantile: %+v", result)
			}
			outcomes := 0
			for _, count := range estimate.PredictedOutcomes {
				outcomes += count
			}
			if outcomes != tc.n {
				t.Fatalf("raw outcomes omitted requests: %+v", estimate)
			}
			candidate, err := schedulerReplayEstimate(context.Background(), settings, snapshot, candidate, 1, nil, nil)
			if err != nil || len(candidate.Reasons) != 0 || (candidate.Status == schedulerStatusFeasible) != tc.met {
				t.Fatalf("candidate consumer overrode full-population verdict: %+v %v", candidate, err)
			}
			if tc.n > 0 {
				rank := int(math.Ceil(float64(tc.n) * tc.percentile / 100))
				want := 100e6
				if rank > tc.n-tc.misses-tc.late {
					want = 101e6
				}
				actual := candidate.SLO[0].ExpectedNanos
				if rank > tc.n-tc.misses {
					if actual != nil {
						t.Fatalf("missed percentile acquired finite completion: %+v", candidate.SLO)
					}
				} else if actual == nil || *actual != want {
					t.Fatalf("candidate percentile omitted missed requests: want=%g got=%+v", want, candidate.SLO)
				}
			}
		})
	}
}

func TestFeedbackConsumerAllowedTailReachesBothPlannerSearches(t *testing.T) {
	for _, strategy := range []string{"v1", "v2"} {
		t.Run(strategy, func(t *testing.T) {
			settings, snapshot, _ := feedbackPercentileFixture(1000, 10, 0, 99, firstfloor.OutcomeBatchError)
			settings.PlanSearchStrategy = strategy
			settings.AutonomousWorkloads = []string{snapshot.Workloads[0].UID}
			planner := capacityPlannerForTest(t, settings, snapshot)
			keep := planner.evaluateCurrentKeep(planner.period)
			chosen, err := planner.search(time.Now().Add(time.Minute))
			if err != nil || !keep.feasible || !chosen.feasible || chosen.exclusion != "" || chosen.joules > keep.joules {
				t.Fatalf("allowed tail lost feasibility or energy order: keep=%+v chosen=%+v err=%v", keep, chosen, err)
			}
			if len(chosen.feedback) != 1 || chosen.feedback[0].Failed != 10 || chosen.feedback[0].Completed != 990 {
				t.Fatalf("search discarded failures to become feasible: %+v", chosen)
			}
		})
	}
}

func TestFeedbackConsumerCapacityWindowAllowsConfiguredTail(t *testing.T) {
	for _, tc := range []struct {
		name       string
		misses     int
		percentile float64
		met        bool
	}{
		{"p99 one drop", 1, 99, true},
		{"p99 ten drops", 10, 99, true},
		{"p99 eleven drops", 11, 99, false},
		{"p100 exact target", 0, 100, true},
		{"p100 one drop", 1, 100, false},
		{"p99.9 one drop", 1, 99.9, true},
		{"p99.9 two drops", 2, 99.9, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const n = 1000
			settings, snapshot := capacityFixture()
			definition := snapshot.Workloads[0]
			definition.SLO = SLO{Percentile: tc.percentile, TargetLatency: time.Millisecond}
			option := snapshot.Options[0]
			option.Instance.MaxBatch = 1
			option.Instance.Profile.Condition.Firstfloor.DeadlinePolicy = firstfloor.DeadlineDrop
			option.Instance.Profile.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1, EnergyPerInputJoules: 1, MemoryBytes: 100}, []float64{1e6})}
			// Isolated arrivals complete at exactly 1ms. A middle simultaneous
			// burst drops precisely misses requests, then isolated work continues.
			gaps := make([]float64, n)
			for j := range n {
				gaps[j] = .01
			}
			for j := range tc.misses {
				gaps[500+j] = 0
			}
			replay := capacityReplay{settings: settings, at: snapshot.At, option: option, w: definition, occupancy: snapshot.DispatchOccupancy[0], gaps: map[[2]int][]float64{{1, n}: gaps}}
			window, err := replay.window(context.Background(), 1, n, 1)
			if err != nil || window.met != tc.met {
				t.Fatalf("capacity consumer rejected allowed tail or admitted excess: %+v %v", window, err)
			}
			if tc.met && (window.requests != n || window.latency != 1 || window.requestJoules != float64(n-tc.misses)) {
				t.Fatalf("passing window lost full population or success-only evidence: %+v", window)
			}
		})
	}
}
