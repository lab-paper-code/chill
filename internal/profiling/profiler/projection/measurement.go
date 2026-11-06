// Package projection converts stored execution evidence into measurement inputs.
package projection

import (
	"errors"
	"reflect"
	"slices"

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

// IdleTrial compares the selected physical execution before combining idle
// measurements. Loader validation owns each record's runtime and numeric inputs.
func IdleTrial(evidence, reference profilerevidence.VerifiedIdleEvidence) (profilederivation.IdleTrial, error) {
	execution, raw, err := evidence.Records()
	if err != nil {
		return profilederivation.IdleTrial{}, err
	}
	frozen, _, err := reference.Records()
	if err != nil {
		return profilederivation.IdleTrial{}, err
	}
	a, b := execution.RunConfig, frozen.RunConfig
	if execution.Terminal.Node.UID != frozen.Terminal.Node.UID ||
		a.State.Model != b.State.Model || a.State.RuntimeFamily != b.State.RuntimeFamily ||
		a.State.Backend != b.State.Backend || a.State.PowerMode != b.State.PowerMode ||
		a.Images.Artifact != b.Images.Artifact ||
		!slices.Equal(a.State.ArtifactFiles, b.State.ArtifactFiles) ||
		a.Actor.GPUUUID != b.Actor.GPUUUID || a.Measurement.Batch != b.Measurement.Batch ||
		a.State.Preprocessor.Image != b.State.Preprocessor.Image ||
		a.State.Preprocessor.Path != b.State.Preprocessor.Path ||
		a.State.Preprocessor.MaxNewTokens != b.State.Preprocessor.MaxNewTokens ||
		!slices.Equal(a.State.Preprocessor.Inputs, b.State.Preprocessor.Inputs) ||
		!slices.Equal(a.State.Preprocessor.Outputs, b.State.Preprocessor.Outputs) {
		return profilederivation.IdleTrial{}, errors.New("idle measurements have different selected physical executions")
	}
	if (a.Actuation == nil) != (b.Actuation == nil) ||
		a.Actuation != nil && (a.Actuation.PowerMode != b.Actuation.PowerMode ||
			!reflect.DeepEqual(a.Actuation.Actuator, b.Actuation.Actuator)) {
		return profilederivation.IdleTrial{}, errors.New("idle measurements have different actual power actuation")
	}
	observation, err := projectIdleObservation(execution, raw)
	if err != nil {
		return profilederivation.IdleTrial{}, err
	}
	return profilederivation.MeasureIdle(observation)
}
