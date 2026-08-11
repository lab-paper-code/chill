// Package batchsaturation contains the Kubernetes-independent progress rules
// that drive one batch-saturation measurement from its admitted trials.
package batchsaturation

import (
	"errors"
	"fmt"
)

const (
	WorkloadModeActive WorkloadMode = "active"
	WorkloadModeIdle   WorkloadMode = "idle"

	VerdictPending  Verdict = "Pending"
	VerdictAccepted Verdict = "Accepted"
	VerdictFailed   Verdict = "Failed"

	ActionCreateRun Action = "CreateRun"
	ActionWait      Action = "Wait"
	ActionFinish    Action = "Finish"
	ActionFail      Action = "Fail"
)

type (
	WorkloadMode string
	Verdict      string
	Action       string
)

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

// Snapshot is built from the immutable spec and the owned Runs only. It has no
// status field, so a decision cannot depend on previously reported progress.
type Snapshot struct {
	RetryLimit int
	// EligibleBatches is the frozen set of batch sizes the selected artifact
	// declares, in ascending order. The sweep advances through it, and
	// exhausting it is the capability boundary.
	EligibleBatches []int
	Trials          []Trial
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
	// EligibleBatches is the frozen set of batch sizes the selected artifact
	// declares, in ascending order. The sweep advances through it, and
	// exhausting it is the capability boundary. Ascending order is a
	// precondition of this type.
	EligibleBatches []int
	Accepted        []Measurement
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

func (s Snapshot) Validate() error {
	if s.RetryLimit < 0 {
		return errors.New("retry limit must not be negative")
	}
	if len(s.EligibleBatches) == 0 {
		return errors.New("a non-empty eligible batch set is required")
	}
	previous := 0
	for _, batch := range s.EligibleBatches {
		if batch < 1 {
			return errors.New("eligible batches must be positive")
		}
		if batch <= previous {
			return errors.New("eligible batches must be strictly increasing")
		}
		previous = batch
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
