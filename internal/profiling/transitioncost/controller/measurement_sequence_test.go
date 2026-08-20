package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
)

func TestMeasurementSequenceAdvancesOnlyFirstMissingAlternatingAttempt(t *testing.T) {
	fixture := newMeasurementSequenceFixture(t)
	fixture.retain(t, 0, 1, transitioncost.VerdictAccepted)
	fixture.retain(t, 1, 1, transitioncost.VerdictAccepted)

	progress, err := fixture.sequence.advance(context.Background(), fixture.measurement)
	if err != nil {
		t.Fatal(err)
	}
	if progress.stage != measurementStageAttemptActive || len(progress.attempts) != 2 ||
		len(fixture.materializer.calls) != 1 || fixture.materializer.calls[0] != (sequenceCoordinate{0, 2}) ||
		fixture.preparations.calls != 1 {
		t.Fatalf("progress = %#v, materializer calls = %#v, preparation calls = %d",
			progress, fixture.materializer.calls, fixture.preparations.calls)
	}
	wantLoads := []sequenceCoordinate{{0, 1}, {1, 1}, {0, 2}}
	assertSequenceLoads(t, fixture, wantLoads)
}

func TestMeasurementSequenceContinuesAlternationAfterOneDirectionReachesTarget(t *testing.T) {
	fixture := newMeasurementSequenceFixture(t)
	fixture.measurement.Spec.AttemptLimit = 6
	for attempt := int32(1); attempt <= 4; attempt++ {
		fixture.retain(t, 0, attempt, transitioncost.VerdictAccepted)
		fixture.retain(t, 1, attempt, transitioncost.VerdictAccepted)
	}
	fixture.retain(t, 0, 5, transitioncost.VerdictAccepted)
	fixture.retain(t, 1, 5, transitioncost.VerdictInvalid)

	progress, err := fixture.sequence.advance(context.Background(), fixture.measurement)
	if err != nil {
		t.Fatal(err)
	}
	if progress.stage != measurementStageAttemptActive || len(progress.attempts) != 10 ||
		len(fixture.materializer.calls) != 1 || fixture.materializer.calls[0] != (sequenceCoordinate{0, 6}) {
		t.Fatalf("progress = %#v, materializer calls = %#v", progress, fixture.materializer.calls)
	}
}

func TestMeasurementSequenceStopsWhenOneDirectionCannotReachTarget(t *testing.T) {
	t.Run("direction zero limit stops before direction one", func(t *testing.T) {
		fixture := newMeasurementSequenceFixture(t)
		for attempt := int32(1); attempt <= 4; attempt++ {
			fixture.retain(t, 0, attempt, transitioncost.VerdictAccepted)
			fixture.retain(t, 1, attempt, transitioncost.VerdictAccepted)
		}
		fixture.materializer.progress = AttemptProgress{Stage: AttemptStageFinalizationReady}
		fixture.finalizer.finalized = sequenceFinalizedAttempt(
			t, fixture.prepared, 0, transitioncost.VerdictInvalid, "d0-a5",
		)

		progress, err := fixture.sequence.advance(context.Background(), fixture.measurement)
		if err != nil {
			t.Fatal(err)
		}
		if progress.stage != measurementStageInsufficient || progress.reason != measurementReasonAttemptLimitReached ||
			len(progress.attempts) != 9 || len(fixture.materializer.calls) != 1 ||
			fixture.materializer.calls[0] != (sequenceCoordinate{0, 5}) {
			t.Fatalf("progress = %#v, materializer calls = %#v", progress, fixture.materializer.calls)
		}
		assertSequenceLoads(t, fixture, []sequenceCoordinate{
			{0, 1}, {1, 1}, {0, 2}, {1, 2}, {0, 3}, {1, 3}, {0, 4}, {1, 4}, {0, 5},
		})
	})

	t.Run("sufficient direction zero allows direction one limit", func(t *testing.T) {
		fixture := newMeasurementSequenceFixture(t)
		for attempt := int32(1); attempt <= 4; attempt++ {
			fixture.retain(t, 0, attempt, transitioncost.VerdictAccepted)
			fixture.retain(t, 1, attempt, transitioncost.VerdictAccepted)
		}
		fixture.retain(t, 0, 5, transitioncost.VerdictAccepted)
		fixture.retain(t, 1, 5, transitioncost.VerdictInvalid)

		progress, err := fixture.sequence.advance(context.Background(), fixture.measurement)
		if err != nil {
			t.Fatal(err)
		}
		if progress.stage != measurementStageInsufficient || len(progress.attempts) != 10 ||
			len(fixture.finalizer.loads) != 10 {
			t.Fatalf("progress = %#v, load count = %d", progress, len(fixture.finalizer.loads))
		}
	})
}

func TestMeasurementSequenceCompletesExactDirectedTable(t *testing.T) {
	fixture := newMeasurementSequenceFixture(t)
	for attempt := int32(1); attempt <= 5; attempt++ {
		fixture.retain(t, 0, attempt, transitioncost.VerdictAccepted)
		if attempt < 5 {
			fixture.retain(t, 1, attempt, transitioncost.VerdictAccepted)
		}
	}
	fixture.materializer.progress = AttemptProgress{Stage: AttemptStageFinalizationReady}
	fixture.finalizer.finalized = sequenceFinalizedAttempt(
		t, fixture.prepared, 1, transitioncost.VerdictAccepted, "d1-a5",
	)

	progress, err := fixture.sequence.advance(context.Background(), fixture.measurement)
	if err != nil {
		t.Fatal(err)
	}
	if progress.stage != measurementStageComplete || progress.table == nil || len(progress.attempts) != 10 ||
		len(fixture.materializer.calls) != 1 || fixture.materializer.calls[0] != (sequenceCoordinate{1, 5}) {
		t.Fatalf("progress = %#v", progress)
	}
	record := progress.table.Record()
	if len(record.Entries) != 2 || record.Entries[0].SourceState.PowerMode != "low" ||
		record.Entries[0].TargetState.PowerMode != "high" || record.Entries[0].AcceptedCount != 5 ||
		record.Entries[1].SourceState.PowerMode != "high" ||
		record.Entries[1].TargetState.PowerMode != "low" || record.Entries[1].AcceptedCount != 5 {
		t.Fatalf("directed table = %#v", record)
	}
}

func TestMeasurementSequenceFinalizesOnceThenAdvancesOppositeDirection(t *testing.T) {
	fixture := newMeasurementSequenceFixture(t)
	fixture.materializer.progress = AttemptProgress{Stage: AttemptStageFinalizationReady}
	fixture.finalizer.finalized = sequenceFinalizedAttempt(t, fixture.prepared, 0, transitioncost.VerdictAccepted, "new")

	progress, err := fixture.sequence.advance(context.Background(), fixture.measurement)
	if err != nil {
		t.Fatal(err)
	}
	if progress.stage != measurementStageAttemptFinalized || len(progress.attempts) != 1 ||
		len(fixture.finalizer.finalizeCalls) != 1 || fixture.finalizer.finalizeCalls[0] != (sequenceCoordinate{0, 1}) {
		t.Fatalf("first progress = %#v, finalize calls = %#v", progress, fixture.finalizer.finalizeCalls)
	}

	fixture.materializer.progress = AttemptProgress{Stage: AttemptStageObserverStarting}
	progress, err = fixture.sequence.advance(context.Background(), fixture.measurement)
	if err != nil {
		t.Fatal(err)
	}
	if progress.stage != measurementStageAttemptActive || len(progress.attempts) != 1 ||
		len(fixture.finalizer.finalizeCalls) != 1 ||
		fixture.materializer.calls[len(fixture.materializer.calls)-1] != (sequenceCoordinate{1, 1}) {
		t.Fatalf("recovered progress = %#v, finalize calls = %#v, materializer calls = %#v",
			progress, fixture.finalizer.finalizeCalls, fixture.materializer.calls)
	}
}

func TestMeasurementSequenceStopsOnAttemptTerminal(t *testing.T) {
	fixture := newMeasurementSequenceFixture(t)
	fixture.materializer.progress = AttemptProgress{
		Stage: AttemptStageTerminal, Reason: AttemptReasonSourceStateMismatch,
	}
	progress, err := fixture.sequence.advance(context.Background(), fixture.measurement)
	if err != nil {
		t.Fatal(err)
	}
	if progress.stage != measurementStageFailed || progress.reason != AttemptReasonSourceStateMismatch ||
		len(fixture.finalizer.finalizeCalls) != 0 {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestMeasurementSequencePropagatesFinalizationError(t *testing.T) {
	fixture := newMeasurementSequenceFixture(t)
	fixture.materializer.progress = AttemptProgress{Stage: AttemptStageFinalizationReady}
	fixture.finalizer.finalizeErr = errors.New("finalize")
	if _, err := fixture.sequence.advance(context.Background(), fixture.measurement); err == nil {
		t.Fatal("advance() unexpectedly succeeded")
	}
}

type measurementSequenceFixture struct {
	measurement  *edgev1alpha1.PowerModeTransitionCost
	prepared     PreparedMeasurement
	preparations *sequencePreparationSource
	materializer *sequenceMaterializer
	finalizer    *sequenceFinalizer
	sequence     *measurementSequence
}

func newMeasurementSequenceFixture(t *testing.T) measurementSequenceFixture {
	t.Helper()
	measurement, prepared, _ := preparationRecordFixture(t)
	measurement.Spec.AcceptedCountTarget = 5
	measurement.Spec.AttemptLimit = 5
	measurement.Spec.EvidenceStore = attemptEvidenceStore
	preparations := &sequencePreparationSource{prepared: prepared}
	materializer := &sequenceMaterializer{progress: AttemptProgress{Stage: AttemptStageObserverStarting}}
	finalizer := &sequenceFinalizer{retained: map[evidencestore.ObjectLocation]FinalizedAttempt{}}
	sequence, err := newMeasurementSequence(preparations, materializer, finalizer)
	if err != nil {
		t.Fatal(err)
	}
	return measurementSequenceFixture{
		measurement: measurement, prepared: prepared, preparations: preparations,
		materializer: materializer, finalizer: finalizer, sequence: sequence,
	}
}

func (f *measurementSequenceFixture) retain(
	t *testing.T,
	direction int,
	attempt int32,
	verdict transitioncost.Verdict,
) {
	t.Helper()
	selection, err := selectAttempt(f.measurement, direction, attempt)
	if err != nil {
		t.Fatal(err)
	}
	f.finalizer.retained[selection.finalLocation] = sequenceFinalizedAttempt(
		t, f.prepared, direction, verdict, fmt.Sprintf("d%d-a%d", direction, attempt),
	)
}

func sequenceFinalizedAttempt(
	t *testing.T,
	prepared PreparedMeasurement,
	direction int,
	verdict transitioncost.Verdict,
	suffix string,
) FinalizedAttempt {
	t.Helper()
	record := transitioncost.FinalizedObservationRecord{
		FrozenContract: prepared.Directions[direction].FrozenContract,
		Verdict:        verdict,
	}
	switch verdict {
	case transitioncost.VerdictAccepted:
		record.Reason = transitioncost.ReasonAccepted
		record.AcceptedObservation = &transitioncost.FinalizedAcceptedObservationRecord{
			StartedAt: time.Unix(100, 0).UTC(), CompletedAt: time.Unix(101, 0).UTC(),
			BoundaryMappingUncertaintyMilliseconds: 25,
			TauMilliseconds:                        1000, TransitionWallJoules: 15, EtaJoules: 3, ServingSurvived: true,
		}
	case transitioncost.VerdictInvalid:
		record.Reason, record.Detail = transitioncost.ReasonObservationMalformed, "invalid observation"
	default:
		t.Fatalf("unsupported fixture verdict %q", verdict)
	}
	observation, err := transitioncost.RestoreFinalizedObservation(record)
	if err != nil {
		t.Fatal(err)
	}
	return FinalizedAttempt{
		RawReference: evidencestore.ObjectIdentity{Store: attemptEvidenceStore, Key: suffix + "/raw.json", Size: 1},
		FinalReference: evidencestore.ObjectIdentity{
			Store: attemptEvidenceStore, Key: suffix + "/final.json", Size: 1,
		},
		Observation: observation,
	}
}

func assertSequenceLoads(t *testing.T, fixture measurementSequenceFixture, want []sequenceCoordinate) {
	t.Helper()
	if len(fixture.finalizer.loads) != len(want) {
		t.Fatalf("loads = %#v, want %#v", fixture.finalizer.loads, want)
	}
	for index := range want {
		if fixture.finalizer.loads[index] != want[index] {
			t.Fatalf("load %d = %#v, want %#v", index, fixture.finalizer.loads[index], want[index])
		}
	}
}

type sequenceCoordinate struct {
	direction int
	attempt   int32
}

type sequencePreparationSource struct {
	prepared PreparedMeasurement
	calls    int
}

func (s *sequencePreparationSource) Resolve(
	context.Context,
	*edgev1alpha1.PowerModeTransitionCost,
) (PreparedMeasurement, bool, error) {
	s.calls++
	return s.prepared, false, nil
}

type sequenceMaterializer struct {
	progress AttemptProgress
	calls    []sequenceCoordinate
}

func (m *sequenceMaterializer) Advance(
	_ context.Context,
	_ *edgev1alpha1.PowerModeTransitionCost,
	_ PreparedMeasurement,
	direction int,
	attempt int32,
) (AttemptProgress, error) {
	m.calls = append(m.calls, sequenceCoordinate{direction, attempt})
	return m.progress, nil
}

type sequenceFinalizer struct {
	retained      map[evidencestore.ObjectLocation]FinalizedAttempt
	finalized     FinalizedAttempt
	finalizeErr   error
	loads         []sequenceCoordinate
	finalizeCalls []sequenceCoordinate
}

func (f *sequenceFinalizer) load(
	_ context.Context,
	location evidencestore.ObjectLocation,
) (FinalizedAttempt, bool, error) {
	coordinate, err := sequenceCoordinateFromLocation(location)
	if err != nil {
		return FinalizedAttempt{}, false, err
	}
	f.loads = append(f.loads, coordinate)
	finalized, found := f.retained[location]
	return finalized, found, nil
}

func (f *sequenceFinalizer) Finalize(
	_ context.Context,
	measurement *edgev1alpha1.PowerModeTransitionCost,
	_ PreparedMeasurement,
	direction int,
	attempt int32,
	_ AttemptProgress,
) (FinalizedAttempt, error) {
	coordinate := sequenceCoordinate{direction, attempt}
	f.finalizeCalls = append(f.finalizeCalls, coordinate)
	if f.finalizeErr != nil {
		return FinalizedAttempt{}, f.finalizeErr
	}
	selection, err := selectAttempt(measurement, direction, attempt)
	if err != nil {
		return FinalizedAttempt{}, err
	}
	f.retained[selection.finalLocation] = f.finalized
	return f.finalized, nil
}

func sequenceCoordinateFromLocation(location evidencestore.ObjectLocation) (sequenceCoordinate, error) {
	var direction int
	var attempt int32
	if _, err := fmt.Sscanf(
		location.Key,
		"power-mode-transition-costs/transition-cost-uid/d%d-a%d/final.json",
		&direction,
		&attempt,
	); err != nil {
		return sequenceCoordinate{}, err
	}
	return sequenceCoordinate{direction, attempt}, nil
}
