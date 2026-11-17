package profilederivation

import (
	"errors"
	"math"

	"gonum.org/v1/gonum/stat"
)

// DeriveIdleBaseline summarizes idle trials whose execution conditions have
// already been bound by the evidence projection boundary.
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
		if trial.TrialID == "" ||
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
		len(baseline.Trials) < 2 {
		return errors.New("idle baseline value or identity is invalid")
	}
	if err := validateIdleBaselineSummary(baseline); err != nil {
		return err
	}
	seenTrials := make(map[string]struct{}, len(baseline.Trials))
	seenEvidence := make(map[string]struct{}, len(baseline.Trials))
	for _, trial := range baseline.Trials {
		if trial.StateIdentity == "" || trial.EnvironmentIdentity == "" ||
			trial.MeasurementProtocolIdentity == "" || trial.TrialID == "" ||
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
