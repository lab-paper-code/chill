package profiler

import (
	"errors"
	"fmt"
)

const (
	// FreezeReasonInvalidRunSpec marks an immutable Run request that cannot be projected.
	FreezeReasonInvalidRunSpec = "InvalidRunSpec"
	// FreezeReasonAdmissionPolicyInvalid marks an invalid selected admission policy.
	FreezeReasonAdmissionPolicyInvalid = "AdmissionPolicyInvalid"
	// FreezeReasonExecutionPolicyInvalid marks an invalid selected execution policy.
	FreezeReasonExecutionPolicyInvalid = "ExecutionPolicyInvalid"
	// FreezeReasonActuationPolicyInvalid marks an invalid selected actuation policy.
	FreezeReasonActuationPolicyInvalid = "ActuationPolicyInvalid"
)

// TerminalFreezeError marks an immutable Run or installation-policy error
// that retrying the same ProfilingRun cannot repair. Kubernetes read/write
// failures and mutable fact races deliberately remain ordinary errors.
type TerminalFreezeError struct {
	reason string
	err    error
}

func (e *TerminalFreezeError) Error() string {
	return e.err.Error()
}

func (e *TerminalFreezeError) Unwrap() error {
	return e.err
}

// NewTerminalFreezeError classifies an immutable intent-resolution failure for
// the reconciliation boundary without making transient Kubernetes failures terminal.
func NewTerminalFreezeError(reason string, err error) error {
	if reason == "" || err == nil {
		panic("terminal freeze reason and error are required")
	}
	return &TerminalFreezeError{reason: reason, err: err}
}

// TerminalFreezeReason reports the stable reason carried by a terminal freeze failure.
func TerminalFreezeReason(err error) (string, bool) {
	var terminal *TerminalFreezeError
	if !errors.As(err, &terminal) {
		return "", false
	}
	return terminal.reason, true
}

// InvalidAdmissionPolicy constructs a terminal admission-policy failure.
func InvalidAdmissionPolicy(format string, args ...any) error {
	return NewTerminalFreezeError(FreezeReasonAdmissionPolicyInvalid, fmt.Errorf(format, args...))
}

// InvalidExecutionPolicy constructs a terminal execution-policy failure.
func InvalidExecutionPolicy(format string, args ...any) error {
	return NewTerminalFreezeError(FreezeReasonExecutionPolicyInvalid, fmt.Errorf(format, args...))
}

// InvalidActuationPolicy constructs a terminal actuation-policy failure.
func InvalidActuationPolicy(format string, args ...any) error {
	return NewTerminalFreezeError(FreezeReasonActuationPolicyInvalid, fmt.Errorf(format, args...))
}
