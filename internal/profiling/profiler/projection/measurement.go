// Package projection converts stored execution evidence into measurement inputs.
package projection

import (
	"fmt"

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

// IdleTrial binds repeated idle evidence to the first frozen execution condition.
// Runtime validity belongs to the Loader; environment and measured quantities
// remain observations rather than an equality-based admission policy.
func IdleTrial(evidence, reference profilerevidence.VerifiedIdleEvidence) (profilederivation.IdleTrial, error) {
	execution, raw, err := evidence.Records()
	if err != nil {
		return profilederivation.IdleTrial{}, err
	}
	frozen, _, err := reference.Records()
	if err != nil {
		return profilederivation.IdleTrial{}, err
	}
	config := frozen.RunConfig
	spec, err := ProjectCurvePlanSpec(
		"idle-baseline", config, config, *config.Delivery.ArtifactCapability,
		profilederivation.CurveConversionContract{}, []int{config.Measurement.Batch},
	)
	if err != nil {
		return profilederivation.IdleTrial{}, err
	}
	if err := validateCurvePlanRunConfigs(
		execution.RunConfig, config, execution.Terminal.Node.UID, frozen.Terminal.Node.UID, spec,
	); err != nil {
		return profilederivation.IdleTrial{}, fmt.Errorf("idle execution condition: %w", err)
	}
	observation, err := projectIdleObservation(execution, raw)
	if err != nil {
		return profilederivation.IdleTrial{}, err
	}
	return profilederivation.MeasureIdle(observation)
}
