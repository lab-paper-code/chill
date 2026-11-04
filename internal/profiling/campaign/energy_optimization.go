package batchenergyoptimization

import (
	"errors"
	"fmt"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
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

// ServiceImpossibleOutcome records negative capacity, never a usable batch or κ.
const ServiceImpossibleOutcome = "BatchServiceExceedsSLO"

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
	if len(observed.PlannedBatchSizes) == 0 {
		return Plan{}, errors.New("a non-empty planned batch size set is required")
	}
	counts := make(map[Measurement]int, len(observed.Completed))
	highestActive := 0
	for _, measurement := range observed.Completed {
		counts[measurement]++
		if measurement.WorkloadMode == WorkloadModeActive && measurement.Batch > highestActive {
			highestActive = measurement.Batch
		}
	}
	for _, measurement := range observed.Completed {
		if counts[measurement] > s.config.TrialsPerBatch {
			return Plan{}, fmt.Errorf(
				"%w: %s/b%d: expected batch=%d, expected=%d, completed=%d",
				profilederivation.ErrEnergyObjectivePolicyMismatch, measurement.WorkloadMode,
				measurement.Batch, measurement.Batch, s.config.TrialsPerBatch, counts[measurement],
			)
		}
	}

	baseline := Measurement{WorkloadMode: WorkloadModeIdle, Batch: observed.PlannedBatchSizes[0]}
	if counts[baseline] < s.config.TrialsPerBatch {
		return Plan{Next: &baseline}, nil
	}

	for _, batch := range observed.PlannedBatchSizes {
		if batch > highestActive {
			break
		}
		point := Measurement{WorkloadMode: WorkloadModeActive, Batch: batch}
		if counts[point] < s.config.TrialsPerBatch {
			return Plan{Next: &point}, nil
		}
	}
	if highestActive == 0 {
		first := Measurement{WorkloadMode: WorkloadModeActive, Batch: observed.PlannedBatchSizes[0]}
		return Plan{Next: &first}, nil
	}

	// Only verified current-condition batch-one service evidence may shorten a
	// sweep. A slow percentile or a non-improving energy neighbor cannot.
	if highestActive == 1 && observed.PlannedBatchSizes[0] == 1 && s.curve != nil {
		result, err := s.curve([]int{1})
		if err != nil {
			return Plan{}, fmt.Errorf("derive batch-one feasibility: %w", err)
		}
		if result.Outcome == ServiceImpossibleOutcome {
			return Plan{Result: &Result{Outcome: result.Outcome, Detail: result.Detail}}, nil
		}
		if len(observed.PlannedBatchSizes) == 1 {
			if result.Continue {
				return Plan{}, errors.New("energy derivation continued after exhausting the planned search space")
			}
			return Plan{Result: &Result{Outcome: result.Outcome, Detail: result.Detail}}, nil
		}
	}
	if next, found := nextPlannedBatchSize(observed.PlannedBatchSizes, highestActive); found {
		measurement := Measurement{WorkloadMode: WorkloadModeActive, Batch: next}
		return Plan{Next: &measurement}, nil
	}

	batches := measuredBatchPrefix(observed.PlannedBatchSizes, highestActive)
	if s.curve == nil {
		return Plan{}, errors.New("a curve source is required once a batch is complete")
	}
	result, err := s.curve(batches)
	if err != nil {
		return Plan{}, fmt.Errorf("derive minimum-energy decision: %w", err)
	}
	if !result.Continue {
		return Plan{Result: &Result{Outcome: result.Outcome, Detail: result.Detail}}, nil
	}
	return Plan{}, errors.New("energy derivation continued after exhausting the planned search space")
}

func measuredBatchPrefix(plannedBatchSizes []int, highest int) []int {
	batches := make([]int, 0, len(plannedBatchSizes))
	for _, batch := range plannedBatchSizes {
		if batch > highest {
			break
		}
		batches = append(batches, batch)
	}
	return batches
}

func nextPlannedBatchSize(plannedBatchSizes []int, highest int) (int, bool) {
	for _, plannedBatchSize := range plannedBatchSizes {
		if plannedBatchSize > highest {
			return plannedBatchSize, true
		}
	}
	return 0, false
}
