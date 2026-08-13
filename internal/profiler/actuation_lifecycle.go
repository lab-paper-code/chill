package profiler

import (
	"errors"
	"fmt"
)

// RunActuationState is the smallest controller-independent observation needed
// to gate one actuated profiling Run. The NodeActuator owns its detailed host
// lifecycle; the Profiler only needs to know whether the target is ready or
// the handoff failed.
type RunActuationState string

const (
	RunActuationPending RunActuationState = "Pending"
	RunActuationReady   RunActuationState = "Ready"
	RunActuationFailed  RunActuationState = "Failed"
)

// RunActuationAction is one bounded Profiler operation selected around the
// existing measurement-Job lifecycle.
type RunActuationAction string

const (
	RunActuationWaitForReady  RunActuationAction = "WaitForReady"
	RunActuationCreateJob     RunActuationAction = "CreateJob"
	RunActuationContinue      RunActuationAction = "Continue"
	RunActuationReportFailure RunActuationAction = "ReportFailure"
)

// RunActuationFailure identifies why an actuated Run cannot enter the normal
// evidence path. It is deliberately distinct from the NodeActuator's detailed
// host failure, which remains authoritative in the handoff status.
type RunActuationFailure string

const RunActuationHandoffFailed RunActuationFailure = "HandoffFailed"

// RunActuationSnapshot binds the durable handoff state to the recorded Job fact.
type RunActuationSnapshot struct {
	State       RunActuationState
	JobRecorded bool
}

type RunActuationDecision struct {
	Action  RunActuationAction
	Failure RunActuationFailure
}

// DecideRunActuation gates Job creation without performing I/O. Host-command
// completion never appears here: only independently retained NodeActuation
// state crosses this boundary.
func DecideRunActuation(snapshot RunActuationSnapshot) (RunActuationDecision, error) {
	if err := snapshot.Validate(); err != nil {
		return RunActuationDecision{}, err
	}
	if snapshot.State == RunActuationFailed {
		return failedRunActuation(RunActuationHandoffFailed), nil
	}
	if snapshot.State == RunActuationPending {
		return RunActuationDecision{Action: RunActuationWaitForReady}, nil
	}
	if !snapshot.JobRecorded {
		return RunActuationDecision{Action: RunActuationCreateJob}, nil
	}
	return RunActuationDecision{Action: RunActuationContinue}, nil
}

func (s RunActuationSnapshot) Validate() error {
	switch s.State {
	case RunActuationPending, RunActuationReady, RunActuationFailed:
	default:
		return fmt.Errorf("unsupported Run actuation state %q", s.State)
	}
	if !s.JobRecorded {
		return nil
	}
	if s.State != RunActuationReady {
		return errors.New("recorded Job requires a ready NodeActuation")
	}
	return nil
}

func failedRunActuation(reason RunActuationFailure) RunActuationDecision {
	return RunActuationDecision{Action: RunActuationReportFailure, Failure: reason}
}
