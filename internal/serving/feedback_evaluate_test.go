package serving

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

const feedbackPreparedInstanceID = "prepared"

func feedbackFixture() (SchedulerSettings, SchedulerSnapshot, SchedulerCandidate) {
	settings, snapshot := schedulerFixture()
	settings.AutonomousWorkloads = nil
	settings.CompletionMarginMS = 0
	settings.ComparisonPeriod = time.Millisecond
	profile := snapshot.Instances[0].Profile
	profile.Batches = []BatchProfile{
		schedulerTestBatchProfile(BatchProfile{Batch: 1, EnergyPerInputJoules: 2, MemoryBytes: 100}, []float64{30e6}),
		schedulerTestBatchProfile(BatchProfile{Batch: 2, EnergyPerInputJoules: 1, MemoryBytes: 100}, []float64{70e6}),
	}
	snapshot.Instances[0].MaxBatch = 2
	snapshot.Options[0].Instance = snapshot.Instances[0]
	definition := snapshot.Workloads[0]
	at := snapshot.ObservationEnd.Add(-settings.ObservationWindow)
	snapshot.Arrivals = []SchedulerArrival{
		{ID: "a", At: at, Definition: definition, Accepted: true},
		{ID: "b", At: at, Definition: definition, Accepted: true},
	}
	candidate := SchedulerCandidate{Keep: true, Instances: snapshot.Instances, Nodes: snapshot.Nodes, Routing: snapshot.Routing}
	return settings, snapshot, candidate
}

func feedbackEvaluateForTest(t *testing.T, settings SchedulerSettings, snapshot SchedulerSnapshot, candidate SchedulerCandidate) FeedbackCandidateResult {
	t.Helper()
	result, err := EvaluateFeedbackCandidate(context.Background(), settings, snapshot, candidate, 1)
	if err != nil || len(result.InvalidReasons) != 0 || len(result.Workloads) != 1 || len(result.SLO) != 1 {
		t.Fatalf("feedback evaluation: %+v %v", result, err)
	}
	return result
}

// Consumer smoke entry: actual ChooseBatch changes execution, not a shifted CDF.
func TestFeedbackConsumerPOSTChangesChosenBatch(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	uid := snapshot.Workloads[0].UID
	snapshot.PathFeedback = []PathFeedback{{WorkloadUID: uid, TotalNanos: 10e6, PostNanos: 10e6}}
	low := feedbackEvaluateForTest(t, settings, snapshot, candidate)
	snapshot.PathFeedback[0].TotalNanos, snapshot.PathFeedback[0].PostNanos = 40e6, 40e6
	high := feedbackEvaluateForTest(t, settings, snapshot, candidate)
	if *low.Workloads[0].SuccessQuantileNanos != 80e6 || *high.Workloads[0].SuccessQuantileNanos != 100e6 || high.Workloads[0].Population != 2 || high.Workloads[0].OnTime != 2 {
		t.Fatalf("POST must change batch2 to two batch1 executions: low=%+v high=%+v", low.Workloads, high.Workloads)
	}
	if *low.SLO[0].ExpectedNanos != 80e6 || *high.SLO[0].ExpectedNanos != 100e6 || high.SLO[0].PredictedOutcomes["onTime"] != 2 {
		t.Fatalf("existing SLO consumer diverged: low=%+v high=%+v", low.SLO, high.SLO)
	}
}

// Both rates fail. Actual drops stay failures; conditional compute distinguishes
// options without assigning fake completion latencies to those drops.
func TestFeedbackConsumerZeroWitnessRetainsDropAndPotential(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	settings.CapacityWindows, settings.CapacityPassingWindows, settings.CapacityMinimumRequests = 1, 1, 2
	settings.CapacityTailSamples = 1
	snapshot.Workloads[0].SLO.TargetLatency = 10 * time.Millisecond
	snapshot.Workloads[0].Admission.Policy = AdmissionPolicyReject
	snapshot.Instances[0].MaxBatch = 1
	snapshot.PathFeedback = []PathFeedback{{WorkloadUID: snapshot.Workloads[0].UID}}
	potentials := make([]float64, 0, 2)
	for _, service := range []float64{30e6, 60e6} {
		snapshot.Instances[0].Profile.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{service})}
		snapshot.Options[0].Instance = snapshot.Instances[0]
		candidate.Instances = snapshot.Instances
		rate, err := JudgeFeedbackRates(context.Background(), settings, snapshot.Options[0], snapshot.Workloads[0], DispatchOccupancy{}, snapshot.PathFeedback[0], []float64{1})
		if err != nil || rate.KappaHat != 0 || len(rate.Tests) != 1 || rate.Tests[0].Met {
			t.Fatalf("failed tested witness: %+v %v", rate, err)
		}
		result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
		score := result.Workloads[0]
		if score.Population != 2 || score.Rejected != 2 || score.FailureFraction != 1 || score.OnTime+score.Late != 0 || score.SuccessQuantileNanos != nil || score.PotentialExcess == nil || result.SLO[0].ExpectedNanos != nil || result.SLO[0].PredictedOutcomes[schedulerOutcomeRejected] != 2 {
			t.Fatalf("drop became successful latency or disappeared: %+v", result)
		}
		potentials = append(potentials, *score.PotentialExcess)
	}
	if !reflect.DeepEqual(potentials, []float64{2, 5}) {
		t.Fatalf("equal actual failures must expose conditional improvement: %v", potentials)
	}
}

func TestFeedbackConsumerDelayDrainsFiniteCohortAcrossMeasuredModes(t *testing.T) {
	for _, mode := range []string{firstfloor.DeadlineKeep, firstfloor.DeadlineDefer} {
		t.Run(mode, func(t *testing.T) {
			settings, snapshot, candidate := feedbackFixture()
			snapshot.Instances[0].Profile.Condition.Firstfloor.DeadlinePolicy = mode
			snapshot.Workloads[0].Admission.Policy = AdmissionPolicyDelay
			snapshot.Instances[0].MaxBatch = 1
			snapshot.Workloads[0].SLO.TargetLatency = time.Millisecond
			snapshot.PathFeedback = []PathFeedback{{WorkloadUID: snapshot.Workloads[0].UID, TotalNanos: 20e6, PostNanos: 10e6}}
			result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
			score := result.Workloads[0]
			if score.Population != 2 || score.Late != 2 || score.FailureFraction != 0 || score.Satisfied || *score.SuccessQuantileNanos != 80e6 {
				t.Fatalf("finite Delay cohort must drain beyond 1ms horizon regardless of measured mode: %+v", result)
			}
		})
	}
}

func TestFeedbackConsumerFrozenNativeAndNewReserveStaySeparate(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	snapshot.Instances[0].MaxBatch = 1
	snapshot.Arrivals = snapshot.Arrivals[:1]
	definition := snapshot.Workloads[0]
	oldDefinition := *definition
	oldDefinition.Generation++
	post, sender, native := 10.0, 20.0, 5e6
	snapshot.Outstanding = []SchedulerRequest{{ID: "frozen", Definition: &oldDefinition, Phase: phaseQueued, Instance: "i1", Elapsed: 100 * time.Millisecond, QueueAge: 5 * time.Millisecond, CompletionMarginMS: &post, NativeSenderElapsedMS: &sender, NativeElapsedNanos: &native}}
	snapshot.PathFeedback = []PathFeedback{{WorkloadUID: definition.UID, TotalNanos: 40e6, PostNanos: 40e6}}
	result, err := EvaluateFeedbackCandidate(context.Background(), settings, snapshot, candidate, 1)
	if err != nil || len(result.InvalidReasons) != 0 || len(result.SLO) != 2 || result.Workloads[0].OnTime+result.Workloads[0].Late != 2 {
		t.Fatalf("mixed frozen/current evaluation: %+v %v", result, err)
	}
	for _, estimate := range result.SLO {
		want := 100e6
		if estimate.Definition == oldDefinition.Identity() {
			want = 65e6 // frozen S20 + captured N5 + remaining30 + POST10
		}
		if estimate.ExpectedNanos == nil || *estimate.ExpectedNanos != want {
			t.Fatalf("native age or reserve counted twice: want=%g got=%+v", want, estimate)
		}
	}
}

func TestFeedbackConsumerRunningExpiryUsesSameResidualSample(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	snapshot.Arrivals = nil
	snapshot.Instances[0].MaxBatch = 1
	snapshot.Workloads[0].SLO.TargetLatency = 10 * time.Millisecond
	post, sender, native := 0.0, 0.0, 20e6
	elapsed := int64(20e6)
	life := time.Millisecond
	snapshot.Outstanding = []SchedulerRequest{{ID: "running", Definition: snapshot.Workloads[0], Phase: phaseRunning, Instance: "i1", BatchID: "actual", Elapsed: 20 * time.Millisecond, CompletionMarginMS: &post, NativeSenderElapsedMS: &sender, NativeElapsedNanos: &native, ExchangeElapsedNanos: &elapsed, RemainingLifetime: &life}}
	result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
	score := result.Workloads[0]
	if score.Timeout != 1 || score.OnTime+score.Late != 0 || score.SuccessQuantileNanos != nil || score.PotentialExcess == nil || math.Abs(*score.PotentialExcess-2) > 1e-12 {
		t.Fatalf("running potential must reuse 9ms remaining at expiry, not fresh 30ms: %+v", result)
	}
}

func TestFeedbackConsumerNoExecutionPreservesUnfinishedAndDiagnostic(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	snapshot.Instances[0].Ready = false
	result, err := EvaluateFeedbackCandidate(context.Background(), settings, snapshot, candidate, 1)
	if err != nil || len(result.InvalidReasons) != 0 || len(result.Workloads) != 1 || result.Workloads[0].Unfinished != 2 || result.Workloads[0].Population != 2 || result.Workloads[0].PotentialExcess != nil || result.Workloads[0].SuccessQuantileNanos != nil || len(result.Workloads[0].PotentialUnavailableReasons) == 0 {
		t.Fatalf("no executable route must preserve unfinished outcomes and unknown potential: %+v %v", result, err)
	}
}

func TestFeedbackConsumerSeedAndConditionalSupportRemainDeterministic(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	snapshot.Instances[0].MaxBatch = 1
	snapshot.Instances[0].Profile.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{20e6, 30e6, 40e6})}
	snapshot.PathFeedback = []PathFeedback{{WorkloadUID: snapshot.Workloads[0].UID, TotalNanos: 20e6, PostNanos: 10e6}}
	one := feedbackEvaluateForTest(t, settings, snapshot, candidate)
	two := feedbackEvaluateForTest(t, settings, snapshot, candidate)
	if !reflect.DeepEqual(one, two) || one.ConditionalService == nil || one.ConditionalService.Queries != 2 || one.ConditionalService.StartupQueries != 1 {
		t.Fatalf("same seed/input changed raw conditional sampling: %+v %+v", one, two)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EvaluateFeedbackCandidate(ctx, settings, snapshot, candidate, 1); err != context.Canceled {
		t.Fatalf("cancellation became failed prediction: %v", err)
	}
}

func TestFeedbackRatesCurrentTimingChangesKeyAndVerdict(t *testing.T) {
	settings, snapshot := capacityFixture()
	settings.CapacityWindows, settings.CapacityPassingWindows, settings.CapacityMinimumRequests = 1, 1, 2
	settings.CapacityTailSamples = 1
	settings.CapacityWindow = time.Millisecond
	definition := snapshot.Workloads[0]
	feedback := PathFeedback{WorkloadUID: definition.UID}
	low, err := JudgeFeedbackRates(context.Background(), settings, snapshot.Options[0], definition, snapshot.DispatchOccupancy[0], feedback, []float64{1})
	if err != nil || low.KappaHat != 1 || len(low.Tests) != 1 {
		t.Fatalf("low path reserve: %+v %v", low, err)
	}
	settings.MinimumDemand = map[string]float64{definition.UID: 1e6}
	floored, err := JudgeFeedbackRates(context.Background(), settings, snapshot.Options[0], definition, snapshot.DispatchOccupancy[0], feedback, []float64{1})
	if err != nil || !reflect.DeepEqual(low, floored) {
		t.Fatalf("explicit rate experiment acquired planning demand floor: low=%+v floor=%+v %v", low, floored, err)
	}
	feedback.TotalNanos, feedback.PostNanos = 40e6, 30e6
	high, err := JudgeFeedbackRates(context.Background(), settings, snapshot.Options[0], definition, snapshot.DispatchOccupancy[0], feedback, []float64{1})
	if err != nil || high.KappaHat != 0 || len(high.Tests) != 1 || low.Tests[0].InputKey == high.Tests[0].InputKey {
		t.Fatalf("current feedback reused old rate verdict: low=%+v high=%+v %v", low, high, err)
	}
}

func TestFeedbackConsumerPotentialCannotInventFutureBatchFill(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	snapshot.Arrivals = snapshot.Arrivals[:1]
	snapshot.Workloads[0].SLO.TargetLatency = 10 * time.Millisecond
	snapshot.Workloads[0].Admission.Policy = AdmissionPolicyReject
	snapshot.Instances[0].Profile.Batches = []BatchProfile{
		schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{60e6}),
		schedulerTestBatchProfile(BatchProfile{Batch: 2}, []float64{1e6}),
	}
	result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
	if result.Workloads[0].Rejected != 1 || result.Workloads[0].PotentialExcess == nil || *result.Workloads[0].PotentialExcess != 5 {
		t.Fatalf("unfilled batch2 must not erase batch1 conditional excess: %+v", result)
	}
}

func TestFeedbackConsumerReturningUsesTerminalNativeAge(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	snapshot.Arrivals = nil
	post, sender, native := 10.0, 20.0, 40e6
	snapshot.Outstanding = []SchedulerRequest{{ID: "returning", Definition: snapshot.Workloads[0], Phase: phaseReturning, Instance: "i1", Elapsed: time.Second, ReturnElapsed: 100 * time.Millisecond, CompletionMarginMS: &post, NativeSenderElapsedMS: &sender, NativeElapsedNanos: &native}}
	snapshot.PathFeedback = []PathFeedback{{WorkloadUID: snapshot.Workloads[0].UID, TotalNanos: 500e6, PostNanos: 400e6}}
	result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
	if *result.Workloads[0].SuccessQuantileNanos != 70e6 {
		t.Fatalf("returning native age is terminal; full server age must not enter twice: %+v", result)
	}
}

func TestFeedbackConsumerEmptyDemandIsNotZeroLatencyObservation(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	snapshot.Arrivals = nil
	result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
	score := result.Workloads[0]
	if score.Population != 0 || score.SuccessQuantileNanos != nil || score.FailureFraction != 0 || !score.Satisfied || result.SLO[0].ExpectedNanos != nil {
		t.Fatalf("empty cohort must stay unobserved and obligation-free: %+v", result)
	}
}

func TestFeedbackConsumerCapturedNativeFailuresRemainFixedOutcomes(t *testing.T) {
	for _, outcome := range []string{firstfloor.OutcomeDeadlineDrop, firstfloor.OutcomePreprocessFailed, firstfloor.OutcomeBatchError} {
		t.Run(outcome, func(t *testing.T) {
			settings, snapshot, candidate := feedbackFixture()
			snapshot.Arrivals = nil
			// Terminal failure is known even when its old native timing is absent.
			snapshot.Outstanding = []SchedulerRequest{{ID: "failed", Definition: snapshot.Workloads[0], Phase: phaseReturning, Instance: "i1", NativeOutcome: outcome}}
			result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
			score := result.Workloads[0]
			key, dropped, failed := schedulerOutcomeFailed, 0, 1
			if outcome == firstfloor.OutcomeDeadlineDrop {
				key, dropped, failed = schedulerOutcomeRejected, 1, 0
			}
			if score.Population != 1 || score.Rejected != dropped || score.Failed != failed || score.FailureFraction != 1 || score.OnTime+score.Late != 0 || score.SuccessQuantileNanos != nil || score.PotentialExcess != nil || len(score.PotentialUnavailableReasons) == 0 || result.SLO[0].PredictedOutcomes[key] != 1 || result.SLO[0].ExpectedNanos != nil {
				t.Fatalf("fixed native failure became success or blocked current control: %+v", result)
			}
		})
	}
}

func TestFeedbackConsumerPreparationDrainsFiniteCohortAndFixesPotential(t *testing.T) {
	for _, expires := range []bool{false, true} {
		t.Run(fmt.Sprint(expires), func(t *testing.T) {
			settings, snapshot, candidate := feedbackFixture()
			snapshot.Workloads[0].Admission.Policy = AdmissionPolicyDelay
			settings.DefaultOperationCost[schedulerActionPrepare] = OperationCostValue{Seconds: .05, Joules: 1}
			target := snapshot.Instances[0]
			target.ID, target.MaxBatch = feedbackPreparedInstanceID, 1
			snapshot.Instances = nil
			snapshot.PathFeedback = []PathFeedback{{WorkloadUID: snapshot.Workloads[0].UID, TotalNanos: 20e6, PostNanos: 10e6}}
			if expires {
				for index := range snapshot.Arrivals {
					snapshot.Arrivals[index].Lifetime = time.Millisecond
				}
			}
			candidate.Keep, candidate.Instances = false, []SchedulerInstance{target}
			candidate.Actions = []Action{
				{ID: "prepare", Kind: schedulerActionPrepare, Instance: target.ID, Node: target.Node, From: "unloaded", To: schedulerState(target)},
				{ID: "route", Kind: schedulerActionRoute, DependsOn: []string{"prepare"}},
			}
			result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
			score := result.Workloads[0]
			if expires {
				if score.Timeout != 2 || score.OnTime+score.Late != 0 || score.PotentialExcess == nil || math.Abs(*score.PotentialExcess) > 1e-12 {
					t.Fatalf("started 50ms prepare + 30ms service + TOTAL20 must give conditional 100ms, not completion: %+v", result)
				}
			} else if score.OnTime+score.Late != 2 || *score.SuccessQuantileNanos != 130e6 {
				t.Fatalf("finite cohort must wait for prepare/route then drain two batches after horizon: %+v", result)
			}
		})
	}
}

func TestFeedbackConsumerHistoricalPOSTFallbackPreservesRealZero(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			settings, snapshot, candidate := feedbackFixture()
			settings.CompletionMarginMS = 7
			snapshot.Arrivals = nil
			snapshot.Instances[0].MaxBatch = 1
			zero := 0.0
			margin := &zero
			want := 30e6
			if missing {
				margin, want = nil, 37e6
			}
			snapshot.Outstanding = []SchedulerRequest{{
				ID: "historical", Definition: snapshot.Workloads[0], Phase: phaseQueued, Instance: "i1",
				Elapsed: 100 * time.Millisecond, CompletionMarginMS: margin,
				NativeSenderElapsedMS: &zero, NativeElapsedNanos: &zero,
			}}
			snapshot.PathFeedback = []PathFeedback{{WorkloadUID: snapshot.Workloads[0].UID, TotalNanos: 500e6, PostNanos: 400e6}}
			result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
			if *result.Workloads[0].SuccessQuantileNanos != want || slices.Contains(result.Sources, "configured-initial-post") != missing || (len(result.Assumptions) > 0) != missing {
				t.Fatalf("absent POST needs explicit configured model; real zero stays frozen: want=%g result=%+v", want, result)
			}
			snapshot.Outstanding[0].NativeElapsedNanos = nil
			invalid, err := EvaluateFeedbackCandidate(context.Background(), settings, snapshot, candidate, 1)
			if err != nil || len(invalid.InvalidReasons) != 1 || invalid.InvalidReasons[0] != "MissingNativeElapsedContext:historical" {
				t.Fatalf("POST fallback must not fabricate native elapsed: %+v %v", invalid, err)
			}
		})
	}
}
