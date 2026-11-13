package observation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

func scientificReaders(log *ObservationLog, at time.Time) []struct {
	name string
	read func(context.Context) error
} {
	return []struct {
		name string
		read func(context.Context) error
	}{
		{"query", func(ctx context.Context) error {
			_, err := log.Query(ctx, "w", at, at.Add(time.Minute), nil, nil, 0, "", "")
			return err
		}},
		{"load", func(ctx context.Context) error { _, err := log.LoadWindow(ctx, at, at.Add(time.Minute)); return err }},
		{"scheduler", func(ctx context.Context) error {
			_, _, _, _, err := log.SchedulerObservationPage(ctx, at, at.Add(time.Minute), 1, 0, SchedulerObservationSelection{})
			return err
		}},
		{"occupancy", func(ctx context.Context) error {
			_, err := log.ScanOccupancyRows(ctx, "w", "", at, at.Add(time.Minute), func(serving.RequestRow) {})
			return err
		}},
		{"capacity", func(ctx context.Context) error { _, err := log.CapacityRecords(ctx, []string{"capacity"}); return err }},
		{"events", func(ctx context.Context) error { _, _, _, err := log.EventObservations(ctx, at, 1, ""); return err }},
		{"raw-index", func(ctx context.Context) error { _, err := log.RawHour(ctx, observationHour(at)); return err }},
		{"raw-stream", func(ctx context.Context) error {
			raw, err := log.OpenRaw(ctx, observationHour(at), observationSchedulerStream)
			if err == nil {
				err = raw.Reader.Close()
			}
			return err
		}},
		{"definitions", func(ctx context.Context) error {
			raw, err := log.OpenRawDefinitions(ctx)
			if err == nil {
				err = raw.Reader.Close()
			}
			return err
		}},
	}
}

func TestScientificReadersWaitForWrittenPrefixWithoutBlockingProducers(t *testing.T) {
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	var once sync.Once
	log.writeFile = func(file *os.File, raw []byte) (int, error) {
		once.Do(func() { close(entered); <-release })
		return file.Write(raw)
	}
	if err := log.SchedulerRecord(SchedulerObservation{At: observationTestHour}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not reach the stalled append")
	}
	clock.set(observationTestHour.Add(time.Minute))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readers := scientificReaders(log, observationTestHour)
	done := make(chan error, len(readers))
	for _, reader := range readers {
		go func(read func(context.Context) error) { done <- read(ctx) }(reader.read)
	}
	producer := make(chan error, 1)
	go func() {
		err := log.RequestEvent(serving.RequestEvent{RequestID: "live", WorkloadUID: "w", Kind: "received", At: clock.now(), ReceivedAt: clock.now()})
		if err == nil {
			err = log.ManagementEvent(serving.ManagementEvent{WorkloadUID: "w", At: clock.now()})
		}
		producer <- err
	}()
	awaitObservationLogOperation(t, producer)
	select {
	case err := <-done:
		t.Fatalf("scientific reader passed an unwritten prefix: %v", err)
	default:
	}
	cancel()
	for range readers {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled written-prefix read succeeded: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reader cancellation waited for the storage worker")
		}
	}
	resume()
	if _, err := log.writeBarrier(context.Background(), false); err != nil {
		t.Fatal(err)
	}
}

func TestScientificReadersRejectFailedWrittenPrefix(t *testing.T) {
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	failure := errors.New("scientific prefix write failed")
	log.writeFile = func(*os.File, []byte) (int, error) { return 0, failure }
	if err := log.SchedulerRecord(SchedulerObservation{At: observationTestHour}); err != nil {
		t.Fatal(err)
	}
	for _, reader := range scientificReaders(log, observationTestHour) {
		t.Run(reader.name, func(t *testing.T) {
			if err := reader.read(context.Background()); !errors.Is(err, failure) {
				t.Fatalf("reader returned partial success after writer failure: %v", err)
			}
		})
	}
}

func TestArchiveCannotPublishOrDeleteQueuedPreviousHour(t *testing.T) {
	archive := newReadableObservationArchive(t)
	log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	var once sync.Once
	log.writeFile = func(file *os.File, raw []byte) (int, error) {
		once.Do(func() { close(entered); <-release })
		return file.Write(raw)
	}
	at := observationTestHour.Add(time.Second)
	clock.set(at)
	if err := log.SchedulerRecord(SchedulerObservation{At: at}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not reach the previous hour")
	}
	clock.set(observationTestHour.Add(3 * time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	archived := make(chan error, 1)
	go func() { archived <- log.archive(ctx) }()
	producer := make(chan error, 1)
	go func() { producer <- log.SchedulerRecord(SchedulerObservation{At: clock.now()}) }()
	awaitObservationLogOperation(t, producer)
	cancel()
	select {
	case err := <-archived:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("archive crossed a queued prefix: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("archive frontier ignored cancellation")
	}
	hour := observationHour(observationTestHour)
	if _, err := os.Stat(filepath.Join(log.root, hour)); err != nil {
		t.Fatalf("queued hour removed: %v", err)
	}
	key := observationArchivePrefix + hour + "/" + observationSchedulerStream + ".jsonl.gz"
	if _, err := os.Stat(filepath.Join(archive.directory, key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("queued hour published: %v", err)
	}
	resume()
	if err := log.archive(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(log.root, hour)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sealed committed hour was not retained away: %v", err)
	}
	page, _, _, _, err := log.SchedulerObservationPage(context.Background(), at, at.Add(time.Second), 1, 0, SchedulerObservationSelection{})
	if err != nil || len(page) != 1 || !page[0].At.Equal(at) {
		t.Fatalf("archive lost the delayed original event: %+v %v", page, err)
	}
}

func TestWrittenPrefixCaptureRetainsLiveRowsAndFinalPrecedence(t *testing.T) {
	const versionedRequestID = "versioned"
	ctx := context.Background()
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	at := observationTestHour.Add(time.Second)
	clock.set(at)
	appendAssignedRequest(t, log, versionedRequestID, at)
	if err := log.RequestEvent(serving.RequestEvent{RequestID: versionedRequestID, WorkloadUID: "w", Kind: "response_finished", At: at.Add(time.Second), Outcome: outcomeCompleted}); err != nil {
		t.Fatal(err)
	}
	if _, err := log.writeBarrier(ctx, false); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	var once sync.Once
	log.writeFile = func(file *os.File, raw []byte) (int, error) {
		once.Do(func() { close(entered); <-release })
		return file.Write(raw)
	}
	if err := log.RequestClosed(versionedRequestID, serving.RequestTimings{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("final did not reach storage")
	}
	// These folds are newer than the completed marker. The pending final must
	// neither remove the newer version nor turn capture into a frozen prefix.
	if err := log.RequestEvent(serving.RequestEvent{RequestID: versionedRequestID, WorkloadUID: "w", Kind: "response_finished", At: at.Add(2 * time.Second), Outcome: outcomeDeliveryFailed}); err != nil {
		t.Fatal(err)
	}
	if err := log.RequestEvent(serving.RequestEvent{RequestID: "live-after-marker", WorkloadUID: "w", Kind: "received", At: at, ReceivedAt: at}); err != nil {
		t.Fatal(err)
	}
	// Exercise the same memory-capture/merge stage after the already completed
	// marker while a later disk command is blocked: no extra MVCC cutoff.
	read, segments := log.captureSnapshot(map[string][2]time.Time{observationRequestStream: {}}, nil)
	for stream, files := range segments {
		for _, segment := range files {
			if err := log.openReadFile(read, stream, segment); err != nil {
				read.close()
				t.Fatal(err)
			}
		}
	}
	rows, err := log.requestRows(read, func(*requestEnvelope) bool { return true })
	read.close()
	if err != nil || len(rows) != 2 {
		t.Fatalf("mixed capture lost post-marker live rows: %+v %v", rows, err)
	}
	for _, row := range rows {
		if row.RequestID == versionedRequestID && row.Outcome != outcomeDeliveryFailed {
			t.Fatalf("assigned row masked a newer live fold: %+v", row)
		}
	}
	resume()
	result, err := log.Query(ctx, "w", at, at.Add(time.Minute), nil, nil, 0, "", "")
	if err != nil || result.Total != 2 {
		t.Fatalf("query lost the written-prefix/live mixture: %+v %v", result, err)
	}
	for _, row := range result.Requests {
		if row.RequestID == versionedRequestID && (row.Kind != serving.RequestRowFinal || row.Outcome != outcomeCompleted) {
			t.Fatalf("newer live fold incorrectly masked committed final: %+v", row)
		}
	}
	load, err := log.LoadWindow(ctx, at, at.Add(time.Minute))
	if err != nil || len(load.Arrivals) != 2 {
		t.Fatalf("load lost mixed live receipts: %+v %v", load, err)
	}
	if err := log.RequestClosed(versionedRequestID, serving.RequestTimings{}); err != nil {
		t.Fatal(err)
	}
	result, err = log.Query(ctx, "w", at, at.Add(time.Minute), nil, nil, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range result.Requests {
		if row.RequestID == versionedRequestID && row.Outcome != outcomeDeliveryFailed {
			t.Fatalf("exact-version acknowledgement dropped the next final: %+v", row)
		}
	}
}
