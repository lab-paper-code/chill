package profilederivation

import (
	"errors"
	"math"

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
			trial.ModelLoadedIdleMeanWallWatts < 0 ||
			math.IsNaN(trial.ModelLoadedIdleMeanWallWatts) ||
			math.IsInf(trial.ModelLoadedIdleMeanWallWatts, 0) {
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
		values[index] = trial.ModelLoadedIdleMeanWallWatts
	}
	baseline.ModelLoadedIdleMeanWallWatts, baseline.MeanWallWattsVariance = stat.MeanVariance(values, nil)
	baseline.MeanWallWattsVariance /= float64(len(values))
	baseline.DegreesOfFreedom = len(values) - 1
	baseline.ConfidenceLevel = policy.ConfidenceLevel
	baseline.ConfidenceHalfWidth = studentTCritical(policy.ConfidenceLevel, float64(baseline.DegreesOfFreedom)) *
		math.Sqrt(baseline.MeanWallWattsVariance)
	return baseline, nil
}

func validateIdleBaseline(baseline IdleBaseline) error {
	if baseline.StateIdentity == "" || baseline.EnvironmentIdentity == "" ||
		baseline.MeasurementProtocolIdentity == "" || baseline.PolicyVersion == "" ||
		baseline.AdmissionPolicyVersion == "" || len(baseline.Trials) < 3 {
		return errors.New("idle baseline value or identity is invalid")
	}
	if err := validateIdleBaselineSummary(baseline); err != nil {
		return err
	}
	seenTrials := make(map[string]struct{}, len(baseline.Trials))
	seenEvidence := make(map[string]struct{}, len(baseline.Trials))
	for _, trial := range baseline.Trials {
		if trial.StateIdentity != baseline.StateIdentity ||
			trial.EnvironmentIdentity != baseline.EnvironmentIdentity ||
			trial.MeasurementProtocolIdentity != baseline.MeasurementProtocolIdentity ||
			trial.AdmissionPolicyVersion != baseline.AdmissionPolicyVersion || trial.TrialID == "" ||
			!validRawEvidenceIdentity(trial.RawEvidence) || trial.ModelLoadedIdleMeanWallWatts < 0 ||
			math.IsNaN(trial.ModelLoadedIdleMeanWallWatts) ||
			math.IsInf(trial.ModelLoadedIdleMeanWallWatts, 0) {
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

func validateIdleBaselineSummary(baseline IdleBaseline) error {
	if baseline.ModelLoadedIdleMeanWallWatts < 0 ||
		math.IsNaN(baseline.ModelLoadedIdleMeanWallWatts) ||
		math.IsInf(baseline.ModelLoadedIdleMeanWallWatts, 0) || baseline.MeanWallWattsVariance < 0 ||
		math.IsNaN(baseline.MeanWallWattsVariance) || math.IsInf(baseline.MeanWallWattsVariance, 0) ||
		baseline.DegreesOfFreedom != len(baseline.Trials)-1 || baseline.ConfidenceHalfWidth < 0 ||
		!validProbability(baseline.ConfidenceLevel) ||
		math.IsNaN(baseline.ConfidenceHalfWidth) || math.IsInf(baseline.ConfidenceHalfWidth, 0) {
		return errors.New("idle baseline value or identity is invalid")
	}
	expectedHalfWidth := studentTCritical(baseline.ConfidenceLevel, float64(baseline.DegreesOfFreedom)) *
		math.Sqrt(baseline.MeanWallWattsVariance)
	if math.Abs(baseline.ConfidenceHalfWidth-expectedHalfWidth) >
		1e-12*math.Max(1, expectedHalfWidth) {
		return errors.New("idle baseline uncertainty fields are inconsistent")
	}
	return nil
}

func IncrementalWorkTrial(trial Trial, admission AdmissionResult, baseline IdleBaseline) (AdmittedTrial, error) {
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
	incrementalWorkWallJoules := *admission.TotalWallJoules -
		baseline.ModelLoadedIdleMeanWallWatts*admission.WindowSeconds
	if incrementalWorkWallJoules < 0 || math.IsNaN(incrementalWorkWallJoules) ||
		math.IsInf(incrementalWorkWallJoules, 0) {
		return AdmittedTrial{}, errors.New("incremental energy is invalid")
	}
	completedItems := float64(trial.CompletedItems)
	activeWindowWallJoulesPerItem := *admission.TotalWallJoules / completedItems
	idleBaselineSecondsPerItem := admission.WindowSeconds / completedItems
	estimatorIdentity := "state=" + trial.StateIdentity + "|environment=" + trial.EnvironmentIdentity +
		"|activeProtocol=" + trial.MeasurementProtocolIdentity +
		"|idleProtocol=" + baseline.MeasurementProtocolIdentity +
		"|activeAdmission=" + admission.PolicyVersion +
		"|idleAdmission=" + baseline.AdmissionPolicyVersion + "|idlePoint=" + baseline.PolicyVersion
	return AdmittedTrial{
		StateIdentity:                 trial.StateIdentity,
		EnvironmentIdentity:           trial.EnvironmentIdentity,
		MeasurementProtocolIdentity:   trial.MeasurementProtocolIdentity,
		TrialID:                       trial.TrialID,
		AdmissionPolicyVersion:        admission.PolicyVersion,
		EstimatorIdentity:             estimatorIdentity,
		BaselineMeanWallWattsVariance: baseline.MeanWallWattsVariance,
		BaselineDegreesOfFreedom:      baseline.DegreesOfFreedom,
		BaselineConfidenceLevel:       baseline.ConfidenceLevel,
		ActiveWindowWallJoulesPerItem: activeWindowWallJoulesPerItem,
		IdleBaselineSecondsPerItem:    idleBaselineSecondsPerItem,
		Batch:                         trial.Batch,
		IncrementalWorkJoulesPerItem:  incrementalWorkWallJoules / completedItems,
		LatencyP99Milliseconds:        percentile(trial.LatencyMilliseconds, 0.99),
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
	incrementalWorkJoulesPerItem := make([]float64, len(trials))
	idleBaselineSecondsPerItem := make([]float64, len(trials))
	var latency float64
	seen := map[string]struct{}{}
	admissionVersion := trials[0].AdmissionPolicyVersion
	baselineVariance := trials[0].BaselineMeanWallWattsVariance
	baselineDegreesOfFreedom := trials[0].BaselineDegreesOfFreedom
	for index, trial := range trials {
		if err := validateAdmittedTrialIdentity(trial, point, admissionVersion); err != nil {
			return Point{}, err
		}
		if _, exists := seen[trial.TrialID]; exists {
			return Point{}, errors.New("duplicate active trial ID")
		}
		if err := validateAdmittedTrialStatistics(
			trial, baselineVariance, baselineDegreesOfFreedom, policy.ConfidenceLevel,
		); err != nil {
			return Point{}, err
		}
		seen[trial.TrialID] = struct{}{}
		incrementalWorkJoulesPerItem[index] = trial.IncrementalWorkJoulesPerItem
		idleBaselineSecondsPerItem[index] = trial.IdleBaselineSecondsPerItem
		if trial.LatencyP99Milliseconds > latency {
			latency = trial.LatencyP99Milliseconds
		}
	}
	statistics, err := derivePointStatistics(
		incrementalWorkJoulesPerItem, idleBaselineSecondsPerItem,
		baselineVariance, baselineDegreesOfFreedom, policy.ConfidenceLevel,
	)
	if err != nil {
		return Point{}, err
	}
	point.MeanIncrementalWorkJoulesPerItem = statistics.meanIncrementalWorkJoulesPerItem
	point.IncrementalWorkSampleVariance = statistics.incrementalWorkSampleVariance
	point.MeanIdleBaselineSecondsPerItem = statistics.meanIdleBaselineSecondsPerItem
	point.Confidence = statistics.confidence
	point.LatencyP99Milliseconds = latency
	return point, nil
}

func validateAdmittedTrialIdentity(trial AdmittedTrial, point Point, admissionVersion string) error {
	if trial.StateIdentity != point.StateIdentity ||
		trial.EnvironmentIdentity != point.EnvironmentIdentity ||
		trial.MeasurementProtocolIdentity != point.MeasurementProtocolIdentity ||
		trial.TrialID == "" || trial.AdmissionPolicyVersion != admissionVersion || admissionVersion == "" ||
		trial.EstimatorIdentity != point.EstimatorIdentity || point.EstimatorIdentity == "" ||
		trial.Batch != point.Batch || trial.IncrementalWorkJoulesPerItem <= 0 ||
		math.IsNaN(trial.IncrementalWorkJoulesPerItem) ||
		math.IsInf(trial.IncrementalWorkJoulesPerItem, 0) {
		return errors.New("trial state, environment, protocol, estimator, or batch mismatch")
	}
	return nil
}

func validateAdmittedTrialStatistics(
	trial AdmittedTrial,
	baselineVariance float64,
	baselineDegreesOfFreedom int,
	confidenceLevel float64,
) error {
	if trial.BaselineMeanWallWattsVariance < 0 ||
		math.IsNaN(trial.BaselineMeanWallWattsVariance) ||
		math.IsInf(trial.BaselineMeanWallWattsVariance, 0) ||
		trial.BaselineMeanWallWattsVariance != baselineVariance ||
		trial.BaselineDegreesOfFreedom < 2 || trial.BaselineDegreesOfFreedom != baselineDegreesOfFreedom ||
		trial.BaselineConfidenceLevel != confidenceLevel {
		return errors.New("baseline uncertainty contribution is invalid")
	}
	if trial.ActiveWindowWallJoulesPerItem <= 0 ||
		math.IsNaN(trial.ActiveWindowWallJoulesPerItem) ||
		math.IsInf(trial.ActiveWindowWallJoulesPerItem, 0) ||
		trial.IdleBaselineSecondsPerItem <= 0 ||
		math.IsNaN(trial.IdleBaselineSecondsPerItem) ||
		math.IsInf(trial.IdleBaselineSecondsPerItem, 0) ||
		trial.IncrementalWorkJoulesPerItem > trial.ActiveWindowWallJoulesPerItem {
		return errors.New("trial sufficient statistics are invalid")
	}
	return nil
}
