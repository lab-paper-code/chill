package profilederivation

import (
	"errors"
	"math"
	"sort"

	"gonum.org/v1/gonum/stat"
)

func DeriveIdleBaseline(trials []IdleTrial, policy PointPolicy) (IdleBaseline, error) {
	if err := validatePointPolicy(policy); err != nil || len(trials) < policy.MinimumTrials {
		return IdleBaseline{}, errors.New("idle trial count or policy is insufficient")
	}
	baseline := IdleBaseline{
		StateIdentity:               trials[0].StateIdentity,
		EnvironmentIdentity:         trials[0].EnvironmentIdentity,
		MeasurementProtocolIdentity: trials[0].MeasurementProtocolIdentity,
		Trials:                      append([]IdleTrial(nil), trials...),
		PolicyVersion:               policy.Version,
		AdmissionPolicyVersion:      trials[0].AdmissionPolicyVersion,
	}
	values := make([]float64, len(trials))
	seen := map[string]struct{}{}
	seenEvidence := map[string]struct{}{}
	admissionVersion := trials[0].AdmissionPolicyVersion
	for index, trial := range trials {
		if trial.StateIdentity != baseline.StateIdentity ||
			trial.EnvironmentIdentity != baseline.EnvironmentIdentity ||
			trial.MeasurementProtocolIdentity != baseline.MeasurementProtocolIdentity ||
			trial.TrialID == "" ||
			trial.AdmissionPolicyVersion != admissionVersion ||
			admissionVersion == "" ||
			!validRawEvidenceIdentity(trial.RawEvidence) ||
			trial.MeanWallWatts < 0 ||
			math.IsNaN(trial.MeanWallWatts) ||
			math.IsInf(trial.MeanWallWatts, 0) {
			return IdleBaseline{}, errors.New("idle trial identity or value is invalid")
		}
		if _, exists := seen[trial.TrialID]; exists {
			return IdleBaseline{}, errors.New("duplicate idle trial ID")
		}
		seen[trial.TrialID] = struct{}{}
		evidenceKey := rawEvidenceIdentityKey(trial.RawEvidence)
		if _, exists := seenEvidence[evidenceKey]; exists {
			return IdleBaseline{}, errors.New("duplicate idle raw evidence identity")
		}
		seenEvidence[evidenceKey] = struct{}{}
		values[index] = trial.MeanWallWatts
	}
	baseline.MeanWallWatts, baseline.MeanWallWattsVariance = stat.MeanVariance(values, nil)
	baseline.MeanWallWattsVariance /= float64(len(values))
	baseline.DegreesOfFreedom = len(values) - 1
	baseline.ConfidenceHalfWidth = studentT95(baseline.DegreesOfFreedom) *
		math.Sqrt(baseline.MeanWallWattsVariance)
	return baseline, nil
}

func validateIdleBaseline(baseline IdleBaseline) error {
	if baseline.StateIdentity == "" || baseline.EnvironmentIdentity == "" ||
		baseline.MeasurementProtocolIdentity == "" || baseline.PolicyVersion == "" ||
		baseline.AdmissionPolicyVersion == "" || len(baseline.Trials) < 3 ||
		baseline.MeanWallWatts < 0 || math.IsNaN(baseline.MeanWallWatts) ||
		math.IsInf(baseline.MeanWallWatts, 0) || baseline.MeanWallWattsVariance < 0 ||
		math.IsNaN(baseline.MeanWallWattsVariance) || math.IsInf(baseline.MeanWallWattsVariance, 0) ||
		baseline.DegreesOfFreedom != len(baseline.Trials)-1 || baseline.ConfidenceHalfWidth < 0 ||
		math.IsNaN(baseline.ConfidenceHalfWidth) || math.IsInf(baseline.ConfidenceHalfWidth, 0) {
		return errors.New("idle baseline value or identity is invalid")
	}
	expectedHalfWidth := studentT95(baseline.DegreesOfFreedom) * math.Sqrt(baseline.MeanWallWattsVariance)
	if math.Abs(baseline.ConfidenceHalfWidth-expectedHalfWidth) >
		1e-12*math.Max(1, expectedHalfWidth) {
		return errors.New("idle baseline uncertainty fields are inconsistent")
	}
	seenTrials := make(map[string]struct{}, len(baseline.Trials))
	seenEvidence := make(map[string]struct{}, len(baseline.Trials))
	for _, trial := range baseline.Trials {
		if trial.StateIdentity != baseline.StateIdentity ||
			trial.EnvironmentIdentity != baseline.EnvironmentIdentity ||
			trial.MeasurementProtocolIdentity != baseline.MeasurementProtocolIdentity ||
			trial.AdmissionPolicyVersion != baseline.AdmissionPolicyVersion || trial.TrialID == "" ||
			!validRawEvidenceIdentity(trial.RawEvidence) || trial.MeanWallWatts < 0 ||
			math.IsNaN(trial.MeanWallWatts) || math.IsInf(trial.MeanWallWatts, 0) {
			return errors.New("idle baseline trial is invalid")
		}
		if _, exists := seenTrials[trial.TrialID]; exists {
			return errors.New("idle baseline contains a duplicate trial")
		}
		seenTrials[trial.TrialID] = struct{}{}
		evidenceKey := rawEvidenceIdentityKey(trial.RawEvidence)
		if _, exists := seenEvidence[evidenceKey]; exists {
			return errors.New("idle baseline contains duplicate raw evidence")
		}
		seenEvidence[evidenceKey] = struct{}{}
	}
	return nil
}

func IncrementalTrial(trial Trial, admission AdmissionResult, baseline IdleBaseline) (AdmittedTrial, error) {
	if admission.Verdict != VerdictAccepted || admission.TotalWallJoules == nil {
		return AdmittedTrial{}, errors.New("trial is not admitted")
	}
	if admission.StateIdentity != trial.StateIdentity ||
		admission.EnvironmentIdentity != trial.EnvironmentIdentity ||
		admission.MeasurementProtocolIdentity != trial.MeasurementProtocolIdentity ||
		admission.TrialID != trial.TrialID ||
		admission.PolicyVersion == "" ||
		admission.CompletedItems != trial.CompletedItems ||
		trial.CompletedItems <= 0 ||
		admission.WindowSeconds <= 0 ||
		math.IsNaN(admission.WindowSeconds) ||
		math.IsInf(admission.WindowSeconds, 0) ||
		*admission.TotalWallJoules < 0 ||
		math.IsNaN(*admission.TotalWallJoules) ||
		math.IsInf(*admission.TotalWallJoules, 0) ||
		baseline.StateIdentity != trial.StateIdentity ||
		baseline.EnvironmentIdentity != trial.EnvironmentIdentity ||
		baseline.MeasurementProtocolIdentity == "" {
		return AdmittedTrial{}, errors.New("admitted idle baseline identity is invalid")
	}
	if err := validateIdleBaseline(baseline); err != nil {
		return AdmittedTrial{}, errors.New("admitted idle baseline is invalid")
	}
	incremental := *admission.TotalWallJoules - baseline.MeanWallWatts*admission.WindowSeconds
	if incremental < 0 || math.IsNaN(incremental) || math.IsInf(incremental, 0) {
		return AdmittedTrial{}, errors.New("incremental energy is invalid")
	}
	completedItems := float64(trial.CompletedItems)
	activeWallJoulesPerItem := *admission.TotalWallJoules / completedItems
	baselineCoefficient := admission.WindowSeconds / completedItems
	baselineUncertainty := baseline.ConfidenceHalfWidth * baselineCoefficient
	estimatorIdentity := "state=" + trial.StateIdentity + "|environment=" + trial.EnvironmentIdentity +
		"|activeProtocol=" + trial.MeasurementProtocolIdentity +
		"|idleProtocol=" + baseline.MeasurementProtocolIdentity +
		"|activeAdmission=" + admission.PolicyVersion +
		"|idleAdmission=" + baseline.AdmissionPolicyVersion + "|idlePoint=" + baseline.PolicyVersion
	return AdmittedTrial{
		StateIdentity:                     trial.StateIdentity,
		EnvironmentIdentity:               trial.EnvironmentIdentity,
		MeasurementProtocolIdentity:       trial.MeasurementProtocolIdentity,
		TrialID:                           trial.TrialID,
		AdmissionPolicyVersion:            admission.PolicyVersion,
		EstimatorIdentity:                 estimatorIdentity,
		BaselineUncertaintyJoulesPerItem:  baselineUncertainty,
		BaselineMeanWallWattsVariance:     baseline.MeanWallWattsVariance,
		BaselineDegreesOfFreedom:          baseline.DegreesOfFreedom,
		ActiveWallJoulesPerItem:           activeWallJoulesPerItem,
		BaselineCoefficientSecondsPerItem: baselineCoefficient,
		Batch:                             trial.Batch,
		IncrementalJoulesPerItem:          incremental / completedItems,
		LatencyP99Milliseconds:            percentile(trial.LatencyMilliseconds, 0.99),
	}, nil
}

func Aggregate(trials []AdmittedTrial, policy PointPolicy) (Point, error) {
	if err := validatePointPolicy(policy); err != nil {
		return Point{}, errors.New("point policy is invalid")
	}
	if len(trials) < policy.MinimumTrials {
		return Point{}, errors.New("independent trial count is below policy")
	}
	point := Point{
		StateIdentity:               trials[0].StateIdentity,
		EnvironmentIdentity:         trials[0].EnvironmentIdentity,
		MeasurementProtocolIdentity: trials[0].MeasurementProtocolIdentity,
		EstimatorIdentity:           trials[0].EstimatorIdentity,
		Batch:                       trials[0].Batch,
		PointPolicyVersion:          policy.Version,
		Trials:                      append([]AdmittedTrial(nil), trials...),
	}
	values := make([]float64, len(trials))
	coefficients := make([]float64, len(trials))
	var latency float64
	seen := map[string]struct{}{}
	admissionVersion := trials[0].AdmissionPolicyVersion
	baselineVariance := trials[0].BaselineMeanWallWattsVariance
	baselineDegreesOfFreedom := trials[0].BaselineDegreesOfFreedom
	var baselineUncertainty float64
	for index, trial := range trials {
		if err := validateAdmittedTrialIdentity(trial, point, admissionVersion); err != nil {
			return Point{}, err
		}
		if _, exists := seen[trial.TrialID]; exists {
			return Point{}, errors.New("duplicate active trial ID")
		}
		if err := validateAdmittedTrialStatistics(trial, baselineVariance, baselineDegreesOfFreedom); err != nil {
			return Point{}, err
		}
		seen[trial.TrialID] = struct{}{}
		values[index] = trial.IncrementalJoulesPerItem
		coefficients[index] = trial.BaselineCoefficientSecondsPerItem
		if trial.BaselineUncertaintyJoulesPerItem > baselineUncertainty {
			baselineUncertainty = trial.BaselineUncertaintyJoulesPerItem
		}
		if trial.LatencyP99Milliseconds > latency {
			latency = trial.LatencyP99Milliseconds
		}
	}
	statistics, err := derivePointStatistics(
		values, coefficients, baselineVariance, baselineDegreesOfFreedom, baselineUncertainty,
	)
	if err != nil {
		return Point{}, err
	}
	point.MeanIncrementalJoulesPerItem = statistics.meanIncrementalJoulesPerItem
	point.IncrementalSampleVariance = statistics.incrementalSampleVariance
	point.MeanBaselineCoefficientSecondsPerItem = statistics.meanBaselineCoefficientSecondsPerItem
	point.Confidence = statistics.confidence
	point.PreviousConfidence = statistics.previousConfidence
	point.LatencyP99Milliseconds = latency
	return point, nil
}

func validateAdmittedTrialIdentity(trial AdmittedTrial, point Point, admissionVersion string) error {
	if trial.StateIdentity != point.StateIdentity ||
		trial.EnvironmentIdentity != point.EnvironmentIdentity ||
		trial.MeasurementProtocolIdentity != point.MeasurementProtocolIdentity ||
		trial.TrialID == "" || trial.AdmissionPolicyVersion != admissionVersion || admissionVersion == "" ||
		trial.EstimatorIdentity != point.EstimatorIdentity || point.EstimatorIdentity == "" ||
		trial.Batch != point.Batch || trial.IncrementalJoulesPerItem <= 0 ||
		math.IsNaN(trial.IncrementalJoulesPerItem) || math.IsInf(trial.IncrementalJoulesPerItem, 0) {
		return errors.New("trial state, environment, protocol, estimator, or batch mismatch")
	}
	return nil
}

func validateAdmittedTrialStatistics(
	trial AdmittedTrial,
	baselineVariance float64,
	baselineDegreesOfFreedom int,
) error {
	if trial.BaselineUncertaintyJoulesPerItem < 0 ||
		math.IsNaN(trial.BaselineUncertaintyJoulesPerItem) ||
		math.IsInf(trial.BaselineUncertaintyJoulesPerItem, 0) ||
		trial.BaselineMeanWallWattsVariance < 0 ||
		math.IsNaN(trial.BaselineMeanWallWattsVariance) ||
		math.IsInf(trial.BaselineMeanWallWattsVariance, 0) ||
		trial.BaselineMeanWallWattsVariance != baselineVariance ||
		trial.BaselineDegreesOfFreedom < 2 || trial.BaselineDegreesOfFreedom != baselineDegreesOfFreedom {
		return errors.New("baseline uncertainty contribution is invalid")
	}
	if trial.ActiveWallJoulesPerItem <= 0 || math.IsNaN(trial.ActiveWallJoulesPerItem) ||
		math.IsInf(trial.ActiveWallJoulesPerItem, 0) ||
		trial.BaselineCoefficientSecondsPerItem <= 0 ||
		math.IsNaN(trial.BaselineCoefficientSecondsPerItem) ||
		math.IsInf(trial.BaselineCoefficientSecondsPerItem, 0) ||
		trial.IncrementalJoulesPerItem > trial.ActiveWallJoulesPerItem {
		return errors.New("trial sufficient statistics are invalid")
	}
	expectedBaselineUncertainty := studentT95(trial.BaselineDegreesOfFreedom) *
		math.Sqrt(trial.BaselineMeanWallWattsVariance) * trial.BaselineCoefficientSecondsPerItem
	if !equalWithinTolerance(trial.BaselineUncertaintyJoulesPerItem, expectedBaselineUncertainty) {
		return errors.New("baseline uncertainty contribution is inconsistent")
	}
	return nil
}

// DeriveBSat retains the pre-Plan-5 adjacent rule and its previous confidence
// construction. The opaque-curve confirmatory boundary owns the rebuilt
// confidence and domain-wide rule.
func DeriveBSat(points []Point, policy LegacyBSatPolicy) LegacyBSatResult {
	result := LegacyBSatResult{Status: BSatUnavailable, PolicyVersion: policy.Version, Code: "InsufficientCurve"}
	if policy.Version == "" || policy.RelativeEquivalenceTolerance < 0 || policy.RelativeEquivalenceTolerance >= 1 ||
		len(points) < 2 {
		return result
	}
	ordered := append([]Point(nil), points...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Batch < ordered[j].Batch })
	identity := ordered[0].StateIdentity
	environmentIdentity := ordered[0].EnvironmentIdentity
	measurementProtocolIdentity := ordered[0].MeasurementProtocolIdentity
	estimatorIdentity := ordered[0].EstimatorIdentity
	pointPolicyVersion := ordered[0].PointPolicyVersion
	confidenceEstimator := ordered[0].PreviousConfidence.EstimatorVersion
	confidenceMethod := ordered[0].PreviousConfidence.Method
	confidenceLevel := ordered[0].PreviousConfidence.Level
	curve := curveIdentity{
		state: identity, environment: environmentIdentity, protocol: measurementProtocolIdentity,
		estimator: estimatorIdentity, pointPolicy: pointPolicyVersion,
		confidenceEstimator: confidenceEstimator, confidenceMethod: confidenceMethod,
		confidenceLevel: confidenceLevel,
	}
	result.StateIdentity = identity
	result.EnvironmentIdentity = environmentIdentity
	result.MeasurementProtocolIdentity = measurementProtocolIdentity
	if ordered[0].Batch != 1 {
		result.Code = "MissingLowerBatchDomain"
		return result
	}
	for index, point := range ordered {
		result.MeasuredBatches = append(result.MeasuredBatches, point.Batch)
		if !validCurvePoint(point, curve) {
			result.Code = "CurveIdentityMismatch"
			return result
		}
		if index > 0 && point.Batch != ordered[index-1].Batch+1 {
			result.Code = "MissingAdjacentBatch"
			return result
		}
	}
	for index := 0; index < len(ordered)-1; index++ {
		current, next := ordered[index], ordered[index+1]
		meanPlateau := next.MeanIncrementalJoulesPerItem >= current.MeanIncrementalJoulesPerItem*(1-policy.RelativeEquivalenceTolerance)
		if !meanPlateau {
			continue
		}
		batch := current.Batch
		result.Batch = &batch
		for later := index + 1; later < len(ordered); later++ {
			if ordered[later].MeanIncrementalJoulesPerItem < current.MeanIncrementalJoulesPerItem*(1-policy.RelativeEquivalenceTolerance) {
				result.Status = BSatAmbiguous
				result.Code = "LaterEnergyDecrease"
				return result
			}
		}
		maxImprovement := (current.MeanIncrementalJoulesPerItem + current.PreviousConfidence.HalfWidth) -
			(next.MeanIncrementalJoulesPerItem - next.PreviousConfidence.HalfWidth)
		allowed := policy.RelativeEquivalenceTolerance * math.Max(
			current.MeanIncrementalJoulesPerItem-current.PreviousConfidence.HalfWidth,
			0,
		)
		if maxImprovement <= allowed {
			result.Status = BSatAccepted
			result.Code = "AdjacentEquivalenceConfirmed"
		} else {
			result.Status = BSatCandidateOnly
			result.Code = "UncertaintyNotConclusive"
		}
		return result
	}
	result.Status = BSatCensored
	result.Code = BSatCodeNoSaturationObserved
	return result
}

type curveIdentity struct {
	state               string
	environment         string
	protocol            string
	estimator           string
	pointPolicy         string
	confidenceEstimator string
	confidenceMethod    string
	confidenceLevel     float64
}

func validCurvePoint(point Point, identity curveIdentity) bool {
	return point.StateIdentity == identity.state && identity.state != "" &&
		point.EnvironmentIdentity == identity.environment && identity.environment != "" &&
		point.MeasurementProtocolIdentity == identity.protocol && identity.protocol != "" &&
		point.EstimatorIdentity == identity.estimator && identity.estimator != "" &&
		point.PointPolicyVersion == identity.pointPolicy && identity.pointPolicy != "" &&
		point.PreviousConfidence.EstimatorVersion == identity.confidenceEstimator &&
		point.PreviousConfidence.EstimatorVersion != "" &&
		point.PreviousConfidence.Method == identity.confidenceMethod &&
		point.PreviousConfidence.Level == identity.confidenceLevel &&
		point.MeanIncrementalJoulesPerItem > 0 &&
		!math.IsNaN(point.MeanIncrementalJoulesPerItem) &&
		!math.IsInf(point.MeanIncrementalJoulesPerItem, 0) &&
		point.PreviousConfidence.HalfWidth >= 0 &&
		!math.IsNaN(point.PreviousConfidence.HalfWidth) &&
		!math.IsInf(point.PreviousConfidence.HalfWidth, 0)
}
