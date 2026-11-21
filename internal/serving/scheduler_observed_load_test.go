package serving

import (
	"fmt"
	"math"
	"slices"
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
// instance that stayed, as is every repetition of it at the planned rate.
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
	if after.Target == nil || len(after.Target.Feedback) != 1 || after.Target.Feedback[0].ObservedArrivals != 1 || after.Target.Feedback[0].Population == 0 ||
		after.Target.Feedback[0].OnTime != after.Target.Feedback[0].Population || !after.Target.Feedback[0].Satisfied {
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
	if keep.Measured == nil || !keep.Measured.Satisfied || keep.Measured.Population != 60 || d.Search.Loads[0].InputsPerSecond != 20 {
		t.Fatalf("offered load was not judged by its measurement: load=%+v keep=%+v", d.Search.Loads, keep)
	}
}

// sparseFixture serves w1 on i1 (20 W) alone; the same service on n2 (12 W)
// can be prepared and i1 removed for less energy. Both serve service req/s,
// and w1's arrivals are evenly spaced at rate req/s over the minute.
func sparseFixture(rate, service float64) (SchedulerSettings, SchedulerSnapshot, NodeReference) {
	s, snap, n2 := idleFixture()
	snap.Instances, snap.Options = snap.Instances[:1], snap.Options[:1]
	snap.Routing = map[string]map[string]int32{"w1": {"i1": schedulerRouteUnits}}
	i1 := snap.Instances[0]
	profile := *i1.Profile
	profile.Power = schedulerTestPower(20)
	snap.Instances[0].Profile = &profile
	snap.Options[0].Instance.Profile = &profile
	cheap := poolsInstance(i1, "c", n2, "fixed", "w1")
	cheap.Profile.Power = schedulerTestPower(12)
	snap.Options = append(snap.Options, poolsOption(cheap))
	snap.Costs = []OperationCost{poolsCost(schedulerActionPrepare, n2, "unloaded", schedulerState(cheap), "fixed"), poolsCost(schedulerActionRemove, i1.Node, schedulerState(snap.Instances[0]), "unloaded", "fixed")}
	capacityServiceRatesForTest(&snap, map[string]float64{profile.ID: service, cheap.Profile.ID: service})
	capacityLoad(s, &snap, snap.Workloads[0], rate)
	return s, snap, n2
}

// A sparse Workload whose keep replay loses one of its 18 arrivals in every
// repetition (one whose lifetime ends before any service) misses the rank rule
// but is not significantly short over the 18 arrivals the replay rests on:
// keep meets its SLO and, once decided, stands without a search. The cheaper
// serving on n2 loses the same arrival; it changes w1's serving, so the rank
// rule judges it, and the energy decision that adopts it when nothing is lost
// keeps keep.
func TestSparseKeepStandsOnAnInsignificantLoss(t *testing.T) {
	s, snap, n2 := sparseFixture(0.3, 25)
	moved := func(d SchedulerDecision) bool { return d.Target != nil && poolsOn(*d.Target, n2) }
	if d := capacityDecideForTest(t, s, snap); !moved(d) {
		t.Fatalf("fixture: the energy decision must move w1 to n2 when nothing is lost: %s %v %+v", d.Status, d.Reasons, d.Target)
	}
	snap.Arrivals[3].Lifetime = 1
	d := capacityDecideForTest(t, s, snap)
	keep := measuredScoreOf(t, &d.Keep)
	if keep.ObservedArrivals != 18 || keep.Timeout == 0 || keep.OnTime >= empiricalSLORank(keep.Population, 95) {
		t.Fatalf("fixture: keep's replay must lose the arrival and miss the rank rule: %+v", keep)
	}
	if d.Keep.Status != schedulerStatusFeasible || !keep.Satisfied || moved(d) {
		t.Fatalf("an insignificant loss must leave keep standing: keep %s %+v target %+v", d.Keep.Status, keep, d.Target)
	}
	snap.DecidedFor = snap.Workloads[0].Identity()
	d = capacityDecideForTest(t, s, snap)
	if searched := d.Capacity != nil && d.Capacity.Search != nil && (d.Capacity.Search.Strategy != "" || d.Capacity.Search.ExactEvaluated != 0); d.Status != SchedulerStatusKeep || !slices.Contains(d.Reasons, schedulerLoadUnchanged) || searched {
		t.Fatalf("a decided keep with an insignificant loss must stand: %s %v searched=%v", d.Status, d.Reasons, searched)
	}
}

// While a transition is in flight keep is the configuration it commits: w1
// served by i1 and the preparing slot2 alike (the committed view). That is the
// serving the scheduler decided on, so its replay loss of one sparse arrival
// is judged on its evidence too: keep meets its SLO and no rescue prepares on
// n3.
func TestCommittedKeepIsJudgedOnItsEvidence(t *testing.T) {
	s, snap, n2, n3 := committedFixture()
	inFlight(&snap, n2)
	capacityLoad(s, &snap, snap.Workloads[0], 0.3)
	snap.Arrivals[3].Lifetime = 1
	d := capacityDecideForTest(t, s, snap)
	keep := measuredScoreOf(t, &d.Keep)
	if !slices.Equal(d.Committed, []string{"pod2"}) || keep.ObservedArrivals != 18 || keep.Timeout == 0 || keep.OnTime >= empiricalSLORank(keep.Population, 95) {
		t.Fatalf("fixture: the committed keep's replay must lose the arrival and miss the rank rule: committed %v %+v", d.Committed, keep)
	}
	if d.Keep.Status != schedulerStatusFeasible || !keep.Satisfied || preparesOn(d, n3) {
		t.Fatalf("an insignificant loss of the committed keep opened a rescue: keep %s %+v %v", d.Keep.Status, keep, d.Reasons)
	}
}

// A running configuration whose keep replay is significantly short at the
// planned rate is rescued although it serves the observed rate. 28 arrivals
// in the minute come 2.14 s apart, longer than i1's 1.8 s service, and are all
// on time against the 2 s target; at the planned 0.64 req/s they come 1.56 s
// apart and queue. Keep misses its SLO although it was decided for, and the
// rescue prepares on n2. The supply correction reads the shortfall at the rate
// the replay offered: the completed share times the planned rate.
func TestKeepShortAtThePlannedRateIsRescued(t *testing.T) {
	s, snap, n2 := sparseFixture(28.0/60, 1/1.8)
	snap.Workloads[0].SLO.TargetLatency = 2 * time.Second
	snap.DecidedFor = snap.Workloads[0].Identity()
	p := capacityPlannerForTest(t, s, snap)
	p.loads = schedulerObservedLoadsForTest(s, snap)
	if observed := p.evaluateCurrentKeep(p.periodSeconds); !observed.feasible {
		t.Fatalf("fixture: keep must meet the SLO at the observed rate: %+v", observed.feedback)
	}
	d := capacityDecideForTest(t, s, snap)
	if d.Keep.Status != SchedulerStatusOverload || !preparesOn(d, n2) {
		t.Fatalf("keep short at the planned rate was not rescued: keep %s %+v, %s %v", d.Keep.Status, d.Keep.Feedback, d.Status, d.Reasons)
	}
	p = capacityPlannerForTest(t, s, snap)
	keep := p.evaluateCurrentKeep(p.periodSeconds)
	score := keep.feedback[0]
	want := float64(score.OnTime+score.Late) / float64(score.Population) * p.ws[0].rate
	if got := p.shortfalls(keep); p.ws[0].rate <= p.ws[0].observed || len(got) != 1 || math.Abs(got[0].completed-want) > 1e-12 {
		t.Fatalf("shortfall %+v, want the completed share of the planned %v req/s, %v (observed %v)", got, p.ws[0].rate, want, p.ws[0].observed)
	}
}

// The replay verdict of keep rests on the 18 arrivals of the window, not on
// their repetitions over a 10 min comparison period (about 260 requests at the
// planned rate). Losing 3 of 18 in every repetition misses the rank rule but
// is not significantly short over 18 (P(X <= 15) = 0.058, level 0.05/6),
// while the same share over the repetitions would be.
func TestKeepVerdictRestsOnTheWindowNotItsRepetitions(t *testing.T) {
	s, snap, _ := sparseFixture(0.3, 25)
	s.ComparisonPeriod = 10 * time.Minute
	for _, j := range []int{3, 9, 15} {
		snap.Arrivals[j].Lifetime = 1
	}
	p := capacityPlannerForTest(t, s, snap)
	keep := p.evaluateCurrentKeep(p.periodSeconds)
	score := keep.feedback[0]
	if score.ObservedArrivals != 18 || score.Population < 200 || score.OnTime >= empiricalSLORank(score.Population, 95) {
		t.Fatalf("fixture: keep's replay must lose 3 of 18 in every repetition: %+v", score)
	}
	if !score.Satisfied || !keep.feasible {
		t.Fatalf("3 of 18 lost is no significant shortfall: %+v", score)
	}
}

// A window without arrivals rests the keep verdict on the requests the replay
// still judged: five requests waiting from before it whose lifetimes end
// before any service all fail, which is significantly short (0.05^5), so keep
// misses its SLO.
func TestKeepWithOnlyStaleOutstandingIsJudgedOnThem(t *testing.T) {
	s, snap, _ := sparseFixture(0, 25)
	lifetime := time.Duration(1)
	for j := range 5 {
		snap.Outstanding = append(snap.Outstanding, SchedulerRequest{ID: fmt.Sprint("stale", j), Definition: snap.Workloads[0],
			ReceivedAt: snap.ObservationEnd.Add(-2 * s.ObservationWindow), Phase: phasePending, RemainingLifetime: &lifetime})
	}
	p := capacityPlannerForTest(t, s, snap)
	keep := p.evaluateCurrentKeep(p.periodSeconds)
	score := keep.feedback[0]
	if score.ObservedArrivals != 0 || score.Population != 5 || score.ReplayedArrivals != 0 || score.OnTime != 0 {
		t.Fatalf("fixture: keep's replay must judge only the five stale requests, all failed: %+v", score)
	}
	if score.Satisfied || keep.feasible {
		t.Fatalf("five failed stale requests must miss: %+v", score)
	}
}

// A transition in flight whose committed keep is significantly short (60
// req/s on i1 and the preparing slot2, 25 req/s each) is no decided serving to
// keep: the rescue prepares on n3.
func TestCommittedKeepSignificantlyShortIsRescued(t *testing.T) {
	s, snap, n2, n3 := committedFixture()
	capacityLoad(s, &snap, snap.Workloads[0], 60)
	inFlight(&snap, n2)
	d := capacityDecideForTest(t, s, snap)
	if keep := measuredScoreOf(t, &d.Keep); !slices.Equal(d.Committed, []string{"pod2"}) || keep.Measured != nil || keep.Satisfied {
		t.Fatalf("fixture: the committed keep must be replay-judged short: committed %v %+v", d.Committed, keep)
	}
	if d.Keep.Status != SchedulerStatusOverload || !preparesOn(d, n3) {
		t.Fatalf("a significantly short committed keep did not open a rescue: keep %s, %s %v", d.Keep.Status, d.Status, d.Reasons)
	}
}

// The energy decision chooses at the planned rate. A cheaper serving of a
// sparse Workload on n2 serves in 1.8 s against a 2 s target: at the observed
// 28 arrivals a minute it is on time, at the planned 0.64 req/s it queues and
// misses the rank rule. It changes w1's serving, so the rank rule judges it,
// and the decision keeps i1.
func TestEnergyDecisionChoosesAtThePlannedRate(t *testing.T) {
	s, snap, n2 := sparseFixture(28.0/60, 25)
	snap.Workloads[0].SLO.TargetLatency = 2 * time.Second
	capacityServiceRatesForTest(&snap, map[string]float64{snap.Options[1].Instance.Profile.ID: 1 / 1.8})
	if d := capacityDecideForTest(t, s, snap); d.Target == nil || poolsOn(*d.Target, n2) {
		t.Fatalf("the energy decision adopted a serving that misses at the planned rate: %s %v %+v", d.Status, d.Reasons, d.Target)
	}
	p := capacityPlannerForTest(t, s, snap)
	cheap := capacityChoiceForTest(t, p, map[string]string{n2.UID: planKey([]SchedulerInstance{snap.Options[1].Instance}), snap.Instances[0].Node.UID: ""})
	if planned := p.evaluate(cheap, p.routeOf(cheap)); planned.feasible {
		t.Fatalf("fixture: the cheap serving must miss at the planned rate: %+v", planned.feedback)
	}
	p = capacityPlannerForTest(t, s, snap)
	p.loads = schedulerObservedLoadsForTest(s, snap)
	if observed := p.evaluate(cheap, p.routeOf(cheap)); !observed.feasible {
		t.Fatalf("fixture: the cheap serving must pass at the observed rate: %+v", observed.feedback)
	}
}
