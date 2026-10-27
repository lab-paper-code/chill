package serving

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRouterSourceSnapshotPrecedesWriterCapture(t *testing.T) {
	var log *ObservationLog
	log, _ = clockedObservationLog(t, ObservationLogConfig{RouterSources: func() []RouterObservationSource {
		// Control membership recording takes this same lock. The source callback
		// must not run under capture's writer lock.
		log.mu.Lock()
		log.mu.Unlock()
		return nil
	}}, observationTestHour)
	if _, err := log.LoadWindow(context.Background(), observationTestHour, observationTestHour.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestRouterMissingMountedRequestStreamIsNotZero(t *testing.T) {
	archive := newReadableObservationArchive(t)
	from := observationTestHour
	router, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive, RouterID: "router"}, from)
	clock.set(from.Add(time.Second))
	recordRouterRequest(t, router, "request", &Definition{UID: "uid", Generation: 1}, from.Add(time.Second), time.Millisecond, true)
	// Keep the local hour/catalog while requests is absent: selection is by
	// (router, hour, stream), not by the existence of an hour directory.
	if err := os.Remove(filepath.Join(router.root, observationHour(from), "requests.00.jsonl")); err != nil {
		t.Fatal(err)
	}
	source := RouterObservationSource{RouterID: "router", Directory: router.config.Directory, JoinedAt: from, LeftAt: from.Add(time.Hour)}
	control, now := clockedObservationLog(t, ObservationLogConfig{Writer: archive, RouterSources: func() []RouterObservationSource { return []RouterObservationSource{source} }}, from)
	now.set(from.Add(2 * time.Hour))
	window, err := control.LoadWindow(context.Background(), from, from.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, gap := range window.Coverage.Gaps {
		if gap.RouterID == "router" && gap.Stream == "requests" {
			return
		}
	}
	t.Fatalf("missing mounted stream became zero: %+v", window.Coverage)
}

func TestRouterReportCoverageTailWithMountedBuffer(t *testing.T) {
	ctx := context.Background()
	from := observationTestHour
	archive := newReadableObservationArchive(t)
	router, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive, RouterID: "router"}, from)
	clock.set(from.Add(time.Second))
	recordRouterRequest(t, router, "request", &Definition{UID: "uid", Generation: 1}, from.Add(time.Second), time.Millisecond, true)
	source := RouterObservationSource{RouterID: "router", Directory: router.config.Directory, JoinedAt: from, ObservedAt: from.Add(10 * time.Second)}
	control, now := clockedObservationLog(t, ObservationLogConfig{Writer: archive, RouterSources: func() []RouterObservationSource { return []RouterObservationSource{source} }}, from)
	now.set(from.Add(time.Minute))
	for _, retired := range []bool{false, true} {
		gapEnd := from.Add(20 * time.Second)
		if retired {
			source.LeftAt = from.Add(15 * time.Second)
			gapEnd = source.LeftAt
		}
		window, err := control.LoadWindow(ctx, from, from.Add(20*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, gap := range window.Coverage.Gaps {
			if gap.RouterID == "router" && gap.Reason == "RouterReportUnavailable" {
				found = true
				if !gap.From.Equal(source.ObservedAt) || !gap.To.Equal(gapEnd) || gap.ObservedAt == nil || !gap.ObservedAt.Equal(source.ObservedAt) {
					t.Fatalf("retired=%v report tail: %+v", retired, gap)
				}
			}
		}
		if !found {
			t.Fatalf("retired=%v mounted buffer hid unreported receipts: %+v", retired, window.Coverage)
		}
		for _, coverage := range window.Coverage.Sources {
			if coverage.RouterID == "router" && coverage.To.After(source.ObservedAt) {
				t.Fatalf("report tail claimed complete: %+v", coverage)
			}
		}
		if arrivals, err := control.SchedulerArrivals(ctx, from, source.ObservedAt); err != nil || len(arrivals) != 1 {
			t.Fatalf("known prefix rejected: arrivals=%+v err=%v", arrivals, err)
		}
		if arrivals, err := control.SchedulerArrivals(ctx, from, from.Add(20*time.Second)); err == nil || arrivals != nil {
			t.Fatalf("report tail became zero demand: arrivals=%+v err=%v", arrivals, err)
		}
	}
	// A later report covers an earlier archived hour; no report-tail gap belongs
	// to that historical interval, even without its mounted private buffer.
	clock.set(from.Add(time.Hour))
	if err := router.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	source.Directory, source.LeftAt, source.ObservedAt = "", from.Add(time.Hour), from.Add(time.Hour)
	now.set(from.Add(2 * time.Hour))
	window, err := control.LoadWindow(ctx, from, from.Add(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, gap := range window.Coverage.Gaps {
		if gap.RouterID == "router" {
			t.Fatalf("later report left historical archive gap: %+v", gap)
		}
	}
}

func TestRouterIdleWriterHasKnownEmptyStreams(t *testing.T) {
	ctx := context.Background()
	from := observationTestHour
	archive := newReadableObservationArchive(t)
	router, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive, RouterID: "idle"}, from)
	// Construction uses the real clock before the test clock is installed.
	// Both initialized streams must be real zero-byte segments, not JSON rows.
	for _, stream := range []string{observationRequestStream, observationCatalogStream} {
		files, err := router.hourFiles(router.routerObservationHour, stream)
		if err != nil || len(files) != 1 {
			t.Fatalf("initial %s segment: %+v err=%v", stream, files, err)
		}
		info, err := os.Stat(files[0].path)
		if err != nil || info.Size() != 0 {
			t.Fatalf("idle %s manufactured rows: info=%+v err=%v", stream, info, err)
		}
	}
	source := RouterObservationSource{RouterID: "idle", Directory: router.config.Directory, JoinedAt: from}
	control, now := clockedObservationLog(t, ObservationLogConfig{Writer: archive, RouterSources: func() []RouterObservationSource { return []RouterObservationSource{source} }}, from)
	for _, hour := range []time.Time{from, from.Add(time.Hour)} {
		clock.set(hour.Add(time.Second))
		if err := router.AdvanceRouterObservation(); err != nil {
			t.Fatal(err)
		}
		source.ObservedAt = hour.Add(30 * time.Second)
		now.set(hour.Add(time.Minute))
		arrivals, err := control.SchedulerArrivals(ctx, hour, source.ObservedAt)
		if err != nil || len(arrivals) != 0 {
			t.Fatalf("genuine idle prefix: arrivals=%+v err=%v", arrivals, err)
		}
		// Repeated reports in an hour must not create one empty segment per tick.
		clock.set(hour.Add(time.Minute))
		if err := router.AdvanceRouterObservation(); err != nil {
			t.Fatal(err)
		}
		for _, stream := range []string{observationRequestStream, observationCatalogStream} {
			files, err := router.hourFiles(hour, stream)
			if err != nil || len(files) != 1 {
				t.Fatalf("repeated idle %s segments: %+v err=%v", stream, files, err)
			}
		}
	}
	// Explicitly removed streams stay unavailable; hourly advancement is not a
	// missing-file repair that could turn lost observations into observed zero.
	if err := os.Remove(filepath.Join(router.root, observationHour(from.Add(time.Hour)), "requests.00.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := router.AdvanceRouterObservation(); err != nil {
		t.Fatal(err)
	}
	if arrivals, err := control.SchedulerArrivals(ctx, from.Add(time.Hour), source.ObservedAt); err == nil || arrivals != nil {
		t.Fatalf("removed idle stream became zero: arrivals=%+v err=%v", arrivals, err)
	}
	legacy, _ := clockedObservationLog(t, ObservationLogConfig{}, from)
	if err := legacy.AdvanceRouterObservation(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(legacy.root, observationHour(from))); !os.IsNotExist(err) {
		t.Fatalf("legacy layout changed: %v", err)
	}
}
