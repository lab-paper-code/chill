package serving

import (
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

// A change between control-interval boundaries starts the load at the arrival
// where it happened: evenly spaced arrivals that change rate 5 s before the
// window end give a load at the new rate, within one arrival, that this
// decision marks as changed. An unchanged stream keeps the whole window.
func TestLoadChangeIsLocatedAtAnArrival(t *testing.T) {
	for _, c := range []struct {
		name          string
		before, after float64
	}{{"rise", 64, 96}, {"drop", 96, 16}, {"unchanged", 64, 64}} {
		t.Run(c.name, func(t *testing.T) {
			s, snap := schedulerFixture()
			s.ObservationWindow, s.ControlInterval = time.Minute, 10*time.Second
			end := time.Unix(100000, 0)
			start, step := end.Add(-s.ObservationWindow), end.Add(-5*time.Second)
			times := append(evenTimesForTest(start, step, c.before), evenTimesForTest(step, end, c.after)...)
			load := loadsOverDecisionsForTest(s, snap, times, []time.Time{end})[0]
			if c.before == c.after {
				if !load.WindowStart.Equal(start) || load.Changed {
					t.Fatalf("an unchanged stream: load from %v changed=%v, want the window start %v", load.WindowStart, load.Changed, start)
				}
				return
			}
			gap := time.Duration(float64(time.Second) / min(c.before, c.after))
			if d := load.WindowStart.Sub(step); d < -gap || d > gap || !load.Changed {
				t.Fatalf("load from %v (%v from the change) changed=%v, want within one arrival of the change", load.WindowStart, d, load.Changed)
			}
			if n := float64(load.ComputeInputs); n < c.after*5-1 || n > c.after*5+1 {
				t.Fatalf("%d arrivals in the load, want the %v after the change within one", load.ComputeInputs, c.after*5)
			}
			if got := load.InputsPerSecond; got < c.after-1.0/5 || got > c.after+1.0/5 {
				t.Fatalf("%v req/s, want %v within one arrival over the 5 s", got, c.after)
			}
		})
	}
}

// While keep misses an SLO a feasible candidate that routes within the first
// control interval ranks before a cheaper one that routes in the third, in the
// exact order and the search order alike. While keep meets them, energy ranks
// first whatever the route.
func TestRouteTimeRanksBeforeEnergyOnlyWhileKeepMissesAnSLO(t *testing.T) {
	for _, keepFeasible := range []bool{false, true} {
		p := &capacityPlanner{}
		if !keepFeasible {
			p.routeStep = 10
		}
		fast := capacityResult{feasible: true, route: 6.1, unmet: p.unmet(6.1), joules: 118093, key: "fast"}
		cheap := capacityResult{feasible: true, route: 27.1, unmet: p.unmet(27.1), joules: 98496, key: "cheap"}
		table := &protocol.Table{KeyParts: []string{"fast", "cheap"}}
		fastRank := protocol.Rank{Feasible: true, Route: fast.route, Unmet: p.unmet(fast.route), Joules: fast.joules, Choice: []int{0}, Table: table}
		cheapRank := protocol.Rank{Feasible: true, Route: cheap.route, Unmet: p.unmet(cheap.route), Joules: cheap.joules, Choice: []int{1}, Table: table}
		fastFirst := fast.better(&cheap) && !cheap.better(&fast)
		fastRankFirst := fastRank.Better(&cheapRank) && !cheapRank.Better(&fastRank)
		if fastFirst != !keepFeasible || fastRankFirst != !keepFeasible {
			t.Fatalf("keep feasible %v: fast first in better %v, in Rank.Better %v", keepFeasible, fastFirst, fastRankFirst)
		}
	}
}

// loadDecisionFixture is one Workload served by an instance of 100 req/s at
// 5 req/s, or at the segments' rates when given, over a minute observed whole.
func loadDecisionFixture(rates ...float64) (SchedulerSettings, SchedulerSnapshot) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod = time.Minute, 10*time.Second, time.Minute
	snap.ObservedSince = snap.ObservationEnd.Add(-time.Hour)
	snap.Routing = map[string]map[string]int32{"w1": {"i1": schedulerRouteUnits}}
	capacityServiceRatesForTest(&snap, map[string]float64{snap.Instances[0].Profile.ID: 100})
	if len(rates) == 0 {
		rates = []float64{5, 5, 5, 5, 5, 5}
	}
	capacitySegmentsForTest(&snap, s.ObservationWindow, rates...)
	return s, snap
}

// A configuration that meets every SLO stands without a search while it was
// decided for the current definitions and loads. A load change opens one
// search, made once no action is in flight and no reservation holds Nodes,
// every load is observed for one control interval and the window is whole;
// that decision records what it decided for. A configuration that misses an
// SLO is searched for whatever was decided.
func TestFeasibleKeepSearchesOnceAfterALoadChange(t *testing.T) {
	searched := func(d SchedulerDecision) bool {
		return d.Capacity != nil && d.Capacity.Search != nil && (d.Capacity.Search.Strategy != "" || d.Capacity.Search.ExactEvaluated != 0)
	}
	signature := func(snap SchedulerSnapshot) string { return snap.Workloads[0].Identity() }
	waits := func(t *testing.T, d SchedulerDecision) {
		t.Helper()
		if d.Status != SchedulerStatusKeep || !slices.Contains(d.Reasons, schedulerEnergyDecisionWaits) || slices.Contains(d.Reasons, schedulerLoadUnchanged) || searched(d) || d.DecidedFor != "" {
			t.Fatalf("%s %v searched=%v decidedFor=%q", d.Status, d.Reasons, searched(d), d.DecidedFor)
		}
	}
	t.Run("decided and unchanged", func(t *testing.T) {
		s, snap := loadDecisionFixture()
		snap.DecidedFor = signature(snap)
		d := capacityDecideForTest(t, s, snap)
		if d.Status != SchedulerStatusKeep || !slices.Contains(d.Reasons, schedulerLoadUnchanged) || searched(d) || d.DecidedFor != "" {
			t.Fatalf("%s %v searched=%v decidedFor=%q", d.Status, d.Reasons, searched(d), d.DecidedFor)
		}
	})
	t.Run("changed and settled", func(t *testing.T) {
		s, snap := loadDecisionFixture(5, 5, 5, 10, 10, 10)
		snap.DecidedFor = signature(snap)
		d := capacityDecideForTest(t, s, snap)
		if d.Keep.Status != schedulerStatusFeasible || !d.Search.Loads[0].Changed || !searched(d) || d.DecidedFor != signature(snap) {
			t.Fatalf("keep %s changed=%v searched=%v decidedFor=%q %v", d.Keep.Status, d.Search.Loads[0].Changed, searched(d), d.DecidedFor, d.Reasons)
		}
	})
	t.Run("changed with an action in flight", func(t *testing.T) {
		s, snap := loadDecisionFixture(5, 5, 5, 10, 10, 10)
		snap.DecidedFor = signature(snap)
		snap.Actions = []Action{{ID: "route", Kind: schedulerActionRoute, Status: phaseRunning}}
		waits(t, capacityDecideForTest(t, s, snap))
	})
	t.Run("changed with every action ended and a reservation", func(t *testing.T) {
		s, snap := loadDecisionFixture(5, 5, 5, 10, 10, 10)
		snap.DecidedFor = signature(snap)
		ended := snap.ObservationEnd.Add(-time.Minute)
		snap.Actions = []Action{{ID: "route", Kind: schedulerActionRoute, Status: schedulerActionComplete, EndedAt: &ended}}
		snap.Reservations = []SchedulerReservation{{Owner: "transition", Nodes: []NodeReference{snap.Nodes[0].Node}}}
		waits(t, capacityDecideForTest(t, s, snap))
	})
	t.Run("changed less than one control interval ago", func(t *testing.T) {
		s, snap := loadDecisionFixture()
		snap.DecidedFor = signature(snap)
		d, step := snap.Workloads[0], snap.ObservationEnd.Add(-5*time.Second)
		snap.Arrivals = nil
		for j, at := range append(evenTimesForTest(snap.ObservationEnd.Add(-s.ObservationWindow), step, 5), evenTimesForTest(step, snap.ObservationEnd, 20)...) {
			snap.Arrivals = append(snap.Arrivals, SchedulerArrival{ID: fmt.Sprint(j), At: at, Definition: d, Accepted: true, Compute: true, WorkloadUID: d.UID})
		}
		decision := capacityDecideForTest(t, s, snap)
		if load := decision.Search.Loads[0]; !load.Changed || snap.ObservationEnd.Sub(load.WindowStart) >= s.ControlInterval {
			t.Fatalf("fixture: the change 5 s before the end was not detected: %+v", load)
		}
		waits(t, decision)
	})
	t.Run("undecided in a partial window", func(t *testing.T) {
		s, snap := loadDecisionFixture()
		snap.ObservedSince = snap.ObservationEnd.Add(-30 * time.Second)
		waits(t, capacityDecideForTest(t, s, snap))
	})
	t.Run("keep misses an SLO", func(t *testing.T) {
		s, snap := loadDecisionFixture(150, 150, 150, 150, 150, 150)
		snap.DecidedFor = signature(snap)
		d := capacityDecideForTest(t, s, snap)
		if d.Keep.Status != SchedulerStatusOverload || !searched(d) || d.DecidedFor != "" {
			t.Fatalf("keep %s searched=%v decidedFor=%q %v", d.Keep.Status, searched(d), d.DecidedFor, d.Reasons)
		}
	})
}

// Keep in the committed view is the configuration a reserved transition is
// committing: a preparing instance counted Ready and routed as that target
// routes it. When it meets every SLO the decision shows it as the target and
// creates no action of its own; the transition applies its routes. n3's
// running Fixed lowering moves the target's Node modes off the captured ones,
// which without this would plan a route; the target carries only keep's
// Fixed continuation.
func TestCommittedKeepWithoutASearchCreatesNoAction(t *testing.T) {
	s, snap, n2, n3 := committedFixture()
	inFlight(&snap, n2)
	snap.Actions = []Action{{ID: "lower", Kind: schedulerActionPower, Node: n3, From: "fixed", To: schedulerTestEcoMode, Fixed: true, Status: phaseRunning}}
	d := capacityDecideForTest(t, s, snap)
	if d.Keep.Status != schedulerStatusFeasible || !slices.Contains(d.Reasons, schedulerEnergyDecisionWaits) || d.Target == nil || d.Target.Keep {
		t.Fatalf("fixture: keep %s reasons %v target %+v", d.Keep.Status, d.Reasons, d.Target)
	}
	ids := func(actions []Action) (out []string) {
		for _, a := range actions {
			out = append(out, a.ID)
		}
		return out
	}
	if !slices.Equal(ids(d.Target.Actions), ids(d.Keep.Actions)) || slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return !a.Fixed }) {
		t.Fatalf("the committed configuration became a target with actions %v, keep carries %v", ids(d.Target.Actions), ids(d.Keep.Actions))
	}
}

// A part shorter than the stretch's mean gap between arrivals does not decide
// where the load starts. Sparse arrivals at 0.24 req/s rise to 0.8 req/s 10 s
// before the end; two more arrive 2 ms and 1 ms before it. The split after the
// first of those two scores higher than any other but leaves a second part of
// 2 ms, far shorter than the mean gap (60 s over 22 arrivals). The located
// start leaves both parts at least that long.
func TestLoadStartLeavesBothPartsAtLeastTheMeanGap(t *testing.T) {
	end := time.Unix(100000, 0)
	from := end.Add(-time.Minute)
	at := func(seconds float64) time.Time { return from.Add(time.Duration(seconds * float64(time.Second))) }
	times := append(evenTimesForTest(at(0), at(50), 0.24), evenTimesForTest(at(50), at(60), 0.8)...)
	times = append(times, at(59.998), at(59.999))
	span, total := end.Sub(from).Seconds(), float64(len(times))
	gap := span / total
	likelihood := func(split time.Time) float64 {
		n1 := 0.0
		for _, a := range times {
			if a.Before(split) {
				n1++
			}
		}
		t1 := split.Sub(from).Seconds()
		l := 0.0
		for _, part := range [2][2]float64{{n1, t1}, {total - n1, span - t1}} {
			if part[0] > 0 {
				l += part[0] * math.Log(part[0]/part[1])
			}
		}
		return l
	}
	short := at(59.998).Add(1)
	located := schedulerLoadStart(10*time.Second, &Definition{UID: "w", SLO: SLO{Percentile: 95}}, slices.Clone(times), from, end, time.Time{})
	if end.Sub(short).Seconds() >= gap || likelihood(short) <= likelihood(located) {
		t.Fatalf("fixture: the split after 59.998 s (%v) must score above the located start %v (%v)", likelihood(short), located.Sub(from), likelihood(located))
	}
	if located.Sub(from).Seconds() < gap || end.Sub(located).Seconds() < gap {
		t.Fatalf("located start %v leaves a part shorter than the mean gap %v s", located.Sub(from), gap)
	}
}

// The search ranks by unmet before energy as the exact stage does. With one
// exact finalist the surrogate order alone decides which expansion reaches the
// exact stage. Keep misses its SLO (40 req/s on 34.3); preparing lut takes
// 3.5 s and genesis3 1.5 s, short enough that the surrogate finds both
// feasible, and lut is the cheaper. In whole control intervals (1 s) genesis3
// routes first, so it is the finalist and the target.
func TestSurrogateRanksUnmetBeforeEnergyWhileKeepMisses(t *testing.T) {
	s, snap, genesis3 := capacityE1Fixture()
	snap.Workloads[0].SLO.Percentile = 95
	snap.Costs[0].Trials[0].Duration, snap.Costs[1].Trials[0].Duration = 3500*time.Millisecond, 1500*time.Millisecond
	s.PlanSearchStrategy, s.PlanExactFinalists = protocol.V2, 1
	d := capacityDecideForTest(t, s, snap)
	if d.Keep.Status != SchedulerStatusOverload || d.Target == nil || !poolsOn(*d.Target, genesis3) || poolsOn(*d.Target, NodeReference{Name: "node-lut", UID: "lut"}) {
		t.Fatalf("keep %s: the faster expansion must reach the exact stage: %+v %v", d.Keep.Status, d.Target, d.Reasons)
	}
}
