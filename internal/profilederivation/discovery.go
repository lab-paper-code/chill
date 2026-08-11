package profilederivation

import (
	"errors"
	"math"
)

// DeriveBSatDiscovery deterministically screens the discovery pass of one
// Curve bound to an exact verified plan. It proposes a candidate but makes no
// confidence claim.
func DeriveBSatDiscovery(
	curve Curve,
	policy DiscoveryDerivationPolicy,
) (BSatDiscoveryResult, error) {
	bsat, err := validateDiscoveryPolicyCurve(curve, policy)
	if err != nil {
		return BSatDiscoveryResult{}, err
	}
	return deriveBSatDiscovery(curve, bsat)
}

func deriveBSatDiscovery(curve Curve, policy BSatPolicy) (BSatDiscoveryResult, error) {
	if err := curve.plan.requireVerified(); err != nil {
		return BSatDiscoveryResult{}, err
	}
	if err := validateBSatPolicy(policy); err != nil {
		return BSatDiscoveryResult{}, err
	}
	plannedBatchSizes := curve.plan.recorded.record.Shared.PlannedBatchSizes
	if len(plannedBatchSizes) < 2 || plannedBatchSizes[len(plannedBatchSizes)-1] != policy.KMax {
		return BSatDiscoveryResult{}, errors.New("curve plan does not match policy kmax")
	}
	if _, exists := curve.pass(PassConfirmatory); exists {
		return BSatDiscoveryResult{}, errors.New("discovery screen accepts the discovery pass only")
	}
	discovery, exists := curve.pass(PassDiscovery)
	if !exists {
		return BSatDiscoveryResult{}, errors.New("discovery screen requires a discovery pass")
	}

	result := BSatDiscoveryResult{
		Status: BSatCandidateOnly, Code: BSatCodeDiscoveryCandidateSelected,
		PolicyVersion: policy.Version, KMax: policy.KMax,
		RelativeImprovementTolerance: policy.RelativeImprovementTolerance,
		DiscoveryTrials:              policy.DiscoveryTrials,
		CurvePlanName:                curve.PlanName(),
	}
	for index, point := range discovery.points {
		expectedBatch := plannedBatchSizes[index]
		if point.Batch != expectedBatch || len(point.Trials) != policy.DiscoveryTrials ||
			point.MeanIncrementalWorkJoulesPerItem <= 0 ||
			math.IsNaN(point.MeanIncrementalWorkJoulesPerItem) ||
			math.IsInf(point.MeanIncrementalWorkJoulesPerItem, 0) {
			return BSatDiscoveryResult{}, errors.New("discovery points do not match the frozen policy")
		}
		result.MeasuredBatchSizes = append(result.MeasuredBatchSizes, point.Batch)
	}
	if discovery.capabilityBoundary != nil && *discovery.capabilityBoundary <= policy.KMax {
		boundary := *discovery.capabilityBoundary
		result.Status = BSatCensored
		result.Code = BSatCodeCapabilityBoundary
		result.BoundaryBatch = &boundary
		return result, nil
	}
	if len(discovery.points) != len(plannedBatchSizes) {
		return BSatDiscoveryResult{}, errors.New("discovery pass does not cover kmax")
	}

	for candidate := 0; candidate < len(discovery.points)-1; candidate++ {
		candidateEnergy := discovery.points[candidate].MeanIncrementalWorkJoulesPerItem
		maximumImprovement := math.Inf(-1)
		for later := candidate + 1; later < len(discovery.points); later++ {
			laterEnergy := discovery.points[later].MeanIncrementalWorkJoulesPerItem
			improvement := (candidateEnergy - laterEnergy) / candidateEnergy
			maximumImprovement = math.Max(maximumImprovement, improvement)
		}
		if maximumImprovement <= policy.RelativeImprovementTolerance {
			batch := discovery.points[candidate].Batch
			result.CandidateBatch = &batch
			result.CandidateMaximumRemainingImprovement = &maximumImprovement
			return result, nil
		}
	}
	result.Status = BSatCensored
	result.Code = BSatCodeNoSaturationObserved
	return result, nil
}
