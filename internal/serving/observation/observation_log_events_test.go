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
	events, err := restarted.EventObservations(ctx, at, 10)
	if err != nil || len(events) != 2 || events[0].Action == nil || events[0].Action.Error != timedOut || events[1].Cluster == nil || events[1].Cluster.Reason != "not-ready" {
		t.Fatalf("archived action/cluster recovery: %+v err=%v", events, err)
	}
	empty, err := restarted.EventObservations(ctx, at.Add(time.Nanosecond), 10)
	if err != nil || len(empty) != 0 || archive.catalogReads != 0 {
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
			events, err := log.EventObservations(ctx, at, 2)
			if err != nil || len(events) != 2 {
				t.Fatalf("exact event recovery lost a late append: %+v err=%v", events, err)
			}
			if events[0].Action == nil || events[0].Action.ID != "action" ||
				events[1].Cluster == nil || !events[1].Cluster.At.Equal(at) {
				t.Fatalf("exact event recovery returned different facts: %+v", events)
			}
			if _, err := log.EventObservations(ctx, at, 1); rawErrorCode(err) != "IncompleteObservation" {
				t.Fatalf("late simultaneous event bypassed the bound: %v", err)
			}
			if archive.catalogReads != 0 {
				t.Fatalf("event recovery opened %d catalogs", archive.catalogReads)
			}
		})
	}
}
