package batchenergyoptimization

import (
	"errors"
	"reflect"
	"testing"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

func TestQuickEnergyRequestsIdleBaselineFirst(t *testing.T) {
	method := newTestQuickEnergyMethod(t, nil)
	plan, err := method.Plan(Observed{PlannedBatchSizes: []int{1}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Next == nil || *plan.Next != (Measurement{WorkloadMode: WorkloadModeIdle, Batch: 1}) {
		t.Fatalf("first measurement = %+v, want idle batch 1", plan.Next)
	}
}

func TestQuickEnergyRejectsAPlanThatDoesNotStartAtOne(t *testing.T) {
	method := newTestQuickEnergyMethod(t, nil)
	if _, err := method.Plan(Observed{PlannedBatchSizes: []int{2, 4}}); err == nil {
		t.Fatal("accepted discovery without batch one")
	}
}

func TestQuickEnergyFillsEachBatchBeforeAdvancing(t *testing.T) {
	method := newTestQuickEnergyMethod(t, nil)
	observed := Observed{PlannedBatchSizes: []int{1}, Completed: []Measurement{
		{WorkloadMode: WorkloadModeIdle, Batch: 1},
		{WorkloadMode: WorkloadModeIdle, Batch: 1},
		{WorkloadMode: WorkloadModeIdle, Batch: 1},
		{WorkloadMode: WorkloadModeActive, Batch: 1},
	}}
	plan, err := method.Plan(observed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Next == nil || *plan.Next != (Measurement{WorkloadMode: WorkloadModeActive, Batch: 1}) {
		t.Fatalf("next = %+v, want a second active trial at batch 1", plan.Next)
	}
}

func TestQuickEnergyStepsOverAGapInThePlannedBatchSizeSet(t *testing.T) {
	method := newTestQuickEnergyMethod(t, func(batches []int) (CurveResult, error) {
		return CurveResult{Continue: true}, nil
	})

	plan, err := method.Plan(Observed{
		PlannedBatchSizes: []int{1, 4, 8},
		Completed:         completedForBatches([]int{1}, 3),
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	want := Measurement{WorkloadMode: WorkloadModeActive, Batch: 4}
	if plan.Next == nil || *plan.Next != want {
		t.Fatalf("next = %v, want %+v", plan.Next, want)
	}
}

func TestQuickEnergySweepsEveryPlannedBatchBeforeSelectingMinimum(t *testing.T) {
	planned := []int{1, 2, 4, 8}
	curveCalls := 0
	method := newTestQuickEnergyMethod(t, func(batches []int) (CurveResult, error) {
		curveCalls++
		// A local minimum at batch 2 must not stop the sweep before batch 8.
		return CurveResult{Outcome: "MinimumFound/LocalMinimum"}, nil
	})
	for index := 1; index < len(planned); index++ {
		plan, err := method.Plan(Observed{
			PlannedBatchSizes: planned,
			Completed:         completedForBatches(planned[:index], 3),
		})
		if err != nil {
			t.Fatal(err)
		}
		want := Measurement{WorkloadMode: WorkloadModeActive, Batch: planned[index]}
		if plan.Next == nil || *plan.Next != want || plan.Result != nil || curveCalls != 0 {
			t.Fatalf("prefix %v: plan=%+v, curve calls=%d; want %+v without heuristic truncation",
				planned[:index], plan, curveCalls, want)
		}
	}
}

func TestQuickEnergyRequiresThreeIndependentTrials(t *testing.T) {
	for _, trials := range []int{0, 1, 2, 4} {
		if _, err := NewQuickEnergyMinimization(QuickEnergyConfig{TrialsPerBatch: trials}, nil); err == nil {
			t.Fatalf("accepted %d trials instead of three", trials)
		}
	}
}

func TestQuickEnergyDoesNotDeriveBatchOneFeasibilityBeforeIncreasing(t *testing.T) {
	curveCalls := 0
	method := newTestQuickEnergyMethod(t, func([]int) (CurveResult, error) {
		curveCalls++
		return CurveResult{Outcome: ServiceImpossibleOutcome}, nil
	})
	plan, err := method.Plan(Observed{
		PlannedBatchSizes: []int{1, 2, 4},
		Completed:         completedForBatches([]int{1}, 3),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Next == nil || plan.Next.Batch != 2 || plan.Result != nil || curveCalls != 0 {
		t.Fatalf("batch-one SLO shortened discovery: plan=%+v calls=%d", plan, curveCalls)
	}
}

func TestQuickEnergyPassesTheMeasuredPrefixToTheObjective(t *testing.T) {
	var curveBatches []int
	method := newTestQuickEnergyMethod(t, func(batches []int) (CurveResult, error) {
		curveBatches = append([]int(nil), batches...)
		return CurveResult{Outcome: "MinimumFound/SearchSpaceExhausted", Detail: "curve detail"}, nil
	})

	plan, err := method.Plan(Observed{
		PlannedBatchSizes: []int{1, 2, 4},
		Completed:         completedForBatches([]int{1, 2, 4}, 3),
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if want := []int{1, 2, 4}; !reflect.DeepEqual(curveBatches, want) {
		t.Fatalf("curve batches = %v, want %v", curveBatches, want)
	}
	if plan.Result == nil || plan.Result.Outcome != BatchDiscoveryComplete {
		t.Fatalf("result = %+v", plan.Result)
	}
	detail := plan.Result.Detail.(DiscoveryResult)
	if detail.Energy != "curve detail" || detail.Stop.Reason != DiscoveryStopDomainExhausted ||
		!reflect.DeepEqual(detail.MeasuredBatchSizes, []int{1, 2, 4}) {
		t.Fatalf("discovery detail = %+v", detail)
	}
}

func TestQuickEnergyRejectsExcessSuccessfulTrialsBeforePlanning(t *testing.T) {
	for _, mode := range []WorkloadMode{WorkloadModeIdle, WorkloadModeActive} {
		t.Run(string(mode), func(t *testing.T) {
			curveCalls := 0
			method := newTestQuickEnergyMethod(t, func([]int) (CurveResult, error) {
				curveCalls++
				return CurveResult{Continue: true}, nil
			})
			// The idle baseline and earlier active points are deliberately missing:
			// excess successes must fail before requesting any missing trial.
			measurement := Measurement{WorkloadMode: mode, Batch: 8}
			plan, err := method.Plan(Observed{
				PlannedBatchSizes: []int{1, 2, 4, 8, 16},
				Completed:         []Measurement{measurement, measurement, measurement, measurement},
			})
			if !errors.Is(err, profilederivation.ErrEnergyObjectivePolicyMismatch) {
				t.Fatalf("expected excess-success policy mismatch, got %v", err)
			}
			if plan.Next != nil || plan.Result != nil || curveCalls != 0 {
				t.Fatalf("excess successes planned work: plan=%+v, curve calls=%d", plan, curveCalls)
			}
		})
	}
}

func TestQuickEnergyRejectsAnEmptyPlannedBatchSizeSet(t *testing.T) {
	method := newTestQuickEnergyMethod(t, nil)
	_, err := method.Plan(Observed{})
	if err == nil || err.Error() != "a non-empty planned batch size set is required" {
		t.Fatalf("error = %v, want non-empty search-space rejection", err)
	}
}

func TestQuickEnergyRequiresTheDerivationToCloseAnExhaustedSearchSpace(t *testing.T) {
	method := newTestQuickEnergyMethod(t, func(batches []int) (CurveResult, error) {
		return CurveResult{Continue: true}, nil
	})
	_, err := method.Plan(Observed{
		PlannedBatchSizes: []int{1, 2},
		Completed:         completedThrough(2, 3),
	})
	if err == nil || err.Error() != "energy derivation continued after exhausting the planned search space" {
		t.Fatalf("error = %v, want exhausted-search invariant rejection", err)
	}
}

func TestQuickEnergyStopsWithOnlyTheFullyCompletedPrefix(t *testing.T) {
	for _, reason := range []string{DiscoveryStopOOM, DiscoveryStopMeasurementBudgetExceeded, DiscoveryStopExecutionFailed} {
		t.Run(reason, func(t *testing.T) {
			curveCalls := 0
			energy := &profilederivation.EnergyOptimizationResult{Status: profilederivation.EnergySearchContinue}
			method := newTestQuickEnergyMethod(t, func(batches []int) (CurveResult, error) {
				curveCalls++
				if !reflect.DeepEqual(batches, []int{1, 2}) {
					t.Fatalf("derived incomplete prefix %v", batches)
				}
				return CurveResult{Continue: true, Detail: energy}, nil
			})
			completed := append(completedForBatches([]int{1, 2}, 3), active(4), active(4))
			failed := active(4)
			plan, err := method.Plan(Observed{
				PlannedBatchSizes: []int{1, 2, 4, 8}, Completed: completed,
				Boundary: &DiscoveryStop{Reason: reason, FailedMeasurement: &failed, Attempt: 3},
			})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Next != nil || plan.Result == nil || plan.Result.Outcome != BatchDiscoveryComplete || curveCalls != 1 {
				t.Fatalf("boundary plan=%+v curve calls=%d", plan, curveCalls)
			}
			detail := plan.Result.Detail.(DiscoveryResult)
			if !reflect.DeepEqual(detail.MeasuredBatchSizes, []int{1, 2}) || detail.Stop.Reason != reason ||
				detail.Energy != energy || detail.Stop.FailedMeasurement.Batch != 4 {
				t.Fatalf("boundary detail=%+v", detail)
			}
		})
	}
}

func TestQuickEnergyBatchOneBoundaryHasNoUsablePrefix(t *testing.T) {
	for _, measurement := range []Measurement{idle(1), active(1)} {
		t.Run(string(measurement.WorkloadMode), func(t *testing.T) {
			completed := []Measurement{idle(1)}
			if measurement.WorkloadMode == WorkloadModeActive {
				completed = append(completedForBatches([]int{1}, 0), idle(1), idle(1), idle(1), active(1))
			}
			method := newTestQuickEnergyMethod(t, func([]int) (CurveResult, error) {
				t.Fatal("derived a profile without a completed batch")
				return CurveResult{}, nil
			})
			plan, err := method.Plan(Observed{
				PlannedBatchSizes: []int{1, 2}, Completed: completed,
				Boundary: &DiscoveryStop{Reason: DiscoveryStopOOM, FailedMeasurement: &measurement, Attempt: 2},
			})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Result == nil || plan.Next != nil || plan.Result.Outcome != BatchDiscoveryComplete {
				t.Fatalf("batch-one boundary=%+v", plan)
			}
			detail := plan.Result.Detail.(DiscoveryResult)
			if detail.MeasuredBatchSizes == nil || len(detail.MeasuredBatchSizes) != 0 || detail.Energy != nil {
				t.Fatalf("unusable result=%+v", detail)
			}
		})
	}
}

func TestQuickEnergyRejectsAnUnclassifiedOrMismatchedBoundary(t *testing.T) {
	for _, stop := range []DiscoveryStop{
		{Reason: "InfrastructureFailure", FailedMeasurement: &Measurement{WorkloadMode: WorkloadModeActive, Batch: 2}, Attempt: 1},
		{Reason: DiscoveryStopOOM, FailedMeasurement: &Measurement{WorkloadMode: WorkloadModeActive, Batch: 4}, Attempt: 1},
		{Reason: DiscoveryStopOOM, FailedMeasurement: &Measurement{WorkloadMode: WorkloadModeActive, Batch: 2}},
	} {
		method := newTestQuickEnergyMethod(t, nil)
		if _, err := method.Plan(Observed{
			PlannedBatchSizes: []int{1, 2, 4}, Completed: completedForBatches([]int{1}, 3), Boundary: &stop,
		}); err == nil {
			t.Fatalf("accepted boundary %+v", stop)
		}
	}
}

func newTestQuickEnergyMethod(t *testing.T, curve CurveSource) Method {
	t.Helper()
	method, err := NewQuickEnergyMinimization(QuickEnergyConfig{TrialsPerBatch: 3}, curve)
	if err != nil {
		t.Fatalf("construct quick-energy method: %v", err)
	}
	return method
}

func completedThrough(maxBatch, trials int) []Measurement {
	batches := make([]int, maxBatch)
	for index := range batches {
		batches[index] = index + 1
	}
	return completedForBatches(batches, trials)
}

func completedForBatches(batches []int, trials int) []Measurement {
	completed := make([]Measurement, 0, (len(batches)+1)*trials)
	for count := 0; count < trials; count++ {
		completed = append(completed, Measurement{WorkloadMode: WorkloadModeIdle, Batch: batches[0]})
	}
	for _, batch := range batches {
		for count := 0; count < trials; count++ {
			completed = append(completed, Measurement{WorkloadMode: WorkloadModeActive, Batch: batch})
		}
	}
	return completed
}
