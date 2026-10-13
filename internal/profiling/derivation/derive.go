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
	}
	values := make([]float64, len(trials))
	seen := map[string]struct{}{}
	seenEvidence := map[string]struct{}{}
	for index, trial := range trials {
		if trial.StateIdentity != baseline.StateIdentity ||
			trial.EnvironmentIdentity != baseline.EnvironmentIdentity ||
			trial.MeasurementProtocolIdentity != baseline.MeasurementProtocolIdentity ||
			trial.TrialID == "" ||
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

// RestoreIdleBaseline admits an idle aggregate decoded from an untrusted
// stored result. Fresh DeriveIdleBaseline results do not pass through it.
func RestoreIdleBaseline(record IdleBaseline) (IdleBaseline, error) {
	record.Trials = append([]IdleTrial(nil), record.Trials...)
	if err := validateIdleBaseline(record); err != nil {
		return IdleBaseline{}, err
	}
	return record, nil
}

func validateIdleBaseline(baseline IdleBaseline) error {
	if baseline.StateIdentity == "" || baseline.EnvironmentIdentity == "" ||
		baseline.MeasurementProtocolIdentity == "" || baseline.PolicyVersion == "" ||
		len(baseline.Trials) < 3 {
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
			trial.TrialID == "" ||
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

func IncrementalWorkTrial(trial Trial, baseline IdleBaseline) (EnergyTrial, error) {
	if baseline.StateIdentity != trial.StateIdentity || baseline.EnvironmentIdentity != trial.EnvironmentIdentity || baseline.MeasurementProtocolIdentity == "" {
		return EnergyTrial{}, errors.New("idle baseline does not match the measured state and environment")
	}
	joules, err := measureTrial(trial)
	if err != nil {
		return EnergyTrial{}, err
	}
	windowSeconds := trial.WindowEnd.Sub(trial.WindowStart).Seconds()
	incrementalWorkWallJoules := joules -
		baseline.ModelLoadedIdleMeanWallWatts*windowSeconds
	if math.IsNaN(incrementalWorkWallJoules) ||
		math.IsInf(incrementalWorkWallJoules, 0) {
		return EnergyTrial{}, errors.New("incremental energy is invalid")
	}
	completedItems := float64(trial.CompletedItems)
	activeWindowWallJoulesPerItem := joules / completedItems
	idleBaselineSecondsPerItem := windowSeconds / completedItems
	estimatorIdentity := "state=" + trial.StateIdentity + "|environment=" + trial.EnvironmentIdentity +
		"|activeProtocol=" + trial.MeasurementProtocolIdentity +
		"|idleProtocol=" + baseline.MeasurementProtocolIdentity + "|idlePoint=" + baseline.PolicyVersion
	return EnergyTrial{
		StateIdentity:                 trial.StateIdentity,
		EnvironmentIdentity:           trial.EnvironmentIdentity,
		MeasurementProtocolIdentity:   trial.MeasurementProtocolIdentity,
		TrialID:                       trial.TrialID,
		EstimatorIdentity:             estimatorIdentity,
		BaselineMeanWallWattsVariance: baseline.MeanWallWattsVariance,
		BaselineDegreesOfFreedom:      baseline.DegreesOfFreedom,
		BaselineConfidenceLevel:       baseline.ConfidenceLevel,
		ActiveWindowWallJoulesPerItem: activeWindowWallJoulesPerItem,
		IdleBaselineSecondsPerItem:    idleBaselineSecondsPerItem,
		Batch:                         trial.Batch,
		IncrementalWorkJoulesPerItem:  incrementalWorkWallJoules / completedItems,
		LatencyP99Milliseconds:        percentile(trial.LatencyMilliseconds, 0.99),
		ClientLatencyMilliseconds:     append([]float64(nil), trial.LatencyMilliseconds...),
		NativeSamples:                 append([]NativeServiceSample(nil), trial.NativeSamples...),
	}, nil
}

func Aggregate(trials []EnergyTrial, policy PointPolicy) (Point, error) {
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
		Trials:                      cloneEnergyTrials(trials),
	}
	incrementalWorkJoulesPerItem := make([]float64, len(trials))
	idleBaselineSecondsPerItem := make([]float64, len(trials))
	var latency float64
	seen := map[string]struct{}{}
	baselineVariance := trials[0].BaselineMeanWallWattsVariance
	baselineDegreesOfFreedom := trials[0].BaselineDegreesOfFreedom
	for index, trial := range trials {
		if err := validateEnergyTrialIdentity(trial, point); err != nil {
			return Point{}, err
		}
		if _, exists := seen[trial.TrialID]; exists {
			return Point{}, errors.New("duplicate active trial ID")
		}
		if err := validateEnergyTrialStatistics(
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

func validateEnergyTrialIdentity(trial EnergyTrial, point Point) error {
	if trial.StateIdentity != point.StateIdentity ||
		trial.EnvironmentIdentity != point.EnvironmentIdentity ||
		trial.MeasurementProtocolIdentity != point.MeasurementProtocolIdentity ||
		trial.EstimatorIdentity != point.EstimatorIdentity || point.EstimatorIdentity == "" ||
		trial.Batch != point.Batch ||
		math.IsNaN(trial.IncrementalWorkJoulesPerItem) ||
		math.IsInf(trial.IncrementalWorkJoulesPerItem, 0) {
		return errors.New("trial state, environment, protocol, estimator, or batch mismatch")
	}
	return nil
}

func validateEnergyTrialStatistics(
	trial EnergyTrial,
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
