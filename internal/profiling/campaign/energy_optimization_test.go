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

func TestQuickEnergyStartsAtTheFirstPlannedBatchSize(t *testing.T) {
	method := newTestQuickEnergyMethod(t, nil)

	t.Run("idle baseline", func(t *testing.T) {
		plan, err := method.Plan(Observed{PlannedBatchSizes: []int{2, 4}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := Measurement{WorkloadMode: WorkloadModeIdle, Batch: 2}
		if plan.Next == nil || *plan.Next != want {
			t.Fatalf("first measurement = %+v, want %+v", plan.Next, want)
		}
	})

	t.Run("first active point", func(t *testing.T) {
		completed := []Measurement{
			{WorkloadMode: WorkloadModeIdle, Batch: 2},
			{WorkloadMode: WorkloadModeIdle, Batch: 2},
			{WorkloadMode: WorkloadModeIdle, Batch: 2},
		}
		plan, err := method.Plan(Observed{PlannedBatchSizes: []int{2, 4}, Completed: completed})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := Measurement{WorkloadMode: WorkloadModeActive, Batch: 2}
		if plan.Next == nil || *plan.Next != want {
			t.Fatalf("next measurement = %+v, want %+v", plan.Next, want)
		}
	})
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
		PlannedBatchSizes: []int{1, 2, 4},
		Completed:         completedThrough(2, 3),
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
		if plan.Next == nil || *plan.Next != want || plan.Result != nil || curveCalls != 1 {
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

func TestQuickEnergyOnlyStopsEarlyForCurrentBatchOneServiceImpossibility(t *testing.T) {
	for _, outcome := range []string{ServiceImpossibleOutcome, "MinimumFound/LocalMinimum", ""} {
		method := newTestQuickEnergyMethod(t, func(batches []int) (CurveResult, error) {
			if !reflect.DeepEqual(batches, []int{1}) {
				t.Fatalf("feasibility batches=%v", batches)
			}
			return CurveResult{Continue: outcome == "", Outcome: outcome, Detail: "current verified trials"}, nil
		})
		plan, err := method.Plan(Observed{PlannedBatchSizes: []int{1, 2, 4}, Completed: completedForBatches([]int{1}, 3)})
		if err != nil {
			t.Fatal(err)
		}
		if outcome == ServiceImpossibleOutcome {
			if plan.Result == nil || plan.Result.Outcome != outcome || plan.Next != nil {
				t.Fatalf("impossible cell did not end at batch one: %+v", plan)
			}
		} else if plan.Next == nil || plan.Next.Batch != 2 || plan.Result != nil {
			t.Fatalf("heuristic outcome %q truncated the planned sweep: %+v", outcome, plan)
		}
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
	if plan.Result == nil || plan.Result.Outcome != "MinimumFound/SearchSpaceExhausted" ||
		plan.Result.Detail != "curve detail" {
		t.Fatalf("result = %+v", plan.Result)
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
