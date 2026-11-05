package evidence

import "encoding/json"

const (
	TerminationSchemaVersion = "chill.dacs.io/measurement-worker-termination.v1alpha1"
	StageStartup            = "Startup"
	StageObserver           = "Observer"
	StageExecution          = "Execution"
	StageResultValidation   = "ResultValidation"
	StageEvidenceCommit     = "EvidenceCommit"
	FailureExecution        = "ExecutionFailed"
	FailureMeasurementBudget = "MeasurementBudgetExceeded"
	FailureIntegrity        = "IntegrityError"
	FailureClassExpectedStop = "ExpectedStop"
	FailureClassRetryableFailure = "RetryableFailure"
	FailureClassFatalFailure = "FatalFailure"
	FailureClassUnknownFailure = "UnknownFailure"
)

// TerminationMessage records the current phase before work that can kill the
// container. Successful evidence commit replaces it with the existing receipt.
// Kubernetes remains authoritative for the container's actual termination reason.
type TerminationMessage struct {
	SchemaVersion string `json:"schemaVersion"`
	Stage         string `json:"stage"`
	FailureReason string `json:"failureReason,omitempty"`
}

// DecodeTerminationMessage accepts only the versioned worker phase record.
// Arbitrary logs and successful evidence receipts cannot prove an execution phase.
func DecodeTerminationMessage(message string) (TerminationMessage, bool) {
	var record TerminationMessage
	if len(message) > 4096 || json.Unmarshal([]byte(message), &record) != nil ||
		record.SchemaVersion != TerminationSchemaVersion {
		return TerminationMessage{}, false
	}
	switch record.Stage {
	case StageStartup, StageObserver, StageExecution, StageResultValidation, StageEvidenceCommit:
	default:
		return TerminationMessage{}, false
	}
	switch record.FailureReason {
	case "", FailureExecution, FailureMeasurementBudget, FailureIntegrity:
		return record, true
	default:
		return TerminationMessage{}, false
	}
}
