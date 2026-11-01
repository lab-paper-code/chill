package serving

import (
	"context"
	"testing"
	"time"
)

func TestArchivedEventRecoverySkipsCatalogHydration(t *testing.T) {
	ctx := context.Background()
	archive := &requestQueryArchive{readableObservationArchive: newReadableObservationArchive(t)}
	log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
	at := observationTestHour.Add(time.Second)
	clock.set(at)
	if err := log.SchedulerRecord(SchedulerObservation{At: at, Action: &SchedulerActionObservation{ID: "action", Kind: "power", Phase: "failed", Error: "timeout"}}); err != nil {
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
	if err != nil || len(events) != 2 || events[0].Action == nil || events[0].Action.Error != "timeout" || events[1].Cluster == nil || events[1].Cluster.Reason != "not-ready" {
		t.Fatalf("archived action/cluster recovery: %+v err=%v", events, err)
	}
	empty, err := restarted.EventObservations(ctx, at.Add(time.Nanosecond), 10)
	if err != nil || len(empty) != 0 || archive.catalogReads != 0 {
		t.Fatalf("nearby timestamp invented a match or opened catalogs: %+v err=%v reads=%d", empty, err, archive.catalogReads)
	}
}
