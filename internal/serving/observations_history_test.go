package serving

import (
	"context"
	"testing"
	"time"
)

func TestSchedulerHistoryWindowScopesIncompleteDependencies(t *testing.T) {
	for _, dependency := range []bool{false, true} {
		t.Run(map[bool]string{false: "unrelated old window", true: "required old definition"}[dependency], func(t *testing.T) {
			journal, err := NewJournal(JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = journal.Close(context.Background()) })
			_, snapshot := schedulerFixture()
			definition := snapshot.Workloads[0]
			old := journal.active
			if dependency {
				if err := journal.DefinitionRecord(definition); err != nil {
					t.Fatal(err)
				}
			}
			if err := journal.RequestEvent(RequestEvent{RequestID: "old-unrelated", Kind: phaseReceived, At: time.Now().Add(-24 * time.Hour)}); err != nil {
				t.Fatal(err)
			}
			if err := journal.finish(); err != nil {
				t.Fatal(err)
			}
			from := time.Now().UTC()
			journal.windows[old].StartedAt, journal.windows[old].EndsAt = from.Add(-24*time.Hour), from.Add(-23*time.Hour)
			journal.windows[old].Incomplete = true
			if err := journal.start(from); err != nil {
				t.Fatal(err)
			}
			if !dependency {
				if err := journal.DefinitionRecord(definition); err != nil {
					t.Fatal(err)
				}
			}
			for _, kind := range []string{phaseReceived, "validated", phaseAccepted} {
				if err := journal.RequestEvent(RequestEvent{RequestID: "current-receipt", WorkloadUID: definition.UID, Definition: definition.Identity(), Kind: kind, At: from, ReceivedAt: from}); err != nil {
					t.Fatal(err)
				}
			}
			window, err := journal.SchedulerHistoryWindow(from, from.Add(time.Second))
			if dependency {
				if err == nil {
					t.Fatal("selected immutable definition from incomplete window was ignored")
				}
				return
			}
			if err != nil || len(window.Arrivals) != 1 || window.Arrivals[0].Definition == nil {
				t.Fatalf("unrelated old metadata blocked current evidence: %+v %v", window, err)
			}
			journal.windows[journal.active].Incomplete = true
			if _, err := journal.SchedulerHistoryWindow(from, from.Add(time.Second)); err == nil {
				t.Fatal("overlapping incomplete receipt window was accepted")
			}
		})
	}
}
