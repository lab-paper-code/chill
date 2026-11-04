package observation

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// Writers belong to the test's worker, never to the serving recorder. Tests
// without an archive reader continue to exercise their local-only gap policy.
var observationTestWriters sync.Map

func (a *observationArchive) List(ctx context.Context, prefix string) ([]evidencestore.ObjectIdentity, error) {
	return (&readableObservationArchive{observationArchive: a}).List(ctx, prefix)
}

func (a *observationArchive) Open(ctx context.Context, object evidencestore.ObjectIdentity) (io.ReadCloser, error) {
	return os.Open(filepath.Join(a.directory, object.Key))
}

func observationTestWorker(log *ObservationLog, writer evidencestore.StreamingImmutableWriter) (*ArchiveWorker, error) {
	return NewArchiveWorker(ArchiveWorkerConfig{Directory: log.config.Directory,
		ControlDirectory: filepath.Join(log.config.Directory, "archive-control"), Store: log.config.Store, Writer: writer})
}

// Consumer tests explicitly cross both process boundaries: serving seals, the
// concrete worker publishes a durable receipt, then serving alone retires.
func flushObservationArchive(log *ObservationLog, ctx context.Context) error {
	if err := log.Flush(ctx); err != nil {
		return err
	}
	writer, ok := observationTestWriters.Load(log)
	if !ok {
		return errors.New("test worker writer not registered")
	}
	worker, err := observationTestWorker(log, writer.(evidencestore.StreamingImmutableWriter))
	if err != nil {
		return err
	}
	if err := worker.Scan(ctx); err != nil {
		return err
	}
	return log.Flush(ctx)
}

func TestSealedHourSurvivesOwnerAndWorkerReplacement(t *testing.T) {
	ctx := context.Background()
	archive := &observationArchive{directory: t.TempDir()}
	log, clock := clockedObservationLog(t, ObservationLogConfig{Archive: archive}, observationTestHour)
	assignObservationRequest(t, log, "pending", observationTestHour.Add(time.Second))
	clock.set(observationTestHour.Add(3 * time.Hour))
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(archive.outcomes) != 0 {
		t.Fatal("serving uploaded sealed inputs")
	}
	seal, err := readObservationSeal(log.root, observationTestHour)
	if err != nil || len(seal.Streams) != len(observationStreams) {
		t.Fatalf("seal: %+v %v", seal, err)
	}
	if err := log.Close(ctx); err != nil {
		t.Fatal(err)
	}
	owner, err := AcquireObservationOwnership(log.config.Directory, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	restarted, restartedClock := clockedObservationLog(t, log.config, observationTestHour.Add(3*time.Hour))
	if _, pending := restarted.RuntimeFacts(); len(pending) != 1 || pending[0].RequestID != "pending" {
		t.Fatalf("same-root pending assignment lost: %+v", pending)
	}
	// A worker can finish while serving is closed, and does not compete with its lock.
	worker, err := observationTestWorker(restarted, archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	path := observationReceiptPath(worker.config.ControlDirectory, observationTestHour)
	var receipt observationUpload
	if err := readControlJSON(path, &receipt); err != nil {
		t.Fatal(err)
	}
	if err := validateObservationUpload(receipt, seal, restarted.config.Store); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(restarted.root, seal.Hour)); err != nil {
		t.Fatal("worker/receipt bypassed unsettled barrier", err)
	}
	// Rollback cannot amend a sealed assignment source or its catalog.
	restartedClock.set(observationTestHour)
	if err := restarted.RequestClosed("pending"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(restarted.root, seal.Hour)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("settled receipt did not retire hour", err)
	}
	rows := readRequestRows(t, restarted.config.Directory)
	if len(rows) != 1 || rows[0].Kind != "final" {
		t.Fatalf("later final missing: %+v", rows)
	}
}

func TestSealedInputsNeverRepairOrFollowLinks(t *testing.T) {
	for _, damage := range []string{"torn", "truncated", "symlink"} {
		t.Run(damage, func(t *testing.T) {
			log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
			seedObservationMinutes(t, log, clock, observationTestHour.Add(time.Second))
			clock.set(observationTestHour.Add(time.Hour))
			if err := log.Flush(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := log.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(log.root, observationHour(observationTestHour), "requests.00.jsonl")
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "torn":
				original[len(original)-1] = 'x'
				err = os.WriteFile(path, original, 0600)
			case "truncated":
				err = os.Truncate(path, int64(len(original)-1))
			case "symlink":
				err = os.Remove(path)
				if err == nil {
					err = os.Symlink(filepath.Join(log.root, observationDefinitionsFile), path)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if reopened, err := NewObservationLog(log.config); err == nil {
				_ = reopened.Close(context.Background())
				t.Fatal("damaged seal accepted or repaired")
			}
			if damage == "torn" {
				got, err := os.ReadFile(path)
				if err != nil || len(got) != len(original) || got[len(got)-1] != 'x' {
					t.Fatal("sealed input tail repaired", err)
				}
			}
			worker, err := observationTestWorker(log, &observationArchive{directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.Scan(context.Background()); err == nil {
				t.Fatal("worker accepted damaged seal")
			}
			if _, err := os.Stat(observationReceiptPath(worker.config.ControlDirectory, observationTestHour)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("damaged hour has receipt", err)
			}
		})
	}
}

func TestUnsealedCrashAndReceiptRecovery(t *testing.T) {
	ctx := context.Background()
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	seedObservationMinutes(t, log, clock, observationTestHour.Add(time.Second))
	if err := log.Close(ctx); err != nil {
		t.Fatal(err)
	}
	hourDir := filepath.Join(log.root, observationHour(observationTestHour))
	if _, err := os.Stat(filepath.Join(hourDir, "seal.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Close sealed current hour", err)
	}
	// Neither interrupted marker is a committed seal/receipt.
	if err := os.WriteFile(filepath.Join(hourDir, ".handoff-crash"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(log.config.Directory, "archive-control", ".handoff-crash"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := &observationArchive{directory: t.TempDir()}
	worker, err := observationTestWorker(log, archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Scan(ctx); err != nil || len(archive.outcomes) != 0 {
		t.Fatal("worker guessed unsealed inputs", err)
	}
	restarted, _ := clockedObservationLog(t, log.config, observationTestHour.Add(3*time.Hour))
	if err := restarted.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := worker.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	path := observationReceiptPath(worker.config.ControlDirectory, observationTestHour)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// Simulate uploads committed before a crash, with no final local receipt.
	if err := worker.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	for _, outcome := range archive.outcomes {
		if outcome != evidencestore.CreateOutcomeExisting {
			t.Fatalf("retry did not reuse immutable object: %s", outcome)
		}
	}
	if err := restarted.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hourDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("receipt recovery did not retire raw hour", err)
	}
}

func TestWorkerFlockDoesNotOwnServingDirectory(t *testing.T) {
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	seedObservationMinutes(t, log, clock, observationTestHour.Add(time.Second))
	clock.set(observationTestHour.Add(time.Hour))
	archive := &observationArchive{directory: t.TempDir()}
	first, err := observationTestWorker(log, archive)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := first.acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	second, err := observationTestWorker(log, archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Scan(context.Background()); err == nil {
		t.Fatal("overlapping worker acquired archiver lock")
	}
	if err := log.Flush(context.Background()); err != nil {
		t.Fatal("worker lock blocked serving seal", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Scan(context.Background()); err != nil {
		t.Fatal("replacement worker did not recover lock", err)
	}
}

func TestMissingAndInvalidReceiptsKeepContiguousRetirementBarrier(t *testing.T) {
	ctx := context.Background()
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	for _, hour := range []time.Time{observationTestHour, observationTestHour.Add(time.Hour)} {
		clock.set(hour)
		if err := log.SchedulerRecord(SchedulerObservation{At: hour}); err != nil {
			t.Fatal(err)
		}
	}
	clock.set(observationTestHour.Add(4 * time.Hour))
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	archive := &observationArchive{directory: t.TempDir(), failKey: "20261020T10/"}
	worker, err := observationTestWorker(log, archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Scan(ctx); err == nil {
		t.Fatal("older publication failure lost")
	}
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	for _, hour := range []time.Time{observationTestHour, observationTestHour.Add(time.Hour)} {
		if _, err := os.Stat(filepath.Join(log.root, observationHour(hour))); err != nil {
			t.Fatal("retirement skipped missing older receipt", err)
		}
	}
	path := observationReceiptPath(worker.config.ControlDirectory, observationTestHour)
	invalid := observationUpload{SchemaVersion: observationUploadSchema, Hour: observationHour(observationTestHour), Objects: []observationUploadedObject{}, AbsentStreams: append([]string{}, observationStreams...)}
	if err := atomicObservationJSON(path, invalid); err != nil {
		t.Fatal(err)
	}
	if err := log.Flush(ctx); err == nil {
		t.Fatal("receipt claiming a present stream absent accepted")
	}
	if _, err := os.Stat(filepath.Join(log.root, observationHour(observationTestHour.Add(time.Hour)))); err != nil {
		t.Fatal("invalid older receipt removed newer hour", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	archive.failKey = ""
	if err := worker.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	for _, hour := range []time.Time{observationTestHour, observationTestHour.Add(time.Hour)} {
		if _, err := os.Stat(filepath.Join(log.root, observationHour(hour))); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("completed receipts did not retire contiguous hours", err)
		}
	}
}
