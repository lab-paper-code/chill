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
		StateIdentity:          trials[0].StateIdentity,
		Trials:                 append([]IdleTrial(nil), trials...),
		PolicyVersion:          policy.Version,
		AdmissionPolicyVersion: trials[0].AdmissionPolicyVersion,
	}
	values := make([]float64, len(trials))
	seen := map[string]struct{}{}
	admissionVersion := trials[0].AdmissionPolicyVersion
	for index, trial := range trials {
		if trial.StateIdentity != baseline.StateIdentity || trial.TrialID == "" ||
			trial.AdmissionPolicyVersion != admissionVersion ||
			admissionVersion == "" ||
			trial.MeanWallWatts < 0 ||
			math.IsNaN(trial.MeanWallWatts) ||
			math.IsInf(trial.MeanWallWatts, 0) {
			return IdleBaseline{}, errors.New("idle trial identity or value is invalid")
		}
		if _, exists := seen[trial.TrialID]; exists {
			return IdleBaseline{}, errors.New("duplicate idle trial ID")
		}
		seen[trial.TrialID] = struct{}{}
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
	if admission.StateIdentity != trial.StateIdentity || admission.TrialID != trial.TrialID ||
		admission.PolicyVersion == "" ||
		baseline.StateIdentity != trial.StateIdentity ||
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
	estimatorIdentity := trial.StateIdentity + "|active=" + admission.PolicyVersion +
		"|idleAdmission=" + baseline.AdmissionPolicyVersion + "|idlePoint=" + baseline.PolicyVersion
	return AdmittedTrial{
		StateIdentity:                    trial.StateIdentity,
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
		StateIdentity:      trials[0].StateIdentity,
		EstimatorIdentity:  trials[0].EstimatorIdentity,
		Batch:              trials[0].Batch,
		PointPolicyVersion: policy.Version,
		ConfidenceMethod:   policy.ConfidenceMethod,
		ConfidenceLevel:    policy.ConfidenceLevel,
		Trials:             append([]AdmittedTrial(nil), trials...),
	}
	values := make([]float64, len(trials))
	var latency float64
	seen := map[string]struct{}{}
	admissionVersion := trials[0].AdmissionPolicyVersion
	var baselineUncertainty float64
	for index, trial := range trials {
		if trial.StateIdentity != point.StateIdentity || trial.TrialID == "" ||
			trial.AdmissionPolicyVersion != admissionVersion ||
			admissionVersion == "" ||
			trial.EstimatorIdentity != point.EstimatorIdentity ||
			point.EstimatorIdentity == "" ||
			trial.Batch != point.Batch ||
			trial.IncrementalJoulesPerItem <= 0 ||
			math.IsNaN(trial.IncrementalJoulesPerItem) ||
			math.IsInf(trial.IncrementalJoulesPerItem, 0) {
			return Point{}, errors.New("trial state identity or batch mismatch")
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
	estimatorIdentity := ordered[0].EstimatorIdentity
	pointPolicyVersion := ordered[0].PointPolicyVersion
	confidenceMethod := ordered[0].ConfidenceMethod
	confidenceLevel := ordered[0].ConfidenceLevel
	result.StateIdentity = identity
	if ordered[0].Batch != 1 {
		result.Code = "MissingLowerBatchDomain"
		return result
	}
	for index, point := range ordered {
		result.MeasuredBatches = append(result.MeasuredBatches, point.Batch)
		if point.StateIdentity != identity || point.EstimatorIdentity != estimatorIdentity || estimatorIdentity == "" ||
			point.PointPolicyVersion != pointPolicyVersion ||
			pointPolicyVersion == "" ||
			point.ConfidenceMethod != confidenceMethod ||
			point.ConfidenceLevel != confidenceLevel ||
			point.MeanIncrementalJoulesPerItem <= 0 ||
			math.IsNaN(point.MeanIncrementalJoulesPerItem) ||
			math.IsInf(point.MeanIncrementalJoulesPerItem, 0) ||
			point.ConfidenceHalfWidth < 0 ||
			math.IsNaN(point.ConfidenceHalfWidth) ||
			math.IsInf(point.ConfidenceHalfWidth, 0) {
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

func percentile(values []float64, q float64) float64 {
	copyValues := append([]float64(nil), values...)
	sort.Float64s(copyValues)
	index := int(math.Ceil(q*float64(len(copyValues)))) - 1
	if index < 0 {
		index = 0
	}
	return copyValues[index]
}
func mean(values []float64) float64 {
	var sum float64
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}
func validatePointPolicy(policy PointPolicy) error {
	if policy.Version == "" || policy.MinimumTrials < 3 || policy.ConfidenceMethod != "student-t" ||
		policy.ConfidenceLevel != 0.95 {
		return errors.New("point policy must use 95% Student-t with at least three trials")
	}
	return nil
}
func studentTHalfWidth(values []float64) float64 {
	if len(values) < 3 {
		return math.Inf(1)
	}
	m := mean(values)
	var sum float64
	for _, value := range values {
		delta := value - m
		sum += delta * delta
	}
	critical := studentT95(len(values) - 1)
	return critical * math.Sqrt(sum/float64(len(values)-1)) / math.Sqrt(float64(len(values)))
}
func studentT95(df int) float64 {
	table := []float64{
		0,
		12.706,
		4.303,
		3.182,
		2.776,
		2.571,
		2.447,
		2.365,
		2.306,
		2.262,
		2.228,
		2.201,
		2.179,
		2.160,
		2.145,
		2.131,
		2.120,
		2.110,
		2.101,
		2.093,
		2.086,
		2.080,
		2.074,
		2.069,
		2.064,
		2.060,
		2.056,
		2.052,
		2.048,
		2.045,
		2.042,
	}
	if df < len(table) {
		return table[df]
	}
	return 1.96
}
