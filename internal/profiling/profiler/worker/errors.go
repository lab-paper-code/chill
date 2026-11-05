package worker

import "errors"

var (
	// ErrExecutionContract classifies invalid or drifted RunConfig and delivery inputs.
	ErrExecutionContract = errors.New("invalid worker execution contract")
	// ErrRuntimeMeasurement classifies runtime process and inference failures.
	ErrRuntimeMeasurement = errors.New("runtime measurement failed")
	// ErrMetricObservation classifies observer readiness, handoff, and result failures.
	ErrMetricObservation = errors.New("metric observation failed")
	// ErrWorkloadObservation classifies workload-window handoff and result failures.
	ErrWorkloadObservation = errors.New("workload observation failed")
	// ErrEvidenceCommit classifies encoding, immutable-write, and receipt failures.
	ErrEvidenceCommit = errors.New("evidence commit failed")
	// ErrRuntimeFailed reports that the runtime published runtime-failed and exited.
	ErrRuntimeFailed = errors.New("runtime container failed")
	// ErrMeasurementBudgetExceeded is the cause of the frozen worker attempt deadline.
	// Parent cancellation and earlier parent deadlines do not carry this cause.
	ErrMeasurementBudgetExceeded = errors.New("measurement attempt budget exceeded")
)
