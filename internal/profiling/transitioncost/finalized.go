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

// FinalizedAcceptedObservationRecord contains values derived from one common
// accepted transition window. The target-idle baseline remains in the frozen
// contract and is not duplicated here.
type FinalizedAcceptedObservationRecord struct {
	StartedAt                              time.Time `json:"startedAt"`
	CompletedAt                            time.Time `json:"completedAt"`
	BoundaryMappingUncertaintyMilliseconds int64     `json:"boundaryMappingUncertaintyMilliseconds"`
	TauMilliseconds                        float64   `json:"tauMilliseconds"`
	TransitionWallJoules                   float64   `json:"transitionWallJoules"`
	EtaJoules                              float64   `json:"etaJoules"`
	ServingSurvived                        bool      `json:"servingSurvived"`
}

// FinalizedObservation is one verified, raw-free terminal result suitable for
// retained attempt discovery and directed-table aggregation.
type FinalizedObservation struct {
	record FinalizedObservationRecord
}

// FinalizeResult removes raw evidence from one terminal Derive result.
func FinalizeResult(result Result) (FinalizedObservation, error) {
	record := FinalizedObservationRecord{
		FrozenContract: result.record.frozen,
		Verdict:        result.record.verdict, Reason: result.record.reason, Detail: result.record.detail,
	}
	if accepted := result.record.accepted; accepted != nil {
		if accepted.boundaryMappingUncertainty%time.Millisecond != 0 {
			return FinalizedObservation{}, errors.New("boundary mapping uncertainty must be an exact millisecond duration")
		}
		record.AcceptedObservation = &FinalizedAcceptedObservationRecord{
			StartedAt: accepted.startedAt, CompletedAt: accepted.completedAt,
			BoundaryMappingUncertaintyMilliseconds: accepted.boundaryMappingUncertainty.Milliseconds(),
			TauMilliseconds:                        accepted.tauMilliseconds,
			TransitionWallJoules:                   accepted.transitionWallJoules,
			EtaJoules:                              accepted.etaJoules, ServingSurvived: accepted.servingSurvived,
		}
	}
	return FinalizedObservation{record: record}, nil
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

// AcceptedObservation returns derived common-window values only for an
// accepted finalized observation.
func (o FinalizedObservation) AcceptedObservation() (Observation, bool) {
	accepted := o.record.AcceptedObservation
	if accepted == nil {
		return Observation{}, false
	}
	return Observation{record: acceptedRecord{
		startedAt: accepted.StartedAt, completedAt: accepted.CompletedAt,
		boundaryMappingUncertainty: time.Duration(accepted.BoundaryMappingUncertaintyMilliseconds) * time.Millisecond,
		tauMilliseconds:            accepted.TauMilliseconds, transitionWallJoules: accepted.TransitionWallJoules,
		targetIdleBaseline: o.record.FrozenContract.TargetIdleBaseline,
		etaJoules:          accepted.EtaJoules, servingSurvived: accepted.ServingSurvived,
	}}, true
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
		if record.Reason != ReasonObservationMalformed && record.Reason != ReasonSourceStateMismatch {
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
		case ReasonPowerWindowInsufficient, ReasonBoundaryUncertaintyExceeded, ReasonServingSurvivalUnknown:
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
	return nil
}

func cloneFinalizedObservationRecord(record FinalizedObservationRecord) FinalizedObservationRecord {
	clone := record
	if record.AcceptedObservation != nil {
		accepted := *record.AcceptedObservation
		clone.AcceptedObservation = &accepted
	}
	return clone
}
