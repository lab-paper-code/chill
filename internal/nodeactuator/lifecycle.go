package nodeactuator

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

type Phase string

const (
	PhasePending           Phase = "Pending"
	PhaseAcquiringLease    Phase = "AcquiringLease"
	PhaseCheckingSafety    Phase = "CheckingSafety"
	PhaseObservingPrevious Phase = "ObservingPrevious"
	PhaseApplying          Phase = "Applying"
	PhaseVerifying         Phase = "Verifying"
	PhaseSettling          Phase = "Settling"
	PhaseReady             Phase = "Ready"
	PhaseFailed            Phase = "Failed"
)

type LockState string

const (
	LockNotHeld  LockState = "NotHeld"
	LockHeld     LockState = "Held"
	LockConflict LockState = "Conflict"
	LockLost     LockState = "Lost"
)

type SafetyState string

const (
	SafetyUnknown SafetyState = "Unknown"
	SafetyAllowed SafetyState = "Allowed"
	SafetyBlocked SafetyState = "Blocked"
)

type FailureStage string

const (
	FailureStageLease           FailureStage = "Lease"
	FailureStagePolicy          FailureStage = "Policy"
	FailureStageSafety          FailureStage = "Safety"
	FailureStageObservePrevious FailureStage = "ObservePrevious"
	FailureStageApply           FailureStage = "Apply"
	FailureStageVerify          FailureStage = "Verify"
	FailureStageSettle          FailureStage = "Settle"
)

type Action string

const (
	ActionAcquireLease    Action = "AcquireLease"
	ActionRenewLease      Action = "RenewLease"
	ActionWaitForLease    Action = "WaitForLease"
	ActionCheckSafety     Action = "CheckSafety"
	ActionObservePrevious Action = "ObservePrevious"
	ActionApplyTarget     Action = "ApplyTarget"
	ActionObserveTarget   Action = "ObserveTarget"
	ActionStartSettling   Action = "StartSettling"
	ActionWaitForSettling Action = "WaitForSettling"
	ActionMarkReady       Action = "MarkReady"
	ActionReleaseLease    Action = "ReleaseLease"
	ActionMarkFailed      Action = "MarkFailed"
	ActionComplete        Action = "Complete"
)

type Observation struct {
	PowerMode  string
	Target     Target
	ObservedAt time.Time
}

func (o Observation) Validate() error {
	if err := o.Target.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(o.PowerMode) != o.PowerMode {
		return errors.New("observation power mode cannot contain surrounding whitespace")
	}
	if o.ObservedAt.IsZero() {
		return errors.New("observation time is required")
	}
	return nil
}

type LockObservation struct {
	State      LockState
	RetryAfter time.Duration
}

type Failure struct {
	Stage   FailureStage
	Reason  string
	Message string
}

type Snapshot struct {
	Request             Request
	Now                 time.Time
	Phase               Phase
	Lock                LockObservation
	Safety              SafetyState
	SafetyObservedAt    *time.Time
	PreviousObservation *Observation
	TargetObservation   *Observation
	VerifiedAt          *time.Time
	SettlingStartedAt   *time.Time
	Failure             *Failure
}

type Decision struct {
	Action       Action
	RequeueAfter time.Duration
	Failure      *Failure
}

// Decide chooses one idempotent next action from durable observations. Host
// command success is never inferred here; Apply always leads to a separate
// read-back action in a later phase.
func Decide(snapshot Snapshot) (Decision, error) {
	if err := snapshot.Validate(); err != nil {
		return Decision{}, err
	}
	if snapshot.Phase == PhaseReady || snapshot.Phase == PhaseFailed {
		if snapshot.Lock.State == LockHeld {
			return Decision{Action: ActionReleaseLease, Failure: snapshot.Failure}, nil
		}
		return Decision{Action: ActionComplete, Failure: snapshot.Failure}, nil
	}
	if !snapshot.Now.Before(snapshot.Request.Deadline) {
		failure := &Failure{
			Stage: deadlineFailureStage(snapshot.Phase), Reason: "DeadlineExceeded",
			Message: "transition deadline expired before the target became ready",
		}
		return Decision{Action: ActionMarkFailed, Failure: failure}, nil
	}
	if snapshot.Lock.State != LockHeld {
		if snapshot.Lock.State == LockConflict {
			return Decision{Action: ActionWaitForLease, RequeueAfter: snapshot.Lock.RetryAfter}, nil
		}
		return Decision{Action: ActionAcquireLease}, nil
	}
	if snapshot.Lock.RetryAfter == 0 {
		return Decision{Action: ActionRenewLease}, nil
	}
	if snapshot.Safety == SafetyBlocked {
		failure := &Failure{
			Stage: FailureStageSafety, Reason: "WorkloadSafetyBlocked",
			Message: "an unapproved workload prevents Node-wide actuation",
		}
		return Decision{Action: ActionMarkFailed, Failure: failure}, nil
	}

	return decideHeldPhase(snapshot)
}

func decideHeldPhase(snapshot Snapshot) (Decision, error) {
	switch snapshot.Phase {
	case "", PhasePending, PhaseAcquiringLease:
		return Decision{Action: ActionCheckSafety}, nil
	case PhaseCheckingSafety:
		switch snapshot.Safety {
		case SafetyUnknown:
			return Decision{Action: ActionCheckSafety}, nil
		case SafetyAllowed:
			return Decision{Action: ActionObservePrevious}, nil
		}
	case PhaseObservingPrevious:
		if snapshot.PreviousObservation == nil {
			return Decision{Action: ActionObservePrevious}, nil
		}
		if observationMatchesRequest(snapshot.PreviousObservation, snapshot.Request) {
			return Decision{Action: ActionStartSettling}, nil
		}
		return Decision{Action: ActionApplyTarget}, nil
	case PhaseApplying:
		return Decision{Action: ActionApplyTarget}, nil
	case PhaseVerifying:
		if snapshot.TargetObservation == nil {
			return Decision{Action: ActionObserveTarget}, nil
		}
		if !targetsEqual(snapshot.TargetObservation.Target, snapshot.Request.Target) ||
			snapshot.TargetObservation.PowerMode != snapshot.Request.PowerMode {
			return Decision{Action: ActionMarkFailed, Failure: &Failure{
				Stage: FailureStageVerify, Reason: "TargetReadBackMismatch",
				Message: "independent target observation does not match the requested state",
			}}, nil
		}
		return Decision{Action: ActionStartSettling}, nil
	case PhaseSettling:
		settledAt := snapshot.SettlingStartedAt.Add(time.Duration(snapshot.Request.SettlingDurationSeconds) * time.Second)
		if snapshot.Now.Before(settledAt) {
			return Decision{Action: ActionWaitForSettling, RequeueAfter: earliestRequeue(
				settledAt.Sub(snapshot.Now), snapshot.Lock.RetryAfter,
				snapshot.Request.Deadline.Sub(snapshot.Now),
			)}, nil
		}
		return Decision{Action: ActionMarkReady}, nil
	case PhaseReady:
		return Decision{Action: ActionReleaseLease}, nil
	}
	return Decision{}, fmt.Errorf("unsupported actuation phase %q", snapshot.Phase)
}

func (s Snapshot) Validate() error {
	if err := s.Request.ValidateForNode(s.Request.Node); err != nil {
		return fmt.Errorf("validate request: %w", err)
	}
	if s.Now.IsZero() {
		return errors.New("decision time is required")
	}
	if !validPhase(s.Phase) {
		return fmt.Errorf("unsupported actuation phase %q", s.Phase)
	}
	if !validLockObservation(s.Lock) {
		return errors.New("invalid lock observation")
	}
	if s.Safety != "" && s.Safety != SafetyUnknown && s.Safety != SafetyAllowed && s.Safety != SafetyBlocked {
		return fmt.Errorf("unsupported safety observation %q", s.Safety)
	}
	if (s.Safety == SafetyUnknown || s.Safety == "") && s.SafetyObservedAt != nil ||
		s.Safety != SafetyUnknown && s.Safety != "" && s.SafetyObservedAt == nil {
		return errors.New("safety verdict and observation time must be present together")
	}
	for name, observation := range map[string]*Observation{
		"previous": s.PreviousObservation, "target": s.TargetObservation,
	} {
		if observation != nil {
			if err := observation.Validate(); err != nil {
				return fmt.Errorf("validate %s observation: %w", name, err)
			}
		}
	}
	if s.Phase == PhaseSettling && (s.VerifiedAt == nil || s.SettlingStartedAt == nil) {
		return errors.New("settling phase requires verification and settling timestamps")
	}
	if s.Phase == PhaseFailed && s.Failure == nil {
		return errors.New("failed phase requires a typed failure")
	}
	if s.Failure != nil {
		if !validFailureStage(s.Failure.Stage) || s.Failure.Reason == "" || s.Failure.Message == "" {
			return errors.New("typed failure is incomplete")
		}
	}
	return nil
}

func targetsEqual(left, right Target) bool {
	return reflect.DeepEqual(left, right)
}

func observationMatchesRequest(observation *Observation, request Request) bool {
	return observation != nil && observation.PowerMode == request.PowerMode && targetsEqual(observation.Target, request.Target)
}

func validPhase(phase Phase) bool {
	switch phase {
	case "", PhasePending, PhaseAcquiringLease, PhaseCheckingSafety, PhaseObservingPrevious,
		PhaseApplying, PhaseVerifying, PhaseSettling, PhaseReady, PhaseFailed:
		return true
	default:
		return false
	}
}

func validLockObservation(observation LockObservation) bool {
	switch observation.State {
	case LockNotHeld, LockLost:
		return observation.RetryAfter == 0
	case LockHeld, LockConflict:
		return observation.RetryAfter >= 0
	default:
		return false
	}
}

func validFailureStage(stage FailureStage) bool {
	switch stage {
	case FailureStageLease, FailureStagePolicy, FailureStageSafety, FailureStageObservePrevious, FailureStageApply,
		FailureStageVerify, FailureStageSettle:
		return true
	default:
		return false
	}
}

func deadlineFailureStage(phase Phase) FailureStage {
	switch phase {
	case "", PhasePending, PhaseAcquiringLease:
		return FailureStageLease
	case PhaseCheckingSafety:
		return FailureStageSafety
	case PhaseObservingPrevious:
		return FailureStageObservePrevious
	case PhaseApplying:
		return FailureStageApply
	case PhaseVerifying:
		return FailureStageVerify
	default:
		return FailureStageSettle
	}
}

// earliestRequeue is the soonest of the supplied bounds. Zero means none of
// them is still ahead, which is the value controller-runtime reads as no
// requeue at all.
func earliestRequeue(bounds ...time.Duration) time.Duration {
	var earliest time.Duration
	for _, bound := range bounds {
		if bound <= 0 {
			continue
		}
		if earliest == 0 || bound < earliest {
			earliest = bound
		}
	}
	return earliest
}
