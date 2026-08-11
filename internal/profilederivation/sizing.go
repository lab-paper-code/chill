package profilederivation

import (
	"errors"
	"fmt"
	"math"
)

// DeriveBSatConfirmatorySizing selects the smallest common confirmatory trial
// count that meets the frozen Plan 5 precision rule. The candidate must be the
// deterministic output of the same discovery Curve and policy.
func DeriveBSatConfirmatorySizing(
	curve Curve,
	candidate int,
	policy DiscoveryDerivationPolicy,
) (BSatSizingResult, error) {
	bsat, err := validateDiscoveryPolicyCurve(curve, policy)
	if err != nil {
		return BSatSizingResult{}, err
	}
	return deriveBSatConfirmatorySizing(curve, candidate, bsat)
}

func deriveBSatConfirmatorySizing(
	curve Curve,
	candidate int,
	policy BSatPolicy,
) (BSatSizingResult, error) {
	discoveryResult, err := deriveBSatDiscovery(curve, policy)
	if err != nil {
		return BSatSizingResult{}, err
	}
	if discoveryResult.CandidateBatch == nil || *discoveryResult.CandidateBatch != candidate {
		return BSatSizingResult{}, errors.New("confirmatory sizing candidate does not match discovery")
	}
	if err := validateConfirmatoryEligibleSpan(curve, candidate, policy); err != nil {
		return BSatSizingResult{}, err
	}
	discovery, exists := curve.pass(PassDiscovery)
	if !exists {
		return BSatSizingResult{}, errors.New("confirmatory sizing requires a discovery pass")
	}
	statementCount, err := bsatGlobalStatementCount(policy)
	if err != nil {
		return BSatSizingResult{}, err
	}
	maximumTrials := bsatMaximumFeasibleConfirmatoryTrials(policy, candidate)
	result := BSatSizingResult{
		Status: BSatUnavailable, Code: BSatCodeConfirmatoryBudgetInsufficient,
		CandidateBatch: candidate, MaximumFeasibleConfirmatoryTrials: maximumTrials,
		GlobalStatementCount: statementCount, SizingMethod: BSatConfirmatorySizingMethod,
		PolicyVersion: policy.Version, CurveFamilyName: curve.FamilyName(),
	}
	for batch := candidate; batch <= policy.KMax; batch++ {
		result.ConfirmatoryBatches = append(result.ConfirmatoryBatches, batch)
	}
	if statementCount == 0 || maximumTrials < 3 {
		return result, nil
	}
	quantile := 1 - policy.FamilyWiseAlpha/float64(statementCount)
	if quantile <= 0 || quantile >= 1 || math.IsNaN(quantile) {
		return BSatSizingResult{}, errors.New("global statement family exceeds the supported numeric range")
	}
	result.OneSidedQuantile = quantile
	for trials := policy.MinimumPointTrials; trials <= maximumTrials; trials++ {
		lowerBound, maximumRelativeHalfWidth, err := projectedBSatPrecision(
			discovery.points, curve.baselines, candidate, trials, quantile,
		)
		if err != nil {
			return BSatSizingResult{}, err
		}
		if lowerBound <= 0 || maximumRelativeHalfWidth > policy.RelativeImprovementTolerance {
			continue
		}
		trialCount := trials
		totalRuns := policy.KMax*policy.DiscoveryTrials +
			(policy.KMax-candidate+1)*trials
		lowerBoundCopy := lowerBound
		maximumRelativeHalfWidthCopy := maximumRelativeHalfWidth
		result.Status = BSatCandidateOnly
		result.Code = BSatCodeConfirmatoryTrialsSized
		result.ConfirmatoryTrials = &trialCount
		result.ProjectedReferenceLowerBound = &lowerBoundCopy
		result.ProjectedMaximumRelativeHalfWidth = &maximumRelativeHalfWidthCopy
		result.ProjectedTotalActiveRuns = &totalRuns
		return result, nil
	}
	return result, nil
}

func validateConfirmatoryEligibleSpan(curve Curve, candidate int, policy BSatPolicy) error {
	expected := candidate
	for _, batch := range curve.family.contract.record.Shared.EligibleBatches {
		if batch < candidate {
			continue
		}
		if batch != expected {
			break
		}
		if batch == policy.KMax {
			return nil
		}
		expected++
	}
	return fmt.Errorf(
		"confirmatory method %q requires consecutive eligible batches from candidate %d through KMax %d; gap at batch %d",
		BSatConfirmatorySizingMethod, candidate, policy.KMax, expected,
	)
}

func bsatGlobalStatementCount(policy BSatPolicy) (int, error) {
	var total int
	for candidate := 1; candidate < policy.KMax; candidate++ {
		maximumTrials := bsatMaximumFeasibleConfirmatoryTrials(policy, candidate)
		if maximumTrials < policy.MinimumPointTrials {
			continue
		}
		trialChoices := maximumTrials - policy.MinimumPointTrials + 1
		statements := policy.KMax - candidate + 1
		if trialChoices > math.MaxInt/statements {
			return 0, errors.New("global statement count overflows int")
		}
		candidateStatements := trialChoices * statements
		if total > math.MaxInt-candidateStatements {
			return 0, errors.New("global statement count overflows int")
		}
		total += candidateStatements
	}
	return total, nil
}

func bsatMaximumFeasibleConfirmatoryTrials(policy BSatPolicy, candidate int) int {
	remainingBudget := policy.MaximumActiveRunBudget - policy.KMax*policy.DiscoveryTrials
	confirmatoryBatches := policy.KMax - candidate + 1
	budgetMaximum := remainingBudget / confirmatoryBatches
	return min(policy.MaximumConfirmatoryTrials, budgetMaximum)
}

func projectedBSatPrecision(
	points []Point,
	baselines map[string]IdleBaseline,
	candidate int,
	trials int,
	quantile float64,
) (float64, float64, error) {
	reference := points[candidate-1]
	referenceBaseline, exists := baselines[reference.StateIdentity]
	if !exists {
		return 0, 0, errors.New("reference point lost its exact-state baseline")
	}
	referenceHalfWidth, err := pointHalfWidthForTrials(reference, referenceBaseline, trials, quantile)
	if err != nil {
		return 0, 0, err
	}
	lowerBound := reference.MeanIncrementalJoulesPerItem - referenceHalfWidth
	if lowerBound <= 0 {
		return lowerBound, math.Inf(1), nil
	}
	var maximumRelativeHalfWidth float64
	for later := candidate; later < len(points); later++ {
		laterPoint := points[later]
		laterBaseline, exists := baselines[laterPoint.StateIdentity]
		if !exists {
			return 0, 0, errors.New("later point lost its exact-state baseline")
		}
		halfWidth, err := contrastHalfWidthForTrials(
			reference, referenceBaseline, laterPoint, laterBaseline, trials, quantile,
		)
		if err != nil {
			return 0, 0, err
		}
		maximumRelativeHalfWidth = math.Max(maximumRelativeHalfWidth, halfWidth/lowerBound)
	}
	return lowerBound, maximumRelativeHalfWidth, nil
}

func pointHalfWidthForTrials(
	point Point,
	baseline IdleBaseline,
	trials int,
	quantile float64,
) (float64, error) {
	return projectedHalfWidth(quantile,
		varianceComponent{
			variance:         point.IncrementalSampleVariance / float64(trials),
			degreesOfFreedom: float64(trials - 1),
		},
		varianceComponent{
			variance: math.Pow(point.MeanBaselineCoefficientSecondsPerItem, 2) *
				baseline.MeanWallWattsVariance,
			degreesOfFreedom: float64(baseline.DegreesOfFreedom),
		},
	)
}

func contrastHalfWidthForTrials(
	reference Point,
	referenceBaseline IdleBaseline,
	later Point,
	laterBaseline IdleBaseline,
	trials int,
	quantile float64,
) (float64, error) {
	components := []varianceComponent{
		{
			variance:         reference.IncrementalSampleVariance / float64(trials),
			degreesOfFreedom: float64(trials - 1),
		},
		{
			variance:         later.IncrementalSampleVariance / float64(trials),
			degreesOfFreedom: float64(trials - 1),
		},
	}
	if reference.StateIdentity == later.StateIdentity {
		components = append(components, varianceComponent{
			variance: math.Pow(
				reference.MeanBaselineCoefficientSecondsPerItem-
					later.MeanBaselineCoefficientSecondsPerItem,
				2,
			) * referenceBaseline.MeanWallWattsVariance,
			degreesOfFreedom: float64(referenceBaseline.DegreesOfFreedom),
		})
	} else {
		components = append(components,
			varianceComponent{
				variance: math.Pow(reference.MeanBaselineCoefficientSecondsPerItem, 2) *
					referenceBaseline.MeanWallWattsVariance,
				degreesOfFreedom: float64(referenceBaseline.DegreesOfFreedom),
			},
			varianceComponent{
				variance: math.Pow(later.MeanBaselineCoefficientSecondsPerItem, 2) *
					laterBaseline.MeanWallWattsVariance,
				degreesOfFreedom: float64(laterBaseline.DegreesOfFreedom),
			},
		)
	}
	return projectedHalfWidth(quantile, components...)
}

func projectedHalfWidth(quantile float64, components ...varianceComponent) (float64, error) {
	variance, degreesOfFreedom, err := welchSatterthwaite(components...)
	if err != nil {
		return 0, err
	}
	if degreesOfFreedom == nil {
		return 0, nil
	}
	halfWidth := studentTQuantile(quantile, *degreesOfFreedom) * math.Sqrt(variance)
	if math.IsNaN(halfWidth) || math.IsInf(halfWidth, 0) {
		return 0, errors.New("projected half-width exceeds the supported numeric range")
	}
	return halfWidth, nil
}
