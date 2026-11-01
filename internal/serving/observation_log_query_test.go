package serving

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

type requestQueryArchive struct {
	*readableObservationArchive
	rejectCatalog bool
	catalogReads int
}

func (a *requestQueryArchive) Open(ctx context.Context, object evidencestore.ObjectIdentity) (io.ReadCloser, error) {
	if strings.HasSuffix(object.Key, "/catalog.jsonl.gz") {
		a.catalogReads++
		if a.rejectCatalog {
			return nil, errors.New("request query must not fetch unrelated profile catalogs")
		}
	}
	return a.readableObservationArchive.Open(ctx, object)
}

func TestHistoricalRequestQueryReadsOnlyRequiredEvidenceAndKeepsLateFinalStatistics(t *testing.T) {
	ctx := context.Background()
	archive := &requestQueryArchive{readableObservationArchive: newReadableObservationArchive(t)}
	log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
	at := observationTestHour.Add(time.Second)
	definition := &Definition{Name: "deleted-service", UID: archiveTestUID, Generation: 1, SLO: SLO{Percentile: 90, TargetLatency: time.Second}}
	clock.set(at)
	if err := log.DefinitionRecord(definition); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		for _, kind := range []string{phaseReceived, phaseAssigned} {
			err := log.RequestEvent(RequestEvent{RequestID: id, WorkloadUID: definition.UID, Definition: definition.Identity(), Kind: kind, At: at, ReceivedAt: at,
				Assignment: &Assignment{Instance: "physical", DispatchedAt: at}})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	// Persistence is intentionally hours later; receipt-window clipping must
	// never turn these final responses into incomplete assignments.
	clock.set(at.Add(3 * time.Hour))
	for index, id := range []string{"a", "b", "c"} {
		if err := log.RequestEvent(RequestEvent{RequestID: id, Kind: eventResponseFinished, At: at.Add(time.Duration(index+1) * time.Millisecond), Outcome: outcomeCompleted}); err != nil {
			t.Fatal(err)
		}
		if err := log.RequestClosed(id); err != nil {
			t.Fatal(err)
		}
	}
	clock.set(at.Add(6 * time.Hour))
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	archive.rejectCatalog = true
	for _, window := range []time.Duration{time.Second, 20 * time.Minute} {
		first, err := log.Query(ctx, definition.UID, at, at.Add(window), nil, nil, 2, "")
		if err != nil {
			t.Fatalf("historical %s query fetched irrelevant evidence: %v", window, err)
		}
		if first.Total != 3 || first.Selected != 3 || first.Outcomes[outcomeCompleted] != 3 || len(first.Requests) != 2 || first.NextCursor != "2" || len(first.Cohorts) != 1 {
			t.Fatalf("whole-window aggregation/page changed: %+v", first)
		}
		cohort := first.Cohorts[0]
		if cohort.SampleCount != 3 || cohort.QuantileNanos == nil || *cohort.QuantileNanos != float64(3*time.Millisecond) || len(first.Definitions) != 1 {
			t.Fatalf("late final statistics/definition lost: %+v", first)
		}
		last, err := log.Query(ctx, definition.UID, at, at.Add(window), nil, nil, 2, first.NextCursor)
		if err != nil || len(last.Requests) != 1 || last.Requests[0].RequestID != "c" || last.Requests[0].Kind != requestRowFinal || last.NextCursor != "" || last.Total != first.Total {
			t.Fatalf("historical terminal page: %+v err=%v", last, err)
		}
	}
	if archive.catalogReads != 0 {
		t.Fatalf("request rows read %d irrelevant profile catalogs", archive.catalogReads)
	}
}
