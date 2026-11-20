package serving

import (
	"context"
	"testing"
	"time"
)

// Admission of a launch reads the load the way a decision does: from the change
// an earlier decision detected and remembered (snapshot.LoadChangedAt). The
// oldest 6 of w1's 60 arrivals failed, a miss for the measured verdict of a load
// window that holds them. The window of a fresh test holds them: the decision
// adds an instance, which admission admits. Once the change after them is
// remembered, the window leaves them out: the running instance is kept, and
// admission no longer prefers the added one.
func TestAdmissionSeesTheRememberedLoadChange(t *testing.T) {
	s, snap := measuredFixture()
	measuredReports(&snap, 6)
	fresh := capacityDecideForTest(t, s, snap)
	if fresh.Target == nil || len(fresh.Target.Instances) < 2 {
		t.Fatalf("fixture: the failed arrivals must add capacity: %s %v", fresh.Status, fresh.Reasons)
	}
	if reason, err := SchedulerTargetAdmission(context.Background(), s, snap, *fresh.Target); err != nil || reason != "" {
		t.Fatalf("fixture: the decision's own target is not admitted: %q %v", reason, err)
	}
	snap.LoadChangedAt = map[string]time.Time{"w1": snap.Arrivals[6].At}
	if d := capacityDecideForTest(t, s, snap); d.Status != SchedulerStatusKeep || !d.Search.Loads[0].WindowStart.Equal(snap.Arrivals[6].At) {
		t.Fatalf("fixture: the remembered change must leave the failed arrivals out and keep: %s %+v", d.Status, d.Search.Loads)
	}
	if reason, err := SchedulerTargetAdmission(context.Background(), s, snap, *fresh.Target); err != nil || reason == "" {
		t.Fatalf("admission ignored the remembered change: %q %v", reason, err)
	}
}
