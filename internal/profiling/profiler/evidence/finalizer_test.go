package evidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestFinalizerKeepsImmutableWinnerWithOpaqueRawReference(t *testing.T) {
	config := loadRunConfig(t)
	raw := validRaw(t, config)
	winner := validExecutionEvidence(t, config, raw)
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	request := requestFrom(winner)
	request.FinalizedAt = request.FinalizedAt.Add(time.Minute)
	winner.RawMeasurement.Key = " opaque winner raw.json "
	winner.RawMeasurement.Size++
	winnerBytes, err := winner.CanonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	winnerBytes = append(winnerBytes, '\n')
	for _, race := range []bool{false, true} {
		store := newFakeObjectStore()
		store.objects[winner.RawMeasurement.Location()] = append(rawBytes, '\n')
		if race {
			store.objects[request.RawReceipt.Object.Location()] = append(rawBytes, '\n')
			store.winnerOnPut = winnerBytes
		} else {
			store.objects[request.FinalLocation] = winnerBytes
		}
		finalizer, err := NewFinalizer(store, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		result, err := finalizer.Finalize(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		loader, err := NewLoader(store, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		verified, err := loader.Load(context.Background(), result.Reference)
		if err != nil {
			t.Fatal(err)
		}
		retained, _, err := verified.Records()
		if err != nil || !result.Reused || !retained.FinalizedAt.Equal(winner.FinalizedAt) ||
			retained.RawMeasurement.Location() != winner.RawMeasurement.Location() {
			t.Fatalf("immutable winner was not recovered: %+v err=%v", retained, err)
		}
	}
}

func TestFinalizerReadsWrittenObjectDespiteReceiptMetadata(t *testing.T) {
	config := loadRunConfig(t)
	raw := validRaw(t, config)
	record := validExecutionEvidence(t, config, raw)
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	store := newFakeObjectStore()
	store.objects[record.RawMeasurement.Location()] = append(rawBytes, '\n')
	store.alteredReceipt = true
	finalizer, err := NewFinalizer(store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	result, err := finalizer.Finalize(context.Background(), requestFrom(record))
	if err != nil {
		t.Fatal(err)
	}
	loader, err := NewLoader(store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.Load(context.Background(), result.Reference); err != nil {
		t.Fatalf("written evidence was not readable: %v", err)
	}
}

func TestFinalizerCreatesAndReusesExistingEvidence(t *testing.T) {
	config := loadRunConfig(t)
	raw := validRaw(t, config)
	record := validExecutionEvidence(t, config, raw)
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	store := newFakeObjectStore()
	store.objects[record.RawMeasurement.Location()] = rawBytes
	finalizer, err := NewFinalizer(store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	request := requestFrom(record)

	created, err := finalizer.Finalize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if created.Reused || store.puts != 1 {
		t.Fatalf("first finalization = %+v, puts = %d", created, store.puts)
	}

	// Simulate controller restart and later mutable cluster observation. The
	// deterministic location must recover the first immutable record.
	restarted, err := NewFinalizer(store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	request.FinalizedAt = request.FinalizedAt.Add(time.Minute)
	request.Terminal.Node.UID = "later-node-uid"
	delete(store.objects, record.RawMeasurement.Location())
	reused, err := restarted.Finalize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Reused || reused.Reference != created.Reference || store.puts != 1 {
		t.Fatalf("restart finalization = %+v, first = %+v, puts = %d", reused, created, store.puts)
	}
}

func TestFinalizerReportsConcurrentImmutableWinnerAsReused(t *testing.T) {
	config := loadRunConfig(t)
	raw := validRaw(t, config)
	record := validExecutionEvidence(t, config, raw)
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	store := newFakeObjectStore()
	store.objects[record.RawMeasurement.Location()] = rawBytes
	store.putOutcome = evidencestore.CreateOutcomeExisting
	finalizer, err := NewFinalizer(store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	request := requestFrom(record)

	result, err := finalizer.Finalize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reused || result.SchemaVersion != ExecutionEvidenceSchemaVersion || store.puts != 1 {
		t.Fatalf("concurrent finalization = %+v, puts = %d", result, store.puts)
	}
}

func TestFinalizerPreservesReadAfterCreateError(t *testing.T) {
	config := loadRunConfig(t)
	raw := validRaw(t, config)
	record := validExecutionEvidence(t, config, raw)
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	readErr := errors.New("retained read failed")
	store := newFakeObjectStore()
	store.objects[record.RawMeasurement.Location()] = rawBytes
	store.openErrors = map[int]error{3: readErr}
	finalizer, err := NewFinalizer(store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	request := requestFrom(record)

	_, err = finalizer.Finalize(context.Background(), request)
	wantError := "read finalized evidence after create: retained read failed"
	if !errors.Is(err, readErr) || err.Error() != wantError {
		t.Fatalf("Finalize() error = %v, want %q wrapping %q", err, wantError, readErr)
	}
}

func TestFinalizerAndLoaderPreserveIdleRawSchema(t *testing.T) {
	config := idleRunConfig(t)
	raw := validIdleRaw(t, config)
	record := validIdleExecutionEvidence(t, config, raw)
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	store := newFakeObjectStore()
	store.objects[record.RawMeasurement.Location()] = rawBytes
	finalizer, err := NewFinalizer(store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	result, err := finalizer.Finalize(context.Background(), requestFrom(record))
	if err != nil {
		t.Fatal(err)
	}
	loader, err := NewLoader(store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := loader.LoadIdle(context.Background(), result.Reference)
	if err != nil {
		t.Fatal(err)
	}
	loadedExecution, loadedRaw, err := verified.Records()
	if err != nil {
		t.Fatal(err)
	}
	if loadedExecution.RunConfig.Measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode ||
		loadedRaw.Runtime.InferenceCountAfterModelLoad != 0 || !loadedRaw.Runtime.ModelResidentAtEnd {
		t.Fatalf("idle evidence schema drifted during finalization: %#v %#v", loadedExecution, loadedRaw)
	}
	if _, err := loader.Load(context.Background(), result.Reference); err == nil {
		t.Fatal("active loader accepted retained idle raw evidence")
	}
}

func TestFinalizerAndLoaderPreserveActuatedEvidenceSchema(t *testing.T) {
	t.Parallel()
	_, raw, record := validActuatedExecutionEvidence(t)
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	store := newFakeObjectStore()
	store.objects[record.RawMeasurement.Location()] = rawBytes
	finalizer, err := NewFinalizer(store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	result, err := finalizer.Finalize(context.Background(), requestFrom(record))
	if err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != ExecutionEvidenceSchemaVersion {
		t.Fatalf("finalized schemaVersion = %q", result.SchemaVersion)
	}
	loader, err := NewLoader(store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := loader.Load(context.Background(), result.Reference)
	if err != nil {
		t.Fatal(err)
	}
	execution, _, err := verified.Records()
	if err != nil {
		t.Fatal(err)
	}
	if execution.Actuation == nil || !execution.Actuation.ReadyAt.Equal(record.Actuation.ReadyAt) {
		t.Fatalf("loaded actuation facts = %#v", execution.Actuation)
	}
}

func TestFinalizerFailsClosed(t *testing.T) {
	config := loadRunConfig(t)
	raw := validRaw(t, config)
	record := validExecutionEvidence(t, config, raw)
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("malformed existing final object", func(t *testing.T) {
		store := newFakeObjectStore()
		store.objects[record.RawMeasurement.Location()] = rawBytes
		request := requestFrom(record)
		store.objects[request.FinalLocation] = []byte("{")
		finalizer, err := NewFinalizer(store, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := finalizer.Finalize(context.Background(), request); !errors.Is(err, ErrPermanentValidation) {
			t.Fatalf("invalid existing evidence error = %v, want permanent validation failure", err)
		}
		if store.puts != 0 {
			t.Fatal("invalid existing evidence was overwritten")
		}
	})

	t.Run("writer result independently verified", func(t *testing.T) {
		store := newFakeObjectStore()
		store.objects[record.RawMeasurement.Location()] = rawBytes
		store.corruptAfterPut = true
		finalizer, err := NewFinalizer(store, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := finalizer.Finalize(context.Background(), requestFrom(record)); !errors.Is(err, ErrPermanentValidation) {
			t.Fatalf("corrupt retained record error = %v, want permanent validation failure", err)
		}
	})

	t.Run("bounded read", func(t *testing.T) {
		store := newFakeObjectStore()
		store.objects[record.RawMeasurement.Location()] = rawBytes
		finalizer, err := NewFinalizer(store, int64(len(rawBytes)-1))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := finalizer.Finalize(context.Background(), requestFrom(record)); err == nil {
			t.Fatal("expected oversized object rejection")
		}
	})
}

func requestFrom(record ExecutionEvidence) FinalizeRequest {
	return FinalizeRequest{
		RawReceipt: evidencestore.Receipt{
			SchemaVersion: evidencestore.ReceiptSchemaVersion,
			Object:        record.RawMeasurement,
			Outcome:       evidencestore.CreateOutcomeCreated,
		},
		FinalLocation: evidencestore.ObjectLocation{
			Store: record.RawMeasurement.Store,
			Key:   "runs/run-uid/execution.json",
		},
		Terminal:    record.Terminal,
		Actuation:   record.Actuation,
		FinalizedAt: record.FinalizedAt,
	}
}

type fakeObjectStore struct {
	objects         map[evidencestore.ObjectLocation][]byte
	puts            int
	corruptAfterPut bool
	winnerOnPut     []byte
	alteredReceipt  bool
	putOutcome      evidencestore.CreateOutcome
	openErrors      map[int]error
	openCalls       int
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: map[evidencestore.ObjectLocation][]byte{}}
}

func (s *fakeObjectStore) OpenLocation(
	_ context.Context,
	location evidencestore.ObjectLocation,
) (io.ReadCloser, error) {
	s.openCalls++
	if err := s.openErrors[s.openCalls]; err != nil {
		return nil, err
	}
	payload, ok := s.objects[location]
	if !ok {
		return nil, evidencestore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func (s *fakeObjectStore) PutImmutable(
	_ context.Context,
	identity evidencestore.ObjectIdentity,
	content io.Reader,
) (evidencestore.Receipt, error) {
	s.puts++
	payload, err := io.ReadAll(content)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	location := identity.Location()
	if s.winnerOnPut != nil {
		s.objects[location] = s.winnerOnPut
		s.winnerOnPut = nil
		return fakeReceipt(identity, evidencestore.CreateOutcomeExisting), nil
	}
	if _, ok := s.objects[location]; ok {
		return fakeReceipt(identity, evidencestore.CreateOutcomeExisting), nil
	}
	s.objects[location] = append([]byte(nil), payload...)
	if s.corruptAfterPut {
		s.objects[location] = []byte(fmt.Sprintf("corrupt-%d", len(payload)))
	}
	outcome := s.putOutcome
	if outcome == "" {
		outcome = evidencestore.CreateOutcomeCreated
	}
	receipt := fakeReceipt(identity, outcome)
	if s.alteredReceipt {
		receipt.SchemaVersion = ""
		receipt.Object.Key = "unrelated receipt metadata"
	}
	return receipt, nil
}

func fakeReceipt(
	identity evidencestore.ObjectIdentity,
	outcome evidencestore.CreateOutcome,
) evidencestore.Receipt {
	return evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion,
		Object:        identity,
		Outcome:       outcome,
	}
}
