package batchenergyoptimization

import (
	"errors"
	"fmt"
)

// CurveSource derives the energy-objective decision over all completed planned
// batches. The controller supplies it because building a Curve needs evidence
// the progress rules never see.
type CurveSource func(batches []int) (CurveResult, error)

// CurveResult is the method's view of a derivation outcome. Only fields needed
// to schedule the next measurement cross this boundary.
type CurveResult struct {
	Continue bool
	Outcome  string
	Detail   any
}

type QuickEnergyConfig struct {
	TrialsPerBatch int
}

type quickEnergyMinimization struct {
	config QuickEnergyConfig
	curve  CurveSource
}

// NewQuickEnergyMinimization constructs the full planned-batch energy sweep.
// The exact objective is owned by the derivation boundary.
func NewQuickEnergyMinimization(config QuickEnergyConfig, curve CurveSource) (Method, error) {
	if config.TrialsPerBatch != 3 {
		return nil, errors.New("quick-energy configuration is outside the supported range")
	}
	return &quickEnergyMinimization{config: config, curve: curve}, nil
}

func (s *quickEnergyMinimization) Plan(observed Observed) (Plan, error) {
	counts := make(map[Measurement]int, len(observed.Completed))
	for _, measurement := range observed.Completed {
		counts[measurement]++
	}
	batches, err := discoveryPrefix(observed.PlannedBatchSizes, counts)
	if err != nil {
		return Plan{}, err
	}
	if observed.Boundary != nil {
		if err := validateDiscoveryBoundary(*observed.Boundary, observed.PlannedBatchSizes, batches, counts); err != nil {
			return Plan{}, err
		}
		return s.finish(batches, *observed.Boundary)
	}
	baseline := Measurement{WorkloadMode: WorkloadModeIdle, Batch: observed.PlannedBatchSizes[0]}
	if counts[baseline] < s.config.TrialsPerBatch {
		return Plan{Next: &baseline}, nil
	}
	if len(batches) < len(observed.PlannedBatchSizes) {
		next := Measurement{WorkloadMode: WorkloadModeActive, Batch: observed.PlannedBatchSizes[len(batches)]}
		return Plan{Next: &next}, nil
	}
	return s.finish(batches, DiscoveryStop{Reason: DiscoveryStopDomainExhausted})
}

func (s *quickEnergyMinimization) finish(batches []int, stop DiscoveryStop) (Plan, error) {
	detail := DiscoveryResult{MeasuredBatchSizes: batches, Stop: stop}
	if len(batches) != 0 {
		if s.curve == nil {
			return Plan{}, errors.New("a curve source is required once a batch is complete")
		}
		derived, err := s.curve(batches)
		if err != nil {
			return Plan{}, fmt.Errorf("derive minimum-energy decision: %w", err)
		}
		if stop.Reason == DiscoveryStopDomainExhausted && derived.Continue {
			return Plan{}, errors.New("energy derivation continued after exhausting the planned search space")
		}
		detail.Energy = derived.Detail
	}
	return Plan{Result: &Result{Outcome: BatchDiscoveryComplete, Detail: detail}}, nil
}
