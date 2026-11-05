package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
)

const terminationPathEnvironment = "CHILL_TERMINATION_MESSAGE_PATH"

type terminationPathKey struct{}

// WithTerminationMessagePath binds the native container termination file to one
// attempt. An absent path leaves adapters usable outside the Kubernetes worker.
func WithTerminationMessagePath(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, terminationPathKey{}, path)
}

func terminationMessagePath(ctx context.Context) string {
	path, _ := ctx.Value(terminationPathKey{}).(string)
	return path
}

// RecordTerminationStage persists the phase before entering it, including before
// runtime allocation. Failure to retain this proof stops execution as a contract error.
func RecordTerminationStage(ctx context.Context, stage string) error {
	return writeTerminationMessage(ctx, evidence.TerminationMessage{SchemaVersion: evidence.TerminationSchemaVersion, Stage: stage})
}

// RecordTerminationFailure adds a typed cause without inferring a phase from an
// error string. A deadline counts as a measurement budget only during execution.
func RecordTerminationFailure(ctx context.Context, failure error) error {
	path := terminationMessagePath(ctx)
	if path == "" {
		return nil
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	record, ok := evidence.DecodeTerminationMessage(string(payload))
	if !ok {
		return nil // A success receipt must never become a failure phase record.
	}
	if record.FailureReason != "" {
		return nil // Preserve an execution/integrity failure recorded before cleanup.
	}
	switch {
	case errors.Is(failure, ErrExecutionContract):
		record.FailureReason = evidence.FailureIntegrity
	case record.Stage == evidence.StageExecution && errors.Is(context.Cause(ctx), ErrMeasurementBudgetExceeded):
		record.FailureReason = evidence.FailureMeasurementBudget
	case record.Stage == evidence.StageExecution && ctx.Err() == nil && errors.Is(failure, ErrRuntimeMeasurement):
		record.FailureReason = evidence.FailureExecution
	}
	return writeTerminationMessage(ctx, record)
}

func writeTerminationMessage(ctx context.Context, record evidence.TerminationMessage) error {
	path := terminationMessagePath(ctx)
	if path == "" {
		return nil
	}
	payload, err := json.Marshal(record)
	if err == nil {
		err = os.WriteFile(path, payload, 0o600)
	}
	if err != nil {
		return errors.Join(ErrExecutionContract, err)
	}
	return nil
}
