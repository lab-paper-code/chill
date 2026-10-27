package transitioncost

import (
	"errors"
	"fmt"
	"time"
)

const maximumFinalizedDurationMilliseconds = int64((1<<63 - 1) / time.Millisecond)

// FinalizedObservationRecord is the current scientific projection retained
// separately from its raw observation.
type FinalizedObservationRecord struct {
	FrozenContract      FrozenContract                      `json:"frozenContract"`
	Verdict             Verdict                             `json:"verdict"`
	Reason              Reason                              `json:"reason"`
	Detail              string                              `json:"detail"`
	AcceptedObservation *FinalizedAcceptedObservationRecord `json:"acceptedObservation"`
}

// FinalizedAcceptedObservationRecord contains values derived from one accepted
// transition. CompletedAt is fresh Ready; the energy-cost window ends at
// CompletedAt plus the frozen contract's declared SettlingSeconds.
type FinalizedAcceptedObservationRecord struct {
	StartedAt                              time.Time              `json:"startedAt"`
	CompletedAt                            time.Time              `json:"completedAt"`
	BoundaryMappingUncertaintyMilliseconds int64                  `json:"boundaryMappingUncertaintyMilliseconds"`
	TauMilliseconds                        float64                `json:"tauMilliseconds"`
	TransitionWallJoules                   float64                `json:"transitionWallJoules"`
	EtaJoules                              float64                `json:"etaJoules"`
	ServingSurvived                        bool                   `json:"servingSurvived"`
	BeforeNative                           *NativeNodeObservation `json:"beforeNative"`
	AfterNative                            *NativeNodeObservation `json:"afterNative"`
}

// FinalizedObservation is one verified, raw-free terminal result suitable for
// retained attempt discovery and directed-table aggregation.
type FinalizedObservation struct {
	record FinalizedObservationRecord
}

// RestoreFinalizedObservation verifies one decoded current record without
// reading or re-deriving its separate raw observation.
func RestoreFinalizedObservation(record FinalizedObservationRecord) (FinalizedObservation, error) {
	if err := validateFinalizedObservationRecord(record); err != nil {
		return FinalizedObservation{}, err
	}
	return FinalizedObservation{record: cloneFinalizedObservationRecord(record)}, nil
}

// Record returns an isolated current projection.
func (o FinalizedObservation) Record() FinalizedObservationRecord {
	return cloneFinalizedObservationRecord(o.record)
}

func (o FinalizedObservation) Verdict() Verdict { return o.record.Verdict }

func (o FinalizedObservation) Reason() Reason { return o.record.Reason }

func (o FinalizedObservation) Detail() string { return o.record.Detail }

func (o FinalizedObservation) FrozenContract() FrozenContract { return o.record.FrozenContract }

// AcceptedObservation returns derived values only for an accepted finalized observation.
func (o FinalizedObservation) AcceptedObservation() (Observation, bool) {
	accepted := o.record.AcceptedObservation
	if accepted == nil {
		return Observation{}, false
	}
	return Observation{
		record: *accepted, baseline: o.record.FrozenContract.TargetIdleBaseline,
		settlingSeconds: o.record.FrozenContract.SettlingSeconds,
	}, true
}

func validateFinalizedObservationRecord(record FinalizedObservationRecord) error {
	if err := validateFrozen(record.FrozenContract); err != nil {
		return fmt.Errorf("validate finalized frozen contract: %w", err)
	}
	accepted := record.AcceptedObservation
	switch record.Verdict {
	case VerdictAccepted:
		if record.Reason != ReasonAccepted || record.Detail != "" || accepted == nil {
			return errors.New("accepted finalized observation has an invalid reason, detail, or value")
		}
	case VerdictInvalid:
		switch record.Reason {
		case ReasonObservationMalformed, ReasonSourceStateMismatch, ReasonPowerChangedDuringMeasurement:
		default:
			return errors.New("invalid finalized observation has an unsupported reason")
		}
	case VerdictFailed:
		switch record.Reason {
		case ReasonActuationFailed, ReasonTargetReadBackFailed, ReasonTargetStateMismatch, ReasonReadinessNotReached:
		default:
			return errors.New("failed finalized observation has an unsupported reason")
		}
	case VerdictInsufficient:
		switch record.Reason {
		case ReasonPowerWindowInsufficient, ReasonBoundaryUncertaintyExceeded, ReasonServingSurvivalUnknown, ReasonNativeStateUnverified:
		default:
			return errors.New("insufficient finalized observation has an unsupported reason")
		}
	default:
		return errors.New("finalized observation has an unsupported verdict")
	}
	if record.Verdict != VerdictAccepted {
		if accepted != nil || !validText(record.Detail) {
			return errors.New("non-accepted finalized observation has an invalid detail or accepted value")
		}
		return nil
	}
	if accepted.StartedAt.IsZero() || !accepted.CompletedAt.After(accepted.StartedAt) ||
		accepted.BoundaryMappingUncertaintyMilliseconds < 0 ||
		accepted.BoundaryMappingUncertaintyMilliseconds > maximumFinalizedDurationMilliseconds ||
		!finite(accepted.TauMilliseconds) || accepted.TauMilliseconds <= 0 ||
		!finite(accepted.TransitionWallJoules) || accepted.TransitionWallJoules < 0 || !finite(accepted.EtaJoules) {
		return errors.New("accepted finalized observation contains invalid common-window values")
	}
	if err := VerifyNativeTransition(accepted.BeforeNative, accepted.AfterNative); err != nil {
		return err
	}
	return nil
}

func cloneFinalizedObservationRecord(record FinalizedObservationRecord) FinalizedObservationRecord {
	clone := record
	if record.AcceptedObservation != nil {
		accepted := *record.AcceptedObservation
		accepted.BeforeNative = cloneNativeObservation(accepted.BeforeNative)
		accepted.AfterNative = cloneNativeObservation(accepted.AfterNative)
		clone.AcceptedObservation = &accepted
	}
	return clone
}
