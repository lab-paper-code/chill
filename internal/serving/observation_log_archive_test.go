package serving

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
)

const (
	archiveTestUID   = "uid"
	archiveTestStore = "evidence"
)

// Keep the real immutable writer and filesystem objects; only failed reads and
// cancellation are injected at the Reader/Lister boundary.
type readableObservationArchive struct {
	*observationArchive
	listError, openError error
	lists, opens         int
	beforeList           func()
}

func (a *readableObservationArchive) List(ctx context.Context, prefix string) ([]evidencestore.ObjectIdentity, error) {
	a.lists++
	if a.beforeList != nil {
		a.beforeList()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.listError != nil {
		return nil, a.listError
	}
	objects := []evidencestore.ObjectIdentity{}
	err := filepath.WalkDir(a.directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		key, err := filepath.Rel(a.directory, path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(key, prefix) {
			objects = append(objects, evidencestore.ObjectIdentity{Store: archiveTestStore, Key: key})
		}
		return nil
	})
	return objects, err
}

func (a *readableObservationArchive) Open(ctx context.Context, object evidencestore.ObjectIdentity) (io.ReadCloser, error) {
	a.opens++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.openError != nil {
		return nil, a.openError
	}
	return os.Open(filepath.Join(a.directory, object.Key))
}

func newReadableObservationArchive(t *testing.T) *readableObservationArchive {
	t.Helper()
	return &readableObservationArchive{observationArchive: &observationArchive{directory: t.TempDir()}}
}

func writeArchivedObservation(t *testing.T, archive *readableObservationArchive, hour time.Time, raw []byte) string {
	t.Helper()
	key := observationArchivePrefix + observationHour(hour) + "/" + observationSchedulerStream + ".jsonl.gz"
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(archive.directory, key)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, compressed.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestObservationArchiveTransparentReadsAndCatalog(t *testing.T) {
	ctx := context.Background()
	archive := newReadableObservationArchive(t)
	log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
	from, to := observationTestHour, observationTestHour.Add(2*time.Minute)
	seedObservationMinutes(t, log, clock, from.Add(time.Second), from.Add(time.Minute+time.Second))
	want, err := log.Query(ctx, archiveTestUID, from, to, nil, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	wantLoad, err := log.LoadWindow(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	// Upload without removing the local hour: source selection must not add it twice.
	clock.set(from.Add(time.Hour))
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	checkArchiveLocalOverlap(t, log, ctx, from, to, want, wantLoad)
	clock.set(from.Add(3 * time.Hour))
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	// An empty new root has no retained definitions: only the archive catalog can
	// interpret the deleted UID, even with a current replacement of the same name.
	restarted, _ := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, from.Add(3*time.Hour))
	if err := restarted.DefinitionRecord(&Definition{Name: "workload", UID: "replacement", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	got, err := restarted.Query(ctx, archiveTestUID, from, to, nil, nil, 0, "")
	if err != nil || got.Total != want.Total || !reflect.DeepEqual(got.Requests, want.Requests) || !reflect.DeepEqual(got.Definitions, want.Definitions) || !reflect.DeepEqual(got.Management, want.Management) || got.GapUntil != nil || len(got.Coverage.Gaps) != 0 {
		t.Fatalf("archive changed workload facts/catalog: %+v err=%v", got, err)
	}
	for _, source := range got.Coverage.Sources {
		if source.Source != observationArchiveSource || source.Store != archiveTestStore || source.Key == "" {
			t.Fatalf("archive provenance missing: %+v", source)
		}
	}
	load, err := restarted.LoadWindow(ctx, from, to)
	if err != nil || !reflect.DeepEqual(load.Workloads, wantLoad.Workloads) || !reflect.DeepEqual(load.Arrivals, wantLoad.Arrivals) || load.GapUntil != nil {
		t.Fatalf("archive load: %+v %v", load, err)
	}
	page, next, gap, coverage, err := restarted.SchedulerObservationPage(ctx, from, to, 1, 0)
	if err != nil || len(page) != 1 || page[0].Snapshot.Workloads[0].UID != archiveTestUID || next != 1 || gap != nil || coverage.Sources[0].Source != observationArchiveSource {
		t.Fatalf("archive first page/catalog: %+v %d %v", page, next, err)
	}
	page, next, _, _, err = restarted.SchedulerObservationPage(ctx, from, to, 1, next)
	if err != nil || len(page) != 1 || next != -1 {
		t.Fatalf("archive terminal page: %+v %d %v", page, next, err)
	}
}

func checkArchiveLocalOverlap(t *testing.T, log *ObservationLog, ctx context.Context, from, to time.Time, want ObservationResult, wantLoad LoadWindow) {
	t.Helper()
	got, err := log.Query(ctx, archiveTestUID, from, to, nil, nil, 0, "")
	if err != nil || got.Total != want.Total || len(got.Management) != len(want.Management) {
		t.Fatalf("overlap duplicated requests/management: %+v %v", got, err)
	}
	load, err := log.LoadWindow(ctx, from, to)
	if err != nil || !reflect.DeepEqual(load.Workloads, wantLoad.Workloads) {
		t.Fatalf("overlap load: %+v %v", load, err)
	}
	page, next, gap, coverage, err := log.SchedulerObservationPage(ctx, from, to, 1, 0)
	if err != nil || len(page) != 1 || next != 1 || gap != nil || coverage.Sources[0].Source != "local" {
		t.Fatalf("local page overlap: %+v next=%d gap=%v coverage=%+v err=%v", page, next, gap, coverage, err)
	}
}

func TestObservationArchiveLateFinalAndLocalFastPath(t *testing.T) {
	ctx := context.Background()
	archive := newReadableObservationArchive(t)
	log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
	at := observationTestHour.Add(time.Second)
	clock.set(at)
	assignObservationRequest(t, log, "late", at)
	if _, err := log.SchedulerArrivals(ctx, at, at.Add(time.Second)); err != nil || archive.lists != 0 || archive.opens != 0 {
		t.Fatalf("recent local arrivals touched archive: %v lists=%d opens=%d", err, archive.lists, archive.opens)
	}
	clock.set(observationTestHour.Add(time.Hour))
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := log.RequestEvent(RequestEvent{RequestID: "late", WorkloadUID: archiveTestUID, Kind: eventResponseFinished, At: at.Add(time.Second), Outcome: outcomeCompleted}); err != nil {
		t.Fatal(err)
	}
	if err := log.RequestClosed("late"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(log.root, observationHour(at))); err != nil {
		t.Fatal(err)
	}
	got, err := log.Query(ctx, archiveTestUID, at, at.Add(time.Second), nil, nil, 0, "")
	if err != nil || got.Total != 1 || len(got.Requests) != 1 || got.Requests[0].Kind != requestRowFinal {
		t.Fatalf("archive assigned/local final not merged: %+v %v", got, err)
	}
	load, err := log.LoadWindow(ctx, at, at.Add(time.Second))
	if err != nil || len(load.Workloads) != 1 || load.Workloads[0].Received != 1 || load.Workloads[0].Assigned != 1 {
		t.Fatalf("late final counted twice: %+v %v", load, err)
	}
}

func TestObservationArchiveCoverageGapsAndErrors(t *testing.T) {
	ctx := context.Background()
	archive := newReadableObservationArchive(t)
	from := observationTestHour
	for _, hour := range []time.Time{from, from.Add(2 * time.Hour)} {
		writeArchivedObservation(t, archive, hour, []byte("{\"at\":\""+hour.Add(time.Second).Format(time.RFC3339Nano)+"\"}\n"))
	}
	log, _ := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, from.Add(4*time.Hour))
	page, _, gap, coverage, err := log.SchedulerObservationPage(ctx, from, from.Add(3*time.Hour), 10, 0)
	if err != nil || len(page) != 2 || gap != nil || len(coverage.Gaps) != 1 || !coverage.Gaps[0].From.Equal(from.Add(time.Hour)) || !coverage.Gaps[0].To.Equal(from.Add(2*time.Hour)) || coverage.Gaps[0].Reason != "StreamUnavailable" {
		t.Fatalf("internal gap/provenance: %+v %+v %v", page, coverage, err)
	}
	_, _, gap, coverage, err = log.SchedulerObservationPage(ctx, from.Add(-time.Hour), from.Add(3*time.Hour), 10, 0)
	if err != nil || gap == nil || !gap.Equal(from) || coverage.From == nil || !coverage.From.Equal(from) {
		t.Fatalf("leading actual gap: %+v gap=%v err=%v", coverage, gap, err)
	}
	_, _, _, coverage, err = log.SchedulerObservationPage(ctx, from.Add(-3*time.Hour), from.Add(-2*time.Hour), 10, 0)
	if err != nil || coverage.From != nil || coverage.To != nil || len(coverage.Sources) != 0 || len(coverage.Gaps) != 1 {
		t.Fatalf("unavailable extent must be null: %+v %v", coverage, err)
	}
	failure := errors.New("store failed")
	for _, boundary := range []string{"list", "open"} {
		if boundary == "list" {
			archive.listError = failure
		} else {
			archive.listError, archive.openError = nil, failure
		}
		if _, _, _, _, err := log.SchedulerObservationPage(ctx, from, from.Add(time.Minute), 10, 0); !errors.Is(err, failure) {
			t.Fatalf("%s failure hidden: %v", boundary, err)
		}
		if !log.Health().Healthy {
			t.Fatal("read failure changed append readiness")
		}
	}
	archive.openError = nil
	cancelled, cancel := context.WithCancel(ctx)
	archive.beforeList = cancel
	if _, _, _, _, err := log.SchedulerObservationPage(cancelled, from, from.Add(time.Minute), 10, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("list cancellation lost: %v", err)
	}
}

func TestArchivedScannerCancellationCorruptionAndStop(t *testing.T) {
	archive := newReadableObservationArchive(t)
	key := writeArchivedObservation(t, archive, observationTestHour, []byte("{}\n{}\n"))
	object := evidencestore.ObjectIdentity{Store: archiveTestStore, Key: key}
	ctx, cancel := context.WithCancel(context.Background())
	count := 0
	err := readArchivedStream(ctx, archive, object, func([]byte) error { count++; cancel(); return nil })
	if !errors.Is(err, context.Canceled) || count != 1 {
		t.Fatalf("scan cancellation: %v count=%d", err, count)
	}
	if err := readArchivedStream(context.Background(), archive, object, func([]byte) error { return errObservationStop }); err != nil {
		t.Fatalf("page stop was an error: %v", err)
	}
	name := filepath.Join(archive.directory, key)
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data[:len(data)-5], 0600); err != nil {
		t.Fatal(err)
	}
	if err := readArchivedStream(context.Background(), archive, object, func([]byte) error { return nil }); err == nil {
		t.Fatal("truncated gzip accepted")
	}
	writeArchivedObservation(t, archive, observationTestHour, []byte("{}"))
	if err := readArchivedStream(context.Background(), archive, object, func([]byte) error { return nil }); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("torn archived JSON line accepted: %v", err)
	}
}

func TestObservationArchivePageStopsAcrossSources(t *testing.T) {
	archive := newReadableObservationArchive(t)
	from := observationTestHour
	writeArchivedObservation(t, archive, from, []byte("{\"at\":\""+from.Add(time.Second).Format(time.RFC3339Nano)+"\"}\n{\"at\":\""+from.Add(2*time.Second).Format(time.RFC3339Nano)+"\"}\n{broken}\n"))
	writeArchivedObservation(t, archive, from.Add(time.Hour), []byte("{broken}\n"))
	log, _ := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, from.Add(3*time.Hour))
	page, next, _, _, err := log.SchedulerObservationPage(context.Background(), from, from.Add(2*time.Hour), 1, 0)
	if err != nil || len(page) != 1 || next != 1 || archive.opens != 1 {
		t.Fatalf("archive page scanned past lookahead: %+v next=%d opens=%d err=%v", page, next, archive.opens, err)
	}
}

func TestObservationArchiveCaptureSurvivesDeletion(t *testing.T) {
	archive := newReadableObservationArchive(t)
	log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
	seedObservationMinutes(t, log, clock, observationTestHour.Add(time.Second))
	clock.set(observationTestHour.Add(3 * time.Hour))
	archive.beforeList = func() {
		if err := log.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	result, err := log.Query(context.Background(), archiveTestUID, observationTestHour.Add(-time.Hour), observationTestHour.Add(time.Minute), nil, nil, 0, "")
	if err != nil || result.Total != 1 || len(result.Management) != 1 {
		t.Fatalf("capture raced deletion: %+v %v", result, err)
	}
	for _, source := range result.Coverage.Sources {
		if source.Source != "local" {
			t.Fatalf("captured hour switched sources: %+v", source)
		}
	}
}

func TestObservationArchiveMissingCatalogAndInvalidJSON(t *testing.T) {
	for _, raw := range []string{"{broken}\n", "{\"at\":\"2026-10-20T10:00:01Z\",\"snapshot\":{\"workloadIDs\":[\"missing\"]}}\n"} {
		archive := newReadableObservationArchive(t)
		writeArchivedObservation(t, archive, observationTestHour, []byte(raw))
		log, _ := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour.Add(3*time.Hour))
		if _, _, _, _, err := log.SchedulerObservationPage(context.Background(), observationTestHour, observationTestHour.Add(time.Minute), 10, 0); err == nil {
			t.Fatalf("invalid archive hidden: %s", raw)
		}
	}
}

func TestObservationArchivePowerNeighborsAndOlderMembership(t *testing.T) {
	ctx := context.Background()
	archive := newReadableObservationArchive(t)
	log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive, Retention: 5 * time.Hour}, observationTestHour.Add(-2*time.Hour))
	from := observationTestHour.Add(10 * time.Minute)
	to := from.Add(10 * time.Second)
	node := NodeReference{Name: "node", UID: "node-uid"}
	baselineAt := observationTestHour.Add(-2*time.Hour + time.Minute)
	clock.set(baselineAt)
	definition := &Definition{Name: "deleted", UID: "old-uid", Generation: 1}
	baseline := SchedulerSnapshot{At: baselineAt, Instances: []SchedulerInstance{{ID: "i", Node: node, Ready: true, WorkloadUID: definition.UID}}, Workloads: []*Definition{definition}}
	if err := log.SchedulerRecord(SchedulerObservation{At: baselineAt, Snapshot: &baseline}); err != nil {
		t.Fatal(err)
	}
	middle := observationTestHour.Add(-time.Hour)
	clock.set(middle)
	if err := log.SchedulerRecord(SchedulerObservation{At: middle, Action: &SchedulerActionObservation{ID: "between"}}); err != nil {
		t.Fatal(err)
	}
	policy := powerwindow.Policy{MaximumBoundaryDistance: time.Minute, MaximumGap: time.Minute, MinimumCoverage: 1}
	for _, at := range []time.Time{from.Add(-time.Second), to.Add(time.Second)} {
		clock.set(at)
		power := &metricobserver.Result{Source: operatingTestSource, StartedAt: at.Add(-time.Second), EndedAt: at, Samples: []metricobserver.Sample{{ObservedAt: at, Watts: 20}}}
		if err := log.SchedulerRecord(SchedulerObservation{At: at, Node: &node, Meter: "wall", PowerScope: "node-exclusive-wall", Power: power, PowerCoverage: &policy}); err != nil {
			t.Fatal(err)
		}
	}
	meters := []OperatingPowerMeter{{Node: node, Meter: "wall", Scope: "node-exclusive-wall", Source: operatingTestSource}}
	want, err := log.OperatingPower(ctx, from, to, meters, 20)
	if err != nil || !want.Complete || want.WholePoolJoules == nil || *want.WholePoolJoules != 200 || want.ActivePoolJoules == nil || *want.ActivePoolJoules != 200 {
		t.Fatalf("local older baseline/neighbors: %+v %v", want, err)
	}
	clock.set(observationTestHour.Add(time.Hour))
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	overlap, err := log.OperatingPower(ctx, from, to, meters, 20)
	if err != nil || !overlap.Complete || *overlap.WholePoolJoules != *want.WholePoolJoules {
		t.Fatalf("power overlap duplicated: %+v %v", overlap, err)
	}
	clock.set(observationTestHour.Add(6 * time.Hour))
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := log.OperatingPower(ctx, from, to, meters, 20)
	if err != nil || !got.Complete || got.GapUntil != nil || !reflect.DeepEqual(got.Nodes, want.Nodes) || got.WholePoolJoules == nil || *got.WholePoolJoules != *want.WholePoolJoules || got.ActivePoolJoules == nil || *got.ActivePoolJoules != *want.ActivePoolJoules {
		t.Fatalf("archived older baseline/neighbors: %+v %v", got, err)
	}
	if len(got.Coverage.Sources) != 3 {
		t.Fatalf("power provenance: %+v", got.Coverage)
	}
	for _, source := range got.Coverage.Sources {
		if source.Source != observationArchiveSource {
			t.Fatalf("power provenance: %+v", source)
		}
	}
}

func TestObservationQueriesHonorPreCancelledContext(t *testing.T) {
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	from, to := observationTestHour, observationTestHour.Add(time.Minute)
	_, queryErr := log.Query(ctx, archiveTestUID, from, to, nil, nil, 0, "")
	_, loadErr := log.LoadWindow(ctx, from, to)
	_, arrivalsErr := log.SchedulerArrivals(ctx, from, to)
	_, _, _, _, pageErr := log.SchedulerObservationPage(ctx, from, to, 10, 0)
	_, powerErr := log.OperatingPower(ctx, from, to, nil, 10)
	_, occupancyErr := log.ScanOccupancyRows(ctx, archiveTestUID, "node", from, to, func(RequestRow) {})
	for _, err := range []error{queryErr, loadErr, arrivalsErr, pageErr, powerErr, occupancyErr} {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("query ignored cancellation: %v", err)
		}
	}
}

func TestSchedulerActionObservationPreservesExpectedDuration(t *testing.T) {
	seconds := 2.5
	action := SchedulerActionObservation{ID: "action", ExpectedSeconds: &seconds, ExpectedSource: &ActionExpectedSource{CostID: "cost", Statistic: "mean", TrialCount: 2}}
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	at := observationTestHour.Add(time.Second)
	clock.set(at)
	if err := log.SchedulerRecord(SchedulerObservation{At: at, Action: &action}); err != nil {
		t.Fatal(err)
	}
	page, _, _, _, err := log.SchedulerObservationPage(context.Background(), at, at.Add(time.Second), 10, 0)
	if err != nil || len(page) != 1 || !reflect.DeepEqual(page[0].Action, &action) {
		t.Fatalf("action estimate lost: %+v %v", page, err)
	}
	var old SchedulerActionObservation
	if err := json.Unmarshal([]byte(`{"id":"older"}`), &old); err != nil || old.ExpectedSeconds != nil || old.ExpectedSource != nil {
		t.Fatalf("absent estimate fabricated: %+v %v", old, err)
	}
}
