package profilederivation

import (
	"errors"
	"math"
	"sort"
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
	baseline.MeanWallWatts = mean(values)
	baseline.ConfidenceHalfWidth = studentTHalfWidth(values)
	return baseline, nil
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
		baseline.StateIdentity != trial.StateIdentity ||
		baseline.EnvironmentIdentity != trial.EnvironmentIdentity ||
		baseline.MeasurementProtocolIdentity == "" ||
		baseline.PolicyVersion == "" ||
		baseline.AdmissionPolicyVersion == "" ||
		len(baseline.Trials) < 3 ||
		baseline.MeanWallWatts < 0 ||
		math.IsNaN(baseline.MeanWallWatts) ||
		math.IsInf(baseline.MeanWallWatts, 0) {
		return AdmittedTrial{}, errors.New("admitted idle baseline identity is invalid")
	}
	if baseline.ConfidenceHalfWidth < 0 || math.IsNaN(baseline.ConfidenceHalfWidth) ||
		math.IsInf(baseline.ConfidenceHalfWidth, 0) {
		return AdmittedTrial{}, errors.New("idle baseline uncertainty is invalid")
	}
	incremental := *admission.TotalWallJoules - baseline.MeanWallWatts*admission.WindowSeconds
	if incremental < 0 {
		return AdmittedTrial{}, errors.New("incremental energy is negative")
	}
	baselineUncertainty := baseline.ConfidenceHalfWidth * admission.WindowSeconds / float64(trial.CompletedItems)
	estimatorIdentity := "state=" + trial.StateIdentity + "|environment=" + trial.EnvironmentIdentity +
		"|activeProtocol=" + trial.MeasurementProtocolIdentity +
		"|idleProtocol=" + baseline.MeasurementProtocolIdentity +
		"|activeAdmission=" + admission.PolicyVersion +
		"|idleAdmission=" + baseline.AdmissionPolicyVersion + "|idlePoint=" + baseline.PolicyVersion
	return AdmittedTrial{
		StateIdentity:                    trial.StateIdentity,
		EnvironmentIdentity:              trial.EnvironmentIdentity,
		MeasurementProtocolIdentity:      trial.MeasurementProtocolIdentity,
		TrialID:                          trial.TrialID,
		AdmissionPolicyVersion:           admission.PolicyVersion,
		EstimatorIdentity:                estimatorIdentity,
		BaselineUncertaintyJoulesPerItem: baselineUncertainty,
		Batch:                            trial.Batch,
		IncrementalJoulesPerItem:         incremental / float64(trial.CompletedItems),
		LatencyP99Milliseconds:           percentile(trial.LatencyMilliseconds, 0.99),
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
		ConfidenceMethod:            PointConfidenceMethod,
		ConfidenceLevel:             PointConfidenceLevel,
		Trials:                      append([]AdmittedTrial(nil), trials...),
	}
	values := make([]float64, len(trials))
	var latency float64
	seen := map[string]struct{}{}
	admissionVersion := trials[0].AdmissionPolicyVersion
	var baselineUncertainty float64
	for index, trial := range trials {
		if trial.StateIdentity != point.StateIdentity ||
			trial.EnvironmentIdentity != point.EnvironmentIdentity ||
			trial.MeasurementProtocolIdentity != point.MeasurementProtocolIdentity ||
			trial.TrialID == "" ||
			trial.AdmissionPolicyVersion != admissionVersion ||
			admissionVersion == "" ||
			trial.EstimatorIdentity != point.EstimatorIdentity ||
			point.EstimatorIdentity == "" ||
			trial.Batch != point.Batch ||
			trial.IncrementalJoulesPerItem <= 0 ||
			math.IsNaN(trial.IncrementalJoulesPerItem) ||
			math.IsInf(trial.IncrementalJoulesPerItem, 0) {
			return Point{}, errors.New("trial state, environment, protocol, estimator, or batch mismatch")
		}
		if _, exists := seen[trial.TrialID]; exists {
			return Point{}, errors.New("duplicate active trial ID")
		}
		if trial.BaselineUncertaintyJoulesPerItem < 0 ||
			math.IsNaN(trial.BaselineUncertaintyJoulesPerItem) ||
			math.IsInf(trial.BaselineUncertaintyJoulesPerItem, 0) {
			return Point{}, errors.New("baseline uncertainty contribution is invalid")
		}
		seen[trial.TrialID] = struct{}{}
		values[index] = trial.IncrementalJoulesPerItem
		if trial.BaselineUncertaintyJoulesPerItem > baselineUncertainty {
			baselineUncertainty = trial.BaselineUncertaintyJoulesPerItem
		}
		if trial.LatencyP99Milliseconds > latency {
			latency = trial.LatencyP99Milliseconds
		}
	}
	point.MeanIncrementalJoulesPerItem = mean(values)
	activeUncertainty := studentTHalfWidth(values)
	point.ConfidenceHalfWidth = math.Hypot(activeUncertainty, baselineUncertainty)
	point.LatencyP99Milliseconds = latency
	return point, nil
}

func DeriveBSat(points []Point, policy BSatPolicy) BSatResult {
	result := BSatResult{Status: BSatUnavailable, PolicyVersion: policy.Version, Code: "InsufficientCurve"}
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
	confidenceMethod := ordered[0].ConfidenceMethod
	confidenceLevel := ordered[0].ConfidenceLevel
	curve := curveIdentity{
		state: identity, environment: environmentIdentity, protocol: measurementProtocolIdentity,
		estimator: estimatorIdentity, pointPolicy: pointPolicyVersion,
		confidenceMethod: confidenceMethod, confidenceLevel: confidenceLevel,
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
		maxImprovement := (current.MeanIncrementalJoulesPerItem + current.ConfidenceHalfWidth) - (next.MeanIncrementalJoulesPerItem - next.ConfidenceHalfWidth)
		allowed := policy.RelativeEquivalenceTolerance * math.Max(
			current.MeanIncrementalJoulesPerItem-current.ConfidenceHalfWidth,
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
	result.Code = "NoSaturationObserved"
	return result
}

type curveIdentity struct {
	state            string
	environment      string
	protocol         string
	estimator        string
	pointPolicy      string
	confidenceMethod string
	confidenceLevel  float64
}

func validCurvePoint(point Point, identity curveIdentity) bool {
	return point.StateIdentity == identity.state && identity.state != "" &&
		point.EnvironmentIdentity == identity.environment && identity.environment != "" &&
		point.MeasurementProtocolIdentity == identity.protocol && identity.protocol != "" &&
		point.EstimatorIdentity == identity.estimator && identity.estimator != "" &&
		point.PointPolicyVersion == identity.pointPolicy && identity.pointPolicy != "" &&
		point.ConfidenceMethod == identity.confidenceMethod &&
		point.ConfidenceLevel == identity.confidenceLevel &&
		point.MeanIncrementalJoulesPerItem > 0 &&
		!math.IsNaN(point.MeanIncrementalJoulesPerItem) &&
		!math.IsInf(point.MeanIncrementalJoulesPerItem, 0) &&
		point.ConfidenceHalfWidth >= 0 &&
		!math.IsNaN(point.ConfidenceHalfWidth) &&
		!math.IsInf(point.ConfidenceHalfWidth, 0)
}
