package evidence

import "errors"

// VerifiedEvidence is active evidence that crossed the immutable-storage
// verification boundary. Only Loader can construct a populated value.
type VerifiedEvidence struct {
	execution ExecutionEvidence
	raw       RawMeasurement
	loaded    bool
}

func newVerifiedEvidence(execution ExecutionEvidence, raw RawMeasurement) VerifiedEvidence {
	return VerifiedEvidence{execution: execution, raw: raw, loaded: true}
}

func (v VerifiedEvidence) Records() (ExecutionEvidence, RawMeasurement, error) {
	if !v.loaded {
		return ExecutionEvidence{}, RawMeasurement{}, errors.New("verified active evidence is empty")
	}
	return v.execution, v.raw, nil
}

func (v VerifiedEvidence) IntentDigest() string {
	if !v.loaded {
		return ""
	}
	return v.execution.IntentDigest
}

// VerifiedIdleEvidence is model-loaded-idle evidence that crossed the same
// immutable-storage verification boundary.
type VerifiedIdleEvidence struct {
	execution ExecutionEvidence
	raw       IdleRawMeasurement
	loaded    bool
}

func newVerifiedIdleEvidence(execution ExecutionEvidence, raw IdleRawMeasurement) VerifiedIdleEvidence {
	return VerifiedIdleEvidence{execution: execution, raw: raw, loaded: true}
}

func (v VerifiedIdleEvidence) Records() (ExecutionEvidence, IdleRawMeasurement, error) {
	if !v.loaded {
		return ExecutionEvidence{}, IdleRawMeasurement{}, errors.New("verified idle evidence is empty")
	}
	return v.execution, v.raw, nil
}

func (v VerifiedIdleEvidence) IntentDigest() string {
	if !v.loaded {
		return ""
	}
	return v.execution.IntentDigest
}
