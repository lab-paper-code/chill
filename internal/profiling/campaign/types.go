// Package batchenergyoptimization contains the Kubernetes-independent progress rules
// that drive one batch-energy-optimization measurement from its completed trials.
package batchenergyoptimization

import (
	"errors"
	"fmt"
)

const (
	WorkloadModeActive WorkloadMode = "active"
	WorkloadModeIdle   WorkloadMode = "idle"

	VerdictPending   Verdict = "Pending"
	VerdictCompleted Verdict = "Completed"
	VerdictFailed    Verdict = "Failed"

	TransitionInFlight    TransitionOutcome = "InFlight"
	TransitionEstablished TransitionOutcome = "Established"
	TransitionFailed      TransitionOutcome = "Failed"

	ActionCreateTransition Action = "CreateTransition"
	ActionCreateRun        Action = "CreateRun"
	ActionWait             Action = "Wait"
	ActionFinish           Action = "Finish"
	ActionFail             Action = "Fail"
)

type (
	WorkloadMode      string
	Verdict           string
	TransitionOutcome string
	Action            string
)

// State is the smallest immutable campaign state needed to decide whether its
// sweep may begin.
type State struct {
	TargetNode  string
	DeviceClass string
	PowerMode   string
}

// Transition is the campaign's power mode on its Node projected into the
// progress rules. EstablishedState is present only after the Node settled it.
type Transition struct {
	Outcome          TransitionOutcome
	EstablishedState *State
}

// Measurement names one trial to run. Batch is meaningful for both modes: the
// idle baseline is measured at the batch whose state it belongs to.
type Measurement struct {
	WorkloadMode WorkloadMode
	Batch        int
}

// Trial is one owned ProfilingRun projected into the progress rules. A trial
// carries no measured value; the method receives values separately.
type Trial struct {
	Measurement Measurement
	Attempt     int
	Verdict     Verdict
}

// Snapshot is built from the immutable spec, the owned Runs, and the Node's
// power mode only. It has no status field, so a decision cannot depend on
// previously reported progress.
type Snapshot struct {
	RequiredState      State
	TransitionRequired bool
	Transition         *Transition
	RetryLimit         int
	// PlannedBatchSizes is the recorded set of batch sizes the selected artifact
	// declares, in ascending order. The sweep advances through it, and
	// exhausting it is the capability boundary.
	PlannedBatchSizes []int
	Trials            []Trial
}

type Decision struct {
	Action Action
	Run    *Measurement
	Result *Result
	Reason string
}

// Observed is everything a method may see. Failed, pending, and retried
// attempts are absent by construction.
type Observed struct {
	// PlannedBatchSizes is the recorded set of batch sizes the selected artifact
	// declares, in ascending order. The sweep advances through it, and
	// exhausting it is the capability boundary. Ascending order is a
	// precondition of this type.
	PlannedBatchSizes []int
	Completed         []Measurement
}

type Plan struct {
	Next   *Measurement
	Result *Result
}

// Result is the terminal typed outcome plus the method's own detail, which the
// controller writes to the evidence store without interpreting it.
type Result struct {
	Outcome string
	Detail  any
}

type Method interface {
	Plan(observed Observed) (Plan, error)
}

func (m Measurement) Validate() error {
	if m.WorkloadMode != WorkloadModeActive && m.WorkloadMode != WorkloadModeIdle {
		return fmt.Errorf("unsupported workload mode %q", m.WorkloadMode)
	}
	if m.Batch < 1 {
		return errors.New("batch must be positive")
	}
	return nil
}

func (s State) Validate() error {
	if s.TargetNode == "" {
		return errors.New("required state target Node is empty")
	}
	if s.DeviceClass == "" {
		return errors.New("required state DeviceClass is empty")
	}
	if s.PowerMode == "" {
		return errors.New("required state power mode is empty")
	}
	return nil
}

func (s Snapshot) Validate() error {
	if s.RetryLimit < 0 {
		return errors.New("retry limit must not be negative")
	}
	if len(s.PlannedBatchSizes) == 0 {
		return errors.New("a non-empty planned batch size set is required")
	}
	previous := 0
	for _, batch := range s.PlannedBatchSizes {
		if batch < 1 {
			return errors.New("planned batch sizes must be positive")
		}
		if batch <= previous {
			return errors.New("planned batch sizes must be strictly increasing")
		}
		previous = batch
	}
	if err := s.RequiredState.Validate(); err != nil {
		return err
	}
	if !s.TransitionRequired && s.Transition != nil {
		return errors.New("a campaign without actuation cannot own a transition")
	}
	if s.Transition != nil {
		switch s.Transition.Outcome {
		case TransitionInFlight, TransitionFailed:
			if s.Transition.EstablishedState != nil {
				return errors.New("an incomplete transition cannot establish state")
			}
		case TransitionEstablished:
			if s.Transition.EstablishedState == nil {
				return errors.New("an established transition must project its state")
			}
			if err := s.Transition.EstablishedState.Validate(); err != nil {
				return fmt.Errorf("validate established state: %w", err)
			}
			if *s.Transition.EstablishedState != s.RequiredState {
				return errors.New("owned transition established a different state")
			}
		default:
			return fmt.Errorf("unsupported transition outcome %q", s.Transition.Outcome)
		}
	}
	seen := make(map[Measurement]map[int]struct{}, len(s.Trials))
	for _, trial := range s.Trials {
		if err := trial.Measurement.Validate(); err != nil {
			return fmt.Errorf("validate trial measurement: %w", err)
		}
		if trial.Attempt < 1 {
			return errors.New("trial attempt must be positive")
		}
		attempts, exists := seen[trial.Measurement]
		if !exists {
			attempts = make(map[int]struct{})
			seen[trial.Measurement] = attempts
		}
		if _, duplicate := attempts[trial.Attempt]; duplicate {
			return errors.New("one measurement attempt appears twice")
		}
		attempts[trial.Attempt] = struct{}{}
	}
	return nil
}
