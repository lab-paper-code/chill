package serving

import (
	"context"
	"testing"
	"time"
)

func TestLoadWindowKeepsInflowSeparateFromCompletionAndFilters(t *testing.T) {
	j, err := NewJournal(JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}, Definitions: []string{"unrelated"}, Outcomes: []string{"completed"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := j.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	from := time.Now().UTC().Add(-time.Second)
	to := from.Add(time.Second)
	d := &Definition{Name: "workload", UID: "uid", Generation: 1, SLO: SLO{Percentile: 95, TargetLatency: time.Second}}
	if err = j.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	appendEvent := func(id, uid, kind, outcome string, at time.Time) {
		t.Helper()
		e := RequestEvent{RequestID: id, WorkloadUID: uid, Kind: kind, Outcome: outcome, At: at}
		if kind == "validated" || kind == "accepted" {
			e.Definition = d.Identity()
		}
		if err := j.RequestEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent("old", "uid", "received", "", from.Add(-time.Nanosecond))
	appendEvent("old", "uid", "response_finished", "completed", from)
	appendEvent("valid", "uid", "received", "", from)
	appendEvent("valid", "uid", "validated", "", from)
	appendEvent("valid", "uid", "accepted", "", from)
	appendEvent("valid", "uid", "assigned", "", from)
	appendEvent("valid", "uid", "response_finished", "completed", to)
	appendEvent("bad", "uid", "received", "", from.Add(time.Millisecond))
	appendEvent("bad", "uid", "response_finished", "malformed", from.Add(time.Millisecond))
	appendEvent("missing", "", "received", "", from.Add(2*time.Millisecond))
	appendEvent("at-end", "uid", "received", "", to)
	window, err := j.LoadWindow(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Arrivals) != 3 {
		t.Fatalf("received population: %+v", window)
	}
	byUID := map[string]WorkloadLoad{}
	for _, load := range window.Workloads {
		byUID[load.UID] = load
	}
	if load := byUID["uid"]; load.Received != 2 || load.Accepted != 1 || load.Assigned != 1 || load.Completed != 1 || load.ReceivedRate != 2 || load.CompletionRate != 1 {
		t.Fatalf("event boundary or cohort mixed: %+v", load)
	}
	if byUID[""].Received != 1 {
		t.Fatalf("unknown UID inflow lost: %+v", window)
	}
	if a := window.Arrivals[0]; a.WorkloadUID != d.UID || a.Definition == nil || !a.Accepted || !a.Compute {
		t.Fatalf("frozen compute context lost: %+v", a)
	}
	if window.Arrivals[1].Compute || window.Arrivals[1].Outcome != "malformed" || window.Arrivals[2].Compute {
		t.Fatalf("non-compute demand included: %+v", window.Arrivals)
	}
	empty, err := j.LoadWindow(to.Add(time.Second), to.Add(2*time.Second))
	if err != nil || len(empty.Arrivals) != 0 || len(empty.Workloads) != 0 {
		t.Fatalf("empty event window: %+v %v", empty, err)
	}
}

func TestSchedulerRawCursorBoundsAppendOrderAcrossWindow(t *testing.T) {
	j, err := NewJournal(JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := j.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	from := time.Now().UTC().Add(-time.Second)
	to := from.Add(time.Second)
	for _, event := range []SchedulerObservation{
		{At: from.Add(-time.Nanosecond), Action: &SchedulerActionObservation{ID: "outside"}},
		{At: from.Add(time.Millisecond), Action: &SchedulerActionObservation{ID: "first"}},
		{At: from, Action: &SchedulerActionObservation{ID: "second"}},
		{At: to, Action: &SchedulerActionObservation{ID: "boundary"}},
	} {
		if err = j.SchedulerRecord(event); err != nil {
			t.Fatal(err)
		}
	}
	first, next, err := j.SchedulerObservationPage(from, to, 1, 0)
	if err != nil || len(first) != 1 || first[0].Action.ID != "first" || next != 1 {
		t.Fatalf("first bounded append page: %+v %d %v", first, next, err)
	}
	second, next, err := j.SchedulerObservationPage(from, to, 1, next)
	if err != nil || len(second) != 1 || second[0].Action.ID != "second" || next != -1 {
		t.Fatalf("raw cursor reordered/backfilled events: %+v %d %v", second, next, err)
	}
	if _, _, err = j.SchedulerObservationPage(from, to, 1, 3); err == nil {
		t.Fatal("out-of-range cursor accepted")
	}
}
