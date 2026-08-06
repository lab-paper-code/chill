package worker

import (
	"errors"

	"github.com/lab-paper-code/chill/internal/profilingrun"
)

// FrozenIntent is one intent that crossed the mounted-file validation and
// digest boundary. Its populated form can only be constructed by this package;
// command and coordinator wiring carry the sealed value instead of accepting a
// second mutable intent-plus-digest pair.
type FrozenIntent struct {
	intent profilingrun.Intent
	digest string
	loaded bool
}

func newFrozenIntent(intent profilingrun.Intent, digest string) FrozenIntent {
	return FrozenIntent{intent: cloneIntent(intent), digest: digest, loaded: true}
}

func (i FrozenIntent) snapshot() (profilingrun.Intent, error) {
	if !i.loaded {
		return profilingrun.Intent{}, errors.New("frozen intent is empty")
	}
	return cloneIntent(i.intent), nil
}

// Digest returns the digest verified at the mounted-file boundary. An empty
// value identifies an unpopulated FrozenIntent.
func (i FrozenIntent) Digest() string {
	if !i.loaded {
		return ""
	}
	return i.digest
}

// Metrics returns the immutable scalar metric contract selected by the Run.
func (i FrozenIntent) Metrics() (profilingrun.MetricContract, error) {
	intent, err := i.snapshot()
	if err != nil {
		return profilingrun.MetricContract{}, err
	}
	return intent.Metrics, nil
}

// TargetNode returns the immutable target-Node snapshot selected by the Run.
func (i FrozenIntent) TargetNode() (profilingrun.NodeSnapshot, error) {
	intent, err := i.snapshot()
	if err != nil {
		return profilingrun.NodeSnapshot{}, err
	}
	return intent.TargetNode, nil
}

// SupportsCPUORT reports whether the sealed intent selects the implemented CPU
// ONNX Runtime path.
func (i FrozenIntent) SupportsCPUORT() bool {
	return i.loaded && profilingrun.IsSupportedCPUORTIntent(i.intent)
}

// SupportsRKNNLite reports whether the sealed intent selects the implemented
// RKNNLite path.
func (i FrozenIntent) SupportsRKNNLite() bool {
	return i.loaded && profilingrun.IsSupportedRKNNLiteIntent(i.intent)
}

func cloneIntent(source profilingrun.Intent) profilingrun.Intent {
	result := source
	if source.Actuation != nil {
		actuation := *source.Actuation
		if source.Actuation.Actuator.JetsonNVPModel != nil {
			target := *source.Actuation.Actuator.JetsonNVPModel
			actuation.Actuator.JetsonNVPModel = &target
		}
		if source.Actuation.Actuator.NvidiaLockedGPUClock != nil {
			target := *source.Actuation.Actuator.NvidiaLockedGPUClock
			if source.Actuation.Actuator.NvidiaLockedGPUClock.Mode != nil {
				mode := *source.Actuation.Actuator.NvidiaLockedGPUClock.Mode
				target.Mode = &mode
			}
			actuation.Actuator.NvidiaLockedGPUClock = &target
		}
		result.Actuation = &actuation
	}
	return result
}
