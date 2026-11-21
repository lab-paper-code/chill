package serving

import (
	"testing"
	"time"
)

// A decision reads the load from the change an earlier decision detected and
// remembered (snapshot.LoadChangedAt). The oldest 10 of w1's 60 arrivals
// failed, a significant miss for the measured verdict of a load window that
// holds them. Without the remembered change the window holds them and the
// decision adds an instance. Once the change after them is remembered, the
// window starts there and leaves them out: the running instance is kept.
func TestRememberedLoadChangeLeavesEarlierMissesOut(t *testing.T) {
	s, snap := measuredFixture()
	measuredReports(&snap, 10)
	if d := capacityDecideForTest(t, s, snap); d.Target == nil || len(d.Target.Instances) < 2 {
		t.Fatalf("the failed arrivals in the load window must add capacity: %s %v", d.Status, d.Reasons)
	}
	snap.LoadChangedAt = map[string]time.Time{"w1": snap.Arrivals[10].At}
	if d := capacityDecideForTest(t, s, snap); d.Status != SchedulerStatusKeep || !d.Search.Loads[0].WindowStart.Equal(snap.Arrivals[10].At) {
		t.Fatalf("the remembered change must leave the failed arrivals out and keep: %s %+v", d.Status, d.Search.Loads)
	}
}
