package profilederivation

import (
	"errors"
	"math"
	"reflect"
)

// DeriveBSatConfirmatory evaluates the frozen domain-wide acceptance rule on
// the confirmatory pass of one opaque Curve bound to an exact verified plan.
func DeriveBSatConfirmatory(
	curve Curve,
	candidate int,
	policy ConfirmatoryDerivationPolicy,
) (BSatResult, error) {
	discoveryPolicy, bsat, err := validateConfirmatoryPolicyCurve(curve, policy)
	if err != nil {
		return BSatResult{}, err
	}
	if candidate != policy.CandidateBatch() {
		return BSatResult{}, errors.New("confirmatory candidate does not match the frozen policy")
	}
	discoveryCurve, err := curve.discoveryOnly()
	if err != nil {
		return BSatResult{}, err
	}
	sizing, err := deriveBSatConfirmatorySizing(discoveryCurve, candidate, bsat)
	if err != nil {
		return BSatResult{}, err
	}
	expectedPolicy, err := FreezeConfirmatoryDerivationPolicy(discoveryPolicy, sizing)
	if err != nil {
		return BSatResult{}, err
	}
	if !reflect.DeepEqual(expectedPolicy.record, policy.record) {
		return BSatResult{}, errors.New("confirmatory policy does not match the curve's realized sizing")
	}
	result, err := deriveBSatConfirmatory(curve, candidate, bsat)
	if err != nil {
		return BSatResult{}, err
	}
	result.PolicyName = policy.Name()
	return result, nil
}

func deriveBSatConfirmatory(
	curve Curve,
	candidate int,
	policy BSatPolicy,
) (BSatResult, error) {
	if err := curve.plan.requireVerified(); err != nil {
		return BSatResult{}, err
	}
	discoveryCurve, err := curve.discoveryOnly()
	if err != nil {
		return BSatResult{}, err
	}
	discoveryResult, err := deriveBSatDiscovery(discoveryCurve, policy)
	if err != nil {
		return BSatResult{}, err
	}
	sizing, err := deriveBSatConfirmatorySizing(discoveryCurve, candidate, policy)
	if err != nil {
		return BSatResult{}, err
	}
	discovery, _ := discoveryCurve.pass(PassDiscovery)
	result := newBSatResult(curve, discovery, discoveryResult, sizing, policy)
	if sizing.ConfirmatoryTrials == nil {
		return result, nil
	}
	confirmation, exists := curve.pass(PassConfirmatory)
	if !exists {
		return BSatResult{}, errors.New("confirmatory decision requires a confirmatory pass")
	}
	result.ConfirmatoryPoints = make([]Point, len(confirmation.points))
	for index, point := range confirmation.points {
		result.ConfirmatoryPoints[index] = clonePoint(point)
		result.ConfirmatoryMeasuredBatchSizes = append(result.ConfirmatoryMeasuredBatchSizes, point.Batch)
	}
	if err := validateConfirmatoryPass(
		confirmation, candidate, policy.KMax, *sizing.ConfirmatoryTrials,
	); err != nil {
		return BSatResult{}, err
	}
	if confirmation.capabilityBoundary != nil && *confirmation.capabilityBoundary <= policy.KMax {
		boundary := *confirmation.capabilityBoundary
		result.Status = BSatCensored
		result.Code = BSatCodeCapabilityBoundary
		result.BoundaryBatch = &boundary
		return result, nil
	}

	reference := confirmation.points[0]
	referenceBaseline := curve.baselines[reference.StateIdentity]
	referenceHalfWidth, err := pointHalfWidthForTrials(
		reference, referenceBaseline, *sizing.ConfirmatoryTrials, sizing.OneSidedQuantile,
	)
	if err != nil {
		return BSatResult{}, err
	}
	referenceLowerBound := reference.MeanIncrementalJoulesPerItem - referenceHalfWidth
	referenceLowerBoundCopy := referenceLowerBound
	result.ReferenceLowerBoundJoulesPerItem = &referenceLowerBoundCopy
	result.ContrastCovariances, err = confirmatoryContrastCovariances(
		confirmation.points, curve.baselines, *sizing.ConfirmatoryTrials,
	)
	if err != nil {
		return BSatResult{}, err
	}
	if referenceLowerBound <= 0 {
		result.Status = BSatUnavailable
		result.Code = BSatCodeNonPositiveReferenceLowerBound
		return result, nil
	}

	var maximumRemainingImprovement float64
	for index := 1; index < len(confirmation.points); index++ {
		later := confirmation.points[index]
		laterBaseline := curve.baselines[later.StateIdentity]
		halfWidth, err := contrastHalfWidthForTrials(
			reference, referenceBaseline, later, laterBaseline,
			*sizing.ConfirmatoryTrials, sizing.OneSidedQuantile,
		)
		if err != nil {
			return BSatResult{}, err
		}
		difference := reference.MeanIncrementalJoulesPerItem - later.MeanIncrementalJoulesPerItem
		upperDifference := difference + halfWidth
		relativeUpperBound := 0.0
		if upperDifference > 0 {
			relativeUpperBound = upperDifference / referenceLowerBound
		}
		maximumRemainingImprovement = math.Max(maximumRemainingImprovement, relativeUpperBound)
		result.ContrastBounds = append(result.ContrastBounds, BSatContrastBound{
			LaterBatch: later.Batch, DifferenceEstimateJoulesPerItem: difference,
			DifferenceUpperBoundJoulesPerItem: upperDifference,
			RelativeImprovementUpperBound:     relativeUpperBound,
			HalfWidthJoulesPerItem:            halfWidth,
		})
	}
	maximumRemainingImprovementCopy := maximumRemainingImprovement
	result.MaximumRemainingImprovementUpperBound = &maximumRemainingImprovementCopy
	if maximumRemainingImprovement <= policy.RelativeImprovementTolerance {
		result.Status = BSatAccepted
		result.Code = BSatCodeDomainNoImprovementConfirmed
		result.DomainNoImprovementConfirmed = true
		return result, nil
	}
	result.Status = BSatCandidateOnly
	result.Code = BSatCodeDomainNoImprovementNotConfirmed
	return result, nil
}

func confirmatoryContrastCovariances(
	points []Point,
	baselines map[string]IdleBaseline,
	trials int,
) ([]BSatContrastCovariance, error) {
	reference := points[0]
	result := make([]BSatContrastCovariance, 0, len(points)*(len(points)-1)/2)
	for firstIndex := 1; firstIndex < len(points); firstIndex++ {
		first := points[firstIndex]
		firstBaselineCoefficients := contrastBaselineCoefficients(reference, first)
		for secondIndex := firstIndex; secondIndex < len(points); secondIndex++ {
			second := points[secondIndex]
			secondBaselineCoefficients := contrastBaselineCoefficients(reference, second)
			covariance := reference.IncrementalSampleVariance / float64(trials)
			if first.Batch == second.Batch {
				covariance += first.IncrementalSampleVariance / float64(trials)
			}
			for state, firstCoefficient := range firstBaselineCoefficients {
				baseline, exists := baselines[state]
				if !exists {
					return nil, errors.New("contrast covariance lost an exact-state baseline")
				}
				covariance += firstCoefficient * secondBaselineCoefficients[state] *
					baseline.MeanWallWattsVariance
			}
			if math.IsNaN(covariance) || math.IsInf(covariance, 0) {
				return nil, errors.New("contrast covariance exceeds the supported numeric range")
			}
			result = append(result, BSatContrastCovariance{
				FirstLaterBatch: first.Batch, SecondLaterBatch: second.Batch,
				CovarianceJoulesSquaredPerItemSquared: covariance,
			})
		}
	}
	return result, nil
}

func contrastBaselineCoefficients(reference Point, later Point) map[string]float64 {
	coefficients := map[string]float64{
		reference.StateIdentity: -reference.MeanBaselineCoefficientSecondsPerItem,
	}
	coefficients[later.StateIdentity] += later.MeanBaselineCoefficientSecondsPerItem
	return coefficients
}

func newBSatResult(
	curve Curve,
	discovery curvePass,
	discoveryResult BSatDiscoveryResult,
	sizing BSatSizingResult,
	policy BSatPolicy,
) BSatResult {
	result := BSatResult{
		Status: sizing.Status, Code: sizing.Code, DiscoverySelectedBatch: sizing.CandidateBatch,
		MinimalityConfidenceConfirmed: false,
		IdleBaselinesByState:          curve.IdleBaselinesByState(),
		GlobalStatementCount:          sizing.GlobalStatementCount,
		OneSidedQuantile:              sizing.OneSidedQuantile,
		RelativeImprovementTolerance:  policy.RelativeImprovementTolerance,
		PointEstimatorVersion:         PointEstimatorVersion,
		ConfirmatoryEstimatorVersion:  BSatConfirmatoryEstimatorVersion,
		ConfidenceLevel:               1 - policy.FamilyWiseAlpha,
		SizingMethod:                  sizing.SizingMethod,
		CurvePlanName:                 curve.PlanName(),
	}
	if sizing.ConfirmatoryTrials != nil {
		result.ConfirmatoryTrials = *sizing.ConfirmatoryTrials
	}
	result.DiscoveryPoints = make([]Point, len(discovery.points))
	for index, point := range discovery.points {
		result.DiscoveryPoints[index] = clonePoint(point)
	}
	result.DiscoveryMeasuredBatchSizes = append(
		[]int(nil), discoveryResult.MeasuredBatchSizes...,
	)
	return result
}

func validateConfirmatoryPass(
	confirmation curvePass,
	candidate int,
	kmax int,
	confirmatoryTrials int,
) error {
	for index, point := range confirmation.points {
		expectedBatch := candidate + index
		if point.Batch != expectedBatch || len(point.Trials) != confirmatoryTrials ||
			point.Confidence.EstimatorVersion != PointEstimatorVersion {
			return errors.New("confirmatory points do not match the frozen allocation")
		}
	}
	expectedPointCount := kmax - candidate + 1
	if confirmation.capabilityBoundary != nil {
		expectedBoundary := candidate + len(confirmation.points)
		if *confirmation.capabilityBoundary != expectedBoundary {
			return errors.New("confirmatory capability boundary is not the next allocated batch")
		}
		if *confirmation.capabilityBoundary <= kmax {
			return nil
		}
	}
	if len(confirmation.points) != expectedPointCount {
		return errors.New("confirmatory pass does not cover the allocated domain")
	}
	return nil
}
