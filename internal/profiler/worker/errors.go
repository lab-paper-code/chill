package worker

import "errors"

var (
	// ErrExecutionContract classifies invalid or drifted frozen and delivery inputs.
	ErrExecutionContract = errors.New("invalid worker execution contract")
	// ErrRuntimeMeasurement classifies runtime, provider, inference, and output failures.
	ErrRuntimeMeasurement = errors.New("runtime measurement failed")
	// ErrPowerObservation classifies observer readiness, handoff, and result failures.
	ErrPowerObservation = errors.New("power observation failed")
	// ErrEvidenceCommit classifies encoding, immutable-write, and receipt failures.
	ErrEvidenceCommit = errors.New("evidence commit failed")
)
