package batchsaturation

import (
	"errors"
	"fmt"
)

const sequentialCapabilityBoundaryOutcome = "Stopped/CapabilityBoundary"

// CurveSource derives the sequential decision over the admitted active batches
// it is given. The controller supplies it because building a Curve needs
// evidence the progress rules never see.
type CurveSource func(batches []int) (CurveResult, error)

// CurveResult is the method's view of a derivation outcome. Only the fields the
// stopping rule consumes cross this boundary. It is exported because the
// controller supplies the CurveSource that constructs it.
type CurveResult struct {
	Continue      bool
	SelectedBatch int
	Outcome       string
	Detail        any
}

type SequentialConfig struct {
	MinDelta         float64
	Patience         int
	TrialsPerBatch   int
	DivergenceFactor float64
}

type sequentialEarlyStop struct {
	config SequentialConfig
	curve  CurveSource
}

// NewSequentialEarlyStop mirrors the bounds validateSequentialPolicy enforces
// in internal/profiling/derivation, so an invalid configuration fails here rather
// than after the first Run.
func NewSequentialEarlyStop(config SequentialConfig, curve CurveSource) (Method, error) {
	if config.MinDelta < 0 || config.MinDelta >= 1 ||
		config.Patience < 1 || config.TrialsPerBatch < 3 || config.DivergenceFactor <= 1 {
		return nil, errors.New("sequential configuration is outside the supported range")
	}
	return &sequentialEarlyStop{config: config, curve: curve}, nil
}

func (s *sequentialEarlyStop) Plan(observed Observed) (Plan, error) {
	if len(observed.PlannedBatchSizes) == 0 {
		return Plan{}, errors.New("a non-empty planned batch size set is required")
	}
	counts := make(map[Measurement]int, len(observed.Accepted))
	highestActive := 0
	for _, measurement := range observed.Accepted {
		counts[measurement]++
		if measurement.WorkloadMode == WorkloadModeActive && measurement.Batch > highestActive {
			highestActive = measurement.Batch
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

	batches := make([]int, 0, len(observed.PlannedBatchSizes))
	for _, batch := range observed.PlannedBatchSizes {
		if batch > highestActive {
			break
		}
		batches = append(batches, batch)
	}
	if s.curve == nil {
		return Plan{}, errors.New("a curve source is required once a batch is complete")
	}
	result, err := s.curve(batches)
	if err != nil {
		return Plan{}, fmt.Errorf("derive sequential decision: %w", err)
	}
	if !result.Continue {
		return Plan{Result: &Result{Outcome: result.Outcome, Detail: result.Detail}}, nil
	}
	next, found := nextPlannedBatchSize(observed.PlannedBatchSizes, highestActive)
	if !found {
		return Plan{Result: &Result{
			Outcome: sequentialCapabilityBoundaryOutcome,
			Detail:  result.Detail,
		}}, nil
	}
	measurement := Measurement{WorkloadMode: WorkloadModeActive, Batch: next}
	return Plan{Next: &measurement}, nil
}

// nextPlannedBatchSize returns the smallest planned batch size above the highest one
// already measured. Exhausting the declared set is the capability boundary.
func nextPlannedBatchSize(plannedBatchSizes []int, highest int) (int, bool) {
	for _, plannedBatchSize := range plannedBatchSizes {
		if plannedBatchSize > highest {
			return plannedBatchSize, true
		}
	}
	return 0, false
}
