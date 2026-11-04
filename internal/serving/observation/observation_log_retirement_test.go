package observation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A replacement must reopen the original retained root after any retirement
// unlink; it must not mistake already-published partial raw files for damage.
func TestInterruptedRetirementReopensRetainedOwner(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	owner, err := AcquireObservationOwnership(directory, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	archive := &observationArchive{directory: t.TempDir()}
	log, clock := clockedObservationLog(t, ObservationLogConfig{Directory: directory, Archive: archive}, observationTestHour)
	seedObservationMinutes(t, log, clock, observationTestHour.Add(time.Second))
	clock.set(observationTestHour.Add(3 * time.Hour))
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	seal, err := readObservationSeal(log.root, observationTestHour)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := observationTestWorker(log, archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	var receipt observationUpload
	receiptPath := observationReceiptPath(worker.config.ControlDirectory, observationTestHour)
	if err := readControlJSON(receiptPath, &receipt); err != nil {
		t.Fatal(err)
	}
	if err := validateObservationUpload(receipt, seal, log.config.Store); err != nil {
		t.Fatal(err)
	}
	if len(receipt.Objects) != 4 || len(receipt.AbsentStreams) != 2 {
		t.Fatalf("unexpected all-stream receipt: %+v", receipt)
	}
	var victim string
	for _, stream := range seal.Streams {
		if stream.Kind == observationRequestStream && len(stream.Segments) > 0 {
			victim = stream.Segments[0].RelativePath
		}
	}
	if victim == "" {
		t.Fatal("no sealed request segment")
	}
	interrupted := errors.New("interrupted after first retirement unlink")
	log.removeAll = func(path string) error {
		if err := os.Remove(filepath.Join(path, victim)); err != nil {
			return err
		}
		return interrupted
	}
	if err := log.Flush(ctx); !errors.Is(err, interrupted) {
		t.Fatalf("retirement did not reach interrupted unlink: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := log.Close(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("close: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	replacementOwner, err := AcquireObservationOwnership(directory, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacementOwner.Close() }()
	replacement, err := NewObservationLog(log.config)
	if err != nil {
		t.Fatalf("replacement failed after interrupted published-hour retirement: %v", err)
	}
	defer func() { _ = replacement.Close(cancelled) }()
	if hours, err := replacement.hours(); err != nil || len(hours) != 0 {
		t.Fatalf("retired partial hour remains in restore namespace: %v %v", hours, err)
	}
	for _, object := range receipt.Objects {
		if rows := gunzipLines(t, filepath.Join(archive.directory, object.Object.Key)); len(rows) == 0 {
			t.Fatalf("retirement lost published %s history", object.Stream)
		}
	}
	entries, err := os.ReadDir(replacement.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("interrupted retirement leaked local backlog: %s", entry.Name())
		}
	}
}

func TestIntactUploadedPinnedHourRestoresRetainedOwner(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	owner, err := AcquireObservationOwnership(directory, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	archive := &observationArchive{directory: t.TempDir()}
	log, clock := clockedObservationLog(t, ObservationLogConfig{Directory: directory, Archive: archive}, observationTestHour)
	assignObservationRequest(t, log, "pinned", observationTestHour.Add(time.Second))
	clock.set(observationTestHour.Add(3 * time.Hour))
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	worker, err := observationTestWorker(log, archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	seal, err := readObservationSeal(log.root, observationTestHour)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := observationReceiptPath(worker.config.ControlDirectory, observationTestHour)
	var receipt observationUpload
	if err := readControlJSON(receiptPath, &receipt); err != nil {
		t.Fatal(err)
	}
	if err := validateObservationUpload(receipt, seal, log.config.Store); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	replacementOwner, err := AcquireObservationOwnership(directory, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacementOwner.Close() }()
	replacement, err := NewObservationLog(log.config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacement.Close(ctx) }()
	if _, pending := replacement.RuntimeFacts(); len(pending) != 1 || pending[0].RequestID != "pinned" {
		t.Fatalf("intact uploaded hour lost pinned ownership: %+v", pending)
	}
	if err := replacement.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := checkSealedInputs(replacement.root, seal, false); err != nil {
		t.Fatalf("restart retired intact pinned hour: %v", err)
	}
	if err := readControlJSON(receiptPath, &receipt); err != nil {
		t.Fatalf("restart discarded intact pinned receipt: %v", err)
	}
}
