// Package projection converts stored execution evidence into measurement inputs.
package projection

import (
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
)

func Trial(evidence profilerevidence.VerifiedEvidence) (profilederivation.Trial, error) {
	execution, raw, err := evidence.Records()
	if err != nil {
		return profilederivation.Trial{}, err
	}
	return projectTrial(execution, raw)
}

func IdleTrial(evidence profilerevidence.VerifiedIdleEvidence) (profilederivation.IdleTrial, error) {
	execution, raw, err := evidence.Records()
	if err != nil {
		return profilederivation.IdleTrial{}, err
	}
	observation, err := projectIdleObservation(execution, raw)
	if err != nil {
		return profilederivation.IdleTrial{}, err
	}
	return profilederivation.MeasureIdle(observation)
}
