package serving

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// A real file-backed immutable boundary keeps archive tests about retained
// objects and failures, not about a mock returning the input it received.
type observationArchive struct {
	directory string
	fail      bool
}

func (a *observationArchive) PutImmutable(_ context.Context, identity evidencestore.ObjectIdentity, source io.Reader) (evidencestore.Receipt, error) {
	if a.fail {
		return evidencestore.Receipt{}, errors.New("archive unavailable")
	}
	if err := identity.Validate(); err != nil {
		return evidencestore.Receipt{}, err
	}
	name := filepath.Join(a.directory, identity.Key)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return evidencestore.Receipt{}, err
	}
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	outcome := evidencestore.CreateOutcomeCreated
	if errors.Is(err, os.ErrExist) {
		outcome = evidencestore.CreateOutcomeExisting
	} else if err != nil {
		return evidencestore.Receipt{}, err
	} else {
		n, copyErr := io.Copy(file, source)
		if copyErr == nil {
			copyErr = file.Sync()
		}
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || n != identity.Size {
			return evidencestore.Receipt{}, errors.Join(
				copyErr, closeErr, errors.New("incomplete archive write"), os.Remove(name),
			)
		}
	}
	return evidencestore.Receipt{SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: identity, Outcome: outcome}, nil
}

type pausedObservationArchive struct {
	observationArchive
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
	mu      sync.Mutex
	puts    map[string]int
}

func (a *pausedObservationArchive) PutImmutable(ctx context.Context, identity evidencestore.ObjectIdentity, source io.Reader) (evidencestore.Receipt, error) {
	a.mu.Lock()
	a.puts[identity.Key]++
	a.mu.Unlock()
	pause := false
	a.once.Do(func() {
		pause = true
		close(a.entered)
	})
	if pause {
		<-a.resume
	}
	return a.observationArchive.PutImmutable(ctx, identity, source)
}

func awaitObservationLogOperation(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("observation log operation blocked behind remote publication")
	}
}

func TestObservationLogUploadAllowsRuntimeIngressRotationAndConcurrentClose(t *testing.T) {
	ctx := context.Background()
	archive := &pausedObservationArchive{
		observationArchive: observationArchive{directory: t.TempDir()},
		entered:            make(chan struct{}), resume: make(chan struct{}), puts: map[string]int{},
	}
	observationLog, err := NewObservationLog(ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: archive,
	})
	if err != nil {
		t.Fatal(err)
	}
	var release sync.Once
	resume := func() { release.Do(func() { close(archive.resume) }) }
	t.Cleanup(func() {
		resume()
		_ = observationLog.Close(ctx)
	})
	runtime, err := NewRuntime(ctx, RuntimeConfig{QueueLimit: 1, ObservationLog: observationLog})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.cancel)
	definition := &Definition{Name: "workload", UID: "uid", Generation: 1, Model: Model{UID: "model", Generation: 1}, SLO: SLO{Percentile: 99, TargetLatency: time.Second}}
	if err := runtime.Publish(definition); err != nil {
		t.Fatal(err)
	}
	appendRequest := func() Receipt {
		t.Helper()
		var receipt Receipt
		done := make(chan error, 1)
		go func() {
			var err error
			receipt, err = runtime.Receive(definition.Name, time.Now())
			if err == nil {
				err = runtime.Response(receipt, "rejected", "InvalidInput")
			}
			done <- err
		}()
		awaitObservationLogOperation(t, done)
		return receipt
	}
	expire := func() {
		observationLog.mu.Lock()
		observationLog.windows[observationLog.active].EndsAt = time.Now().Add(-time.Second)
		observationLog.mu.Unlock()
	}
	receipts := []Receipt{appendRequest()}
	expire()
	flushDone := make(chan error, 1)
	go func() { flushDone <- observationLog.Flush(ctx) }()
	select {
	case <-archive.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("flush never reached the file-backed archive")
	}
	receipts = append(receipts, appendRequest())
	expire()
	receipts = append(receipts, appendRequest())

	var health ObservationLogHealth
	var observations ObservationResult
	readDone := make(chan error, 1)
	go func() {
		health = observationLog.Health()
		var err error
		observations, err = observationLog.Query(definition.UID, time.Time{}, time.Time{}, nil, nil, 0, "")
		readDone <- err
	}()
	awaitObservationLogOperation(t, readDone)
	if !health.Healthy || len(health.Windows) != 3 || health.Windows[0].Published ||
		health.Windows[0].ClosedAt == nil || health.Windows[1].ClosedAt == nil || health.Windows[2].ClosedAt != nil {
		t.Fatalf("upload blocked or changed local rotation: %+v", health)
	}
	if observations.Total != 3 || observations.Outcomes["rejected"] != 3 || len(observations.Requests) != 6 {
		t.Fatalf("runtime receipt/response facts unavailable during upload: %+v", observations)
	}

	operations := []func(context.Context) error{observationLog.Flush, observationLog.Close, observationLog.Flush, observationLog.Close}
	started := make(chan struct{}, len(operations))
	done := make(chan error, len(operations))
	for _, operation := range operations {
		go func(operation func(context.Context) error) {
			started <- struct{}{}
			done <- operation(ctx)
		}(operation)
	}
	for range operations {
		<-started
	}
	resume()
	awaitObservationLogOperation(t, flushDone)
	for range operations {
		awaitObservationLogOperation(t, done)
	}
	checkArchivedRuntimeRequests(t, archive, observationLog.Health(), receipts)
}

func checkArchivedRuntimeRequests(t *testing.T, archive *pausedObservationArchive, health ObservationLogHealth, receipts []Receipt) {
	t.Helper()
	if !health.Healthy || len(health.Windows) != 3 {
		t.Fatalf("concurrent publication changed retained windows: %+v", health)
	}
	retained := make(map[string]map[string]int)
	for _, window := range health.Windows {
		if window.ClosedAt == nil || !window.Published || window.Incomplete || len(window.Objects) != 5 {
			t.Fatalf("closed window lost publication: %+v", window)
		}
		for _, object := range window.Objects {
			if _, err := os.Stat(filepath.Join(archive.directory, object.Key)); err != nil {
				t.Fatal(err)
			}
		}
		checkArchivedRequestStream(t, archive.directory, window, retained)
	}
	if len(retained) != len(receipts) {
		t.Fatalf("publication lost or invented runtime requests: %+v", retained)
	}
	for _, receipt := range receipts {
		if events := retained[receipt.ID]; len(events) != 2 || events[phaseReceived] != 1 || events[eventResponseFinished] != 1 {
			t.Fatalf("publication lost or duplicated request %s: %+v", receipt.ID, events)
		}
	}
	archive.mu.Lock()
	defer archive.mu.Unlock()
	if len(archive.puts) != 15 {
		t.Fatalf("publication skipped closed-window objects: %+v", archive.puts)
	}
	for key, count := range archive.puts {
		if count != 1 {
			t.Fatalf("concurrent Flush/Close published %s %d times", key, count)
		}
	}
}

func checkArchivedRequestStream(t *testing.T, directory string, window ObservationWindow, retained map[string]map[string]int) {
	t.Helper()
	file, err := os.Open(filepath.Join(directory, "serving", window.ID, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	decoder := json.NewDecoder(file)
	var header ObservationWindow
	if err := decoder.Decode(&header); err != nil {
		t.Fatal(err)
	}
	if header.ID != window.ID {
		t.Fatalf("archived stream belongs to another window: %+v", header)
	}
	for {
		var record observationRecord
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if record.Window != window.ID || record.Request == nil {
			t.Fatalf("invalid archived request fact: %+v", record)
		}
		event := record.Request
		if retained[event.RequestID] == nil {
			retained[event.RequestID] = map[string]int{}
		}
		retained[event.RequestID][event.Kind]++
	}
}

func TestObservationLogCohortsSurviveRestartAndRegistration(t *testing.T) {
	ctx := context.Background()
	archive := &observationArchive{directory: t.TempDir()}
	config := ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: archive}
	observationLog, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	first := &Definition{Name: "same-name", UID: "old-uid", Generation: 1, SLO: SLO{Percentile: 95, TargetLatency: time.Second}}
	changed := &Definition{Name: "same-name", UID: "old-uid", Generation: 2, SLO: SLO{Percentile: 99, TargetLatency: 2 * time.Second}}
	replacement := &Definition{Name: "same-name", UID: "new-uid", Generation: 1, SLO: first.SLO}
	for _, d := range []*Definition{first, changed, replacement} {
		if err := observationLog.DefinitionRecord(d); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	appendEvent := func(id string, d *Definition, kind, outcome string, elapsed int64) {
		t.Helper()
		if err := observationLog.RequestEvent(RequestEvent{RequestID: id, WorkloadName: d.Name, WorkloadUID: d.UID, Definition: d.Identity(), Kind: kind, Outcome: outcome, At: now.Add(time.Duration(elapsed)), ReceivedAt: now, ElapsedNanos: elapsed}); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent("finished", first, "received", "", 0)
	appendEvent("finished", first, "accepted", "", 0)
	appendEvent("finished", first, "response_finished", "completed", 100)
	appendEvent("finished", first, "backend_finished", "error", 200)
	appendEvent("unfinished", changed, "received", "", 0)
	appendEvent("unfinished", changed, "accepted", "", 0)
	appendEvent("unfinished", changed, "backend_finished", "completed", 300)
	appendEvent("unfinished", changed, "response_finished", outcomeIncomplete, 350)
	appendEvent("delivery", first, "received", "", 0)
	appendEvent("delivery", first, "response_finished", "delivery_failed", 250)
	appendEvent("canceled", first, "received", "", 0)
	appendEvent("canceled", first, "accepted", "", 0)
	appendEvent("canceled", first, "response_finished", "canceled", 500)
	appendEvent("new", replacement, "received", "", 0)
	appendEvent("new", replacement, "response_finished", "completed", 999)
	if err := observationLog.ManagementEvent(ManagementEvent{WorkloadUID: first.UID, Kind: "delete", Phase: "applied", Before: changed, At: now}); err != nil {
		t.Fatal(err)
	}
	checkObservationLogCohortPopulation(t, observationLog, archive, first, changed, now)
	restored, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := restored.Query(first.UID, now, now.Add(time.Second), []string{changed.Identity()}, []string{outcomeIncomplete}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if selected.Total != 1 || selected.Selected != 1 || len(selected.Management) != 1 || selected.Cohorts[0].QuantileNanos != nil {
		t.Fatalf("restart/filter changed facts: %+v", selected)
	}
	newUID, err := restored.Query(replacement.UID, now, now.Add(time.Second), nil, nil, 0, "")
	if err != nil || newUID.Total != 1 {
		t.Fatalf("registration mixed UID: %+v %v", newUID, err)
	}
	empty, err := restored.Query(first.UID, now.Add(time.Second), now.Add(2*time.Second), nil, nil, 0, "")
	if err != nil || empty.Total != 0 {
		t.Fatalf("empty interval: %+v %v", empty, err)
	}
	if err := restored.Close(ctx); err != nil {
		t.Fatal(err)
	}
	config.Definitions = []string{first.Identity()}
	config.Outcomes = []string{"completed"}
	configured, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	defaultSelection, err := configured.Query(first.UID, now, now.Add(time.Second), nil, nil, 0, "")
	if err != nil || defaultSelection.Total != 3 || defaultSelection.Selected != 1 || len(defaultSelection.Cohorts) != 1 || defaultSelection.Cohorts[0].SampleCount != 1 || defaultSelection.Cohorts[0].QuantileNanos == nil || *defaultSelection.Cohorts[0].QuantileNanos != 100 {
		t.Fatalf("configured population ignored or invalid outcomes entered latency: %+v %v", defaultSelection, err)
	}
	if err := configured.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func checkObservationLogCohortPopulation(
	t *testing.T, observationLog *ObservationLog, archive *observationArchive, first, changed *Definition, now time.Time,
) {
	t.Helper()
	before, err := observationLog.Query(first.UID, now, now.Add(time.Second), nil, nil, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if before.Total != 4 || before.Selected != 4 ||
		before.Outcomes[outcomeIncomplete] != 1 || before.Outcomes["completed"] != 1 ||
		before.Outcomes["delivery_failed"] != 1 || before.Outcomes["canceled"] != 1 ||
		len(before.Requests) != 1 || before.NextCursor == "" || len(before.Cohorts) != 2 {
		t.Fatalf("unexpected accounting: %+v", before)
	}
	for _, c := range before.Cohorts {
		if c.Definition == changed.Identity() && (c.SampleCount != 0 || c.QuantileNanos != nil) {
			t.Fatalf("invented incomplete latency: %+v", c)
		}
		if c.Definition == first.Identity() &&
			(c.SampleCount != 1 || c.QuantileNanos == nil || *c.QuantileNanos != 100) {
			t.Fatalf("wrong response population: %+v", c)
		}
	}
	if err := observationLog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	health := observationLog.Health()
	if !health.Windows[0].Published {
		t.Fatalf("window not archived: %+v", health)
	}
	for _, object := range health.Windows[0].Objects {
		if _, err := os.Stat(filepath.Join(archive.directory, object.Key)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestObservationLogArchiveFailureAndInterruptedWindow(t *testing.T) {
	ctx := context.Background()
	archive := &observationArchive{directory: t.TempDir(), fail: true}
	config := ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: archive}
	observationLog, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, kind := range []string{"received", "accepted"} {
		if err := observationLog.RequestEvent(RequestEvent{RequestID: "interrupted", WorkloadUID: "uid", Kind: kind, At: now, ReceivedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate process death, retaining raw streams and an open-window metadata.
	observationLog.mu.Lock()
	for _, file := range observationLog.files {
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	observationLog.closed = true
	observationLog.mu.Unlock()
	oldWindow := observationLog.Health().Windows[0]
	file, err := os.OpenFile(filepath.Join(config.Directory, oldWindow.ID, "requests.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{\"request\":"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := restored.Query("uid", time.Time{}, time.Time{}, nil, nil, 0, "")
	if err != nil || result.Outcomes[outcomeIncomplete] != 1 || len(result.IncompleteWindows) != 1 {
		t.Fatalf("lost interrupted record: %+v %v", result, err)
	}
	if err := restored.Close(ctx); err == nil {
		t.Fatal("archive failure was hidden")
	}
	for _, w := range restored.Health().Windows {
		if w.Published {
			t.Fatal("failed archive marked published")
		}
	}
	archive.fail = false
	if err := restored.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	for _, w := range restored.Health().Windows {
		if !w.Published {
			t.Fatal("archive retry did not publish")
		}
	}
}

func TestObservationLogArchiveFailureDoesNotPoisonRetainedRecords(t *testing.T) {
	ctx := context.Background()
	archive := &observationArchive{directory: t.TempDir(), fail: true}
	observationLog, err := NewObservationLog(ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: archive,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := observationLog.RequestEvent(RequestEvent{
		RequestID: "retained", WorkloadUID: "uid", Kind: "received", At: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := observationLog.Close(ctx); err == nil {
		t.Fatal("archive failure was hidden")
	}
	if !observationLog.Health().Healthy || observationLog.Health().Windows[0].Published {
		t.Fatal("archive-only failure changed local durability or claimed publication")
	}
	archive.fail = false
	if err := observationLog.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := observationLog.Query("uid", time.Time{}, time.Time{}, nil, nil, 0, "")
	if err != nil || result.Total != 1 || !observationLog.Health().Healthy || !observationLog.Health().Windows[0].Published {
		t.Fatalf("retained event or health lost after publication: %+v %v", result, err)
	}
}

func TestObservationLogAppendFailurePublishesIncompleteWindow(t *testing.T) {
	archive := &observationArchive{directory: t.TempDir()}
	directory := t.TempDir()
	observationLog, err := NewObservationLog(ObservationLogConfig{
		Directory: directory, Store: "evidence", Window: time.Hour, Writer: archive,
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := observationLog.files["requests"]
	if err := requests.Close(); err != nil {
		t.Fatal(err)
	}
	observationLog.files["requests"], err = os.Open(filepath.Join(directory, observationLog.Health().Windows[0].ID, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := observationLog.RequestEvent(RequestEvent{
		RequestID: "lost", WorkloadUID: "uid", Kind: "received", At: time.Now().UTC(),
	}); err == nil {
		t.Fatal("readonly stream accepted an event")
	}
	if err := observationLog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	health := observationLog.Health()
	if health.Healthy {
		t.Fatal("event loss did not close local durability health")
	}
	data, err := os.ReadFile(filepath.Join(archive.directory, "serving", health.Windows[0].ID, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var window ObservationWindow
	if err := json.Unmarshal(data, &window); err != nil {
		t.Fatal(err)
	}
	if !window.Incomplete {
		t.Fatal("missing event was archived as a complete window")
	}
}
