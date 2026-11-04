package observation

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestArchivedEventRecoverySkipsCatalogHydration(t *testing.T) {
	const timedOut = "timeout"
	ctx := context.Background()
	archive := &requestQueryArchive{readableObservationArchive: newReadableObservationArchive(t)}
	log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
	at := observationTestHour.Add(time.Second)
	clock.set(at)
	if err := log.SchedulerRecord(SchedulerObservation{At: at, Action: &SchedulerActionObservation{ID: "action", Kind: "power", Phase: "failed", Error: timedOut}}); err != nil {
		t.Fatal(err)
	}
	if err := log.ClusterRecord(ClusterObservation{Kind: "issue", At: at, Event: "raised", Source: "observer", Reason: "not-ready"}); err != nil {
		t.Fatal(err)
	}
	clock.set(at.Add(3 * time.Hour))
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, _ := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, at.Add(3*time.Hour))
	archive.rejectCatalog = true
	events, _, coverage, err := restarted.EventObservations(ctx, at, 10, "")
	if err != nil || len(events) != 2 || events[0].Action == nil || events[0].Action.Error != timedOut || events[1].Cluster == nil || events[1].Cluster.Reason != "not-ready" {
		t.Fatalf("archived action/cluster recovery: %+v err=%v", events, err)
	}
	if coverage.From == nil || !coverage.From.Equal(observationTestHour) || coverage.To == nil || !coverage.To.Equal(observationTestHour.Add(time.Hour)) {
		t.Fatalf("event append-hour coverage: %+v", coverage)
	}
	empty, next, _, err := restarted.EventObservations(ctx, at.Add(time.Nanosecond), 10, "")
	if err != nil || empty != nil || next == "" || archive.catalogReads != 0 {
		t.Fatalf("nearby timestamp invented a match or opened catalogs: %+v err=%v reads=%d", empty, err, archive.catalogReads)
	}
}

func TestEventRecoveryFindsLateAppendsAcrossSegments(t *testing.T) {
	for _, archived := range []bool{false, true} {
		name := "local"
		if archived {
			name = "archived"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			archive := &requestQueryArchive{readableObservationArchive: newReadableObservationArchive(t)}
			log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
			at := observationTestHour.Add(time.Second)
			// Event timestamps predate the segment selected by the append clock.
			clock.set(at.Add(3 * time.Hour))
			if err := log.SchedulerRecord(SchedulerObservation{
				At: at, Action: &SchedulerActionObservation{ID: "action", Kind: "power", Phase: "failed"},
			}); err != nil {
				t.Fatal(err)
			}
			for _, timestamp := range []time.Time{at, at.Add(time.Nanosecond)} {
				if err := log.ClusterRecord(ClusterObservation{
					Kind: "issue", At: timestamp, Event: "raised", Source: "observer", Reason: "late",
				}); err != nil {
					t.Fatal(err)
				}
			}
			if archived {
				clock.set(at.Add(6 * time.Hour))
				if err := log.Flush(ctx); err != nil {
					t.Fatal(err)
				}
				log, _ = clockedObservationLog(t, ObservationLogConfig{Writer: archive}, at.Add(6*time.Hour))
			}
			archive.rejectCatalog = true
			cursor := ""
			var events []RecordedEvent
			for page := range 4 {
				var next string
				var err error
				events, next, _, err = log.EventObservations(ctx, at, 2, cursor)
				if err != nil || page < 3 && (events != nil || next == "") || page == 3 && len(events) != 2 {
					t.Fatalf("late append page %d: %+v next=%q err=%v", page, events, next, err)
				}
				if page < 3 {
					cursor = next
				}
			}
			if events[0].Action == nil || events[0].Action.ID != "action" ||
				events[1].Cluster == nil || !events[1].Cluster.At.Equal(at) {
				t.Fatalf("exact event recovery returned different facts: %+v", events)
			}
			page, next, _, err := log.EventObservations(ctx, at, 1, cursor)
			if err != nil || len(page) != 1 || page[0].Action == nil || next == "" {
				t.Fatalf("bounded exact first page: %+v next=%q err=%v", page, next, err)
			}
			page, _, _, err = log.EventObservations(ctx, at, 1, next)
			if err != nil || len(page) != 1 || page[0].Cluster == nil {
				t.Fatalf("bounded exact continuation lost simultaneous fact: %+v err=%v", page, err)
			}
			if archive.catalogReads != 0 {
				t.Fatalf("event recovery opened %d catalogs", archive.catalogReads)
			}
		})
	}
}

func TestEventRecoveryTwoHourContinuationAndArchiveGaps(t *testing.T) {
	ctx := context.Background()
	archive := &requestQueryArchive{readableObservationArchive: newReadableObservationArchive(t)}
	log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
	at := observationTestHour.Add(time.Second)
	clock.set(at.Add(2 * time.Hour))
	if err := log.ClusterRecord(ClusterObservation{Kind: "issue", At: at, Event: "raised", Source: "observer", Reason: "late"}); err != nil {
		t.Fatal(err)
	}
	clock.set(at.Add(5 * time.Hour))
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, _ := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, clock.now())
	cursor := ""
	for page := range 3 {
		events, next, coverage, err := restarted.EventObservations(ctx, at, 10, cursor)
		from := observationTestHour.Add(time.Duration(page) * time.Hour)
		to := from.Add(time.Hour)
		if err != nil || next != to.Format(time.RFC3339Nano) || coverage.From == nil || !coverage.From.Equal(from) || coverage.To == nil || !coverage.To.Equal(to) {
			t.Fatalf("append page %d: next=%q coverage=%+v err=%v", page, next, coverage, err)
		}
		if page < 2 {
			if events != nil || len(coverage.Gaps) != 2 || len(coverage.Sources) != 0 {
				t.Fatalf("missing archive hour is not empty and uncovered: events=%+v coverage=%+v", events, coverage)
			}
			for _, gap := range coverage.Gaps {
				if !gap.From.Equal(from) || !gap.To.Equal(to) || gap.Reason != "StreamUnavailable" {
					t.Fatalf("wrong missing-hour gap: %+v", gap)
				}
			}
		} else if len(events) != 1 || events[0].Cluster.Reason != "late" || len(coverage.Sources) != 1 || len(coverage.Gaps) != 1 || coverage.Gaps[0].Stream != observationSchedulerStream {
			t.Fatalf("late archived event/coverage: events=%+v coverage=%+v", events, coverage)
		}
		cursor = next
	}
	events, next, coverage, err := restarted.EventObservations(ctx, at.Add(time.Nanosecond), 10, cursor)
	if err != nil || events != nil || next == "" || len(coverage.Gaps) != 2 {
		t.Fatalf("not-found continuation: events=%+v next=%q coverage=%+v err=%v", events, next, coverage, err)
	}
}

func TestEventRecoveryConfiguredAppendHours(t *testing.T) {
	ctx := context.Background()
	log, clock := clockedObservationLog(t, ObservationLogConfig{EventLookupHours: 2}, observationTestHour)
	at := observationTestHour.Add(time.Second)
	clock.set(at.Add(2 * time.Hour))
	action := &SchedulerActionObservation{ID: "late", Kind: "power", Phase: "failed"}
	offsetAt := at.In(time.FixedZone("test", 9*60*60))
	if err := log.SchedulerRecord(SchedulerObservation{At: offsetAt, Action: action}); err != nil {
		t.Fatal(err)
	}
	events, next, coverage, err := log.EventObservations(ctx, at, 10, "")
	end := observationTestHour.Add(2 * time.Hour)
	if err != nil || events != nil || next != end.Format(time.RFC3339Nano) || coverage.To == nil || !coverage.To.Equal(end) {
		t.Fatalf("configured two-hour page leaked the next hour: events=%+v next=%q coverage=%+v err=%v", events, next, coverage, err)
	}
	events, next, coverage, err = log.EventObservations(ctx, at, 10, next)
	if err != nil || len(events) != 1 || events[0].Action.ID != action.ID || next != "" || coverage.To == nil || !coverage.To.Equal(clock.now()) {
		t.Fatalf("late action timestamp/terminal coverage: events=%+v next=%q coverage=%+v err=%v", events, next, coverage, err)
	}
}

func TestEventRecoveryAtAppendHourBoundary(t *testing.T) {
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	at := observationTestHour
	clock.set(at.Add(time.Hour))
	if err := log.ClusterRecord(ClusterObservation{Kind: "issue", At: at, Event: "raised", Source: "observer", Reason: "boundary"}); err != nil {
		t.Fatal(err)
	}
	events, next, _, err := log.EventObservations(context.Background(), at, 10, "")
	if err != nil || events != nil || next != clock.now().Format(time.RFC3339Nano) {
		t.Fatalf("current append hour omitted: events=%+v next=%q err=%v", events, next, err)
	}
	events, next, _, err = log.EventObservations(context.Background(), at, 10, next)
	if err != nil || len(events) != 1 || events[0].Cluster.Reason != "boundary" || next != "" {
		t.Fatalf("append-boundary event lost: events=%+v next=%q err=%v", events, next, err)
	}
}

func TestEventRangeAppendHourAndRowContinuation(t *testing.T) {
	for _, archived := range []bool{false, true} {
		t.Run(fmt.Sprintf("archived=%t", archived), func(t *testing.T) {
			archive := &requestQueryArchive{readableObservationArchive: newReadableObservationArchive(t)}
			log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
			from := observationTestHour.Add(time.Minute)
			boundary := observationTestHour.Add(time.Hour)
			clock.set(boundary.Add(-time.Nanosecond))
			if err := log.SchedulerRecord(SchedulerObservation{At: from.Add(2 * time.Second), Action: &SchedulerActionObservation{ID: "before", Kind: "power"}}); err != nil {
				t.Fatal(err)
			}
			if err := log.ClusterRecord(ClusterObservation{Kind: "issue", At: from.Add(time.Second), Event: "raised", Source: "observer", Reason: "same-hour"}); err != nil {
				t.Fatal(err)
			}
			clock.set(boundary)
			if err := log.SchedulerRecord(SchedulerObservation{At: from, Action: &SchedulerActionObservation{ID: "boundary-late", Kind: "power"}}); err != nil {
				t.Fatal(err)
			}
			clock.set(boundary.Add(time.Hour + time.Second))
			if archived {
				clock.set(boundary.Add(4 * time.Hour))
				if err := log.Flush(context.Background()); err != nil {
					t.Fatal(err)
				}
				log, _ = clockedObservationLog(t, ObservationLogConfig{Writer: archive}, clock.now())
			}
			archive.rejectCatalog = true
			cursor := ""
			var got []string
			for i := range 3 {
				events, next, coverage, err := log.EventObservationPage(context.Background(), from, from.Add(3*time.Second), 1, cursor)
				if err != nil || len(events) != 1 || coverage.From == nil || coverage.To == nil {
					t.Fatalf("page %d: %+v next=%q coverage=%+v err=%v", i, events, next, coverage, err)
				}
				start := observationTestHour
				if i == 2 {
					start = boundary
				}
				if !coverage.From.Equal(start) || !coverage.To.Equal(start.Add(time.Hour)) {
					t.Fatalf("page %d searched beyond its append hour: %+v", i, coverage)
				}
				if events[0].Action != nil {
					got = append(got, events[0].Action.ID)
				} else {
					got = append(got, events[0].Cluster.Reason)
				}
				wantNext := boundary.Format(time.RFC3339Nano)
				if i == 0 {
					wantNext = observationTestHour.Format(time.RFC3339Nano) + "/1"
				} else if i == 2 {
					wantNext = boundary.Add(time.Hour).Format(time.RFC3339Nano)
				}
				if next != wantNext {
					t.Fatalf("page %d continuation %q want %q", i, next, wantNext)
				}
				cursor = next
			}
			if !reflect.DeepEqual(got, []string{"before", "same-hour", "boundary-late"}) || archive.catalogReads != 0 {
				t.Fatalf("append-hour continuation lost/duplicated facts or hydrated catalog: %v reads=%d", got, archive.catalogReads)
			}
			hour := observationTestHour.Format(time.RFC3339Nano)
			for _, bad := range []string{hour + "/", hour + "/-1", hour + "/+1", hour + "/1/2", hour + "/999999999999999999999", hour + "/3", boundary.Add(24 * time.Hour).Format(time.RFC3339Nano)} {
				if _, _, _, err := log.EventObservationPage(context.Background(), from, from.Add(3*time.Second), 1, bad); rawErrorCode(err) != powerSeriesInvalidQuery {
					t.Fatalf("invalid cursor accepted %q: %v", bad, err)
				}
			}
		})
	}
}

func TestEventRangeRecoversAppendAfterFirstPage(t *testing.T) {
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	from := observationTestHour.Add(time.Minute)
	clock.set(from)
	if err := log.ClusterRecord(ClusterObservation{Kind: "issue", At: from, Event: "raised", Source: "observer", Reason: "first"}); err != nil {
		t.Fatal(err)
	}
	boundary := observationTestHour.Add(time.Hour)
	clock.set(boundary.Add(time.Second))
	page, next, _, err := log.EventObservationPage(context.Background(), from, from.Add(time.Second), 1, "")
	if err != nil || len(page) != 1 || page[0].Cluster.Reason != "first" || next != boundary.Format(time.RFC3339Nano) {
		t.Fatalf("first page: %+v next=%q err=%v", page, next, err)
	}
	if err := log.ClusterRecord(ClusterObservation{Kind: "issue", At: from, Event: "raised", Source: "observer", Reason: "late"}); err != nil {
		t.Fatal(err)
	}
	page, next, _, err = log.EventObservationPage(context.Background(), from, from.Add(time.Second), 1, next)
	if err != nil || len(page) != 1 || page[0].Cluster.Reason != "late" || next != "" {
		t.Fatalf("late append: %+v next=%q err=%v", page, next, err)
	}
}

func TestObservationTimestampMatchesFullDecode(t *testing.T) {
	for _, raw := range []string{
		`{"at":"2026-11-02T03:25:22.100721506Z","action":{"id":"a"}}`,
		`{"action":{"id":"a"},"at":"2026-11-02T03:25:22.100721506Z"}`,
		`{"at":"2026-11-02T12:25:22.100721506+09:00"}`,
		`{"kind":"issue","at":"2026-11-02T03:25:22Z"}`,
		`{"at":null}`,
	} {
		var full struct{ At time.Time }
		if err := json.Unmarshal([]byte(raw), &full); err != nil {
			t.Fatal(err)
		}
		at, err := observationTimestamp([]byte(raw))
		if err != nil || !at.Equal(full.At) {
			t.Fatalf("prefilter changed timestamp: %s -> %s want %s err=%v", raw, at, full.At, err)
		}
	}
	for _, raw := range []string{`{"at":`, `{"at":"bad"}`, `{"other":{}`} {
		if _, err := observationTimestamp([]byte(raw)); err == nil {
			t.Fatalf("malformed timestamp accepted: %s", raw)
		}
	}
}
