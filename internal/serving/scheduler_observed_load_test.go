package serving

import (
	"testing"
	"time"
)

// idleFixture runs w1 on n1 (i1) and on n2 (i2, 20 W idle: removing it saves
// 10 W) with no arrival in the window.
func idleFixture() (SchedulerSettings, SchedulerSnapshot, NodeReference) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod = time.Minute, 10*time.Second, time.Minute
	snap.ObservedSince = snap.ObservationEnd.Add(-time.Hour)
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	i2 := poolsInstance(snap.Instances[0], "i2", n2, "fixed", "w1")
	profile := *i2.Profile
	profile.Power = schedulerTestPower(20)
	i2.Profile = &profile
	snap.Instances = append(snap.Instances, i2)
	snap.Options = append(snap.Options, poolsOption(i2))
	snap.Routing = map[string]map[string]int32{"w1": {"i1": 500, "i2": 500}}
	snap.Costs = []OperationCost{poolsCost(schedulerActionRemove, n2, schedulerState(i2), "unloaded", "fixed")}
	capacityServiceRatesForTest(&snap, map[string]float64{snap.Instances[0].Profile.ID: 25, i2.Profile.ID: 25})
	return s, snap, n2
}

// A Workload always keeps a routed instance, so an idle Workload with a spare
// gives the spare up for the energy and keeps the other; the replay holds no
// request nobody sent. A request that comes after that is admitted by the
// instance that stayed.
func TestIdleWorkloadKeepsOneRoutedInstance(t *testing.T) {
	s, snap, n2 := idleFixture()
	d := capacityDecideForTest(t, s, snap)
	if d.Target == nil || d.Target.Keep || poolsOn(*d.Target, n2) || len(d.Target.Instances) != 1 || d.Target.Routing["w1"]["i1"] != schedulerRouteUnits {
		t.Fatalf("idle spare kept or the last instance lost its route: %s %v %+v", d.Status, d.Reasons, d.Target)
	}
	if len(d.Target.Feedback) != 1 || d.Target.Feedback[0].Population != 0 || d.Target.Feedback[0].ReplayedArrivals != 0 {
		t.Fatalf("replay held load nobody sent: %+v", d.Target.Feedback)
	}
	// The configuration the decision reached, then one request.
	snap.Instances, snap.Routing = d.Target.Instances[:1], map[string]map[string]int32{"w1": {"i1": schedulerRouteUnits}}
	snap.Arrivals = []SchedulerArrival{{ID: "late", At: snap.ObservationEnd.Add(-time.Second), Definition: snap.Workloads[0], Accepted: true, Compute: true, WorkloadUID: "w1"}}
	after := capacityDecideForTest(t, s, snap)
	if after.Target == nil || len(after.Target.Feedback) != 1 || after.Target.Feedback[0].Population != 1 || !after.Target.Feedback[0].Satisfied {
		t.Fatalf("request after idle not admitted by the remaining instance: %s %v %+v", after.Status, after.Reasons, after.Target)
	}
}

// A transition at idle is judged like any other: a prepare whose cost the
// idle saving does not repay over the comparison period is not started.
func TestIdleStartsNoPrepareThatDoesNotRepay(t *testing.T) {
	s, snap, n2 := idleFixture()
	snap.Instances, snap.Options = snap.Instances[:1], snap.Options[:1]
	snap.Routing = map[string]map[string]int32{"w1": {"i1": schedulerRouteUnits}}
	i1 := snap.Instances[0]
	profile := *i1.Profile
	profile.Power = schedulerTestPower(20) // moving to n2 (12 W) saves 8 W
	snap.Instances[0].Profile = &profile
	snap.Options[0].Instance.Profile = &profile
	cheap := poolsInstance(i1, "c", n2, "fixed", "w1")
	cheap.Profile.Power = schedulerTestPower(12)
	snap.Options = append(snap.Options, poolsOption(cheap))
	prepare := poolsCost(schedulerActionPrepare, n2, "unloaded", schedulerState(cheap), "fixed")
	snap.Costs = []OperationCost{prepare, poolsCost(schedulerActionRemove, i1.Node, schedulerState(snap.Instances[0]), "unloaded", "fixed")}
	capacityServiceRatesForTest(&snap, map[string]float64{profile.ID: 25, cheap.Profile.ID: 25})
	moved := func(d SchedulerDecision) bool { return d.Target != nil && poolsOn(*d.Target, n2) }
	if d := capacityDecideForTest(t, s, snap); !moved(d) {
		t.Fatalf("a repaying prepare at idle was not started: %s %v %+v", d.Status, d.Reasons, d.Target)
	}
	snap.Costs[0].Trials = []OperationTrial{{Duration: time.Second, Joules: 100000}}
	if d := capacityDecideForTest(t, s, snap); moved(d) {
		t.Fatalf("a prepare the saving cannot repay was started: %s %v", d.Status, d.Reasons)
	}
}

// An idle Workload beside a loaded one keeps its instance even where removing
// it would save energy.
func TestIdleWorkloadBesideLoadedKeepsItsInstance(t *testing.T) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads = []string{"w1", "w2"}
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod = time.Minute, 10*time.Second, time.Minute
	snap.ObservedSince = snap.ObservationEnd.Add(-time.Hour)
	w2 := poolsAddWorkload(&snap, "w2")
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	i2 := poolsInstance(snap.Instances[0], "i2", n2, "fixed", "w2")
	profile := *i2.Profile
	profile.Power = schedulerTestPower(20) // removing i2 would save 10 W
	i2.Profile = &profile
	snap.Instances = append(snap.Instances, i2)
	snap.Options = append(snap.Options, poolsOption(i2))
	snap.Routing = map[string]map[string]int32{"w1": {"i1": schedulerRouteUnits}, "w2": {"i2": schedulerRouteUnits}}
	snap.Costs = []OperationCost{poolsCost(schedulerActionRemove, n2, schedulerState(i2), "unloaded", "fixed")}
	capacityServiceRatesForTest(&snap, map[string]float64{snap.Instances[0].Profile.ID: 25, i2.Profile.ID: 25})
	capacityLoad(s, &snap, snap.Workloads[0], 10)
	d := capacityDecideForTest(t, s, snap)
	if d.Target == nil || !poolsOn(*d.Target, n2) || d.Target.Routing[w2.UID]["i2"] != schedulerRouteUnits {
		t.Fatalf("idle workload lost its only instance: %s %v %+v", d.Status, d.Reasons, d.Target)
	}
}

// The window's measurement judges the running set at the load it was offered,
// however low: 20 req/s, below the 40 req/s floor that used to replace it
// with an even replay (control411 PLC).
func TestMeasuredVerdictAtOfferedLoad(t *testing.T) {
	s, snap := measuredFixture()
	measuredReports(&snap, 0)
	d := capacityDecideForTest(t, s, snap)
	keep := measuredScoreOf(t, &d.Keep)
	if keep.Measured == nil || !keep.Measured.Satisfied || keep.Measured.Population != 60 || d.Capacity.Workloads[0].Rate != 20 {
		t.Fatalf("offered load was not judged by its measurement: rate=%v keep=%+v", d.Capacity.Workloads, keep)
	}
}
