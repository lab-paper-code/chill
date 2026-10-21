package serving

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	failKey   string
	mu        sync.Mutex
	outcomes  map[string]evidencestore.CreateOutcome
	// puts counts every call per key, failed ones included.
	puts map[string]int
}

func (a *observationArchive) PutImmutable(_ context.Context, identity evidencestore.ObjectIdentity, source io.Reader) (evidencestore.Receipt, error) {
	a.mu.Lock()
	if a.puts == nil {
		a.puts = map[string]int{}
	}
	a.puts[identity.Key]++
	a.mu.Unlock()
	if a.fail || a.failKey != "" && strings.Contains(identity.Key, a.failKey) {
		return evidencestore.Receipt{}, errors.New("archive unavailable")
	}
	if err := identity.Validate(); err != nil {
		return evidencestore.Receipt{}, err
	}
	data, err := io.ReadAll(source)
	if err != nil || int64(len(data)) != identity.Size {
		return evidencestore.Receipt{}, errors.Join(err, errors.New("incomplete archive write"))
	}
	name := filepath.Join(a.directory, identity.Key)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return evidencestore.Receipt{}, err
	}
	outcome := evidencestore.CreateOutcomeCreated
	if existing, err := os.ReadFile(name); err == nil {
		if !bytes.Equal(existing, data) {
			return evidencestore.Receipt{}, errors.New("immutable object conflict")
		}
		outcome = evidencestore.CreateOutcomeExisting
	} else if err := os.WriteFile(name, data, 0600); err != nil {
		return evidencestore.Receipt{}, err
	}
	a.mu.Lock()
	if a.outcomes == nil {
		a.outcomes = map[string]evidencestore.CreateOutcome{}
	}
	a.outcomes[identity.Key] = outcome
	a.mu.Unlock()
	return evidencestore.Receipt{SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: identity, Outcome: outcome}, nil
}

type pausedObservationArchive struct {
	*observationArchive
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (a *pausedObservationArchive) PutImmutable(ctx context.Context, identity evidencestore.ObjectIdentity, source io.Reader) (evidencestore.Receipt, error) {
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

// observationClock chooses the write time, and so the segment file, in tests.
type observationClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *observationClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *observationClock) set(at time.Time) {
	c.mu.Lock()
	c.at = at
	c.mu.Unlock()
}

var observationTestHour = time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)

// clockedObservationLog writes at clock time; its process started at started.
func clockedObservationLog(t *testing.T, config ObservationLogConfig, started time.Time) (*ObservationLog, *observationClock) {
	t.Helper()
	if config.Directory == "" {
		config.Directory = t.TempDir()
	}
	if config.Store == "" {
		config.Store = "evidence"
	}
	if config.Writer == nil {
		config.Writer = &observationArchive{directory: t.TempDir()}
	}
	observationLog, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	clock := &observationClock{at: started}
	observationLog.now, observationLog.started, observationLog.floor = clock.now, started, started
	t.Cleanup(func() { _ = observationLog.Close(context.Background()) })
	return observationLog, clock
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

func TestObservationLogUploadAllowsRuntimeIngressAndConcurrentClose(t *testing.T) {
	ctx := context.Background()
	archive := &pausedObservationArchive{
		observationArchive: &observationArchive{directory: t.TempDir()},
		entered:            make(chan struct{}), resume: make(chan struct{}),
	}
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour.Add(59*time.Minute))
	var release sync.Once
	resume := func() { release.Do(func() { close(archive.resume) }) }
	t.Cleanup(resume)
	runtime, err := NewRuntime(ctx, RuntimeConfig{QueueLimit: 1, ObservationLog: observationLog})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.cancel)
	definition := &Definition{Name: "workload", UID: "uid", Generation: 1, Model: Model{UID: "model", Generation: 1}, SLO: SLO{Percentile: 99, TargetLatency: time.Second}}
	if err := runtime.Publish(definition); err != nil {
		t.Fatal(err)
	}
	appendRequest := func() {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			receipt, err := runtime.Receive(definition.Name, time.Now())
			if err == nil {
				err = runtime.Response(receipt, "rejected", "InvalidInput")
			}
			done <- err
		}()
		awaitObservationLogOperation(t, done)
	}
	appendRequest()
	clock.set(observationTestHour.Add(time.Hour + 30*time.Second))
	flushDone := make(chan error, 1)
	go func() { flushDone <- observationLog.Flush(ctx) }()
	select {
	case <-archive.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("flush never reached the file-backed archive")
	}
	appendRequest()
	appendRequest()
	var observations ObservationResult
	readDone := make(chan error, 1)
	go func() {
		var err error
		observations, err = observationLog.Query(definition.UID, time.Time{}, time.Time{}, nil, nil, 0, "")
		readDone <- err
	}()
	awaitObservationLogOperation(t, readDone)
	if observations.Total != 3 || observations.Outcomes["rejected"] != 3 || len(observations.Requests) != 3 {
		t.Fatalf("request rows unavailable during upload: %+v", observations)
	}
	operations := []func(context.Context) error{observationLog.Flush, observationLog.Close, observationLog.Flush, observationLog.Close}
	done := make(chan error, len(operations))
	for _, operation := range operations {
		go func(operation func(context.Context) error) { done <- operation(ctx) }(operation)
	}
	resume()
	awaitObservationLogOperation(t, flushDone)
	for range operations {
		awaitObservationLogOperation(t, done)
	}
	rows := gunzipLines(t, filepath.Join(archive.directory, observationArchivePrefix+"20261020T10", "requests.jsonl.gz"))
	if len(rows) != 1 || !strings.Contains(rows[0], `"kind":"final"`) {
		t.Fatalf("past hour archive lost or invented rows: %q", rows)
	}
	if !observationLog.Health().Healthy {
		t.Fatalf("concurrent archive passes changed local health: %+v", observationLog.Health())
	}
}

func gunzipLines(t *testing.T, name string) []string {
	t.Helper()
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func TestObservationSegmentsFollowWriteMinute(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	for _, at := range []time.Time{observationTestHour.Add(3*time.Minute + 59*time.Second), observationTestHour.Add(4 * time.Minute), observationTestHour.Add(time.Hour)} {
		clock.set(at)
		// The event time does not choose the file; the write time does.
		if err := observationLog.ManagementEvent(ManagementEvent{WorkloadUID: "uid", Kind: "apply", At: observationTestHour}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"20261020T10/management.03.jsonl", "20261020T10/management.04.jsonl", "20261020T11/management.00.jsonl"} {
		data, err := os.ReadFile(filepath.Join(observationLog.root, name))
		if err != nil || bytes.Count(data, []byte{'\n'}) != 1 {
			t.Fatalf("%s: %q %v", name, data, err)
		}
	}
}

func seedObservationMinutes(t *testing.T, observationLog *ObservationLog, clock *observationClock, minutes ...time.Time) {
	t.Helper()
	for index, at := range minutes {
		clock.set(at)
		id := "request-" + at.Format("1504")
		d := &Definition{Name: "workload", UID: "uid", Generation: int64(index + 1)}
		if err := observationLog.DefinitionRecord(d); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{phaseReceived, phaseAccepted, eventResponseFinished} {
			if err := observationLog.RequestEvent(RequestEvent{RequestID: id, WorkloadUID: "uid", Definition: d.Identity(), Kind: kind, At: at, ReceivedAt: at, Outcome: map[string]string{eventResponseFinished: outcomeCompleted}[kind]}); err != nil {
				t.Fatal(err)
			}
		}
		if err := observationLog.RequestClosed(id); err != nil {
			t.Fatal(err)
		}
		snapshot := SchedulerSnapshot{At: at, Workloads: []*Definition{d}}
		if err := observationLog.SchedulerRecord(SchedulerObservation{At: at, Snapshot: &snapshot}); err != nil {
			t.Fatal(err)
		}
		if err := observationLog.ManagementEvent(ManagementEvent{WorkloadUID: "uid", Kind: "apply", At: at}); err != nil {
			t.Fatal(err)
		}
	}
}

// A past hour no longer changes, so an hour is stored once per process and
// only a failed hour is sent again. Re-sending stored hours every minute drew
// S3 connection resets (D-R6-7).
func TestObservationArchiveRetriesFailedHour(t *testing.T) {
	ctx := context.Background()
	archive := &observationArchive{directory: t.TempDir(), failKey: "scheduler"}
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
	seedObservationMinutes(t, observationLog, clock, observationTestHour.Add(10*time.Second), observationTestHour.Add(time.Minute+10*time.Second))
	hour := filepath.Join(observationLog.root, "20261020T10")
	clock.set(observationTestHour.Add(time.Hour + 5*time.Minute))
	if err := observationLog.ManagementEvent(ManagementEvent{WorkloadUID: "uid", Kind: "apply", At: clock.now()}); err != nil {
		t.Fatal(err)
	}
	if err := observationLog.Flush(ctx); err == nil {
		t.Fatal("failed stream upload was hidden")
	}
	archive.failKey = ""
	clock.set(observationTestHour.Add(time.Hour + 6*time.Minute))
	if err := observationLog.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	for _, stream := range observationStreams {
		var want []string
		for _, minute := range []string{"00", "01"} {
			data, err := os.ReadFile(filepath.Join(hour, stream+"."+minute+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			want = append(want, strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")...)
		}
		got := gunzipLines(t, filepath.Join(archive.directory, observationArchivePrefix+"20261020T10", stream+".jsonl.gz"))
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s archive differs from its minute files:\n got %q\nwant %q", stream, got, want)
		}
	}
	if _, err := os.Stat(hour); err != nil {
		t.Fatalf("the previous hour was deleted: %v", err)
	}
	clock.set(observationTestHour.Add(2*time.Hour + time.Minute))
	if err := observationLog.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hour); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uploaded hour was kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(observationLog.root, "20261020T11")); err != nil {
		t.Fatalf("the previous hour was deleted: %v", err)
	}
	for _, stream := range observationStreams {
		key := observationArchivePrefix + "20261020T10/" + stream + ".jsonl.gz"
		if archive.puts[key] != 2 {
			t.Fatalf("%s sent %d times, want 2", stream, archive.puts[key])
		}
	}
}

func TestObservationStartTruncatesTornLines(t *testing.T) {
	config := ObservationLogConfig{Directory: t.TempDir()}
	observationLog, clock := clockedObservationLog(t, config, observationTestHour)
	config = observationLog.config
	seedObservationMinutes(t, observationLog, clock, observationTestHour.Add(time.Second))
	if err := observationLog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	torn := []string{
		filepath.Join(observationLog.root, "20261020T10", "management.00.jsonl"),
		filepath.Join(observationLog.root, observationDefinitionsFile),
	}
	for _, name := range torn {
		file, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(`{"workloadUID":"uid","kind":`); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	restored, _ := clockedObservationLog(t, config, observationTestHour)
	for _, name := range torn {
		data, err := os.ReadFile(name)
		if err != nil || bytes.Count(data, []byte{'\n'}) != 1 || data[len(data)-1] != '\n' {
			t.Fatalf("%s kept a torn line: %q %v", name, data, err)
		}
	}
	if err := restored.ManagementEvent(ManagementEvent{WorkloadUID: "uid", Kind: "apply", At: observationTestHour}); err != nil {
		t.Fatal(err)
	}
	result, err := restored.Query("uid", time.Time{}, time.Time{}, nil, nil, 0, "")
	if err != nil || len(result.Management) != 2 || result.Total != 1 || len(result.Definitions) != 1 {
		t.Fatalf("repaired files lost records: %+v %v", result, err)
	}
}

func TestObservationQueriesOpenOnlyTheirRange(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	from := observationTestHour.Add(30 * time.Minute)
	seedObservationMinutes(t, observationLog, clock, observationTestHour.Add(time.Second), from.Add(-30*time.Second), from.Add(time.Second))
	// Files outside each query's range would fail any read.
	for _, name := range []string{"requests.00", "management.00", "management.31", "scheduler.00", "scheduler.33"} {
		if err := os.WriteFile(filepath.Join(observationLog.root, "20261020T10", name+".jsonl"), []byte("not json\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	to := from.Add(2 * time.Second)
	if result, err := observationLog.Query("uid", from, to, nil, nil, 0, ""); err != nil || result.Total != 1 || len(result.Management) != 1 {
		t.Fatalf("workload query: %+v %v", result, err)
	}
	if load, err := observationLog.LoadWindow(from, to); err != nil || len(load.Arrivals) != 1 {
		t.Fatalf("load window: %+v %v", load, err)
	}
	if page, _, _, err := observationLog.SchedulerObservationPage(from, to, 10, 0); err != nil || len(page) != 1 {
		t.Fatalf("scheduler page: %+v %v", page, err)
	}
	if power, err := observationLog.OperatingPower(from, to, nil, 10); err != nil || power.GapUntil != nil {
		t.Fatalf("operating power: %+v %v", power, err)
	}
	if !observationLog.Health().Healthy {
		t.Fatalf("a query opened a file outside its range: %+v", observationLog.Health())
	}
}

func TestObservationGapBeforeLocalRetention(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour.Add(5*time.Minute))
	seedObservationMinutes(t, observationLog, clock, observationTestHour.Add(10*time.Minute))
	early, start := observationTestHour, observationTestHour.Add(5*time.Minute)
	result, err := observationLog.Query("uid", early, early.Add(time.Hour), nil, nil, 0, "")
	if err != nil || result.GapUntil == nil || !result.GapUntil.Equal(start) || result.Total != 1 {
		t.Fatalf("workload gap: %+v %v", result, err)
	}
	load, err := observationLog.LoadWindow(early, early.Add(time.Hour))
	if err != nil || load.GapUntil == nil || !load.GapUntil.Equal(start) {
		t.Fatalf("load gap: %+v %v", load, err)
	}
	power, err := observationLog.OperatingPower(early, early.Add(time.Hour), nil, 10)
	if err != nil || power.GapUntil == nil || power.Complete || !strings.Contains(strings.Join(power.Reasons, " "), "retained locally only") {
		t.Fatalf("power gap: %+v %v", power, err)
	}
	if fresh, err := observationLog.Query("uid", start, start.Add(time.Hour), nil, nil, 0, ""); err != nil || fresh.GapUntil != nil {
		t.Fatalf("retained interval reported a gap: %+v %v", fresh, err)
	}
}

func TestObservationQueryDuringDeletionKeepsResult(t *testing.T) {
	ctx := context.Background()
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	seedObservationMinutes(t, observationLog, clock, observationTestHour.Add(time.Second), observationTestHour.Add(time.Minute))
	want, err := observationLog.Query("uid", time.Time{}, time.Time{}, nil, nil, 0, "")
	if err != nil || want.Total != 2 {
		t.Fatalf("seeded rows: %+v %v", want, err)
	}
	read, err := observationLog.capture(map[string][2]time.Time{observationRequestStream: {}})
	if err != nil {
		t.Fatal(err)
	}
	defer read.close()
	clock.set(observationTestHour.Add(3 * time.Hour))
	if err := observationLog.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(observationLog.root, "20261020T10")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hour was not deleted: %v", err)
	}
	rows, err := observationLog.requestRows(read, func(*requestEnvelope) bool { return true })
	if err != nil || len(rows) != len(want.Requests) {
		t.Fatalf("deletion changed an open read: %+v %v", rows, err)
	}
	// Concurrent reads and deletion under -race.
	seedObservationMinutes(t, observationLog, clock, observationTestHour.Add(3*time.Hour))
	clock.set(observationTestHour.Add(6 * time.Hour))
	done := make(chan error, 1)
	go func() { done <- observationLog.Flush(ctx) }()
	for range 20 {
		if _, err := observationLog.Query("uid", time.Time{}, time.Time{}, nil, nil, 0, ""); err != nil {
			t.Fatal(err)
		}
	}
	awaitObservationLogOperation(t, done)
}

//nolint:gocyclo // Every final row field against its event.
func TestRequestFinalRowMatchesEventCohort(t *testing.T) {
	observationLog, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	d := &Definition{Name: "workload", UID: "uid", Generation: 1, SLO: SLO{Percentile: 95, TargetLatency: time.Second}}
	if err := observationLog.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	received := observationTestHour.Add(time.Second)
	at := func(ms int) time.Time { return received.Add(time.Duration(ms) * time.Millisecond) }
	assignment := &Assignment{Node: NodeReference{Name: "node", UID: "node-uid"}, Instance: "i1", Profile: "p1", DispatchedAt: at(20)}
	native := &BackendProgress{Phase: phaseFinished, BatchID: 7, BatchSize: 2, QueueMS: 3, ServiceMS: 40, BackendMS: 45, NativeElapsedMS: 50, DecisionID: "decision", ExecutionComplete: true}
	for _, e := range []RequestEvent{
		{Kind: phaseReceived, At: received},
		{Kind: "validated", At: at(5), ElapsedNanos: int64(5 * time.Millisecond)},
		{Kind: phaseAccepted, At: at(10), ElapsedNanos: int64(10 * time.Millisecond)},
		{Kind: phaseAssigned, At: at(20), ElapsedNanos: int64(20 * time.Millisecond), Assignment: assignment},
		{Kind: "backend_queued", At: at(30)},
		{Kind: "backend_running", At: at(40)},
		{Kind: "backend_finished", At: at(90), NativeProgress: native, ExecutionComplete: true},
		{Kind: eventBackendExchangeFinished, At: at(95), ElapsedNanos: int64(95 * time.Millisecond), Outcome: outcomeCompleted, ClientSpanNanos: 7},
		{Kind: eventResponseFinished, At: at(100), ElapsedNanos: int64(100 * time.Millisecond), Outcome: outcomeCompleted},
	} {
		e.RequestID, e.WorkloadUID, e.Definition, e.ReceivedAt = restoredRequest, d.UID, d.Identity(), received
		if err := observationLog.RequestEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := observationLog.RequestClosed(restoredRequest); err != nil {
		t.Fatal(err)
	}
	result, err := observationLog.Query(d.UID, received, received.Add(time.Second), nil, nil, 0, "")
	if err != nil || len(result.Requests) != 1 || len(result.Cohorts) != 1 {
		t.Fatalf("final row: %+v %v", result, err)
	}
	row := result.Requests[0]
	ms := func(value *int64) int64 { return *value / int64(time.Millisecond) }
	if row.Kind != requestRowFinal || row.DefinitionID != d.Identity() || row.ProfileID != "p1" || row.Instance != "i1" || row.DecisionID != "decision" ||
		ms(row.ValidatedNanos) != 5 || ms(row.AcceptedNanos) != 10 || ms(row.DispatchedNanos) != 20 || ms(row.QueuedNanos) != 30 ||
		ms(row.RunningNanos) != 40 || ms(row.ExecutedNanos) != 90 || ms(row.ExchangeNanos) != 95 || ms(row.ElapsedNanos) != 100 ||
		row.BatchID != 7 || row.BatchSize != 2 || *row.NativeElapsedMS != 50 || !row.ExecutionComplete || row.ClientSpanNanos != 7 || row.DispatchedAt != nil {
		t.Fatalf("final row fields: %+v", row)
	}
	cohort := result.Cohorts[0]
	if len(cohort.LatencyNanos) != 1 || cohort.LatencyNanos[0] != float64(100*time.Millisecond) ||
		len(cohort.CompletionResidualNanos) != 1 || cohort.CompletionResidualNanos[0] != float64(30*time.Millisecond) {
		t.Fatalf("latency or completion residual: %+v", cohort)
	}
}

func TestObservationLogCohortsSurviveRestartAndRegistration(t *testing.T) {
	ctx := context.Background()
	config := ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Writer: &observationArchive{directory: t.TempDir()}}
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
	for _, id := range []string{"finished", "unfinished", "delivery", "canceled", "new"} {
		if err := observationLog.RequestClosed(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := observationLog.ManagementEvent(ManagementEvent{WorkloadUID: first.UID, Kind: "delete", Phase: "applied", Before: changed, At: now}); err != nil {
		t.Fatal(err)
	}
	checkObservationLogCohortPopulation(t, observationLog, first, changed, now)
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

func checkObservationLogCohortPopulation(t *testing.T, observationLog *ObservationLog, first, changed *Definition, now time.Time) {
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
}

func TestRequestFinalAfterTwoMinutesIsFoundByQuery(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	received := observationTestHour.Add(time.Second)
	assignment := &Assignment{Node: NodeReference{Name: "node", UID: "node-uid"}, Instance: "i1", Profile: "p1", DispatchedAt: received}
	for _, kind := range []string{phaseReceived, phaseAccepted, phaseAssigned} {
		if err := observationLog.RequestEvent(RequestEvent{RequestID: "late", WorkloadUID: "uid", Kind: kind, At: received, ReceivedAt: received, Assignment: assignment}); err != nil {
			t.Fatal(err)
		}
	}
	clock.set(received.Add(2 * time.Minute))
	if err := observationLog.RequestEvent(RequestEvent{RequestID: "late", Kind: eventResponseFinished, At: clock.now(), ReceivedAt: received, ElapsedNanos: int64(2 * time.Minute), Outcome: outcomeCompleted}); err != nil {
		t.Fatal(err)
	}
	if err := observationLog.RequestClosed("late"); err != nil {
		t.Fatal(err)
	}
	result, err := observationLog.Query("uid", received, received.Add(time.Second), nil, nil, 0, "")
	if err != nil || len(result.Requests) != 1 || result.Requests[0].Kind != requestRowFinal || result.Outcomes[outcomeCompleted] != 1 {
		t.Fatalf("late final row missed: %+v %v", result, err)
	}
}

func TestLoadWindowCountsAssignedAndFinalRequestOnce(t *testing.T) {
	observationLog, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	received := observationTestHour.Add(time.Second)
	assignment := &Assignment{Node: NodeReference{Name: "node", UID: "node-uid"}, Instance: "i1", Profile: "p1", DispatchedAt: received.Add(time.Millisecond)}
	for _, e := range []RequestEvent{
		{Kind: phaseReceived, At: received},
		{Kind: "validated", At: received},
		{Kind: phaseAccepted, At: received},
		{Kind: phaseAssigned, At: received.Add(time.Millisecond), Assignment: assignment},
		{Kind: eventResponseFinished, At: received.Add(2 * time.Millisecond), ElapsedNanos: int64(2 * time.Millisecond), Outcome: outcomeCompleted},
	} {
		e.RequestID, e.WorkloadUID, e.ReceivedAt = restoredRequest, "uid", received
		if err := observationLog.RequestEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := observationLog.RequestClosed(restoredRequest); err != nil {
		t.Fatal(err)
	}
	if rows := readRequestRows(t, observationLog.config.Directory); len(rows) != 2 {
		t.Fatalf("assigned and final rows: %+v", rows)
	}
	load, err := observationLog.LoadWindow(received, received.Add(time.Second))
	if err != nil || len(load.Arrivals) != 1 || len(load.Workloads) != 1 || load.Workloads[0].Received != 1 ||
		load.Workloads[0].Accepted != 1 || load.Workloads[0].Assigned != 1 || load.Workloads[0].Completed != 1 || load.Workloads[0].Instances["i1"] != 1 {
		t.Fatalf("load counted a request twice: %+v %v", load, err)
	}
	arrivals, err := observationLog.SchedulerArrivals(received, received.Add(time.Second))
	if err != nil || len(arrivals) != 1 || !arrivals[0].Accepted || !arrivals[0].Compute || arrivals[0].Outcome != outcomeCompleted {
		t.Fatalf("arrivals: %+v %v", arrivals, err)
	}
}

func TestAssignmentAppendFailureRevertsAssignment(t *testing.T) {
	r, i, _ := routingRuntime(t, nil)
	observationLog := r.config.ObservationLog
	// A file where the hour directory belongs makes every append fail.
	observationLog.now = func() time.Time { return observationTestHour }
	if err := os.WriteFile(filepath.Join(observationLog.root, "20261020T10"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, snapshot := schedulerFixture()
	d := snapshot.Workloads[0]
	receipt, err := r.Receive(d.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call, err := r.Accept(receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = call.Wait(context.Background())
	var domain *Error
	if !errors.As(err, &domain) || domain.Code != "ObservationUnavailable" || call.assignment != nil || r.instances[i.ID].outstanding != 0 {
		t.Fatalf("failed assigned row kept the assignment: %v %+v", err, call.assignment)
	}
}

func TestBackendProgressForUnknownRequestWritesNothing(t *testing.T) {
	observationLog, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	// A native log read again after the request closed.
	for _, kind := range []string{phaseReceived, eventResponseFinished} {
		if err := observationLog.RequestEvent(RequestEvent{RequestID: "settled", WorkloadUID: "uid", Kind: kind, At: observationTestHour, Outcome: "rejected"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := observationLog.RequestClosed("settled"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"backend_queued", "backend_running", "backend_finished"} {
		if err := observationLog.RequestEvent(RequestEvent{RequestID: "settled", WorkloadUID: "uid", Kind: kind, At: observationTestHour}); err != nil {
			t.Fatal(err)
		}
	}
	if err := observationLog.RequestClosed("settled"); err != nil {
		t.Fatal(err)
	}
	if rows := readRequestRows(t, observationLog.config.Directory); len(rows) != 1 || rows[0].QueuedNanos != nil || len(observationLog.open) != 0 {
		t.Fatalf("unknown request progress wrote rows: %+v", rows)
	}
}

// Scale: a profile with 19 evidence references, as in the E1 assignment.
func TestRequestRowsStayUnderBudgetWithNineteenEvidenceReferences(t *testing.T) {
	observationLog, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	_, snapshot := schedulerFixture()
	profile := *snapshot.Instances[0].Profile
	profile.Evidence = make([]evidencestore.ObjectIdentity, 19)
	for index := range profile.Evidence {
		profile.Evidence[index] = evidencestore.ObjectIdentity{Store: "evidence", Key: "profiling/runs/node-a/0123456789abcdef/raw/trial-" + strings.Repeat("0", 2) + string(rune('a'+index)) + ".json", Size: 123456}
	}
	d := snapshot.Workloads[0]
	if err := observationLog.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	received := observationTestHour.Add(time.Second)
	at := func(ms int) time.Time { return received.Add(time.Duration(ms) * time.Millisecond) }
	assignment := &Assignment{Node: NodeReference{Name: "edge-node-a", UID: "6f1c8f0e-3b8a-4c6f-9d55-1c2b3a4d5e6f"}, Instance: "pool-6f1c8f0e-instance-0", Profile: profile.ID, DispatchedAt: at(2), ProfileBody: &profile}
	native := &BackendProgress{Phase: phaseFinished, BatchID: 123456, BatchSize: 4, QueueMS: 12.345, ServiceMS: 45.678, BackendMS: 50.123, PreprocessMS: 3.21, PostprocessMS: 1.23, NativeElapsedMS: 61.234, DecisionID: "decision-20261020T100000.123456789Z", ExecutionComplete: true}
	for _, e := range []RequestEvent{
		{Kind: phaseReceived, At: received},
		{Kind: "validated", At: at(1)},
		{Kind: phaseAccepted, At: at(1)},
		{Kind: phaseAssigned, At: at(2), Assignment: assignment},
		{Kind: "backend_queued", At: at(3), NativeProgress: native},
		{Kind: "backend_running", At: at(20), NativeProgress: native},
		{Kind: "backend_finished", At: at(70), NativeProgress: native, ExecutionComplete: true},
		{Kind: eventBackendExchangeFinished, At: at(75), Outcome: outcomeCompleted, ClientSpanNanos: 74123456},
		{Kind: eventResponseFinished, At: at(76), Outcome: outcomeCompleted},
	} {
		e.RequestID, e.WorkloadUID, e.Definition, e.ReceivedAt, e.LifetimeNanos = "0123456789abcdef0123456789abcdef", d.UID, d.Identity(), received, int64(time.Second)
		e.Assignment = assignment
		if err := observationLog.RequestEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := observationLog.RequestClosed("0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	sizes := map[string]int{}
	total := 0
	for _, row := range readRequestRows(t, observationLog.config.Directory) {
		data, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		sizes[row.Kind] = len(data) + 1
		total += len(data) + 1
	}
	t.Logf("request row bytes: assigned %d final %d total %d", sizes[requestRowAssigned], sizes[requestRowFinal], total)
	if total > 1536 {
		t.Fatalf("request rows %d bytes exceed 1.5 KB", total)
	}
}

// Startup reads only the root definitions and the retained hours; older
// stores under the directory, such as v1alpha4 windows, are never opened.
func TestObservationStartupIgnoresOtherHistory(t *testing.T) {
	directory := t.TempDir()
	window := filepath.Join(directory, "20261019T000000.000000000Z-old")
	if err := os.MkdirAll(window, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"metadata.json", "requests.jsonl", "scheduler.jsonl"} {
		if err := os.WriteFile(filepath.Join(window, name), []byte("not json\n"), 0000); err != nil {
			t.Fatal(err)
		}
	}
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: directory, Store: "evidence", Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := observationLog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
