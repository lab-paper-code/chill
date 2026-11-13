package evidence

import "errors"

// VerifiedEvidence is finalized active evidence decoded from immutable storage.
// Only Loader can construct a populated value.
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

// VerifiedIdleEvidence is finalized model-loaded-idle evidence decoded from
// immutable storage.
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
