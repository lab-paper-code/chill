package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
)

func TestMeasurementResultWriterStoresCompleteTableAndParityProgress(t *testing.T) {
	measurement, progress := completeMeasurementResultFixture(t)
	store := newMeasurementResultMemoryStore()
	writer := mustMeasurementResultWriter(t, store)

	stored, err := writer.write(context.Background(), measurement, progress)
	if err != nil {
		t.Fatal(err)
	}
	if store.puts != 1 || stored.record.Outcome != measurementStageComplete ||
		stored.record.Reason != "" || stored.record.DirectedTable == nil {
		t.Fatalf("stored result = %#v, puts = %d", stored, store.puts)
	}
	if stored.reference.Store != measurement.Spec.EvidenceStore ||
		stored.reference.Key != "power-mode-transition-costs/transition-cost-uid/result.json" ||
		stored.reference.SchemaVersion != measurementResultSchemaVersion || stored.reference.Size < 1 {
		t.Fatalf("stored reference = %#v", stored.reference)
	}
	for directionIndex, direction := range stored.record.Directions {
		if direction.AcceptedCount != 5 || len(direction.FinalEvidence) != 5 {
			t.Fatalf("direction %d progress = %#v", directionIndex, direction)
		}
		for attemptIndex, reference := range direction.FinalEvidence {
			want := progress.attempts[attemptIndex*2+directionIndex].FinalReference
			if reference != want {
				t.Fatalf("direction %d final reference %d = %#v, want %#v",
					directionIndex, attemptIndex, reference, want)
			}
		}
	}
	table := stored.record.DirectedTable
	if len(table.Entries) != 2 ||
		table.Entries[0].SourceState.PowerMode != measurement.Spec.Modes[0].PowerMode ||
		table.Entries[1].SourceState.PowerMode != measurement.Spec.Modes[1].PowerMode {
		t.Fatalf("stored directed table = %#v", table)
	}
}

func TestMeasurementResultWriterStoresTerminalOutcomeWithoutInventingTable(t *testing.T) {
	measurement, complete := completeMeasurementResultFixture(t)
	tests := []struct {
		name           string
		progress       measurementProgress
		wantAccepted   [2]int32
		wantReferences [2]int
	}{
		{
			name: "insufficient retained attempts",
			progress: measurementProgress{
				stage: measurementStageInsufficient, reason: measurementReasonAttemptLimitReached,
				attempts: complete.attempts[:3],
			},
			wantAccepted: [2]int32{2, 1}, wantReferences: [2]int{2, 1},
		},
		{
			name:         "failed before finalized attempt",
			progress:     measurementProgress{stage: measurementStageFailed, reason: AttemptReasonSourceStateMismatch},
			wantAccepted: [2]int32{}, wantReferences: [2]int{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMeasurementResultMemoryStore()
			stored, err := mustMeasurementResultWriter(t, store).
				write(context.Background(), measurement, test.progress)
			if err != nil {
				t.Fatal(err)
			}
			if stored.record.DirectedTable != nil || stored.record.Reason != test.progress.reason {
				t.Fatalf("stored terminal result = %#v", stored.record)
			}
			for index, direction := range stored.record.Directions {
				if direction.AcceptedCount != test.wantAccepted[index] ||
					direction.FinalEvidence == nil ||
					len(direction.FinalEvidence) != test.wantReferences[index] {
					t.Fatalf("direction %d progress = %#v", index, direction)
				}
			}
		})
	}
}

func TestMeasurementResultWriterLoadsExistingObjectAsAuthority(t *testing.T) {
	measurement, incoming := completeMeasurementResultFixture(t)
	winner := measurementProgress{stage: measurementStageFailed, reason: AttemptReasonSourceStateMismatch}
	winnerPayload := measurementResultPayload(t, winner)
	location, err := measurementResultLocation(measurement)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("already present", func(t *testing.T) {
		store := newMeasurementResultMemoryStore()
		store.objects[location] = winnerPayload
		stored, err := mustMeasurementResultWriter(t, store).
			write(context.Background(), measurement, incoming)
		if err != nil {
			t.Fatal(err)
		}
		if stored.record.Outcome != measurementStageFailed || store.puts != 0 || store.opens != 1 {
			t.Fatalf("stored result = %#v, opens = %d, puts = %d", stored.record, store.opens, store.puts)
		}
	})

	t.Run("immutable create race", func(t *testing.T) {
		store := newMeasurementResultMemoryStore()
		store.winnerOnPut = winnerPayload
		stored, err := mustMeasurementResultWriter(t, store).
			write(context.Background(), measurement, incoming)
		if err != nil {
			t.Fatal(err)
		}
		if stored.record.Outcome != measurementStageFailed || store.puts != 1 || store.opens != 2 {
			t.Fatalf("stored result = %#v, opens = %d, puts = %d", stored.record, store.opens, store.puts)
		}
	})
}

func TestDecodeMeasurementResultRejectsUntrustedStoredRecord(t *testing.T) {
	_, progress := completeMeasurementResultFixture(t)
	valid := measurementResultPayload(t, progress)
	tests := []struct {
		name    string
		payload func() []byte
	}{
		{name: "retired schema", payload: func() []byte {
			record, err := decodeMeasurementResult(valid)
			if err != nil {
				t.Fatal(err)
			}
			record.SchemaVersion = "chill.dacs.io/transition-cost-measurement-result.v1alpha0"
			return marshalMeasurementResultRecord(t, record)
		}},
		{name: "unknown field", payload: func() []byte {
			var fields map[string]any
			if err := json.Unmarshal(valid, &fields); err != nil {
				t.Fatal(err)
			}
			fields["unknown"] = true
			payload, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			return payload
		}},
		{name: "trailing value", payload: func() []byte {
			return append(append([]byte(nil), valid...), []byte(" {}")...)
		}},
		{name: "invalid complete table", payload: func() []byte {
			record, err := decodeMeasurementResult(valid)
			if err != nil {
				t.Fatal(err)
			}
			record.DirectedTable.Entries[0].AcceptedCount--
			return marshalMeasurementResultRecord(t, record)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeMeasurementResult(test.payload()); err == nil {
				t.Fatal("decodeMeasurementResult() unexpectedly succeeded")
			}
		})
	}
}

func TestMeasurementResultWriterRejectsNonterminalProgressAndUnexpectedReceipt(t *testing.T) {
	measurement, progress := completeMeasurementResultFixture(t)
	writer := mustMeasurementResultWriter(t, newMeasurementResultMemoryStore())
	if _, err := writer.write(context.Background(), measurement, measurementProgress{
		stage: measurementStageAttemptActive,
	}); err == nil {
		t.Fatal("nonterminal progress was stored")
	}

	store := newMeasurementResultMemoryStore()
	store.badReceipt = true
	writer = mustMeasurementResultWriter(t, store)
	if _, err := writer.write(context.Background(), measurement, progress); err == nil {
		t.Fatal("unexpected immutable-write receipt was accepted")
	}
}

func completeMeasurementResultFixture(
	t *testing.T,
) (*edgev1alpha1.PowerModeTransitionCost, measurementProgress) {
	t.Helper()
	fixture := newMeasurementSequenceFixture(t)
	attempts := make([]FinalizedAttempt, 0, 10)
	observations := make([]transitioncost.FinalizedObservation, 0, 10)
	for attempt := 1; attempt <= 5; attempt++ {
		for direction := 0; direction < 2; direction++ {
			finalized := sequenceFinalizedAttempt(
				t, fixture.prepared, direction, transitioncost.VerdictAccepted,
				fmt.Sprintf("d%d-a%d", direction, attempt),
			)
			attempts = append(attempts, finalized)
			observations = append(observations, finalized.Observation)
		}
	}
	directions := make([]transitioncost.DirectedStatePair, 2)
	for index, prepared := range fixture.prepared.Directions {
		directions[index] = transitioncost.DirectedStatePair{
			Source: prepared.FrozenContract.SourceState, Target: prepared.FrozenContract.TargetState,
		}
	}
	table, err := transitioncost.Summarize(directions, 5, observations)
	if err != nil {
		t.Fatal(err)
	}
	return fixture.measurement, measurementProgress{
		stage: measurementStageComplete, attempts: attempts, table: &table,
	}
}

func measurementResultPayload(
	t *testing.T,
	progress measurementProgress,
) []byte {
	t.Helper()
	record, err := buildMeasurementResult(progress)
	if err != nil {
		t.Fatal(err)
	}
	return marshalMeasurementResultRecord(t, record)
}

func marshalMeasurementResultRecord(t *testing.T, record measurementResultRecord) []byte {
	t.Helper()
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func mustMeasurementResultWriter(
	t *testing.T,
	store measurementResultStore,
) *measurementResultWriter {
	t.Helper()
	writer, err := newMeasurementResultWriter(store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return writer
}

type measurementResultMemoryStore struct {
	objects     map[evidencestore.ObjectLocation][]byte
	winnerOnPut []byte
	puts        int
	opens       int
	badReceipt  bool
}

func newMeasurementResultMemoryStore() *measurementResultMemoryStore {
	return &measurementResultMemoryStore{objects: map[evidencestore.ObjectLocation][]byte{}}
}

func (s *measurementResultMemoryStore) OpenLocation(
	_ context.Context,
	location evidencestore.ObjectLocation,
) (io.ReadCloser, error) {
	s.opens++
	payload, found := s.objects[location]
	if !found {
		return nil, evidencestore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func (s *measurementResultMemoryStore) PutImmutable(
	_ context.Context,
	identity evidencestore.ObjectIdentity,
	content io.Reader,
) (evidencestore.Receipt, error) {
	s.puts++
	if s.winnerOnPut != nil {
		s.objects[identity.Location()] = append([]byte(nil), s.winnerOnPut...)
		s.winnerOnPut = nil
		return measurementResultReceipt(identity, evidencestore.CreateOutcomeExisting), nil
	}
	payload, err := io.ReadAll(content)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	s.objects[identity.Location()] = payload
	receipt := measurementResultReceipt(identity, evidencestore.CreateOutcomeCreated)
	if s.badReceipt {
		receipt.Object.Key += ".unexpected"
	}
	return receipt, nil
}

func measurementResultReceipt(
	identity evidencestore.ObjectIdentity,
	outcome evidencestore.CreateOutcome,
) evidencestore.Receipt {
	return evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: identity, Outcome: outcome,
	}
}
