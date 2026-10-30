package serving

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// SCH-AC-52: a raw snapshot must neither rewrite bytes nor export the open minute.
func TestRawObservationClosedMinutesAndDefinitions(t *testing.T) {
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	ctx := context.Background()
	hour := observationHour(observationTestHour)
	clock.set(observationTestHour.Add(time.Second))
	if err := log.SchedulerRecord(SchedulerObservation{At: clock.now()}); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(log.root, hour, "scheduler.00.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.OpenRaw(ctx, hour, "scheduler"); rawErrorCode(err) != "ObservationNotReady" {
		t.Fatalf("open minute: %v", err)
	}
	clock.set(observationTestHour.Add(time.Minute + time.Second))
	if err := log.SchedulerRecord(SchedulerObservation{At: clock.now()}); err != nil {
		t.Fatal(err)
	}
	index, err := log.RawHour(ctx, hour)
	if err != nil || index.Status != "open" || !index.At.Equal(clock.now()) {
		t.Fatalf("index: %+v %v", index, err)
	}
	for i, kind := range index.Kinds {
		if i > 0 && index.Kinds[i-1].Kind >= kind.Kind {
			t.Fatalf("unordered kinds: %+v", index.Kinds)
		}
		if kind.Kind == "scheduler" && !reflect.DeepEqual(kind.Segments, []RawSegment{{Minute: "00", Bytes: int64(len(first))}}) {
			t.Fatalf("open minute leaked: %+v", kind)
		}
	}
	raw, err := log.OpenRaw(ctx, hour, "scheduler")
	if err != nil {
		t.Fatal(err)
	}
	// Rotation and removal after capture cannot invalidate the open descriptor.
	clock.set(observationTestHour.Add(2 * time.Minute))
	if err := log.SchedulerRecord(SchedulerObservation{At: clock.now()}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(log.root, hour, "scheduler.00.jsonl")); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(raw.Reader)
	if closeErr := raw.Reader.Close(); err != nil || closeErr != nil || !bytes.Equal(body, first) || raw.Bytes != int64(len(first)) || raw.ContentType != "application/x-ndjson" {
		t.Fatalf("local bytes changed: %q %v %v", body, err, closeErr)
	}
	if _, err := log.definitions.WriteString("{}\n{\"torn\":"); err != nil {
		t.Fatal(err)
	}
	definitions, err := log.OpenRawDefinitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.definitions.WriteString("true}\n"); err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(definitions.Reader)
	if closeErr := definitions.Reader.Close(); err != nil || closeErr != nil || string(body) != "{}\n" || definitions.Bytes != 3 {
		t.Fatalf("definitions snapshot: %q %v %v", body, err, closeErr)
	}
}

func rawErrorCode(err error) string {
	var domain *Error
	if errors.As(err, &domain) {
		return domain.Code
	}
	return ""
}

// This wrapper supplies real object lengths, including for pre-evidence streams.
type rawTestArchive struct{ *readableObservationArchive }

func (a rawTestArchive) List(ctx context.Context, prefix string) ([]evidencestore.ObjectIdentity, error) {
	objects, err := a.readableObservationArchive.List(ctx, prefix)
	for i := range objects {
		info, statErr := os.Stat(filepath.Join(a.directory, objects[i].Key))
		if statErr != nil {
			return nil, statErr
		}
		objects[i].Size = info.Size()
	}
	return objects, err
}

func TestRawObservationArchiveLocalPriorityAndOldStreams(t *testing.T) {
	archive := rawTestArchive{newReadableObservationArchive(t)}
	log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
	hour := observationHour(observationTestHour)
	key := writeArchivedObservation(t, archive.readableObservationArchive, observationTestHour, []byte("{\"original\": true}\n"))
	stored, err := os.ReadFile(filepath.Join(archive.directory, key))
	if err != nil {
		t.Fatal(err)
	}
	clock.set(observationTestHour.Add(time.Hour))
	index, err := log.RawHour(context.Background(), hour)
	if err != nil || index.Status != "archived" {
		t.Fatalf("archive index: %+v %v", index, err)
	}
	for _, kind := range index.Kinds {
		if kind.Kind == "scheduler" {
			if !kind.Available || kind.Archive == nil || kind.Archive.Bytes != int64(len(stored)) || len(kind.Segments) != 0 {
				t.Fatalf("archive location: %+v", kind)
			}
		} else if kind.Available || kind.Archive != nil {
			t.Fatalf("old archive invented stream: %+v", kind)
		}
	}
	raw, err := log.OpenRaw(context.Background(), hour, "scheduler")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(raw.Reader)
	_ = raw.Reader.Close()
	if err != nil || !bytes.Equal(body, stored) || raw.ContentType != "application/gzip" {
		t.Fatalf("gzip rewritten: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(log.root, hour), 0700); err != nil {
		t.Fatal(err)
	}
	index, err = log.RawHour(context.Background(), hour)
	if err != nil || index.Status != "closed-local" {
		t.Fatalf("local directory priority: %+v %v", index, err)
	}
	if _, err := log.OpenRaw(context.Background(), hour, "scheduler"); rawErrorCode(err) != observationUnavailableCode {
		t.Fatalf("mixed local and archive: %v", err)
	}
	if archive.opens != 1 || archive.lists != 2 {
		t.Fatalf("local path contacted archive: %+v", archive)
	}
	for _, badHour := range []string{"", "20261030T24", "2026103T01", "../../20261030T01", "20261030T01/requests"} {
		if _, err := log.RawHour(context.Background(), badHour); rawErrorCode(err) != "InvalidQuery" {
			t.Fatalf("unsafe hour accepted: %q %v", badHour, err)
		}
	}
	if _, err := log.OpenRaw(context.Background(), hour, "../definitions"); rawErrorCode(err) != "InvalidQuery" {
		t.Fatalf("unsafe kind accepted: %v", err)
	}
}

func TestRawObservationArchiveFailuresAndCancellation(t *testing.T) {
	archive := rawTestArchive{newReadableObservationArchive(t)}
	log, _ := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour.Add(time.Hour))
	hour := observationHour(observationTestHour)
	writeArchivedObservation(t, archive.readableObservationArchive, observationTestHour, []byte("{}\n"))
	failure := errors.New("store read failed")
	archive.listError = failure
	if _, err := log.RawHour(context.Background(), hour); !errors.Is(err, failure) {
		t.Fatalf("list failure hidden: %v", err)
	}
	archive.listError = nil
	archive.openError = failure
	if _, err := log.OpenRaw(context.Background(), hour, "scheduler"); !errors.Is(err, failure) {
		t.Fatalf("open failure hidden: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	archive.openError = nil
	raw, err := log.OpenRaw(ctx, hour, "scheduler")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := io.ReadAll(raw.Reader); !errors.Is(err, context.Canceled) {
		t.Fatalf("read ignored cancellation: %v", err)
	}
	_ = raw.Reader.Close()
	if _, err := log.RawHour(ctx, hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("list ignored cancellation: %v", err)
	}
	if _, err := log.RawHour(context.Background(), "20000101T00"); rawErrorCode(err) != observationUnavailableCode {
		t.Fatalf("missing hour: %v", err)
	}
}

func TestRawObservationCancellationClosesBlockedReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	defer func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	}()
	raw := newRawObservation(ctx, reader, []io.Closer{reader}, "application/gzip", 1)
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, raw.Reader); done <- err }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled archive read succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close archive read")
	}
	if err := raw.Reader.Close(); err != nil && !strings.Contains(err.Error(), "closed") {
		t.Fatal(err)
	}
}
