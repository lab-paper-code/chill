package profiler

import (
	"errors"
	"fmt"
	"time"
)

// RunActuationState is the smallest controller-independent observation needed
// to gate one actuated profiling Run. The NodeActuator owns its detailed host
// lifecycle; the Profiler only needs to know whether the target is ready,
// restoration is underway, or the handoff is terminal.
type RunActuationState string

const (
	RunActuationPending   RunActuationState = "Pending"
	RunActuationReady     RunActuationState = "Ready"
	RunActuationRestoring RunActuationState = "Restoring"
	RunActuationRestored  RunActuationState = "Restored"
	RunActuationFailed    RunActuationState = "Failed"
)

// RunActuationAction is one bounded Profiler operation selected around the
// existing measurement-Job lifecycle.
type RunActuationAction string

const (
	RunActuationWaitForReady   RunActuationAction = "WaitForReady"
	RunActuationCreateJob      RunActuationAction = "CreateJob"
	RunActuationHoldTarget     RunActuationAction = "HoldTarget"
	RunActuationRequestRestore RunActuationAction = "RequestRestore"
	RunActuationWaitForRestore RunActuationAction = "WaitForRestore"
	RunActuationContinue       RunActuationAction = "Continue"
	RunActuationReportFailure  RunActuationAction = "ReportFailure"
)

// RunActuationFailure identifies why an actuated Run cannot enter the normal
// evidence path. It is deliberately distinct from the NodeActuator's detailed
// host failure, which remains authoritative in the handoff status.
type RunActuationFailure string

const (
	RunActuationHandoffFailed        RunActuationFailure = "HandoffFailed"
	RunActuationEndedBeforeExecution RunActuationFailure = "EndedBeforeExecution"
	RunActuationInterruptedExecution RunActuationFailure = "InterruptedExecution"
	RunActuationRestoreNotRequested  RunActuationFailure = "RestoreNotRequested"
	RunActuationRestoreTooEarly      RunActuationFailure = "RestoreTooEarly"
)

// RunActuationSnapshot binds the durable handoff state to the recorded Job
// fact. A successful Job may continue to evidence finalization only when its
// terminal time is no later than the Profiler-owned restore request.
type RunActuationSnapshot struct {
	State              RunActuationState
	RestoreRequested   bool
	RestoreRequestedAt time.Time
	JobRecorded        bool
	Job                JobObservation
	JobFinishedAt      time.Time
}

type RunActuationDecision struct {
	Action  RunActuationAction
	Failure RunActuationFailure
}

// DecideRunActuation gates Job creation and restoration without performing I/O.
// Host-command completion never appears here: only independently retained
// NodeActuation state crosses this boundary.
func DecideRunActuation(snapshot RunActuationSnapshot) (RunActuationDecision, error) {
	if err := snapshot.Validate(); err != nil {
		return RunActuationDecision{}, err
	}
	if snapshot.State == RunActuationFailed {
		return failedRunActuation(RunActuationHandoffFailed), nil
	}
	if !snapshot.JobRecorded {
		switch snapshot.State {
		case RunActuationReady:
			if snapshot.RestoreRequested {
				return RunActuationDecision{Action: RunActuationWaitForRestore}, nil
			}
			return RunActuationDecision{Action: RunActuationCreateJob}, nil
		case RunActuationRestored:
			return failedRunActuation(RunActuationEndedBeforeExecution), nil
		case RunActuationPending:
			return RunActuationDecision{Action: RunActuationWaitForReady}, nil
		case RunActuationRestoring:
			return RunActuationDecision{Action: RunActuationWaitForRestore}, nil
		}
	}

	switch snapshot.Job {
	case JobActive:
		if snapshot.State == RunActuationReady && !snapshot.RestoreRequested {
			return RunActuationDecision{Action: RunActuationHoldTarget}, nil
		}
		if snapshot.State == RunActuationRestored {
			return failedRunActuation(RunActuationInterruptedExecution), nil
		}
		return RunActuationDecision{Action: RunActuationWaitForRestore}, nil
	case JobSucceeded:
		if snapshot.State == RunActuationRestored {
			if !snapshot.RestoreRequested {
				return failedRunActuation(RunActuationRestoreNotRequested), nil
			}
			if snapshot.JobFinishedAt.After(snapshot.RestoreRequestedAt) {
				return failedRunActuation(RunActuationRestoreTooEarly), nil
			}
			return RunActuationDecision{Action: RunActuationContinue}, nil
		}
	case JobFailed, JobMissing:
		if snapshot.State == RunActuationRestored {
			return RunActuationDecision{Action: RunActuationContinue}, nil
		}
	}
	if !snapshot.RestoreRequested {
		return RunActuationDecision{Action: RunActuationRequestRestore}, nil
	}
	return RunActuationDecision{Action: RunActuationWaitForRestore}, nil
}

func (s RunActuationSnapshot) Validate() error {
	switch s.State {
	case RunActuationPending, RunActuationReady, RunActuationRestoring,
		RunActuationRestored, RunActuationFailed:
	default:
		return fmt.Errorf("unsupported Run actuation state %q", s.State)
	}
	if s.RestoreRequested != !s.RestoreRequestedAt.IsZero() {
		return errors.New("restore request and timestamp must be observed together")
	}
	if !s.JobRecorded {
		if s.Job != JobNotObserved || !s.JobFinishedAt.IsZero() {
			return errors.New("Job facts exist without a recorded Job")
		}
		return nil
	}
	if !validJobObservation(s.Job) {
		return fmt.Errorf("recorded Job requires a supported observation, got %q", s.Job)
	}
	if s.Job == JobSucceeded && s.JobFinishedAt.IsZero() {
		return errors.New("successful Job requires a terminal timestamp")
	}
	if s.Job != JobSucceeded && !s.JobFinishedAt.IsZero() {
		return errors.New("only a successful Job carries a terminal timestamp in the actuation gate")
	}
	return nil
}

func failedRunActuation(reason RunActuationFailure) RunActuationDecision {
	return RunActuationDecision{Action: RunActuationReportFailure, Failure: reason}
}
