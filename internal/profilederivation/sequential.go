package profilederivation

import (
	"errors"
	"math"
)

type SequentialStatus string

const (
	SequentialContinue SequentialStatus = "Continue"
	SequentialStopped  SequentialStatus = "Stopped"
)

type SequentialStopReason string

const (
	StopNoFurtherImprovement SequentialStopReason = "NoFurtherImprovement"
	StopCapabilityBoundary   SequentialStopReason = "CapabilityBoundary"
	StopEnergyDivergence     SequentialStopReason = "EnergyDivergence"
)

// SequentialPolicy is fixed before the first campaign Run. The implementation
// supports one relative best-so-far rule, so it has no method selector.
type SequentialPolicy struct {
	Version          string  `json:"version"`
	MinDelta         float64 `json:"minDelta"`
	Patience         int     `json:"patience"`
	TrialsPerBatch   int     `json:"trialsPerBatch"`
	DivergenceFactor float64 `json:"divergenceFactor"`
}

type SequentialBatchMean struct {
	Batch                    int     `json:"batch"`
	IncrementalJoulesPerItem float64 `json:"incrementalJoulesPerItem"`
}

type SequentialResult struct {
	Status                       SequentialStatus        `json:"status"`
	StopReason                   SequentialStopReason    `json:"stopReason,omitempty"`
	SelectedBatch                *int                    `json:"selectedBatch,omitempty"`
	BoundaryBatch                *int                    `json:"boundaryBatch,omitempty"`
	BestIncrementalJoulesPerItem *float64                `json:"bestIncrementalJoulesPerItem,omitempty"`
	MeasuredBatches              []int                   `json:"measuredBatches"`
	EpsilonByBatch               []SequentialBatchMean   `json:"epsilonByBatch"`
	PolicyVersion                string                  `json:"policyVersion"`
	MinDelta                     float64                 `json:"minDelta"`
	Patience                     int                     `json:"patience"`
	TrialsPerBatch               int                     `json:"trialsPerBatch"`
	DivergenceFactor             float64                 `json:"divergenceFactor"`
	CurveFamilyName              string                  `json:"curveFamilyName"`
	IdleBaselinesByState         map[string]IdleBaseline `json:"idleBaselinesByState"`
}

// DeriveSequentialBatchDecision applies the relative best-so-far stopping rule
// to the discovery pass of an exact-family Curve. A prefix without a stop
// condition returns Continue so an offline driver can request the next member.
func DeriveSequentialBatchDecision(curve Curve, policy SequentialPolicy) (SequentialResult, error) {
	if err := curve.family.requireVerified(); err != nil {
		return SequentialResult{}, err
	}
	if err := validateSequentialPolicy(policy); err != nil {
		return SequentialResult{}, err
	}
	if _, exists := curve.pass(PassConfirmatory); exists {
		return SequentialResult{}, errors.New("sequential decision accepts the discovery pass only")
	}
	discovery, exists := curve.pass(PassDiscovery)
	if !exists {
		return SequentialResult{}, errors.New("sequential decision requires a discovery pass")
	}

	result := SequentialResult{
		Status:        SequentialContinue,
		PolicyVersion: policy.Version, MinDelta: policy.MinDelta,
		Patience: policy.Patience, TrialsPerBatch: policy.TrialsPerBatch,
		DivergenceFactor:     policy.DivergenceFactor,
		CurveFamilyName:      curve.FamilyName(),
		IdleBaselinesByState: make(map[string]IdleBaseline),
	}
	best := math.Inf(1)
	bestBatch := 0
	wait := 0
	for index, point := range discovery.points {
		expectedBatch := index + 1
		if point.Batch != expectedBatch || len(point.Trials) != policy.TrialsPerBatch ||
			point.MeanIncrementalJoulesPerItem <= 0 ||
			math.IsNaN(point.MeanIncrementalJoulesPerItem) ||
			math.IsInf(point.MeanIncrementalJoulesPerItem, 0) {
			return SequentialResult{}, errors.New("discovery points do not match the frozen sequential policy")
		}
		result.MeasuredBatches = append(result.MeasuredBatches, point.Batch)
		result.EpsilonByBatch = append(result.EpsilonByBatch, SequentialBatchMean{
			Batch: point.Batch, IncrementalJoulesPerItem: point.MeanIncrementalJoulesPerItem,
		})
		if baseline, exists := curve.baselines[point.StateIdentity]; exists {
			result.IdleBaselinesByState[point.StateIdentity] = cloneIdleBaseline(baseline)
		} else {
			return SequentialResult{}, errors.New("discovery point lost its exact-state idle baseline")
		}

		value := point.MeanIncrementalJoulesPerItem
		if bestBatch == 0 || value < best*(1-policy.MinDelta) {
			best = value
			bestBatch = point.Batch
			wait = 0
			continue
		}
		wait++
		if wait >= policy.Patience {
			result.Status = SequentialStopped
			result.StopReason = StopNoFurtherImprovement
		} else if value > best*policy.DivergenceFactor {
			result.Status = SequentialStopped
			result.StopReason = StopEnergyDivergence
		}
		if result.Status == SequentialStopped && index != len(discovery.points)-1 {
			return SequentialResult{}, errors.New("discovery curve contains points after its stopping condition")
		}
	}

	if bestBatch != 0 {
		selected := bestBatch
		bestValue := best
		result.SelectedBatch = &selected
		result.BestIncrementalJoulesPerItem = &bestValue
	}
	if result.Status == SequentialStopped && discovery.capabilityBoundary != nil {
		return SequentialResult{}, errors.New("discovery attempted a capability boundary after its stopping condition")
	}
	if result.Status != SequentialStopped && discovery.capabilityBoundary != nil {
		expectedBoundary := len(discovery.points) + 1
		if *discovery.capabilityBoundary != expectedBoundary {
			return SequentialResult{}, errors.New("capability boundary does not follow the measured prefix")
		}
		boundary := *discovery.capabilityBoundary
		result.Status = SequentialStopped
		result.StopReason = StopCapabilityBoundary
		result.BoundaryBatch = &boundary
	}
	return result, nil
}

func validateSequentialPolicy(policy SequentialPolicy) error {
	if policy.Version == "" || policy.MinDelta < 0 || policy.MinDelta >= 1 ||
		math.IsNaN(policy.MinDelta) || math.IsInf(policy.MinDelta, 0) ||
		policy.Patience < 1 || policy.TrialsPerBatch < 3 ||
		policy.DivergenceFactor <= 1 || math.IsNaN(policy.DivergenceFactor) ||
		math.IsInf(policy.DivergenceFactor, 0) {
		return errors.New("sequential policy is invalid")
	}
	return nil
}
