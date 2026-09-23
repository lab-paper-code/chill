package profiler

import (
	"errors"
	"fmt"
)

const (
	// ResolutionReasonInvalidRunSpec marks an immutable Run request that cannot be projected.
	ResolutionReasonInvalidRunSpec = "InvalidRunSpec"
	// ResolutionReasonExecutionPolicyInvalid marks an invalid selected execution policy.
	ResolutionReasonExecutionPolicyInvalid = "ExecutionPolicyInvalid"
	// ResolutionReasonActuationPolicyInvalid marks an invalid selected actuation policy.
	ResolutionReasonActuationPolicyInvalid = "ActuationPolicyInvalid"
)

// TerminalResolutionError marks an immutable Run or installation-policy error
// that retrying the same ProfilingRun cannot repair. Kubernetes read/write
// failures and mutable fact races deliberately remain ordinary errors.
type TerminalResolutionError struct {
	reason string
	err    error
}

func (e *TerminalResolutionError) Error() string {
	return e.err.Error()
}

func (e *TerminalResolutionError) Unwrap() error {
	return e.err
}

// NewTerminalResolutionError classifies an immutable RunConfig resolution failure for
// the reconciliation boundary without making transient Kubernetes failures terminal.
func NewTerminalResolutionError(reason string, err error) error {
	if reason == "" || err == nil {
		panic("terminal resolution reason and error are required")
	}
	return &TerminalResolutionError{reason: reason, err: err}
}

// TerminalResolutionReason reports the stable reason carried by a terminal resolution failure.
func TerminalResolutionReason(err error) (string, bool) {
	var terminal *TerminalResolutionError
	if !errors.As(err, &terminal) {
		return "", false
	}
	return terminal.reason, true
}

// InvalidExecutionPolicy constructs a terminal execution-policy failure.
func InvalidExecutionPolicy(format string, args ...any) error {
	return NewTerminalResolutionError(ResolutionReasonExecutionPolicyInvalid, fmt.Errorf(format, args...))
}

// InvalidActuationPolicy constructs a terminal actuation-policy failure.
func InvalidActuationPolicy(format string, args ...any) error {
	return NewTerminalResolutionError(ResolutionReasonActuationPolicyInvalid, fmt.Errorf(format, args...))
}
