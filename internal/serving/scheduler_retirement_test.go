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

// A node-local removal can be the source half of required cross-node recovery,
// not optional shrinkage. MaxReplicas leaves no room to retain the old source.
func TestPlannerCrossNodeRecoveryAllowsHigherEnergy(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod = time.Minute, 10*time.Second, 10*time.Minute
	s.MaxReplicas, s.MaxInstances = 1, 4
	s.Controls.Power = false
	snap.Workloads[0].SLO.TargetLatency = time.Second
	old := snap.Instances[0]
	destination := poolsAddNode(&snap, "recovery-b", map[string]float64{"fixed": 10})
	replacement := poolsInstance(old, "recovery-b-option", destination, "fixed", "w1")
	replacement.Profile.IdleBaselineWatts = 30
	snap.Options = append(snap.Options, poolsOption(replacement))
	snap.Routing = map[string]map[string]int32{"w1": {old.ID: schedulerRouteUnits}}
	snap.Costs = []OperationCost{
		poolsCost(schedulerActionPrepare, destination, "unloaded", schedulerState(replacement), "fixed"),
		poolsCost(schedulerActionRemove, old.Node, schedulerState(old), "unloaded", "fixed", schedulerState(old)),
	}
	capacityLoad(s, &snap, snap.Workloads[0], 80)
	rows := capacityRowsForTest(s, snap, func(o SchedulerOption) float64 {
		if o.Instance.Node == old.Node {
			return 40
		}
		return 100
	})
	d := capacityDecideForTest(t, s, snap, rows)
	if d.Status != schedulerStatusFeasible || d.Target == nil || d.Target.Keep ||
		len(d.Target.Instances) != 1 || d.Target.Instances[0].Node != destination {
		t.Fatalf("cross-node recovery rejected: status=%s target=%+v reasons=%v", d.Status, d.Target, d.Reasons)
	}
	if d.Target.ExpectedJoules == nil || d.Keep.ExpectedJoules == nil || *d.Target.ExpectedJoules <= *d.Keep.ExpectedJoules {
		t.Fatal("fixture must require higher-energy recovery")
	}
	kinds := make([]string, 0, len(d.Target.Actions))
	for _, a := range d.Target.Actions {
		kinds = append(kinds, a.Kind)
	}
	if !slices.Equal(kinds, []string{schedulerActionPrepare, schedulerActionRoute, schedulerActionStop, schedulerActionDrain, schedulerActionRemove}) {
		t.Fatalf("recovery lost prepare/route/retire ordering: %v", kinds)
	}
	// Better than A's κ=40 is not enough: κ=60 still cannot serve λ=80.
	// An infeasible target must not acquire the feasible-recovery exception.
	for n := range rows {
		if rows[n].Input.Node == destination {
			kappa := 60.0
			rows[n].Kappa = &kappa
		}
	}
	d = capacityDecideForTest(t, s, snap, rows)
	if d.Status != SchedulerStatusOverload || d.Target == nil || !d.Target.Keep ||
		len(d.Target.Instances) != 1 || d.Target.Instances[0].ID != old.ID || len(d.Target.Actions) != 0 {
		t.Fatalf("less-bad infeasible target retired A: status=%s target=%+v reasons=%v", d.Status, d.Target, d.Reasons)
	}
}
