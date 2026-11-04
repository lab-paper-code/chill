package serving

import (
	"slices"
	"testing"
	"time"
)

// Retirement on an unrelated idle node is not overload recovery. The two
// overloaded cases reproduce T15:5939/5971's IC rate, queue and remaining κ.
func TestPlannerRetirementUsesSLOAndHorizonGain(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rate       float64
		queued     int
		loaded     float64
		remove     bool
		terminated bool
	}{
		{"T15-5939", 99.7, 193, 60, false, false},
		{"T15-5971", 101.666667, 214, 60, false, false},
		{"feasible-removal-cost-exceeds-saving", 2, 0, 50.07, false, false},
		{"feasible-negative-gain", 2, 0, 49.80, false, false},
		{"feasible-positive-gain-unconfirmed-sibling", 2, 0, 60, true, false},
		{"terminated-occupancy-is-not-live-retirement", 99.7, 193, 60, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, snap, _, idleNode, badNode := capacityR5Fixture()
			s.Controls.Routing = false
			snap.Costs[0].Trials[0].Joules = 100
			snap.Workloads[0].SLO.TargetLatency = time.Second
			snap.EmptyNodeScaleDownAllowed = true
			s.ScaleDownStabilization = time.Minute
			snap.ScaleDown = map[string]SchedulerDownState{"w1": {Allowed: true, Supply: 70.4}}
			since := snap.At.Add(-s.ObservationWindow)
			for k := 1; k < len(snap.Instances); k++ {
				i := &snap.Instances[k]
				i.UnroutedSince = &since
				if i.Node == idleNode {
					profile := *i.Profile
					profile.IdleBaselineWatts = tc.loaded
					i.Profile = &profile
					i.WorkloadUID, i.OwnerWorkloadUID = "", ""
					i.Terminated, i.Ready = tc.terminated, !tc.terminated
					snap.Options[k].Instance = *i
				} else {
					i.Ready = false
				}
			}
			snap.Nodes[2].Reasons = []string{NodeInstanceUnavailable}
			capacityLoad(s, &snap, snap.Workloads[0], tc.rate-float64(tc.queued)/s.ObservationWindow.Seconds())
			runtime, _ := backlogRuntimeForTest(snap, phaseAssigned, tc.queued, false)
			snap.Outstanding = runtime.SchedulerInputs(func() time.Time { return snap.At }).Outstanding
			rows := capacityRowsForTest(s, snap, func(SchedulerOption) float64 { return 70.4 })
			d := capacityDecideForTest(t, s, snap, rows)
			if d.Target == nil {
				t.Fatalf("missing target: %s %v", d.Status, d.Reasons)
			}
			removed := slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return a.Kind == schedulerActionRemove && a.Node == idleNode })
			if removed != tc.remove || poolsOn(*d.Target, idleNode) == tc.remove {
				t.Fatalf("removed=%v want=%v status=%s reasons=%v target=%+v", removed, tc.remove, d.Status, d.Reasons, d.Target)
			}
			defectERequireNoPhysicalActions(t, d.Target.Actions, badNode)
			if tc.queued > 0 {
				w := d.Capacity.Workloads[0]
				if d.Status != SchedulerStatusOverload || w.WaitSeconds == nil || *w.WaitSeconds <= .971 || w.Queued != tc.queued {
					t.Fatalf("lost overload witness: %s %+v", d.Status, w)
				}
			} else if tc.remove && (d.Target.ExpectedJoules == nil || d.Keep.ExpectedJoules == nil || *d.Target.ExpectedJoules >= *d.Keep.ExpectedJoules) {
				t.Fatalf("retirement lacks positive H gain: %+v", d)
			}
		})
	}
}
