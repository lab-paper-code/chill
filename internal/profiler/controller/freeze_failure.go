package controller

import (
	"errors"
	"fmt"
)

const (
	freezeReasonInvalidRunSpec         = "InvalidRunSpec"
	freezeReasonAdmissionPolicyInvalid = "AdmissionPolicyInvalid"
	freezeReasonExecutionPolicyInvalid = "ExecutionPolicyInvalid"
)

// terminalFreezeError marks an immutable Run or installation-policy error
// that retrying the same ProfilingRun cannot repair. Kubernetes read/write
// failures and mutable fact races deliberately remain ordinary errors.
type terminalFreezeError struct {
	reason string
	err    error
}

func (e *terminalFreezeError) Error() string {
	return e.err.Error()
}

func (e *terminalFreezeError) Unwrap() error {
	return e.err
}

func newTerminalFreezeError(reason string, err error) error {
	if reason == "" || err == nil {
		panic("terminal freeze reason and error are required")
	}
	return &terminalFreezeError{reason: reason, err: err}
}

func terminalFreezeReason(err error) (string, bool) {
	var terminal *terminalFreezeError
	if !errors.As(err, &terminal) {
		return "", false
	}
	return terminal.reason, true
}

func invalidAdmissionPolicy(format string, args ...any) error {
	return newTerminalFreezeError(freezeReasonAdmissionPolicyInvalid, fmt.Errorf(format, args...))
}

func invalidExecutionPolicy(format string, args ...any) error {
	return newTerminalFreezeError(freezeReasonExecutionPolicyInvalid, fmt.Errorf(format, args...))
}
