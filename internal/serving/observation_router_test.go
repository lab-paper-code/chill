package serving

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func recordRouterRequest(t *testing.T, log *ObservationLog, id string, definition *Definition, at time.Time, elapsed time.Duration, closeRow bool) {
	t.Helper()
	if err := log.DefinitionRecord(definition); err != nil {
		t.Fatal(err)
	}
	assignment := &Assignment{Node: NodeReference{Name: "node", UID: "node-uid"}, Instance: "pod/container", PodUID: "pod", ContainerID: "container", DispatchedAt: at}
	for _, kind := range []string{phaseReceived, "validated", phaseAccepted, phaseAssigned} {
		if err := log.RequestEvent(RequestEvent{RequestID: id, WorkloadUID: definition.UID, Definition: definition.Identity(), PlanVersion: "plan-1", ReceivedAt: at, At: at, Kind: kind, Assignment: assignment}); err != nil {
			t.Fatal(err)
		}
	}
	if closeRow {
		if err := log.RequestEvent(RequestEvent{RequestID: id, At: at.Add(elapsed), Kind: eventResponseFinished, Outcome: outcomeCompleted, ElapsedNanos: int64(elapsed)}); err != nil {
			t.Fatal(err)
		}
		if err := log.RequestClosed(id); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRouterObservationUnionLocalArchiveAndLateFinal(t *testing.T) {
	ctx := context.Background()
	from := observationTestHour
	archive := newReadableObservationArchive(t)
	a, ac := clockedObservationLog(t, ObservationLogConfig{Writer: archive, RouterID: "router-a"}, from)
	b, bc := clockedObservationLog(t, ObservationLogConfig{Writer: archive, RouterID: "router-b"}, from)
	d := &Definition{Name: "workload", UID: "uid", Generation: 1, SLO: SLO{Percentile: 95, TargetLatency: time.Second}}
	ac.set(from.Add(time.Second))
	recordRouterRequest(t, a, "a", d, from.Add(time.Second), 10*time.Millisecond, true)
	bc.set(from.Add(2 * time.Second))
	recordRouterRequest(t, b, "b", d, from.Add(2*time.Second), 100*time.Millisecond, true)
	for _, router := range []*ObservationLog{a, b} {
		if err := router.ManagementEvent(ManagementEvent{At: from.Add(3 * time.Second), WorkloadUID: "uid", Kind: "router-only"}); err != nil {
			t.Fatal(err)
		}
	}
	bc.set(from.Add(time.Hour))
	if err := b.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	sources := []RouterObservationSource{
		{RouterID: "router-a", Directory: a.config.Directory, JoinedAt: from, LeftAt: from.Add(time.Hour), ObservedAt: from.Add(time.Hour)},
		{RouterID: "router-b", JoinedAt: from, LeftAt: from.Add(time.Hour), ObservedAt: from.Add(time.Hour)},
	}
	control, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive, RouterSources: func() []RouterObservationSource { return sources }}, from)
	if err := control.ManagementEvent(ManagementEvent{At: from.Add(3 * time.Second), WorkloadUID: "uid", Kind: "control-only"}); err != nil {
		t.Fatal(err)
	}
	clock.set(from.Add(2 * time.Hour))
	got, err := control.Query(ctx, "uid", from, from.Add(time.Minute), nil, nil, 0, "")
	if err != nil || got.Total != 2 || len(got.Cohorts) != 1 || got.Cohorts[0].SampleCount != 2 || got.Cohorts[0].QuantileNanos == nil || *got.Cohorts[0].QuantileNanos != float64(100*time.Millisecond) {
		t.Fatalf("union cohort: %+v err=%v", got, err)
	}
	if len(got.Management) != 1 || got.Management[0].Kind != "control-only" {
		t.Fatalf("control records multiplied: %+v", got.Management)
	}
	seen := map[string]string{}
	for _, source := range got.Coverage.Sources {
		if source.RouterID != "" {
			seen[source.RouterID] = source.Source
		}
	}
	if seen["router-a"] != "local" || seen["router-b"] != "archive" {
		t.Fatalf("source isolation: %+v", got.Coverage)
	}
	for _, row := range got.Requests {
		if row.RouterID == "" || row.PlanVersion != "plan-1" || row.PodUID != "pod" || row.ContainerID != "container" {
			t.Fatalf("assignment identity: %+v", row)
		}
	}
	load, err := control.LoadWindow(ctx, from, from.Add(time.Minute))
	if err != nil || len(load.Workloads) != 1 || load.Workloads[0].Received != 2 || len(load.Arrivals) != 2 {
		t.Fatalf("union load: %+v err=%v", load, err)
	}
	count := 0
	if _, err := control.ScanOccupancyRows(ctx, "uid", "node-uid", from, from.Add(time.Minute), func(row RequestRow) {
		if row.Kind != RequestRowNative {
			count++
		}
	}); err != nil || count != 2 {
		t.Fatalf("union occupancy count=%d err=%v", count, err)
	}

	// A different hour's local final must supersede an archived assignment,
	// including finals appended after membership ended and stale open reports.
	bc.set(from.Add(time.Hour + time.Second))
	recordRouterRequest(t, b, "late", d, from.Add(time.Hour+time.Second), time.Millisecond, false)
	bc.set(from.Add(2 * time.Hour))
	if err := b.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	stale := b.OpenRows()
	bc.set(from.Add(2*time.Hour + time.Second))
	if err := b.RequestEvent(RequestEvent{RequestID: "late", At: from.Add(2*time.Hour + time.Second), Kind: eventResponseFinished, Outcome: outcomeCompleted, ElapsedNanos: int64(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := b.RequestClosed("late"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(b.root, observationHour(from.Add(time.Hour)))); err != nil {
		t.Fatal(err)
	}
	sources[1].Directory, sources[1].LeftAt, sources[1].Open = b.config.Directory, from.Add(time.Hour+time.Minute), stale
	clock.set(from.Add(3 * time.Hour))
	got, err = control.Query(ctx, "uid", from.Add(time.Hour), from.Add(time.Hour+time.Minute), nil, nil, 0, "")
	if err != nil || got.Total != 1 || len(got.Requests) != 1 || got.Requests[0].Kind != requestRowFinal {
		t.Fatalf("late final dedup: %+v err=%v", got, err)
	}
	b.mu.Lock()
	_, err = b.appendRowLocked(&got.Requests[0]) // A retransmitted final is not another physical member.
	b.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	count = 0
	if _, err := control.ScanOccupancyRows(ctx, "uid", "node-uid", from.Add(time.Hour), from.Add(time.Hour+time.Minute), func(row RequestRow) {
		if row.Kind != RequestRowNative {
			count++
		}
	}); err != nil || count != 1 {
		t.Fatalf("late final/open occupancy dedup count=%d err=%v", count, err)
	}
}

func TestRouterObservationCatalogIsolationAndMissingCoverage(t *testing.T) {
	ctx := context.Background()
	from := observationTestHour
	archive := newReadableObservationArchive(t)
	a, ac := clockedObservationLog(t, ObservationLogConfig{Writer: archive, RouterID: "a"}, from)
	b, bc := clockedObservationLog(t, ObservationLogConfig{Writer: archive, RouterID: "b"}, from)
	d := &Definition{Name: "workload", UID: "uid", Generation: 1}
	ac.set(from.Add(time.Second))
	bc.set(from.Add(time.Second))
	recordRouterRequest(t, a, "a", d, from.Add(time.Second), time.Millisecond, true)
	recordRouterRequest(t, b, "b", d, from.Add(time.Second), time.Millisecond, true)
	bc.set(from.Add(time.Hour))
	if err := b.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(archive.directory, observationRouterPrefix("b"), observationHour(from), observationCatalogStream+".jsonl.gz")); err != nil {
		t.Fatal(err)
	}
	sources := []RouterObservationSource{{RouterID: "a", Directory: a.config.Directory, JoinedAt: from}, {RouterID: "b", JoinedAt: from}}
	control, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive, RouterSources: func() []RouterObservationSource { return sources }}, from)
	clock.set(from.Add(2 * time.Hour))
	if _, err := control.Query(ctx, "uid", from, from.Add(time.Minute), nil, nil, 0, ""); err == nil {
		t.Fatal("router a catalog incorrectly resolved router b reference")
	}
	// No mounted private buffer is not proof of a zero live stream.
	sources = []RouterObservationSource{{RouterID: "missing", JoinedAt: from, ObservedAt: from.Add(2 * time.Hour)}}
	window, err := control.LoadWindow(ctx, from.Add(2*time.Hour), from.Add(2*time.Hour+time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, gap := range window.Coverage.Gaps {
		found = found || gap.RouterID == "missing" && gap.Reason == "LiveBufferUnavailable"
	}
	if !found {
		t.Fatalf("live buffer loss became zero: %+v", window.Coverage)
	}
	if arrivals, err := control.SchedulerArrivals(ctx, from.Add(2*time.Hour), from.Add(2*time.Hour+time.Minute)); err == nil || arrivals != nil {
		t.Fatalf("missing router became zero scheduler demand: arrivals=%+v err=%v", arrivals, err)
	}
	failure := errors.New("archive unavailable")
	archive.listError = failure
	if _, err := control.LoadWindow(ctx, from, from.Add(time.Minute)); !errors.Is(err, failure) {
		t.Fatalf("archive error hidden: %v", err)
	}
}

func TestRouterObservationWriterNamespaceAndOpenCopies(t *testing.T) {
	archive := newReadableObservationArchive(t)
	for _, id := range []string{"../escape", "a/b", ".", "a.b", "a b"} {
		if _, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Writer: archive, Retention: time.Hour, RouterID: id}); err == nil {
			t.Fatalf("unsafe router ID %q accepted", id)
		}
	}
	log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive, RouterID: "pod-generation_1"}, observationTestHour)
	clock.set(observationTestHour.Add(time.Second))
	recordRouterRequest(t, log, "open", &Definition{UID: "uid", Generation: 1}, observationTestHour.Add(time.Second), 0, false)
	rows := log.OpenRows()
	rows[0].Node.UID = "mutated"
	*rows[0].DispatchedAt = time.Time{}
	if got := log.OpenRows()[0]; got.Node.UID != "node-uid" || got.DispatchedAt.IsZero() {
		t.Fatalf("OpenRows aliases writer: %+v", got)
	}
	clock.set(observationTestHour.Add(time.Hour))
	if err := log.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	key := observationRouterPrefix("pod-generation_1") + observationHour(observationTestHour) + "/requests.jsonl.gz"
	if _, err := os.Stat(filepath.Join(archive.directory, key)); err != nil {
		t.Fatalf("writer namespace missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(archive.directory, observationArchivePrefix, observationHour(observationTestHour), "requests.jsonl.gz")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("router wrote legacy key: %v", err)
	}
}
