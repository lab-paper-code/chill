package profilederivation

import (
	"errors"
	"math"
)

// DeriveBSatDiscovery deterministically screens the discovery pass of one
// exact-family Curve. It proposes a candidate but makes no confidence claim.
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
	if err := curve.family.requireVerified(); err != nil {
		return BSatDiscoveryResult{}, err
	}
	if err := validateBSatPolicy(policy); err != nil {
		return BSatDiscoveryResult{}, err
	}
	if len(curve.family.members) != policy.KMax {
		return BSatDiscoveryResult{}, errors.New("curve family does not match policy kmax")
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
		CurveFamilyContractDigest:    curve.ContractDigest(),
		CurveFamilyIdentity:          curve.FamilyIdentity(),
	}
	for index, point := range discovery.points {
		expectedBatch := index + 1
		if point.Batch != expectedBatch || len(point.Trials) != policy.DiscoveryTrials ||
			point.MeanIncrementalJoulesPerItem <= 0 ||
			math.IsNaN(point.MeanIncrementalJoulesPerItem) ||
			math.IsInf(point.MeanIncrementalJoulesPerItem, 0) {
			return BSatDiscoveryResult{}, errors.New("discovery points do not match the frozen policy")
		}
		result.MeasuredBatches = append(result.MeasuredBatches, point.Batch)
	}
	if discovery.capabilityBoundary != nil && *discovery.capabilityBoundary <= policy.KMax {
		boundary := *discovery.capabilityBoundary
		result.Status = BSatCensored
		result.Code = BSatCodeCapabilityBoundary
		result.BoundaryBatch = &boundary
		return result, nil
	}
	if len(discovery.points) != policy.KMax {
		return BSatDiscoveryResult{}, errors.New("discovery pass does not cover kmax")
	}

	for candidate := 0; candidate < policy.KMax-1; candidate++ {
		candidateEnergy := discovery.points[candidate].MeanIncrementalJoulesPerItem
		maximumImprovement := math.Inf(-1)
		for later := candidate + 1; later < policy.KMax; later++ {
			laterEnergy := discovery.points[later].MeanIncrementalJoulesPerItem
			improvement := (candidateEnergy - laterEnergy) / candidateEnergy
			maximumImprovement = math.Max(maximumImprovement, improvement)
		}
		if maximumImprovement <= policy.RelativeImprovementTolerance {
			batch := candidate + 1
			result.CandidateBatch = &batch
			result.CandidateMaximumRemainingImprovement = &maximumImprovement
			return result, nil
		}
	}
	result.Status = BSatCensored
	result.Code = BSatCodeNoSaturationObserved
	return result, nil
}
