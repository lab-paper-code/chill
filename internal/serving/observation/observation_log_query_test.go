package observation

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

type requestQueryArchive struct {
	*readableObservationArchive
	rejectCatalog bool
	catalogReads  int
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
	definition := &serving.Definition{Name: "deleted-service", UID: archiveTestUID, Generation: 1, SLO: serving.SLO{Percentile: 90, TargetLatency: time.Second}}
	clock.set(at)
	if err := log.DefinitionRecord(definition); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		for _, kind := range []string{"received", "assigned"} {
			err := log.RequestEvent(serving.RequestEvent{RequestID: id, WorkloadUID: definition.UID, Definition: definition.Identity(), Kind: kind, At: at, ReceivedAt: at,
				Assignment: &serving.Assignment{Instance: "physical", DispatchedAt: at}})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	// Persistence is intentionally hours later; receipt-window clipping must
	// never turn these final responses into incomplete assignments.
	clock.set(at.Add(3 * time.Hour))
	for index, id := range []string{"a", "b", "c"} {
		if err := log.RequestEvent(serving.RequestEvent{RequestID: id, Kind: "response_finished", At: at.Add(time.Duration(index+1) * time.Millisecond), Outcome: "completed"}); err != nil {
			t.Fatal(err)
		}
		if err := log.RequestClosed(id, serving.RequestTimings{}); err != nil {
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
		if first.Total != 3 || first.Selected != 3 || first.Outcomes["completed"] != 3 || len(first.Requests) != 2 || first.NextCursor != "2" || len(first.Cohorts) != 1 {
			t.Fatalf("whole-window aggregation/page changed: %+v", first)
		}
		cohort := first.Cohorts[0]
		if cohort.SampleCount != 3 || cohort.QuantileNanos == nil || *cohort.QuantileNanos != float64(3*time.Millisecond) || len(first.Definitions) != 1 {
			t.Fatalf("late final statistics/definition lost: %+v", first)
		}
		last, err := log.Query(ctx, definition.UID, at, at.Add(window), nil, nil, 2, first.NextCursor)
		if err != nil || len(last.Requests) != 1 || last.Requests[0].RequestID != "c" || last.Requests[0].Kind != serving.RequestRowFinal || last.NextCursor != "" || last.Total != first.Total {
			t.Fatalf("historical terminal page: %+v err=%v", last, err)
		}
	}
	if archive.catalogReads != 0 {
		t.Fatalf("request rows read %d irrelevant profile catalogs", archive.catalogReads)
	}
}

func TestRequestQuerySeparatesCompletedLatencyFromAllOtherOutcomes(t *testing.T) {
	ctx := context.Background()
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	at := observationTestHour.Add(time.Second)
	clock.set(at)
	definition := &serving.Definition{Name: "overloaded", UID: archiveTestUID, Generation: 1, SLO: serving.SLO{Percentile: 95, TargetLatency: time.Second}}
	if err := log.DefinitionRecord(definition); err != nil {
		t.Fatal(err)
	}
	outcomes := []string{"completed", "rejected", "timeout", "error", "canceled", "delivery_failed", "incomplete", "", ""}
	for index, outcome := range outcomes {
		id := string(rune('a' + index))
		events := []serving.RequestEvent{{Kind: "received", At: at}}
		if index != len(outcomes)-1 {
			events = append(events, serving.RequestEvent{Kind: "accepted", At: at})
		}
		elapsed := time.Millisecond
		if outcome == "completed" {
			elapsed = 100 * time.Millisecond
		}
		events = append(events, serving.RequestEvent{Kind: "response_finished", At: at.Add(elapsed), ElapsedNanos: int64(elapsed), Outcome: outcome})
		for _, event := range events {
			event.RequestID, event.WorkloadUID, event.Definition, event.ReceivedAt = id, definition.UID, definition.Identity(), at
			if err := log.RequestEvent(event); err != nil {
				t.Fatal(err)
			}
		}
		if err := log.RequestClosed(id, serving.RequestTimings{}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := log.Query(ctx, definition.UID, at, at.Add(time.Second), nil, nil, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != len(outcomes) || result.Selected != len(outcomes) || len(result.Requests) != 1 || len(result.Cohorts) != 1 {
		t.Fatalf("population/page accounting: %+v", result)
	}
	for _, outcome := range outcomes[:7] {
		want := 1
		if outcome == "incomplete" {
			want = 2
		}
		if result.Outcomes[outcome] != want {
			t.Fatalf("%s count = %d, want %d", outcome, result.Outcomes[outcome], want)
		}
	}
	if result.Outcomes["pending"] != 1 {
		t.Fatalf("outcome-less elapsed row became completion: %+v", result.Outcomes)
	}
	cohort := result.Cohorts[0]
	if cohort.Total != len(outcomes) || cohort.SampleCount != 1 || len(cohort.LatencyNanos) != 1 ||
		cohort.LatencyNanos[0] != float64(100*time.Millisecond) || cohort.QuantileNanos == nil || *cohort.QuantileNanos != float64(100*time.Millisecond) {
		t.Fatalf("non-completed latency entered distribution: %+v", cohort)
	}
}
