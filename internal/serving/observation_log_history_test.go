package serving

import (
	"context"
	"testing"
	"time"
)

func TestSchedulerHistoryWindowScopesIncompleteDependencies(t *testing.T) {
	for _, dependency := range []bool{false, true} {
		t.Run(map[bool]string{false: "unrelated old window", true: "required old definition"}[dependency], func(t *testing.T) {
			observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = observationLog.Close(context.Background()) })
			_, snapshot := schedulerFixture()
			definition := snapshot.Workloads[0]
			old := observationLog.active
			if dependency {
				if err := observationLog.DefinitionRecord(definition); err != nil {
					t.Fatal(err)
				}
			}
			if err := observationLog.RequestEvent(RequestEvent{RequestID: "old-unrelated", Kind: phaseReceived, At: time.Now().Add(-24 * time.Hour)}); err != nil {
				t.Fatal(err)
			}
			if err := observationLog.finish(); err != nil {
				t.Fatal(err)
			}
			from := time.Now().UTC()
			observationLog.windows[old].StartedAt, observationLog.windows[old].EndsAt = from.Add(-24*time.Hour), from.Add(-23*time.Hour)
			observationLog.windows[old].Incomplete = true
			if err := observationLog.start(from); err != nil {
				t.Fatal(err)
			}
			if !dependency {
				if err := observationLog.DefinitionRecord(definition); err != nil {
					t.Fatal(err)
				}
			}
			for _, kind := range []string{phaseReceived, "validated", phaseAccepted} {
				if err := observationLog.RequestEvent(RequestEvent{RequestID: "current-receipt", WorkloadUID: definition.UID, Definition: definition.Identity(), Kind: kind, At: from, ReceivedAt: from}); err != nil {
					t.Fatal(err)
				}
			}
			window, err := observationLog.SchedulerHistoryWindow(from, from.Add(time.Second))
			if dependency {
				if err == nil {
					t.Fatal("selected immutable definition from incomplete window was ignored")
				}
				return
			}
			if err != nil || len(window.Arrivals) != 1 || window.Arrivals[0].Definition == nil {
				t.Fatalf("unrelated old metadata blocked current evidence: %+v %v", window, err)
			}
			observationLog.windows[observationLog.active].Incomplete = true
			if _, err := observationLog.SchedulerHistoryWindow(from, from.Add(time.Second)); err == nil {
				t.Fatal("overlapping incomplete receipt window was accepted")
			}
		})
	}
}
