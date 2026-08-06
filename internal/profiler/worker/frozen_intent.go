package worker

import "github.com/lab-paper-code/chill/internal/profilingrun"

// FrozenIntent is one intent that crossed the mounted-file validation and
// digest boundary. Its populated form can only be constructed by this package;
// command and coordinator wiring carry the sealed value instead of accepting a
// second mutable intent-plus-digest pair.
type FrozenIntent struct {
	verified profilingrun.VerifiedIntent
}

func sealFrozenIntent(intent profilingrun.VerifiedIntent) FrozenIntent {
	return FrozenIntent{verified: intent}
}

func (i FrozenIntent) snapshot() (profilingrun.Intent, error) {
	return i.verified.Record()
}

func (i FrozenIntent) verifiedIntent() profilingrun.VerifiedIntent {
	return i.verified
}

// Digest returns the digest verified at the mounted-file boundary. An empty
// value identifies an unpopulated FrozenIntent.
func (i FrozenIntent) Digest() string {
	return i.verified.Digest()
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
	intent, err := i.snapshot()
	return err == nil && profilingrun.IsSupportedCPUORTIntent(intent)
}

// SupportsRKNNLite reports whether the sealed intent selects the implemented
// RKNNLite path.
func (i FrozenIntent) SupportsRKNNLite() bool {
	intent, err := i.snapshot()
	return err == nil && profilingrun.IsSupportedRKNNLiteIntent(intent)
}
