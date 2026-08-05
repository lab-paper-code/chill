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
	PhaseRestoring         Phase = "Restoring"
	PhaseVerifyingRestore  Phase = "VerifyingRestore"
	PhaseRestored          Phase = "Restored"
	PhaseFailed            Phase = "Failed"
)

type LeaseState string

const (
	LeaseNotHeld  LeaseState = "NotHeld"
	LeaseHeld     LeaseState = "Held"
	LeaseConflict LeaseState = "Conflict"
	LeaseLost     LeaseState = "Lost"
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
	FailureStageHold            FailureStage = "Hold"
	FailureStageRestore         FailureStage = "Restore"
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
	ActionHold            Action = "Hold"
	ActionBeginRestore    Action = "BeginRestore"
	ActionRestorePrevious Action = "RestorePrevious"
	ActionObserveRestored Action = "ObserveRestored"
	ActionMarkRestored    Action = "MarkRestored"
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

type LeaseObservation struct {
	State      LeaseState
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
	Lease               LeaseObservation
	Safety              SafetyState
	SafetyObservedAt    *time.Time
	SafetyValidFor      time.Duration
	PreviousObservation *Observation
	TargetObservation   *Observation
	RestoredObservation *Observation
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
// command success is never inferred here; Apply and Restore always lead to a
// separate read-back action in a later phase.
func Decide(snapshot Snapshot) (Decision, error) {
	if err := snapshot.Validate(); err != nil {
		return Decision{}, err
	}
	if snapshot.Phase == PhaseRestored {
		if snapshot.Lease.State == LeaseHeld {
			return Decision{Action: ActionReleaseLease}, nil
		}
		return Decision{Action: ActionComplete}, nil
	}
	if snapshot.Phase == PhaseFailed {
		return decideFailed(snapshot), nil
	}
	if snapshot.Lease.State != LeaseHeld {
		if snapshot.Lease.State == LeaseConflict {
			return Decision{Action: ActionWaitForLease, RequeueAfter: snapshot.Lease.RetryAfter}, nil
		}
		return Decision{Action: ActionAcquireLease}, nil
	}
	if snapshot.Lease.RetryAfter == 0 {
		return Decision{Action: ActionRenewLease}, nil
	}
	if snapshot.Safety == SafetyBlocked {
		failure := &Failure{
			Stage: FailureStageSafety, Reason: "WorkloadSafetyBlocked",
			Message: "an unapproved workload prevents Node-wide actuation",
		}
		if snapshot.PreviousObservation == nil {
			return Decision{Action: ActionMarkFailed, Failure: failure}, nil
		}
		return Decision{Action: ActionBeginRestore, Failure: failure}, nil
	}
	if shouldBeginRestore(snapshot) {
		failure := snapshot.Failure
		if failure == nil && !snapshot.Now.Before(snapshot.Request.Deadline) {
			failure = &Failure{
				Stage: FailureStageHold, Reason: "DeadlineExceeded",
				Message: "actuation deadline expired before restoration was requested",
			}
		}
		if snapshot.PreviousObservation == nil {
			if failure == nil {
				failure = &Failure{
					Stage: FailureStageObservePrevious, Reason: "PreviousStateUnavailable",
					Message: "restoration was requested before a previous state was retained",
				}
			}
			return Decision{Action: ActionMarkFailed, Failure: failure}, nil
		}
		return Decision{Action: ActionBeginRestore, Failure: failure}, nil
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
		return Decision{Action: ActionApplyTarget}, nil
	case PhaseApplying:
		return Decision{Action: ActionApplyTarget}, nil
	case PhaseVerifying:
		if snapshot.TargetObservation == nil {
			return Decision{Action: ActionObserveTarget}, nil
		}
		if !targetsEqual(snapshot.TargetObservation.Target, snapshot.Request.Target) {
			return Decision{Action: ActionBeginRestore, Failure: &Failure{
				Stage: FailureStageVerify, Reason: "TargetReadBackMismatch",
				Message: "independent target observation does not match the requested state",
			}}, nil
		}
		return Decision{Action: ActionStartSettling}, nil
	case PhaseSettling:
		settledAt := snapshot.VerifiedAt.Add(time.Duration(snapshot.Request.SettlingDurationSeconds) * time.Second)
		if snapshot.Now.Before(settledAt) {
			return Decision{Action: ActionWaitForSettling, RequeueAfter: minimumPositive(
				settledAt.Sub(snapshot.Now), snapshot.Lease.RetryAfter,
				snapshot.Request.Deadline.Sub(snapshot.Now),
			)}, nil
		}
		return Decision{Action: ActionMarkReady}, nil
	case PhaseReady:
		if safetyExpired(snapshot) {
			return Decision{Action: ActionCheckSafety}, nil
		}
		return Decision{Action: ActionHold, RequeueAfter: minimumPositive(
			snapshot.Lease.RetryAfter, snapshot.Request.Deadline.Sub(snapshot.Now),
			snapshot.SafetyObservedAt.Add(snapshot.SafetyValidFor).Sub(snapshot.Now),
		)}, nil
	case PhaseRestoring:
		return Decision{Action: ActionRestorePrevious, Failure: snapshot.Failure}, nil
	case PhaseVerifyingRestore:
		if snapshot.RestoredObservation == nil {
			return Decision{Action: ActionObserveRestored, Failure: snapshot.Failure}, nil
		}
		if !targetsEqual(snapshot.RestoredObservation.Target, snapshot.PreviousObservation.Target) {
			return Decision{Action: ActionMarkFailed, Failure: &Failure{
				Stage: FailureStageRestore, Reason: "RestoreReadBackMismatch",
				Message: "independent restoration observation does not match the retained previous state",
			}}, nil
		}
		return Decision{Action: ActionMarkRestored, Failure: snapshot.Failure}, nil
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
	if !validLeaseObservation(s.Lease) {
		return errors.New("invalid Lease observation")
	}
	if s.Safety != "" && s.Safety != SafetyUnknown && s.Safety != SafetyAllowed && s.Safety != SafetyBlocked {
		return fmt.Errorf("unsupported safety observation %q", s.Safety)
	}
	if s.SafetyValidFor <= 0 {
		return errors.New("positive safety validity duration is required")
	}
	if (s.Safety == SafetyUnknown || s.Safety == "") && s.SafetyObservedAt != nil ||
		s.Safety != SafetyUnknown && s.Safety != "" && s.SafetyObservedAt == nil {
		return errors.New("safety verdict and observation time must be present together")
	}
	for name, observation := range map[string]*Observation{
		"previous": s.PreviousObservation, "target": s.TargetObservation, "restored": s.RestoredObservation,
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
	if s.Phase == PhaseVerifyingRestore && s.PreviousObservation == nil {
		return errors.New("restore verification requires a retained previous observation")
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

func decideFailed(snapshot Snapshot) Decision {
	if snapshot.Failure.Stage == FailureStageRestore {
		if snapshot.Lease.State == LeaseHeld && snapshot.Lease.RetryAfter == 0 {
			return Decision{Action: ActionRenewLease, Failure: snapshot.Failure}
		}
		if snapshot.Lease.State != LeaseHeld && snapshot.Lease.State != LeaseConflict {
			return Decision{Action: ActionAcquireLease, Failure: snapshot.Failure}
		}
		return Decision{Action: ActionHold, RequeueAfter: snapshot.Lease.RetryAfter, Failure: snapshot.Failure}
	}
	if snapshot.RestoredObservation == nil && snapshot.PreviousObservation != nil {
		if snapshot.Lease.State == LeaseConflict {
			return Decision{
				Action: ActionWaitForLease, RequeueAfter: snapshot.Lease.RetryAfter,
				Failure: snapshot.Failure,
			}
		}
		if snapshot.Lease.State != LeaseHeld {
			return Decision{Action: ActionAcquireLease, Failure: snapshot.Failure}
		}
		if snapshot.Lease.RetryAfter == 0 {
			return Decision{Action: ActionRenewLease, Failure: snapshot.Failure}
		}
		return Decision{Action: ActionBeginRestore, Failure: snapshot.Failure}
	}
	if snapshot.Lease.State == LeaseHeld {
		return Decision{Action: ActionReleaseLease, Failure: snapshot.Failure}
	}
	return Decision{Action: ActionComplete, Failure: snapshot.Failure}
}

func shouldBeginRestore(snapshot Snapshot) bool {
	if snapshot.Phase == PhaseRestoring || snapshot.Phase == PhaseVerifyingRestore {
		return false
	}
	return snapshot.Request.DesiredStage == DesiredStageRestore || snapshot.Failure != nil ||
		!snapshot.Now.Before(snapshot.Request.Deadline)
}

func targetsEqual(left, right Target) bool {
	return reflect.DeepEqual(left, right)
}

func safetyExpired(snapshot Snapshot) bool {
	return snapshot.Safety != SafetyAllowed || snapshot.SafetyObservedAt == nil ||
		!snapshot.Now.Before(snapshot.SafetyObservedAt.Add(snapshot.SafetyValidFor))
}

func validPhase(phase Phase) bool {
	switch phase {
	case "", PhasePending, PhaseAcquiringLease, PhaseCheckingSafety, PhaseObservingPrevious,
		PhaseApplying, PhaseVerifying, PhaseSettling, PhaseReady, PhaseRestoring,
		PhaseVerifyingRestore, PhaseRestored, PhaseFailed:
		return true
	default:
		return false
	}
}

func validLeaseObservation(observation LeaseObservation) bool {
	switch observation.State {
	case LeaseNotHeld, LeaseLost:
		return observation.RetryAfter == 0
	case LeaseHeld, LeaseConflict:
		return observation.RetryAfter >= 0
	default:
		return false
	}
}

func validFailureStage(stage FailureStage) bool {
	switch stage {
	case FailureStageLease, FailureStagePolicy, FailureStageSafety, FailureStageObservePrevious, FailureStageApply,
		FailureStageVerify, FailureStageSettle, FailureStageHold, FailureStageRestore:
		return true
	default:
		return false
	}
}

func minimumPositive(values ...time.Duration) time.Duration {
	var result time.Duration
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if result == 0 || value < result {
			result = value
		}
	}
	return result
}
