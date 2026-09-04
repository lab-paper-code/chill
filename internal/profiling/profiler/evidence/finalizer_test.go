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
	requireStoreCalls(t, store,
		"open:"+record.RawMeasurement.Key,
		"open:"+request.FinalLocation.Key,
		"put:"+request.FinalLocation.Key,
		"open:"+request.FinalLocation.Key,
	)

	// Simulate controller restart and later mutable cluster observation. The
	// deterministic location must recover the first immutable record.
	restarted, err := NewFinalizer(store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	request.FinalizedAt = request.FinalizedAt.Add(time.Minute)
	request.Terminal.NodeWorkload.ObservedAt = request.Terminal.NodeWorkload.ObservedAt.Add(time.Minute)
	request.Terminal.NodeWorkload.Pods = append(request.Terminal.NodeWorkload.Pods, WorkloadPod{
		Namespace: "later", Name: "unrelated", UID: "later-pod", Phase: "Running",
	})
	store.calls = nil
	reused, err := restarted.Finalize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Reused || reused.Reference != created.Reference || store.puts != 1 {
		t.Fatalf("restart finalization = %+v, first = %+v, puts = %d", reused, created, store.puts)
	}
	requireStoreCalls(t, store,
		"open:"+record.RawMeasurement.Key,
		"open:"+request.FinalLocation.Key,
	)
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
	requireStoreCalls(t, store,
		"open:"+record.RawMeasurement.Key,
		"open:"+request.FinalLocation.Key,
		"put:"+request.FinalLocation.Key,
		"open:"+request.FinalLocation.Key,
	)
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
	requireStoreCalls(t, store,
		"open:"+record.RawMeasurement.Key,
		"open:"+request.FinalLocation.Key,
		"put:"+request.FinalLocation.Key,
		"open:"+request.FinalLocation.Key,
	)
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

	t.Run("raw receipt mismatch", func(t *testing.T) {
		store := newFakeObjectStore()
		store.objects[record.RawMeasurement.Location()] = append(rawBytes, '\n')
		finalizer, err := NewFinalizer(store, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := finalizer.Finalize(context.Background(), requestFrom(record)); !errors.Is(err, evidencestore.ErrContentMismatch) {
			t.Fatalf("Finalize() error = %v, want ErrContentMismatch", err)
		}
		if store.puts != 0 {
			t.Fatal("final evidence was written after raw verification failure")
		}
	})

	t.Run("invalid existing final object", func(t *testing.T) {
		store := newFakeObjectStore()
		store.objects[record.RawMeasurement.Location()] = rawBytes
		request := requestFrom(record)
		store.objects[request.FinalLocation] = []byte(`{"schemaVersion":"foreign"}`)
		finalizer, err := NewFinalizer(store, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := finalizer.Finalize(context.Background(), request); err == nil {
			t.Fatal("expected invalid existing evidence rejection")
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
		if _, err := finalizer.Finalize(context.Background(), requestFrom(record)); err == nil {
			t.Fatal("expected retained-record validation failure")
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
	putOutcome      evidencestore.CreateOutcome
	openErrors      map[int]error
	openCalls       int
	calls           []string
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: map[evidencestore.ObjectLocation][]byte{}}
}

func (s *fakeObjectStore) OpenLocation(
	_ context.Context,
	location evidencestore.ObjectLocation,
) (io.ReadCloser, error) {
	s.openCalls++
	s.calls = append(s.calls, "open:"+location.Key)
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
	s.calls = append(s.calls, "put:"+identity.Key)
	payload, err := io.ReadAll(content)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	location := identity.Location()
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
	return fakeReceipt(identity, outcome), nil
}

func requireStoreCalls(t *testing.T, store *fakeObjectStore, want ...string) {
	t.Helper()
	if fmt.Sprint(store.calls) != fmt.Sprint(want) {
		t.Fatalf("store calls = %v, want %v", store.calls, want)
	}
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
