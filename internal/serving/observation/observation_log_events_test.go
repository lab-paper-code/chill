package observation

import (
	"context"
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
			if _, _, _, err := log.EventObservations(ctx, at, 1, cursor); rawErrorCode(err) != "IncompleteObservation" {
				t.Fatalf("late simultaneous event bypassed the bound: %v", err)
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
