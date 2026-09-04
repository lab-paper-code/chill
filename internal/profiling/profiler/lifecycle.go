// Package profiler coordinates one profiling Run without owning Kubernetes or
// storage implementations.
package profiler

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

// JobObservation is the controller-independent fact observed for a previously
// recorded Job reference.
type JobObservation string

const (
	JobNotObserved JobObservation = ""
	JobMissing     JobObservation = "Missing"
	JobActive      JobObservation = "Active"
	JobSucceeded   JobObservation = "Succeeded"
	JobFailed      JobObservation = "Failed"
)

// Action is the one bounded next operation selected for a Run. It is internal
// orchestration state, not a public API phase.
type Action string

const (
	ActionStop                   Action = "Stop"
	ActionCreateJob              Action = "CreateJob"
	ActionWaitForJob             Action = "WaitForJob"
	ActionFinalizeEvidence       Action = "FinalizeEvidence"
	ActionAdmitEvidence          Action = "AdmitEvidence"
	ActionComplete               Action = "Complete"
	ActionReportExecutionLost    Action = "ReportExecutionLost"
	ActionReportExecutionFailure Action = "ReportExecutionFailure"
)

// Snapshot contains only reconstructable facts needed to choose the next Run
// operation. JobReference is opaque to this package; Kubernetes adapters own
// its concrete name and UID semantics.
type Snapshot struct {
	Deleting          bool
	RunConfig         *profilingrun.RunConfig
	JobReference      string
	Job               JobObservation
	EvidenceReference *evidencestore.ObjectIdentity
	EvidenceInvalid   bool
	AdmissionRecorded bool
}

// Decision carries only the data needed by the selected adapter. Callers must
// not infer success from an action before that adapter completes.
type Decision struct {
	Action            Action
	RunConfig         *profilingrun.RunConfig
	EvidenceReference *evidencestore.ObjectIdentity
}

// Decide selects one deterministic, bounded next operation. It performs no
// I/O and never mutates the supplied Snapshot.
func Decide(snapshot Snapshot) (Decision, error) {
	if snapshot.Deleting {
		return Decision{Action: ActionStop}, nil
	}
	if err := snapshot.Validate(); err != nil {
		return Decision{}, err
	}
	if snapshot.EvidenceInvalid {
		return Decision{Action: ActionComplete}, nil
	}
	if snapshot.EvidenceReference != nil {
		reference := *snapshot.EvidenceReference
		if snapshot.AdmissionRecorded {
			return Decision{Action: ActionComplete}, nil
		}
		return Decision{Action: ActionAdmitEvidence, EvidenceReference: &reference}, nil
	}
	if snapshot.JobReference == "" {
		config := *snapshot.RunConfig
		return Decision{Action: ActionCreateJob, RunConfig: &config}, nil
	}

	switch snapshot.Job {
	case JobMissing:
		return Decision{Action: ActionReportExecutionLost}, nil
	case JobActive:
		return Decision{Action: ActionWaitForJob}, nil
	case JobSucceeded:
		return Decision{Action: ActionFinalizeEvidence}, nil
	case JobFailed:
		return Decision{Action: ActionReportExecutionFailure}, nil
	default:
		return Decision{}, fmt.Errorf("unsupported Job observation %q", snapshot.Job)
	}
}

func (s Snapshot) Validate() error {
	if s.RunConfig == nil {
		return errors.New("RunConfig is required")
	}
	if err := s.RunConfig.Validate(); err != nil {
		return fmt.Errorf("validate RunConfig: %w", err)
	}
	if strings.TrimSpace(s.JobReference) != s.JobReference {
		return errors.New("Job reference must not contain surrounding whitespace")
	}
	if s.JobReference == "" && s.Job != JobNotObserved {
		return errors.New("Job observation exists without a recorded Job reference")
	}
	if s.JobReference != "" && !validJobObservation(s.Job) {
		return errors.New("recorded Job reference requires a supported observation")
	}
	if s.AdmissionRecorded && s.EvidenceReference == nil {
		return errors.New("admission cannot be recorded without verified evidence")
	}
	if s.EvidenceInvalid {
		if s.EvidenceReference != nil {
			return errors.New("terminally invalid evidence contradicts a verified evidence reference")
		}
		if s.Job != JobSucceeded && s.Job != JobMissing {
			return errors.New("terminally invalid evidence contradicts observed Job outcome")
		}
	}
	if s.EvidenceReference != nil {
		if err := s.EvidenceReference.Validate(); err != nil {
			return fmt.Errorf("validate evidence reference: %w", err)
		}
		if s.JobReference == "" {
			return errors.New("verified evidence exists without a recorded Job reference")
		}
		if s.Job != JobSucceeded && s.Job != JobMissing {
			return errors.New("verified evidence contradicts observed Job outcome")
		}
	}
	return nil
}

func validJobObservation(observation JobObservation) bool {
	switch observation {
	case JobMissing, JobActive, JobSucceeded, JobFailed:
		return true
	default:
		return false
	}
}
