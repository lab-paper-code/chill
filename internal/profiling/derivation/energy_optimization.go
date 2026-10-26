package profilederivation

import (
	"errors"
	"fmt"
	"math"
)

const EnergyObjectiveMinIncrementalWork = "min-incremental-work-joules-per-item"

// ErrEnergyObjectivePolicyMismatch identifies evidence that cannot satisfy the
// recorded energy-objective policy, rather than a transient derivation failure.
var ErrEnergyObjectivePolicyMismatch = errors.New("energy-objective policy mismatch")

type EnergySearchStatus string

const (
	EnergySearchContinue EnergySearchStatus = "Continue"
	EnergySearchComplete EnergySearchStatus = "Complete"
)

type EnergySearchCompletionReason string

const (
	EnergySearchLocalMinimum   EnergySearchCompletionReason = "LocalMinimum"
	EnergySearchSpaceExhausted EnergySearchCompletionReason = "SearchSpaceExhausted"
)

// EnergyObjectivePolicy is recorded before the first campaign Run. The objective
// and search algorithm are versioned constants; only the repeated measurement
// count is configurable.
type EnergyObjectivePolicy struct {
	Version        string `json:"version"`
	TrialsPerBatch int    `json:"trialsPerBatch"`
}

type BatchEnergyMean struct {
	Batch                        int     `json:"batch"`
	IncrementalWorkJoulesPerItem float64 `json:"incrementalWorkJoulesPerItem"`
}

type EnergyOptimizationResult struct {
	Status                              EnergySearchStatus           `json:"status"`
	CompletionReason                    EnergySearchCompletionReason `json:"completionReason,omitempty"`
	BestBatch                           *int                         `json:"bestBatch,omitempty"`
	MinimumIncrementalWorkJoulesPerItem *float64                     `json:"minimumIncrementalWorkJoulesPerItem,omitempty"`
	MeasuredBatchSizes                  []int                        `json:"measuredBatchSizes"`
	IncrementalWorkByBatch              []BatchEnergyMean            `json:"incrementalWorkByBatch"`
	Objective                           string                       `json:"objective"`
	PolicyVersion                       string                       `json:"policyVersion"`
	TrialsPerBatch                      int                          `json:"trialsPerBatch"`
	CurvePlanName                       string                       `json:"curvePlanName"`
	IdleBaselinesByState                map[string]IdleBaseline      `json:"idleBaselinesByState"`
}

// DeriveMinimumEnergyBatch applies a one-dimensional quick search to the
// measured prefix of a verified batch plan. Every strictly lower mean advances
// the search, regardless of the size of the improvement. The first point that
// does not improve the objective closes the local search; exhausting the
// declared search space is a separate successful completion.
func DeriveMinimumEnergyBatch(
	curve Curve,
	policy EnergyObjectivePolicy,
) (EnergyOptimizationResult, error) {
	if err := curve.plan.requireVerified(); err != nil {
		return EnergyOptimizationResult{}, err
	}
	if err := validateEnergyObjectivePolicy(policy); err != nil {
		return EnergyOptimizationResult{}, err
	}
	search, exists := curve.pass(PassSearch)
	if !exists {
		return EnergyOptimizationResult{}, errors.New("energy optimization requires a search pass")
	}

	result := EnergyOptimizationResult{
		Status:               EnergySearchContinue,
		Objective:            EnergyObjectiveMinIncrementalWork,
		PolicyVersion:        policy.Version,
		TrialsPerBatch:       policy.TrialsPerBatch,
		CurvePlanName:        curve.PlanName(),
		IdleBaselinesByState: curve.IdleBaselinesByState(),
	}
	best := math.Inf(1)
	bestBatch := 0
	plannedBatchSizes := curve.plan.recorded.record.Shared.PlannedBatchSizes
	for index, point := range search.points {
		expectedBatch := plannedBatchSizes[index]
		if point.Batch != expectedBatch || len(point.Trials) != policy.TrialsPerBatch ||
			math.IsNaN(point.MeanIncrementalWorkJoulesPerItem) ||
			math.IsInf(point.MeanIncrementalWorkJoulesPerItem, 0) {
			return EnergyOptimizationResult{}, fmt.Errorf(
				"%w: active/b%d: expected batch=%d, expected=%d, completed=%d, mean=%g",
				ErrEnergyObjectivePolicyMismatch, point.Batch, expectedBatch,
				policy.TrialsPerBatch, len(point.Trials), point.MeanIncrementalWorkJoulesPerItem,
			)
		}
		result.MeasuredBatchSizes = append(result.MeasuredBatchSizes, point.Batch)
		result.IncrementalWorkByBatch = append(result.IncrementalWorkByBatch, BatchEnergyMean{
			Batch: point.Batch, IncrementalWorkJoulesPerItem: point.MeanIncrementalWorkJoulesPerItem,
		})

		value := point.MeanIncrementalWorkJoulesPerItem
		if value < best {
			best = value
			bestBatch = point.Batch
			continue
		}
		result.Status = EnergySearchComplete
		result.CompletionReason = EnergySearchLocalMinimum
		if index != len(search.points)-1 {
			return EnergyOptimizationResult{}, errors.New("search curve contains points after its local minimum")
		}
	}

	if bestBatch != 0 {
		selected := bestBatch
		bestValue := best
		result.BestBatch = &selected
		result.MinimumIncrementalWorkJoulesPerItem = &bestValue
	}
	if result.Status == EnergySearchContinue && len(search.points) == len(plannedBatchSizes) {
		result.Status = EnergySearchComplete
		result.CompletionReason = EnergySearchSpaceExhausted
	}
	return result, nil
}

func validateEnergyObjectivePolicy(policy EnergyObjectivePolicy) error {
	if policy.Version == "" || policy.TrialsPerBatch < 3 {
		return errors.New("energy-objective policy is invalid")
	}
	return nil
}
