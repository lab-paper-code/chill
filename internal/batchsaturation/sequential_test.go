package batchsaturation

import "testing"

func TestSequentialRequestsIdleBaselineFirst(t *testing.T) {
	method, err := NewSequentialEarlyStop(SequentialConfig{
		MinDelta: 0.05, Patience: 1, TrialsPerBatch: 3, DivergenceFactor: 1.1,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	plan, err := method.Plan(Observed{EligibleBatches: []int{1}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Next == nil || *plan.Next != (Measurement{WorkloadMode: WorkloadModeIdle, Batch: 1}) {
		t.Fatalf("first measurement = %+v, want idle batch 1", plan.Next)
	}
}

func TestSequentialFillsEachBatchBeforeAdvancing(t *testing.T) {
	method, err := NewSequentialEarlyStop(SequentialConfig{
		MinDelta: 0.05, Patience: 1, TrialsPerBatch: 3, DivergenceFactor: 1.1,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	observed := Observed{EligibleBatches: []int{1}, Accepted: []Measurement{
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

func TestSequentialStepsOverAGapInTheEligibleSet(t *testing.T) {
	method, err := NewSequentialEarlyStop(
		SequentialConfig{MinDelta: 0.05, Patience: 2, TrialsPerBatch: 3, DivergenceFactor: 2},
		func(batches []int) (CurveResult, error) {
			return CurveResult{Continue: true, SelectedBatch: 2}, nil
		},
	)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}

	plan, err := method.Plan(Observed{
		EligibleBatches: []int{1, 2, 4},
		Accepted:        acceptedThrough(2, 3),
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Next == nil || plan.Next.Batch != 4 {
		t.Fatalf("next = %v, want the declared batch 4", plan.Next)
	}
}

func TestSequentialStopsWhenTheEligibleSetIsExhausted(t *testing.T) {
	method, err := NewSequentialEarlyStop(
		SequentialConfig{MinDelta: 0.05, Patience: 2, TrialsPerBatch: 3, DivergenceFactor: 2},
		func(batches []int) (CurveResult, error) {
			return CurveResult{Continue: true, SelectedBatch: 2, Outcome: "Stopped/CapabilityBoundary"}, nil
		},
	)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}

	plan, err := method.Plan(Observed{
		EligibleBatches: []int{1, 2},
		Accepted:        acceptedThrough(2, 3),
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Result == nil || plan.Result.Outcome != "Stopped/CapabilityBoundary" {
		t.Fatalf("result = %v, want a capability-boundary stop", plan.Result)
	}
}

func acceptedThrough(maxBatch, trials int) []Measurement {
	var accepted []Measurement
	for count := 0; count < trials; count++ {
		accepted = append(accepted, Measurement{WorkloadMode: WorkloadModeIdle, Batch: 1})
	}
	for batch := 1; batch <= maxBatch; batch++ {
		for count := 0; count < trials; count++ {
			accepted = append(accepted, Measurement{WorkloadMode: WorkloadModeActive, Batch: batch})
		}
	}
	return accepted
}
