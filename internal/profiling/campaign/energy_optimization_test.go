package batchenergyoptimization

import (
	"reflect"
	"testing"
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
		accepted := []Measurement{
			{WorkloadMode: WorkloadModeIdle, Batch: 2},
			{WorkloadMode: WorkloadModeIdle, Batch: 2},
			{WorkloadMode: WorkloadModeIdle, Batch: 2},
		}
		plan, err := method.Plan(Observed{PlannedBatchSizes: []int{2, 4}, Accepted: accepted})
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
	observed := Observed{PlannedBatchSizes: []int{1}, Accepted: []Measurement{
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
		Accepted:          acceptedThrough(2, 3),
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	want := Measurement{WorkloadMode: WorkloadModeActive, Batch: 4}
	if plan.Next == nil || *plan.Next != want {
		t.Fatalf("next = %v, want %+v", plan.Next, want)
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
		Accepted:          acceptedForBatches([]int{1, 2, 4}, 3),
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
		Accepted:          acceptedThrough(2, 3),
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

func acceptedThrough(maxBatch, trials int) []Measurement {
	batches := make([]int, maxBatch)
	for index := range batches {
		batches[index] = index + 1
	}
	return acceptedForBatches(batches, trials)
}

func acceptedForBatches(batches []int, trials int) []Measurement {
	accepted := make([]Measurement, 0, (len(batches)+1)*trials)
	for count := 0; count < trials; count++ {
		accepted = append(accepted, Measurement{WorkloadMode: WorkloadModeIdle, Batch: batches[0]})
	}
	for _, batch := range batches {
		for count := 0; count < trials; count++ {
			accepted = append(accepted, Measurement{WorkloadMode: WorkloadModeActive, Batch: batch})
		}
	}
	return accepted
}
