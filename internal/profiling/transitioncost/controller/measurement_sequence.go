package controller

import (
	"context"
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
)

type measurementStage string

const (
	measurementStageAttemptActive    measurementStage = "AttemptActive"
	measurementStageAttemptFinalized measurementStage = "AttemptFinalized"
	measurementStageComplete         measurementStage = "Complete"
	measurementStageInsufficient     measurementStage = "Insufficient"
	measurementStageFailed           measurementStage = "Failed"

	measurementReasonAttemptLimitReached = "AttemptLimitReached"
)

// measurementProgress is the package-local handoff to the enclosing reconciler
// and final-result writer. It retains no mutable cursor.
type measurementProgress struct {
	stage         measurementStage
	reason        string
	evidenceStore string
	// runnerProfileID is the frozen preparation's runner profile, recorded by the terminal result.
	runnerProfileID string
	attempts        []FinalizedAttempt
	table           *transitioncost.DirectedTable
}

// measurementSequence reconstructs and advances the fixed alternating prefix
// of one two-direction measurement within the enclosing reconciler.
type measurementSequence struct {
	resolve     func(context.Context, *edgev1alpha1.PowerModeTransitionCost) (PreparedMeasurement, bool, error)
	materialize func(context.Context, *edgev1alpha1.PowerModeTransitionCost, PreparedMeasurement, int, int32) (AttemptProgress, error)
	load        func(context.Context, evidencestore.ObjectLocation) (FinalizedAttempt, bool, error)
	finalize    func(context.Context, *edgev1alpha1.PowerModeTransitionCost, PreparedMeasurement, int, int32, AttemptProgress) (FinalizedAttempt, error)
}

func newMeasurementSequence(
	resolve func(context.Context, *edgev1alpha1.PowerModeTransitionCost) (PreparedMeasurement, bool, error),
	materialize func(context.Context, *edgev1alpha1.PowerModeTransitionCost, PreparedMeasurement, int, int32) (AttemptProgress, error),
	load func(context.Context, evidencestore.ObjectLocation) (FinalizedAttempt, bool, error),
	finalize func(context.Context, *edgev1alpha1.PowerModeTransitionCost, PreparedMeasurement, int, int32, AttemptProgress) (FinalizedAttempt, error),
) (*measurementSequence, error) {
	if resolve == nil || materialize == nil || load == nil || finalize == nil {
		return nil, errors.New("transition preparation, attempt materializer, and attempt finalizer are required")
	}
	return &measurementSequence{
		resolve: resolve, materialize: materialize, load: load, finalize: finalize,
	}, nil
}

func (s *measurementSequence) advance(
	ctx context.Context,
	measurement *edgev1alpha1.PowerModeTransitionCost,
) (measurementProgress, error) {
	prepared, _, err := s.resolve(ctx, measurement)
	if err != nil {
		return measurementProgress{}, fmt.Errorf("resolve immutable transition preparation: %w", err)
	}

	attempts := make([]FinalizedAttempt, 0)
	accepted := [2]int32{}
	for attemptNumber := int32(1); attemptNumber <= measurement.Spec.AttemptLimit; attemptNumber++ {
		for directionIndex := 0; directionIndex < len(prepared.Directions); directionIndex++ {
			selection, err := selectAttempt(
				measurement, prepared.EvidenceStore, directionIndex, attemptNumber,
			)
			if err != nil {
				return measurementProgress{}, err
			}
			finalized, found, err := s.load(ctx, selection.finalLocation)
			if err != nil {
				return measurementProgress{}, fmt.Errorf(
					"load finalized transition d%d-a%d: %w", directionIndex, attemptNumber, err,
				)
			}
			if found {
				attempts, accepted = retainFinalized(attempts, accepted, directionIndex, finalized)
				if progress, terminal, err := evaluateMeasurement(
					measurement, prepared, attempts, accepted, directionIndex, attemptNumber,
				); terminal || err != nil {
					return progress, err
				}
				continue
			}

			progress, err := s.materialize(
				ctx, measurement, prepared, directionIndex, attemptNumber,
			)
			if err != nil {
				return measurementProgress{}, fmt.Errorf(
					"advance transition attempt d%d-a%d: %w", directionIndex, attemptNumber, err,
				)
			}
			switch progress.Stage {
			case AttemptStageObserverStarting, AttemptStageActuationRunning:
				return measurementProgress{
					stage: measurementStageAttemptActive, evidenceStore: prepared.EvidenceStore, attempts: attempts,
				}, nil
			case AttemptStageTerminal:
				return measurementProgress{
					stage: measurementStageFailed, reason: progress.Reason,
					evidenceStore: prepared.EvidenceStore, runnerProfileID: prepared.RunnerProfileID, attempts: attempts,
				}, nil
			case AttemptStageFinalizationReady:
				finalized, err = s.finalize(
					ctx, measurement, prepared, directionIndex, attemptNumber, progress,
				)
				if err != nil {
					return measurementProgress{}, fmt.Errorf(
						"finalize transition attempt d%d-a%d: %w", directionIndex, attemptNumber, err,
					)
				}
				attempts, accepted = retainFinalized(attempts, accepted, directionIndex, finalized)
				if decision, terminal, err := evaluateMeasurement(
					measurement, prepared, attempts, accepted, directionIndex, attemptNumber,
				); terminal || err != nil {
					return decision, err
				}
				return measurementProgress{
					stage:         measurementStageAttemptFinalized,
					evidenceStore: prepared.EvidenceStore, attempts: attempts,
				}, nil
			default:
				return measurementProgress{}, fmt.Errorf("unsupported transition attempt stage %q", progress.Stage)
			}
		}
	}
	return measurementProgress{}, errors.New("transition measurement exhausted its alternating prefix without a decision")
}

func retainFinalized(
	attempts []FinalizedAttempt,
	accepted [2]int32,
	directionIndex int,
	finalized FinalizedAttempt,
) ([]FinalizedAttempt, [2]int32) {
	attempts = append(attempts, finalized)
	if finalized.Observation.Verdict() == transitioncost.VerdictAccepted {
		accepted[directionIndex]++
	}
	return attempts, accepted
}

func evaluateMeasurement(
	measurement *edgev1alpha1.PowerModeTransitionCost,
	prepared PreparedMeasurement,
	attempts []FinalizedAttempt,
	accepted [2]int32,
	directionIndex int,
	attemptNumber int32,
) (measurementProgress, bool, error) {
	if accepted[0] >= measurement.Spec.AcceptedCountTarget &&
		accepted[1] >= measurement.Spec.AcceptedCountTarget {
		observations := make([]transitioncost.FinalizedObservation, len(attempts))
		for index := range attempts {
			observations[index] = attempts[index].Observation
		}
		directions := make([]transitioncost.DirectedStatePair, len(prepared.Directions))
		for index := range prepared.Directions {
			frozen := prepared.Directions[index].FrozenContract
			directions[index] = transitioncost.DirectedStatePair{Source: frozen.SourceState, Target: frozen.TargetState}
		}
		table, err := transitioncost.Summarize(
			directions, int(measurement.Spec.AcceptedCountTarget), observations,
		)
		if err != nil {
			return measurementProgress{}, false, fmt.Errorf("summarize transition measurement: %w", err)
		}
		return measurementProgress{
			stage: measurementStageComplete, evidenceStore: prepared.EvidenceStore, runnerProfileID: prepared.RunnerProfileID,
			attempts: attempts, table: &table,
		}, true, nil
	}
	if attemptNumber == measurement.Spec.AttemptLimit &&
		accepted[directionIndex] < measurement.Spec.AcceptedCountTarget {
		return measurementProgress{
			stage: measurementStageInsufficient, reason: measurementReasonAttemptLimitReached,
			evidenceStore: prepared.EvidenceStore, runnerProfileID: prepared.RunnerProfileID, attempts: attempts,
		}, true, nil
	}
	return measurementProgress{}, false, nil
}
