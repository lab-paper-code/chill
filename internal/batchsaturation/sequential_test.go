package batchsaturation

import "testing"

func TestSequentialRequestsIdleBaselineFirst(t *testing.T) {
	method, err := NewSequentialEarlyStop(SequentialConfig{
		MinDelta: 0.05, Patience: 1, TrialsPerBatch: 3, DivergenceFactor: 1.1,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	plan, err := method.Plan(Observed{})
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
	observed := Observed{Accepted: []Measurement{
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

func TestSequentialStopsAtACapabilityBoundary(t *testing.T) {
	boundary := 3
	method, err := NewSequentialEarlyStop(SequentialConfig{
		MinDelta: 0.05, Patience: 1, TrialsPerBatch: 3, DivergenceFactor: 1.1,
	}, func(batches []int) (CurveResult, error) {
		return CurveResult{SelectedBatch: 2, Outcome: "Stopped/CapabilityBoundary"}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	observed := Observed{CapabilityBoundary: &boundary, Accepted: acceptedThrough(2, 3)}
	plan, err := method.Plan(observed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Result == nil || plan.Result.Outcome != "Stopped/CapabilityBoundary" {
		t.Fatalf("result = %+v, want a capability-boundary stop", plan.Result)
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
