package controller

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	idlebaseline "github.com/lab-paper-code/chill/internal/profiling/idlebaseline"
)

func TestResultWriterCreatesAndLoadsCompleteAggregate(t *testing.T) {
	parent := testBaselineParent()
	result := testCompleteResult(t)
	store := newIdleResultMemoryStore()
	writer := mustResultWriter(t, store)
	reference, err := writer.Write(context.Background(), parent, result)
	if err != nil {
		t.Fatal(err)
	}
	if store.puts != 1 || reference.Store != "evidence" ||
		reference.Key != "model-loaded-idle-baselines/idle-baseline-uid/result.json" ||
		reference.SchemaVersion != idlebaseline.ResultSchemaVersion || reference.Size < 1 {
		t.Fatalf("stored reference = %#v, puts=%d", reference, store.puts)
	}
	loaded, found, err := writer.Load(context.Background(), parent)
	if err != nil || !found || loaded != reference {
		t.Fatalf("loaded reference = %#v, found=%v, err=%v", loaded, found, err)
	}
}

func TestResultWriterUsesExistingObjectAsAuthority(t *testing.T) {
	parent := testBaselineParent()
	winner := testCompleteResult(t)
	winnerPayload, err := winner.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	location := evidencestore.ObjectLocation{
		Store: "evidence", Key: "model-loaded-idle-baselines/idle-baseline-uid/result.json",
	}

	t.Run("already present", func(t *testing.T) {
		store := newIdleResultMemoryStore()
		store.objects[location] = winnerPayload
		if _, err := mustResultWriter(t, store).Write(context.Background(), parent, testCompleteResult(t)); err != nil {
			t.Fatal(err)
		}
		if store.puts != 0 || store.opens != 1 {
			t.Fatalf("existing authority opens=%d puts=%d", store.opens, store.puts)
		}
	})

	t.Run("immutable race", func(t *testing.T) {
		store := newIdleResultMemoryStore()
		store.winnerOnPut = winnerPayload
		if _, err := mustResultWriter(t, store).Write(context.Background(), parent, testCompleteResult(t)); err != nil {
			t.Fatal(err)
		}
		if store.puts != 1 || store.opens != 2 {
			t.Fatalf("race authority opens=%d puts=%d", store.opens, store.puts)
		}
	})
}

func testCompleteResult(t *testing.T) idlebaseline.Result {
	t.Helper()
	result, err := idlebaseline.NewResult(
		measurementState(testBaselineParent()), testDerivedBaseline(t, 3), testFinalReferences(3),
	)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustResultWriter(t *testing.T, store ResultStore) *ResultWriter {
	t.Helper()
	writer, err := NewResultWriter(store, ResultWriterOptions{Store: "evidence", MaxObjectBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	return writer
}

type idleResultMemoryStore struct {
	objects     map[evidencestore.ObjectLocation][]byte
	winnerOnPut []byte
	puts        int
	opens       int
}

func newIdleResultMemoryStore() *idleResultMemoryStore {
	return &idleResultMemoryStore{objects: map[evidencestore.ObjectLocation][]byte{}}
}

func (s *idleResultMemoryStore) OpenLocation(
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

func (s *idleResultMemoryStore) PutImmutable(
	_ context.Context,
	identity evidencestore.ObjectIdentity,
	content io.Reader,
) (evidencestore.Receipt, error) {
	s.puts++
	if s.winnerOnPut != nil {
		s.objects[identity.Location()] = append([]byte(nil), s.winnerOnPut...)
		s.winnerOnPut = nil
		return idleResultReceipt(identity, evidencestore.CreateOutcomeExisting), nil
	}
	payload, err := io.ReadAll(content)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	s.objects[identity.Location()] = payload
	return idleResultReceipt(identity, evidencestore.CreateOutcomeCreated), nil
}

func idleResultReceipt(
	identity evidencestore.ObjectIdentity,
	outcome evidencestore.CreateOutcome,
) evidencestore.Receipt {
	return evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: identity, Outcome: outcome,
	}
}
