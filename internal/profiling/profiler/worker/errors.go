package worker

import "errors"

var (
	// ErrExecutionContract classifies invalid or drifted RunConfig and delivery inputs.
	ErrExecutionContract = errors.New("invalid worker execution contract")
	// ErrRuntimeMeasurement classifies runtime, provider, inference, and output failures.
	ErrRuntimeMeasurement = errors.New("runtime measurement failed")
	// ErrMetricObservation classifies observer readiness, handoff, and result failures.
	ErrMetricObservation = errors.New("metric observation failed")
	// ErrWorkloadObservation classifies workload-window handoff and result failures.
	ErrWorkloadObservation = errors.New("workload observation failed")
	// ErrEvidenceCommit classifies encoding, immutable-write, and receipt failures.
	ErrEvidenceCommit = errors.New("evidence commit failed")
)
