package serving

import (
	"context"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// capacityRowsForTest returns a built row with κ = kappa(variant) (100 when
// kappa is nil) for every option variant and autonomous Workload, as
// CapacityTable.Rows returns them.
func capacityRowsForTest(s SchedulerSettings, snap SchedulerSnapshot, kappa func(SchedulerOption) float64) []CapacityRow {
	if kappa == nil {
		kappa = func(SchedulerOption) float64 { return 100 }
	}
	rows := []CapacityRow{}
	for _, w := range snap.Workloads {
		if !slices.Contains(s.AutonomousWorkloads, w.UID) {
			continue
		}
		for _, option := range snap.Options {
			if !slices.Contains(option.CompatibleUIDs, w.UID) {
				continue
			}
			for _, v := range CapacityVariants(option) {
				in := NewCapacityInput(s, snap.DispatchOccupancy, v, w)
				k := kappa(v)
				rows = append(rows, CapacityRow{Key: in.Key(), Input: in, Status: CapacityBuilt, Kappa: &k})
			}
		}
	}
	return rows
}

// capacityKappaByProfile gives each profile ID its κ, 100 otherwise.
func capacityKappaByProfile(kappa map[string]float64) func(SchedulerOption) float64 {
	return func(o SchedulerOption) float64 {
		if k, ok := kappa[o.Instance.Profile.ID]; ok {
			return k
		}
		return 100
	}
}

func capacityDecideForTest(t testing.TB, s SchedulerSettings, snap SchedulerSnapshot, rows []CapacityRow) SchedulerDecision {
	t.Helper()
	d, err := EvaluateScheduler(context.Background(), s, snap, rows)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// capacityLoad replaces the arrivals with rate req/s of d spread evenly over
// the observation window.
func capacityLoad(s SchedulerSettings, snap *SchedulerSnapshot, d *Definition, rate float64) {
	n := int(math.Round(rate * s.ObservationWindow.Seconds()))
	start := snap.ObservationEnd.Add(-s.ObservationWindow)
	snap.Arrivals = slices.DeleteFunc(snap.Arrivals, func(a SchedulerArrival) bool { return a.WorkloadUID == d.UID })
	for j := range n {
		snap.Arrivals = append(snap.Arrivals, SchedulerArrival{ID: fmt.Sprintf("%s-%05d", d.UID, j), At: start.Add(time.Duration(j) * s.ObservationWindow / time.Duration(n)), Definition: d, Accepted: true, Compute: true, WorkloadUID: d.UID})
	}
}

// capacityPlannerForTest builds the planner EvaluateScheduler builds, so a
// test can evaluate any plan set.
func capacityPlannerForTest(t testing.TB, s SchedulerSettings, snap SchedulerSnapshot, rows []CapacityRow) *capacityPlanner {
	t.Helper()
	ctx := context.Background()
	snap = schedulerCanonical(snap)
	keep := SchedulerCandidate{ID: "keep", Keep: true, Nodes: schedulerCommonNodes(s, snap), Instances: snap.Instances, Routing: schedulerInstanceRouting(ctx, snap.Workloads, snap.Instances)}
	for uid, routing := range snap.Routing {
		keep.Routing[uid] = routing
	}
	decision := SchedulerDecision{Search: schedulerSearchPopulation(ctx, s, snap), Workloads: schedulerWorkloadStates(s, snap)}
	p := newCapacityPlanner(ctx, s, snap, keep, rows, decision)
	if err := p.build(); err != nil {
		t.Fatal(err)
	}
	return p
}

// capacityChoiceForTest picks, per Node UID, the plan with that key (keep elsewhere).
func capacityChoiceForTest(t testing.TB, p *capacityPlanner, keys map[string]string) []int {
	t.Helper()
	choice := make([]int, len(p.plans))
	for n, plans := range p.plans {
		key, ok := keys[p.snapshot.Nodes[n].Node.UID]
		if !ok {
			continue
		}
		choice[n] = slices.IndexFunc(plans, func(plan *capacityPlan) bool { return plan.record.Key == key && plan.record.Eligible })
		if choice[n] < 0 {
			t.Fatalf("no eligible plan %s on %s", key, p.snapshot.Nodes[n].Node.UID)
		}
	}
	return choice
}

// capacityE1Fixture is the §3 example: rasp5 serves 40 req/s with q1 (κ 34.3);
// q1-lut on rasp5 (κ 34.9) prepares in 20.2 s, q2 on genesis3 (κ 109.7) in
// 10.7 s. L 1 s, m 177 ms.
func capacityE1Fixture() (SchedulerSettings, SchedulerSnapshot, []CapacityRow, NodeReference) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ComparisonPeriod, s.MaxObservationAge, s.CompletionMarginMS = time.Second, time.Minute, time.Hour, 177
	d := snap.Workloads[0]
	d.SLO = SLO{Percentile: 95, TargetLatency: time.Second}
	// E1 placed q1-lut beside q1 on rasp5; with one instance per Workload per
	// Node (D-R8-1) it stands on a Node of its own with the same κ and chain.
	lutNode := poolsAddNode(&snap, "lut", map[string]float64{"fixed": 1})
	lut := poolsInstance(snap.Instances[0], "q1-lut", lutNode, "fixed", "w1")
	genesis3 := poolsAddNode(&snap, "genesis3", map[string]float64{"fixed": 30})
	q2 := poolsInstance(snap.Instances[0], "q2", genesis3, "fixed", "w1")
	profile := *q2.Profile
	profile.IdleBaselineWatts = 60
	q2.Profile = &profile
	snap.Options = append(snap.Options, poolsOption(lut), poolsOption(q2))
	prepareLUT := poolsCost(schedulerActionPrepare, lutNode, "unloaded", schedulerState(lut), "fixed")
	prepareLUT.Trials = []OperationTrial{{Duration: 20200 * time.Millisecond, Joules: 300}}
	prepareQ2 := poolsCost(schedulerActionPrepare, genesis3, "unloaded", schedulerState(q2), "fixed")
	prepareQ2.Trials = []OperationTrial{{Duration: 10700 * time.Millisecond, Joules: 300}}
	snap.Costs = []OperationCost{prepareLUT, prepareQ2}
	capacityLoad(s, &snap, d, 40)
	rows := capacityRowsForTest(s, snap, capacityKappaByProfile(map[string]float64{"profile": 34.3, lut.Profile.ID: 34.9, q2.Profile.ID: 109.7}))
	return s, snap, rows, genesis3
}

// §9 6 판정: the two 40 req/s examples of §3. Energy alone picks q1 + q1-lut;
// its backlog wait 1.67 s exceeds L − m, and genesis3 q2 (wait 0.42 s) wins.
func TestCapacityTransientRuleRejectsCheaperSlowRoute(t *testing.T) {
	s, snap, rows, genesis3 := capacityE1Fixture()
	d := capacityDecideForTest(t, s, snap, rows)
	if d.Status != schedulerStatusFeasible || d.Target == nil || !poolsOn(*d.Target, genesis3) || poolsOn(*d.Target, NodeReference{Name: "node-lut", UID: "lut"}) {
		t.Fatalf("genesis3 q2 must be the target: %+v", d)
	}
	w := d.Capacity.Workloads[0]
	if math.Abs(w.Rate-40) > 1e-9 || math.Abs(w.KappaNew-144) > 1e-9 || math.Abs(w.KappaRetained-34.3) > 1e-9 || math.Abs(w.Backlog-5.7*10.7) > 1e-6 ||
		w.WaitSeconds == nil || math.Abs(*w.WaitSeconds-5.7*10.7/144) > 1e-9 || d.Capacity.Route != 10700*time.Millisecond {
		t.Fatalf("genesis3 judgment: %+v route %v", w, d.Capacity.Route)
	}
	p := capacityPlannerForTest(t, s, snap, rows)
	slow := p.evaluate(capacityChoiceForTest(t, p, map[string]string{"lut": "profile@lut-fixed/b1/w1"}), 20.2)
	fast := p.evaluate(capacityChoiceForTest(t, p, map[string]string{"genesis3": "profile@genesis3-fixed/b1/w1"}), 10.7)
	j := slow.judgments[0]
	if slow.feasible || math.Abs(j.backlog-5.7*20.2) > 1e-6 || math.Abs(*j.wait-5.7*20.2/69.2) > 1e-9 || *j.wait <= 1-0.177 {
		t.Fatalf("q1-lut judgment B=%g W=%v", j.backlog, *j.wait)
	}
	if slow.joules >= fast.joules || !fast.feasible {
		t.Fatalf("fixture must make the slow route cheaper: %g vs %g", slow.joules, fast.joules)
	}
}

// m4 r6: 20 s after a ChillServer restart under 40 req/s the 60 s window held
// only 20 s of arrivals, λ read 13.3 and the planner removed instances under
// load. λ is measured over the part of the window observed since the process
// started; under one control interval of it the decision waits.
func TestCapacityRateCoversOnlyTheObservedWindow(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.MaxObservationAge = time.Minute, 10*time.Second, time.Hour
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	i2 := poolsInstance(snap.Instances[0], "i2", n2, "fixed", "w1")
	profile := *i2.Profile
	profile.IdleBaselineWatts = 20 // removing i2 saves 10 W
	i2.Profile = &profile
	snap.Instances = append(snap.Instances, i2)
	snap.Options = append(snap.Options, poolsOption(i2))
	snap.Routing = map[string]map[string]int32{"w1": {"i1": 500, "i2": 500}}
	snap.Costs = []OperationCost{poolsCost(schedulerActionRemove, n2, schedulerState(i2), "unloaded", "fixed")}
	capacityLoad(s, &snap, snap.Workloads[0], 40)
	snap.ObservedSince = snap.ObservationEnd.Add(-20 * time.Second)
	snap.Arrivals = slices.DeleteFunc(snap.Arrivals, func(a SchedulerArrival) bool { return a.At.Before(snap.ObservedSince) })
	rows := capacityRowsForTest(s, snap, func(SchedulerOption) float64 { return 25 })
	d := capacityDecideForTest(t, s, snap, rows)
	if d.Target == nil || !poolsOn(*d.Target, n2) || !d.Search.WindowStart.Equal(snap.ObservedSince) ||
		d.Capacity == nil || math.Abs(d.Capacity.Workloads[0].Rate-40) > 1e-9 {
		t.Fatalf("partial window read as the whole window: %s %+v %+v", d.Status, d.Search, d.Capacity)
	}
	snap.ObservedSince = snap.ObservationEnd.Add(-5 * time.Second)
	if d = capacityDecideForTest(t, s, snap, rows); d.Status != schedulerStatusPending || !slices.Contains(d.Reasons, schedulerPartialObservation) {
		t.Fatalf("under one control interval observed: %s %v", d.Status, d.Reasons)
	}
}

// m4 r7 07:00:38 (plan-dc6ebf670833): 19 s after the restart the observed
// window held λ 20.6 of an offered 40 req/s, because clients were still
// backing off the outage, and the plan removed a routed rasp5 q1 that the
// load needed. Until one observation window is observed since the process
// started, plans only expand: no removal, replacement, unrouting or idle
// removal. Expansion still runs, and a full window allows the removal.
func TestCapacityPartialWindowOnlyExpands(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.MaxObservationAge, s.MaxReplicas = time.Minute, 10*time.Second, time.Hour, 3
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	i2 := poolsInstance(snap.Instances[0], "i2", n2, "fixed", "w1")
	profile := *i2.Profile
	profile.IdleBaselineWatts = 20 // removing i2 saves 10 W
	i2.Profile = &profile
	n3 := poolsAddNode(&snap, "n3", map[string]float64{"fixed": 10})
	i3 := poolsInstance(snap.Instances[0], "i3", n3, "fixed", "w1")
	snap.Instances = append(snap.Instances, i2)
	snap.Options = append(snap.Options, poolsOption(i2), poolsOption(i3))
	snap.Routing = map[string]map[string]int32{"w1": {"i1": 500, "i2": 500}}
	snap.Costs = []OperationCost{
		poolsCost(schedulerActionRemove, n2, schedulerState(i2), "unloaded", "fixed"),
		poolsCost(schedulerActionPrepare, n3, "unloaded", schedulerState(i3), "fixed"),
	}
	rows := capacityRowsForTest(s, snap, capacityKappaByProfile(map[string]float64{"profile": 25.69, i2.Profile.ID: 29.30, i3.Profile.ID: 70.94}))
	observe := func(rate float64, since time.Duration) SchedulerDecision {
		capacityLoad(s, &snap, snap.Workloads[0], rate)
		snap.ObservedSince = snap.ObservationEnd.Add(-since)
		snap.Arrivals = slices.DeleteFunc(snap.Arrivals, func(a SchedulerArrival) bool { return a.At.Before(snap.ObservedSince) })
		return capacityDecideForTest(t, s, snap, rows)
	}
	if d := observe(20.6, 19*time.Second); d.Target == nil || !poolsOn(*d.Target, n2) || len(d.Target.Routing["w1"]) != 2 {
		t.Fatalf("19 s observed removed or unrouted i2: %s %v %+v", d.Status, d.Reasons, d.Target)
	}
	if d := observe(60, 19*time.Second); d.Target == nil || !poolsOn(*d.Target, n3) || !poolsOn(*d.Target, n2) {
		t.Fatalf("19 s observed did not expand at λ 60: %s %v", d.Status, d.Reasons)
	}
	if d := observe(20.6, time.Minute); d.Target == nil || poolsOn(*d.Target, n2) {
		t.Fatalf("a full window did not remove i2: %s %v", d.Status, d.Reasons)
	}
}

// m4 r7 06:53:58 (plan-f95bc856f060): rasp5 q1-lut and q2 (κ 25.69 + 29.30)
// carried λ 40.35 at weights 467/533. Loaded genesis3 q2 (κ 70.94) draws less
// than its empty Node (49.80 W against 49.97 W), so preparing it was 1.5 J
// cheaper over T while the carried weights left it at weight 0; one window
// later idle removal took it out, and the next decision prepared it again.
// A plan never prepares an instance its routing leaves at weight 0.
func TestCapacityNeverPreparesAnUnroutedInstance(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod, s.MaxObservationAge, s.CompletionMarginMS = time.Minute, 10*time.Second, 10*time.Minute, time.Hour, 29
	s.MaxReplicas = 3
	d := snap.Workloads[0]
	d.SLO = SLO{Percentile: 95, TargetLatency: time.Second}
	lut := snap.Instances[0]
	q2 := schedulerTestCPU(lut)
	q2.ID = "q2"
	genesis3 := poolsAddNode(&snap, "genesis3", map[string]float64{"fixed": 49.97})
	g3 := poolsInstance(lut, "g3", genesis3, "fixed", "w1")
	profile := *g3.Profile
	profile.IdleBaselineWatts = 49.80
	g3.Profile = &profile
	snap.Instances = append(snap.Instances, q2)
	snap.Options = append(snap.Options, poolsOption(q2), poolsOption(g3))
	snap.Routing = map[string]map[string]int32{"w1": {lut.ID: 467, q2.ID: 533}}
	snap.Costs = []OperationCost{poolsCost(schedulerActionPrepare, genesis3, "unloaded", schedulerState(g3), "fixed")}
	capacityLoad(s, &snap, d, 40.35)
	rows := capacityRowsForTest(s, snap, capacityKappaByProfile(map[string]float64{lut.Profile.ID: 25.69, q2.Profile.ID: 29.30, g3.Profile.ID: 70.94}))
	got := capacityDecideForTest(t, s, snap, rows)
	if got.Target == nil {
		t.Fatalf("no target: %s %v", got.Status, got.Reasons)
	}
	for _, a := range got.Target.Actions {
		if a.Kind == schedulerActionPrepare && !schedulerInstanceRouted(got.Target.Routing, a.Instance) {
			t.Fatalf("prepared %s at weight 0: routing %v", a.Instance, got.Target.Routing)
		}
	}
	if got.Status != schedulerStatusKeep {
		t.Fatalf("keep carries the load: %s %v", got.Status, got.Reasons)
	}
}

// m4 r7 07:08:31 (plan-618aad6eb054): genesis3 q2 (κ 70.94) carried 532 of
// the weights and rasp5 q1 (κ 25.69) 468 at λ 38.6. The plan replaced q2 with
// q1 on the same CPU, so its first action stopped q2; the transition judged
// κ_ret 25.69 against the measured chain, the prepare ran 68 s and SLO fell to
// 0. A routed instance is stopped only after the route, so the replacement
// plan is no candidate and keep carries the load.
func TestCapacityStopsRoutedInstancesOnlyAfterTheRoute(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod, s.MaxObservationAge, s.CompletionMarginMS = time.Minute, 10*time.Second, 10*time.Minute, time.Hour, 29
	s.MaxReplicas = 3
	d := snap.Workloads[0]
	d.SLO = SLO{Percentile: 95, TargetLatency: time.Second}
	rasp5 := snap.Instances[0]
	genesis3 := poolsAddNode(&snap, "genesis3", map[string]float64{"fixed": 30})
	q2 := poolsInstance(rasp5, "g3-q2", genesis3, "fixed", "w1")
	profile := *q2.Profile
	profile.IdleBaselineWatts = 60
	q2.Profile = &profile
	q1 := poolsInstance(rasp5, "g3-q1", genesis3, "fixed", "w1")
	cheaper := *q1.Profile
	cheaper.ID, cheaper.IdleBaselineWatts = "profile-q1@genesis3", 50
	q1.Profile = &cheaper
	snap.Instances = append(snap.Instances, q2)
	snap.Options = append(snap.Options, poolsOption(q1))
	snap.Routing = map[string]map[string]int32{"w1": {rasp5.ID: 468, q2.ID: 532}}
	snap.Costs = []OperationCost{
		poolsCost(schedulerActionRemove, genesis3, schedulerState(q2), "unloaded", "fixed"),
		poolsCost(schedulerActionPrepare, genesis3, "unloaded", schedulerState(q1), "fixed"),
	}
	capacityLoad(s, &snap, d, 38.6)
	rows := capacityRowsForTest(s, snap, capacityKappaByProfile(map[string]float64{rasp5.Profile.ID: 25.69, q2.Profile.ID: 70.94, q1.Profile.ID: 65.1}))
	got := capacityDecideForTest(t, s, snap, rows)
	if got.Target == nil {
		t.Fatalf("no target: %s %v", got.Status, got.Reasons)
	}
	route := slices.IndexFunc(got.Target.Actions, func(a Action) bool { return a.Kind == schedulerActionRoute })
	for _, a := range got.Target.Actions {
		if a.Kind == schedulerActionStop && schedulerInstanceRouted(snap.Routing, a.Instance) && (route < 0 || !slices.Contains(a.DependsOn, got.Target.Actions[route].ID)) {
			t.Fatalf("stopped routed %s before the route: %+v", a.Instance, got.Target.Actions)
		}
	}
	if !slices.ContainsFunc(poolsPlans(got, genesis3), func(p SchedulerNodePlan) bool {
		return p.Key == q1.Profile.ID+"/b1/w1" && !p.Eligible && p.Reason == schedulerRoutedStopsFirst
	}) {
		t.Fatalf("replacement plan not excluded: %+v", poolsPlans(got, genesis3))
	}
}

// m4 r5 04:28:18: the first decision of the 40 req/s phase saw λ 42.5 with 161
// queued. With the r5 κ (rasp5 q1 25.29, q1-lut 25.69, genesis3 q2 70.94) it
// expands whatever the comparison period: the SLO judgment orders plans before
// energy. The decision records T = max(comparisonPeriod, longest chain).
func TestCapacityComparisonPeriodKeepsExpansionOnLoadRise(t *testing.T) {
	s, snap, _, genesis3 := capacityE1Fixture()
	s.ObservationWindow = time.Minute
	d := snap.Workloads[0]
	capacityLoad(s, &snap, d, 40)
	for k := range 161 {
		snap.Outstanding = append(snap.Outstanding, SchedulerRequest{ID: fmt.Sprint("queued-", k), Definition: d, ReceivedAt: snap.At, Phase: phasePending})
	}
	rows := capacityRowsForTest(s, snap, capacityKappaByProfile(map[string]float64{
		"profile": 25.288198615188904, snap.Options[1].Instance.Profile.ID: 25.68831509322024, snap.Options[2].Instance.Profile.ID: 70.93509923703007,
	}))
	var chosen string
	for _, period := range []time.Duration{time.Minute, 10 * time.Minute} {
		s.ComparisonPeriod = period
		got := capacityDecideForTest(t, s, snap, rows)
		if got.Target == nil || got.Target.Keep || !poolsOn(*got.Target, genesis3) || got.Capacity == nil || got.Capacity.Period != period {
			t.Fatalf("T %v: no expansion on the first decision: %s %+v", period, got.Status, got.Capacity)
		}
		if chosen != "" && got.Target.ID != chosen {
			t.Fatalf("T %v changed the expansion: %s vs %s", period, got.Target.ID, chosen)
		}
		chosen = got.Target.ID
	}
}

// SLO first: at 40 req/s with an empty queue and the r5 κ, keep ends
// infeasible forever (final 40/25.3 − 1 = 0.58) while adding genesis3 ends
// feasible with only a transient wait over L − m (0.99). A plan that ends
// feasible ranks before any plan that does not, so the expansion wins.
func TestCapacityPlanEndingFeasibleRanksBeforeTransientExceedance(t *testing.T) {
	s, snap, _, genesis3 := capacityE1Fixture()
	rows := capacityRowsForTest(s, snap, capacityKappaByProfile(map[string]float64{
		"profile": 25.288198615188904, snap.Options[1].Instance.Profile.ID: 25.68831509322024, snap.Options[2].Instance.Profile.ID: 70.93509923703007,
	}))
	d := capacityDecideForTest(t, s, snap, rows)
	if d.Status != SchedulerStatusOverload || d.Target == nil || d.Target.Keep || !poolsOn(*d.Target, genesis3) {
		t.Fatalf("keep outranked an expansion that ends feasible: %s %+v", d.Status, d.Target)
	}
	if w := d.Capacity.Workloads[0]; w.FinalExceedance == nil || *w.FinalExceedance > 0 || w.TransitionExceedance == nil || *w.TransitionExceedance <= 0 {
		t.Fatalf("fixture must end feasible with a transient exceedance: %+v", w)
	}
}

// §9 6 판정: a mode change stops the routed instance before the power action
// (κ_ret 0); a replacement on free CPUs removes after the route and keeps the
// current κ until then.
func TestCapacityRetainedKappaFollowsTheChain(t *testing.T) {
	s, snap := schedulerFixture()
	current := snap.Instances[0]
	n1 := current.Node
	eco := poolsInstance(current, "eco", n1, schedulerTestEcoMode, "w1")
	other := schedulerTestCPU(current)
	snap.Options = append(snap.Options, poolsOption(eco), poolsOption(other))
	unloaded := snap.Baselines[0]
	unloaded.ID, unloaded.PowerMode = "unloaded-n1-eco", schedulerTestEcoMode
	snap.Baselines = append(snap.Baselines, unloaded)
	snap.Costs = []OperationCost{
		poolsCost(schedulerActionPower, n1, "fixed", schedulerTestEcoMode, "", schedulerState(current)),
		poolsCost(schedulerActionRemove, n1, schedulerState(current), "unloaded", schedulerTestEcoMode),
		poolsCost(schedulerActionRemove, n1, schedulerState(current), "unloaded", "fixed"),
		poolsCost(schedulerActionPrepare, n1, "unloaded", schedulerState(eco), schedulerTestEcoMode),
		poolsCost(schedulerActionPrepare, n1, "unloaded", schedulerState(other), "fixed"),
	}
	snap.Costs[2].ID += "-fixed"
	d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, func(SchedulerOption) float64 { return 50 }))
	retained := map[string]float64{}
	for _, plan := range poolsPlans(d, n1) {
		if plan.Eligible {
			retained[plan.Key] = plan.KappaRetained["w1"]
		}
	}
	if k, ok := retained[eco.Profile.ID+"/b1/w1"]; !ok || k != 0 {
		t.Fatalf("mode change retained κ: %v", retained)
	}
	if k, ok := retained[other.Profile.ID+"/b1/w1"]; !ok || k != 50 {
		t.Fatalf("replacement removed after the route lost keep κ: %v", retained)
	}
}

// A Workload served by one Node can meet its SLO only by changing that Node's
// mode: the change stops the routed instance before the power action, the
// chain charges that outage to κ_ret and r, and the plan stays a candidate.
func TestCapacitySingleNodeRaisesItsMode(t *testing.T) {
	s, snap := schedulerFixture()
	current := snap.Instances[0]
	n1 := current.Node
	raised := poolsInstance(current, "raised", n1, schedulerTestEcoMode, "w1")
	snap.Options = append(snap.Options, poolsOption(raised))
	unloaded := snap.Baselines[0]
	unloaded.ID, unloaded.PowerMode = "unloaded-n1-eco", schedulerTestEcoMode
	snap.Baselines = append(snap.Baselines, unloaded)
	snap.Costs = []OperationCost{
		poolsCost(schedulerActionPower, n1, "fixed", schedulerTestEcoMode, "", schedulerState(current)),
		poolsCost(schedulerActionRemove, n1, schedulerState(current), "unloaded", schedulerTestEcoMode),
		poolsCost(schedulerActionPrepare, n1, "unloaded", schedulerState(raised), schedulerTestEcoMode),
	}
	capacityLoad(s, &snap, snap.Workloads[0], 30)
	d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, capacityKappaByProfile(map[string]float64{current.Profile.ID: 10, raised.Profile.ID: 50})))
	if d.Target == nil || d.Target.Nodes[0].PowerMode != schedulerTestEcoMode || !slices.ContainsFunc(d.Target.Instances, func(i SchedulerInstance) bool { return i.Profile.ID == raised.Profile.ID }) {
		t.Fatalf("one Node could not change its mode to meet the SLO: %s %v %+v", d.Status, d.Reasons, poolsPlans(d, n1))
	}
}

// §9 6 판정: without a plan set that meets both judgments the smallest worst
// exceedance wins over energy, and the decision is Overload.
func TestCapacityOverloadOrdersByWorstExceedance(t *testing.T) {
	s, snap := schedulerFixture()
	s.MaxReplicas = 3
	small := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 1})
	large := poolsAddNode(&snap, "n3", map[string]float64{"fixed": 50})
	a := poolsInstance(snap.Instances[0], "a", small, "fixed", "w1")
	b := poolsInstance(snap.Instances[0], "b", large, "fixed", "w1")
	snap.Options = append(snap.Options, poolsOption(a), poolsOption(b))
	snap.Costs = []OperationCost{
		poolsCost(schedulerActionPrepare, small, "unloaded", schedulerState(a), "fixed"),
		poolsCost(schedulerActionPrepare, large, "unloaded", schedulerState(b), "fixed"),
	}
	capacityLoad(s, &snap, snap.Workloads[0], 600)
	d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, capacityKappaByProfile(map[string]float64{a.Profile.ID: 150, b.Profile.ID: 250})))
	if d.Status != SchedulerStatusOverload || d.Target == nil || !poolsOn(*d.Target, small) || !poolsOn(*d.Target, large) ||
		!slices.Contains(d.Reasons, schedulerNoPlanMeetsSLO) || math.Abs(d.Target.WorstExceedance-0.2) > 1e-9 {
		t.Fatalf("overload must add every instance (exceedance 600/500 − 1): %+v", d)
	}
}

// §9 6 판정: two Workloads with different SLOs are judged each with its own L − m.
func TestCapacityJudgesEachWorkloadWithItsSLO(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ComparisonPeriod, s.MaxObservationAge, s.CompletionMarginMS = time.Second, time.Minute, time.Hour, 100
	s.AutonomousWorkloads = []string{"w1", "w2"}
	relaxed := snap.Workloads[0]
	relaxed.SLO.TargetLatency = 10 * time.Second
	tight := poolsAddWorkload(&snap, "w2")
	tight.SLO.TargetLatency = time.Second
	tight.Model.UID = "m2"
	n2, n3, n4 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10}), poolsAddNode(&snap, "n3", map[string]float64{"fixed": 10}), poolsAddNode(&snap, "n4", map[string]float64{"fixed": 10})
	i2 := poolsInstance(snap.Instances[0], "i2", n2, "fixed", "w2")
	snap.Instances = append(snap.Instances, i2)
	snap.Routing = map[string]map[string]int32{"w1": {"i1": 1}, "w2": {"i2": 1}}
	add1, add2 := poolsInstance(snap.Instances[0], "add1", n3, "fixed", "w1"), poolsInstance(snap.Instances[0], "add2", n4, "fixed", "w2")
	snap.Options = []SchedulerOption{poolsOption(snap.Instances[0]), poolsOption(i2), poolsOption(add1), poolsOption(add2)}
	snap.Costs = nil
	for _, i := range []SchedulerInstance{add1, add2} {
		cost := poolsCost(schedulerActionPrepare, i.Node, "unloaded", schedulerState(i), "fixed")
		cost.Trials = []OperationTrial{{Duration: 10 * time.Second, Joules: 1}}
		snap.Costs = append(snap.Costs, cost)
	}
	capacityLoad(s, &snap, relaxed, 150)
	capacityLoad(s, &snap, tight, 150)
	rows := capacityRowsForTest(s, snap, nil)
	// The 10 s route backs up both Workloads, but only adding n3 and n4 ends
	// feasible (keep's final 150/100 − 1 lasts), so it wins over keep's 0.5.
	if d := capacityDecideForTest(t, s, snap, rows); d.Status != SchedulerStatusOverload || d.Target == nil || !poolsOn(*d.Target, n3) || !poolsOn(*d.Target, n4) || math.Abs(d.Target.WorstExceedance-(2.5/0.9-1)) > 1e-9 {
		t.Fatalf("the expansion that ends feasible must win: %+v", d.Target)
	}
	p := capacityPlannerForTest(t, s, snap, rows)
	both := p.evaluate(capacityChoiceForTest(t, p, map[string]string{"n3": "profile@n3-fixed/b1/w1", "n4": "profile@n4-fixed/b1/w2"}), 10)
	// B = (150 − 100) × 10 s = 500, W = 500 / 200 = 2.5 s for both.
	want := map[string]float64{"w1": 2.5/9.9 - 1, "w2": 2.5/0.9 - 1}
	for w, j := range both.judgments {
		uid := p.ws[w].d.UID
		if j.transition == nil || math.Abs(*j.transition-want[uid]) > 1e-9 || j.satisfied != (uid == "w1") {
			t.Fatalf("%s judged against another SLO: %+v", uid, j)
		}
	}
}

// §4: a non-Fixed action of the running goal holds planning; keep continues it.
func TestCapacityTransitionInProgressContinuesGoal(t *testing.T) {
	s, snap := schedulerFixture()
	goal := SchedulerCandidate{ID: "goal", Nodes: snap.Nodes, Instances: snap.Instances, Routing: map[string]map[string]int32{"w1": {"i1": 2}},
		Actions: []Action{{ID: "route", Kind: schedulerActionRoute, Status: schedulerActionPlanned}}}
	snap.InProgressTarget = &goal
	snap.Actions = []Action{{ID: "route", Kind: schedulerActionRoute, Status: phaseRunning}}
	d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
	if d.Status != schedulerStatusKeep || d.Target == nil || d.Target.ID != "goal" || !d.Target.Keep || !slices.Contains(d.Reasons, schedulerTransitionInProgress) || d.Capacity != nil {
		t.Fatalf("in-progress goal must continue: %+v", d)
	}
	// The goal's copies stay planned; once the run reports the route complete, planning resumes.
	snap.Actions[0].Status = schedulerActionComplete
	if d = capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, nil)); slices.Contains(d.Reasons, schedulerTransitionInProgress) {
		t.Fatalf("a finished transition must not hold planning: %+v", d)
	}
}

// Routing control off keeps current weights and judges each instance's κ against them.
func TestCapacityRoutingOffKeepsWeightsAndChecksKappa(t *testing.T) {
	items := []capacityItem{{id: "a", kappa: 30, weight: 1}, {id: "b", kappa: 100, weight: 3}}
	weights, y, ok := capacityFixedWeights(100, items)
	if !ok || !slices.Equal(weights, []int32{1, 3}) || y[0] != 25 || y[1] != 75 {
		t.Fatalf("fixed weights must stay and fit κ: %v %v %t", weights, y, ok)
	}
	items[0].kappa = 20
	if _, _, ok = capacityFixedWeights(100, items); ok {
		t.Fatal("a fixed share beyond κ must fail the final judgment")
	}
	s, snap := schedulerFixture()
	s.Controls.Routing = false
	snap.Routing = map[string]map[string]int32{"w1": {"i1": 7}}
	d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
	if d.Target != nil && d.Target.Routing["w1"]["i1"] != 7 {
		t.Fatalf("routing off launched new weights: %+v", d.Target.Routing)
	}
}

// §2: a controlled Workload without any κ makes the decision CapacityPending.
func TestCapacityPendingWithoutRows(t *testing.T) {
	s, snap := schedulerFixture()
	d := capacityDecideForTest(t, s, snap, nil)
	if d.Status != CapacityPending || d.Target != nil || !slices.Contains(d.Reasons, CapacityPending+":w1") {
		t.Fatalf("no rows: %+v", d)
	}
}

// capacityTestSettings bound synthetic planners loosely, replicas ON.
var capacityTestSettings = SchedulerSettings{ComparisonPeriod: time.Minute, MaxReplicas: 100, MaxInstances: 100, PlanSearchWorkers: 2, Controls: SchedulerControls{Replicas: true, Routing: true}}

// capacitySearchForTest runs the plan search without a time budget.
func capacitySearchForTest(t testing.TB, p *capacityPlanner) capacityResult {
	t.Helper()
	best, err := p.search(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return best
}

// capacityRandomPlanner builds a planner over synthetic node plans.
func capacityRandomPlanner(rng *rand.Rand, workloads, nodes int) *capacityPlanner {
	p := &capacityPlanner{ctx: context.Background(), settings: capacityTestSettings}
	for w := range workloads {
		rate := 0.0
		if rng.IntN(4) > 0 {
			rate = 10 + 90*rng.Float64()
		}
		ws := capacityWorkload{d: &Definition{UID: fmt.Sprint("w", w)}, rate: rate, queued: rng.IntN(6), slack: 0.2 + 2*rng.Float64()}
		p.ws = append(p.ws, ws)
	}
	pre, post := 0.0, 0.0
	for n := range nodes {
		p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{UID: fmt.Sprint("n", n)}})
		var plans []*capacityPlan
		for j := range 1 + rng.IntN(4) {
			plan := &capacityPlan{keep: j == 0, items: make([][]capacityItem, workloads), retained: make([][]capacityItem, workloads), before: 1 + 10*rng.Float64()}
			plan.record.Eligible, plan.record.Key = true, fmt.Sprint("plan-", j)
			for w := range workloads {
				for k := range rng.IntN(3) {
					item := capacityItem{id: fmt.Sprint(n, "-", j, "-", w, "-", k), kappa: 60 * rng.Float64(), e: rng.Float64(), weight: 1}
					plan.items[w] = append(plan.items[w], item)
					if plan.keep || rng.IntN(2) == 0 {
						plan.retained[w] = append(plan.retained[w], item)
					}
				}
			}
			if !plan.keep {
				plan.preMax = []float64{0, 5, 20, 197}[rng.IntN(4)]
				plan.preMean, plan.postMax, plan.postMean = plan.preMax*rng.Float64(), 5*rng.Float64(), 0
				plan.preJ, plan.postJ, plan.mid, plan.after = 100*rng.Float64(), 10*rng.Float64(), 1+10*rng.Float64(), 1+10*rng.Float64()
				pre, post = max(pre, plan.preMax), max(post, plan.postMax)
			}
			p.sum(plan)
			plans = append(plans, plan)
		}
		p.plans = append(p.plans, plans)
		p.compared = append(p.compared, len(plans) > 1)
	}
	p.period = max(60, pre+post)
	return p
}

// capacityBruteForce evaluates every plan set within the instance bounds that
// gives no new instance weight 0, at its own route time, and returns the
// cheapest feasible one (nil when none is feasible).
func capacityBruteForce(p *capacityPlanner) *capacityResult {
	var best *capacityResult
	choice := make([]int, len(p.plans))
	for {
		if res := p.evaluate(slices.Clone(choice), p.routeOf(choice)); p.bounded(choice) && !res.unroutedPrepare && res.feasible && res.better(best) {
			best = &res
		}
		n := 0
		for ; n < len(choice); n++ {
			if choice[n]++; choice[n] < len(p.plans[n]) {
				break
			}
			choice[n] = 0
		}
		if n == len(choice) {
			return best
		}
	}
}

// §9 6 search: on random fixtures of 1–3 Nodes the k = 3 neighborhood holds
// every plan set, so the search equals the exact-κ brute force.
func TestCapacitySearchMatchesBruteForce(t *testing.T) {
	for trial := range 300 {
		p := capacityRandomPlanner(rand.New(rand.NewPCG(uint64(trial), 7)), 1+trial%3, 1+trial%3)
		best := capacitySearchForTest(t, p)
		exact := capacityBruteForce(p)
		if best.feasible != (exact != nil) || exact != nil && (best.key != exact.key || best.joules != exact.joules) {
			t.Fatalf("trial %d: search %v %s %g, brute force %+v", trial, best.feasible, best.key, best.joules, exact)
		}
	}
}

// capacityThreeNodeSwap: three Nodes serve λ 29 with κ 10 each at 10 W. Node 0
// can serve κ 30 alone at 25 W and Nodes 1 and 2 can empty. Changing one or
// two Nodes loses κ or costs more; changing all three saves 5 W.
func capacityThreeNodeSwap() *capacityPlanner {
	p := &capacityPlanner{ctx: context.Background(), period: 60, settings: capacityTestSettings}
	p.ws = []capacityWorkload{{d: &Definition{UID: "w"}, rate: 29, slack: 1}}
	for n := range 3 {
		p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{UID: fmt.Sprint("n", n)}})
		item := capacityItem{id: fmt.Sprint("i", n), kappa: 10, e: 1}
		keep := &capacityPlan{keep: true, items: [][]capacityItem{{item}}, retained: [][]capacityItem{{item}}, before: 10}
		other := &capacityPlan{items: [][]capacityItem{nil}, retained: [][]capacityItem{{item}}}
		if n == 0 {
			other.items, other.after = [][]capacityItem{{{id: "big", kappa: 30, e: 1}}}, 25
		}
		keep.record.Eligible, other.record.Eligible, other.record.Key = true, true, "swap"
		p.sum(keep)
		p.sum(other)
		p.plans = append(p.plans, []*capacityPlan{keep, other})
		p.compared = append(p.compared, true)
	}
	return p
}

// §9 6 search: k = 3 continues from the k = 2 optimum and finds the plan set
// that changes three Nodes; the record names the neighborhood it reached.
func TestCapacitySearchThreeNodesFindsWhatTwoMiss(t *testing.T) {
	p := capacityThreeNodeSwap()
	keep := p.evaluate([]int{0, 0, 0}, 0)
	for _, c := range [][]int{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}, {1, 1, 0}, {1, 0, 1}, {0, 1, 1}} {
		if res := p.evaluate(c, 0); res.better(&keep) {
			t.Fatalf("fixture: %v beats keep", c)
		}
	}
	best := capacitySearchForTest(t, p)
	if !best.feasible || !slices.Equal(best.choice, []int{1, 1, 1}) {
		t.Fatalf("k = 3 must change all three Nodes: %+v", best)
	}
	if s := p.searched; s.Neighborhood != 3 || s.BudgetCut || s.Evaluated == 0 || s.Duration <= 0 {
		t.Fatalf("search record: %+v", s)
	}
}

// capacityTwinSwap: a Node serves λ 10 with instance a at 10 W; plan "swap"
// puts the twin b there and saves gain J over T 60 s. Both profiles carry
// the m4 rasp5 q1 model-loaded idle half-width (0.282 W at 0.95) and a
// per-input half-width of 1 mJ.
func capacityTwinSwap(gain float64) *capacityPlanner {
	p := &capacityPlanner{ctx: context.Background(), period: 60, settings: capacityTestSettings}
	p.ws = []capacityWorkload{{d: &Definition{UID: "w"}, rate: 10, slack: 1}}
	p.snapshot.Nodes = []SchedulerNode{{Node: NodeReference{UID: "n0"}}}
	profile := func(id string) *Profile {
		return &Profile{ID: id, IdleBaselineHalfWidthWatts: 0.282, Batches: []BatchProfile{{Batch: 1, EnergyPerInputJoules: 1, EnergyPerInputHalfWidthJoules: 0.001}}}
	}
	a := SchedulerInstance{ID: "a", WorkloadUID: "w", Profile: profile("pa")}
	b := SchedulerInstance{ID: "b", WorkloadUID: "w", Profile: profile("pb")}
	keep := &capacityPlan{keep: true, instances: []SchedulerInstance{a}, items: [][]capacityItem{{{id: "a", kappa: 50, e: 1}}}, retained: [][]capacityItem{{{id: "a", kappa: 50, e: 1}}}, before: 10}
	swap := &capacityPlan{instances: []SchedulerInstance{b}, midInstances: []SchedulerInstance{a, b}, items: [][]capacityItem{{{id: "b", kappa: 50, e: 1}}}, retained: [][]capacityItem{{{id: "a", kappa: 50, e: 1}}}, mid: 10, after: 10 - gain/60}
	keep.record.Eligible, swap.record.Eligible, swap.record.Key = true, true, "swap"
	p.sum(keep)
	p.sum(swap)
	p.plans, p.compared = [][]*capacityPlan{{keep, swap}}, []bool{true}
	return p
}

// §9 6 minimum gain: a transition is chosen over keep only when its gain over
// T exceeds the measurement uncertainty of the two candidates. The identical-
// node swap of capacity-table-evidence §7.6 (2.7 J) keeps; 100 J moves.
func TestCapacityTransitionNeedsGainBeyondUncertainty(t *testing.T) {
	for gain, keeps := range map[float64]bool{2.7: true, 100: false} {
		p := capacityTwinSwap(gain)
		best := capacitySearchForTest(t, p)
		if best.keep || math.Abs(p.evaluate([]int{0}, 0).joules-best.joules-gain) > 1e-6 {
			t.Fatalf("fixture: the swap must save %g J: %+v", gain, best)
		}
		if got := p.minimumGain(best); got.keep != keeps || got.keep != got.withinUncertainty {
			t.Fatalf("gain %g J: keep=%v withinUncertainty=%v", gain, got.keep, got.withinUncertainty)
		}
	}
}

// §9 6 minimum gain: an instance a plan keeps carries the same idle
// measurement in both candidates, so its error cancels except for the chain
// time. A Node that keeps a (0.5 W half-width) and adds b (0.01 W) saving 10 J
// moves: the threshold is b's 0.6 J. Chain instances count once.
func TestCapacityKeptInstanceAddsNoUncertainty(t *testing.T) {
	p := &capacityPlanner{ctx: context.Background(), period: 60, settings: capacityTestSettings}
	p.ws = []capacityWorkload{{d: &Definition{UID: "w"}, rate: 10, slack: 1}}
	p.snapshot.Nodes = []SchedulerNode{{Node: NodeReference{UID: "n0"}}}
	a := SchedulerInstance{ID: "a", WorkloadUID: "w", Profile: &Profile{ID: "pa", IdleBaselineHalfWidthWatts: 0.5}}
	b := SchedulerInstance{ID: "b", WorkloadUID: "w", Profile: &Profile{ID: "pb", IdleBaselineHalfWidthWatts: 0.01}}
	item := capacityItem{id: "a", kappa: 50, e: 1, weight: 1000}
	keep := &capacityPlan{keep: true, instances: []SchedulerInstance{a}, items: [][]capacityItem{{item}}, retained: [][]capacityItem{{item}}, before: 10}
	add := &capacityPlan{instances: []SchedulerInstance{a, b}, midInstances: []SchedulerInstance{a, b}, items: [][]capacityItem{{item, {id: "b", kappa: 50, e: 1}}},
		retained: [][]capacityItem{{item}}, mid: 10, after: 10 - 10.0/60}
	keep.record.Eligible, add.record.Eligible, add.record.Key = true, true, "add"
	p.sum(keep)
	p.sum(add)
	p.plans, p.compared = [][]*capacityPlan{{keep, add}}, []bool{true}
	best := p.evaluate([]int{1}, 0)
	if !best.feasible || best.keep {
		t.Fatalf("fixture: %+v", best)
	}
	if got := p.minimumGain(best); got.keep {
		t.Fatalf("a kept instance's idle half-width held back a 10 J gain: %+v", got)
	}
}

// §9 6 search: the emptied start obeys the instance bounds like every step.
// Replicas OFF with κ 0 everywhere leaves every set infeasible without bound;
// the emptied set is cheapest yet changes w1's count, so it must not win.
func TestCapacityEmptiedStartObeysReplicasOff(t *testing.T) {
	s, snap := schedulerFixture()
	s.Controls.Replicas, s.ComparisonPeriod = false, 10*time.Minute
	capacityLoad(s, &snap, snap.Workloads[0], 50)
	snap.Costs = []OperationCost{poolsCost(schedulerActionRemove, snap.Nodes[0].Node, schedulerState(snap.Instances[0]), "unloaded", "fixed")}
	d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, func(SchedulerOption) float64 { return 0 }))
	if d.Target == nil || !slices.ContainsFunc(d.Target.Instances, func(i SchedulerInstance) bool { return i.ID == "i1" }) {
		t.Fatalf("replicas OFF removed the Workload's only instance: %+v %v", d.Target, d.Reasons)
	}
}

// §9 6 search: four Nodes serve λ 39.5 with κ 10 each at 10 W; Node 0 can
// instead hold κ 40 at 35 W. That saves 5 W only with all three others
// emptied, four changed Nodes, beyond k = 3 from keep. The second start, every
// Node emptied, reaches it in one step.
func TestCapacitySearchEmptyStartFindsFourNodeChange(t *testing.T) {
	p := &capacityPlanner{ctx: context.Background(), period: 60, settings: capacityTestSettings}
	p.ws = []capacityWorkload{{d: &Definition{UID: "w"}, rate: 39.5, slack: 1}}
	for n := range 4 {
		p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{UID: fmt.Sprint("n", n)}})
		i := SchedulerInstance{ID: fmt.Sprint("i", n), WorkloadUID: "w"}
		item := capacityItem{id: i.ID, kappa: 10, e: 1}
		keep := &capacityPlan{keep: true, instances: []SchedulerInstance{i}, items: [][]capacityItem{{item}}, retained: [][]capacityItem{{item}}, before: 10}
		empty := &capacityPlan{items: [][]capacityItem{nil}, retained: [][]capacityItem{{item}}}
		plans := []*capacityPlan{keep, empty}
		if n == 0 {
			big := SchedulerInstance{ID: "big", WorkloadUID: "w"}
			plans = append(plans, &capacityPlan{instances: []SchedulerInstance{big}, items: [][]capacityItem{{{id: "big", kappa: 40, e: 1}}}, retained: [][]capacityItem{{item}}, after: 35})
		}
		for j, plan := range plans {
			plan.record.Eligible, plan.record.Key = true, fmt.Sprint("plan-", j)
			p.sum(plan)
		}
		p.plans = append(p.plans, plans)
		p.compared = append(p.compared, true)
	}
	if best := capacitySearchForTest(t, p); !best.feasible || !slices.Equal(best.choice, []int{2, 1, 1, 1}) {
		t.Fatalf("the empty start must reach the four-Node change: %v %v %g", best.feasible, best.choice, best.joules)
	}
}

// §9 6 search: a search past its budget keeps the best plan set it found and
// records the cut.
func TestCapacitySearchBudgetCutIsRecorded(t *testing.T) {
	p := capacityThreeNodeSwap()
	best, err := p.search(time.Now())
	if err != nil || !best.keep {
		t.Fatalf("a spent budget must leave keep: %+v %v", best, err)
	}
	if s := p.searched; !s.BudgetCut || s.Neighborhood != 2 {
		t.Fatalf("search record: %+v", s)
	}
}

// §9 6 search: the decision records the search scope with the capacity judgment.
func TestCapacityDecisionRecordsSearch(t *testing.T) {
	s, snap, rows, _ := capacityE1Fixture()
	d := capacityDecideForTest(t, s, snap, rows)
	if d.Capacity == nil || d.Capacity.Search == nil || d.Capacity.Search.Neighborhood != 3 || d.Capacity.Search.Evaluated == 0 || d.Capacity.Search.BudgetCut {
		t.Fatalf("decision search record: %+v", d.Capacity)
	}
}

// §9 6 weights: route weights sum to 1,000 and stay within κ after rounding.
func TestCapacityRouteWeightsStayWithinKappa(t *testing.T) {
	items := func(kappa ...float64) []capacityItem {
		out := []capacityItem{}
		for k, x := range kappa {
			out = append(out, capacityItem{id: fmt.Sprint("i", k), kappa: x})
		}
		return out
	}
	for _, c := range []struct {
		rate   float64
		kappa  []float64
		want   []int32
		within bool
	}{
		{100, []float64{99.6, 0.4}, []int32{996, 4}, true},
		{100, []float64{34, 34, 34}, []int32{340, 340, 320}, true},
		{0, []float64{1, 2, 3}, []int32{334, 333, 333}, true},
		{100, []float64{100, 50}, []int32{1000, 0}, true},
		// Rounding gives 334 to the first, 33.4 req/s over 33.35, and no other has room.
		{100, []float64{33.35, 33.35, 33.3}, nil, false},
	} {
		weights, _, ok := capacityWeights(c.rate, 0, items(c.kappa...))
		sum := int32(0)
		for k, w := range weights {
			sum += w
			if ok && c.rate*float64(w)/schedulerRouteUnits > c.kappa[k]*(1+1e-12) {
				t.Fatalf("%v: weight %d exceeds κ", c, w)
			}
		}
		if ok != c.within || sum != schedulerRouteUnits || c.want != nil && !slices.Equal(weights, c.want) {
			t.Fatalf("rate %g κ %v: weights %v ok %v", c.rate, c.kappa, weights, ok)
		}
	}
}

// m4 r3: a closed-loop client held the measured λ at 29.5 below rasp5 q1's
// κ 34.3 while 160 requests stayed queued. Keep drains them in
// W = 160 / 34.3 = 4.66 s, past L − m = 0.861 s, so keep is not feasible and
// adding genesis3 q2 (κ 109.7, W = 160 / 144 = 1.11 s) wins on exceedance.
func TestCapacityQueuedBacklogFailsKeep(t *testing.T) {
	p := &capacityPlanner{ctx: context.Background(), period: 60, settings: capacityTestSettings}
	p.ws = []capacityWorkload{{d: &Definition{UID: "w"}, rate: 29.5, queued: 160, slack: 0.861}}
	rasp5 := capacityItem{id: "q1", kappa: 34.3, e: 1, weight: 1000}
	keep0 := &capacityPlan{keep: true, items: [][]capacityItem{{rasp5}}, retained: [][]capacityItem{{rasp5}}, before: 1}
	keep1 := &capacityPlan{keep: true, items: [][]capacityItem{nil}, retained: [][]capacityItem{nil}, before: 1}
	add := &capacityPlan{items: [][]capacityItem{{{id: "q2", kappa: 109.7, e: 2}}}, retained: [][]capacityItem{nil}, preMax: 10.7, preMean: 10.7, mid: 2, after: 2}
	keep0.record.Eligible, keep1.record.Eligible, add.record.Eligible, add.record.Key = true, true, true, "add"
	p.plans = [][]*capacityPlan{{keep0}, {keep1, add}}
	p.compared = []bool{true, true}
	for n := range 2 {
		p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{UID: fmt.Sprint("n", n)}})
	}
	for _, plan := range []*capacityPlan{keep0, keep1, add} {
		p.sum(plan)
	}
	keep := p.evaluate([]int{0, 0}, 0)
	if j := keep.judgments[0]; keep.feasible || j.wait == nil || math.Abs(*j.wait-160/34.3) > 1e-9 || *j.transition <= 0 {
		t.Fatalf("keep with 160 queued judged feasible: %+v", j)
	}
	if best := capacitySearchForTest(t, p); !slices.Equal(best.choice, []int{0, 1}) {
		t.Fatalf("the backlog must force expansion: %+v", best)
	}
}

// m4 r2: λ 12.8 lagged a 40 req/s load with 160 queued and no instance left
// (κ_ret 0). Filling λ alone gave the cheapest new instance (rasp5 q2) all
// 1,000 units; λ plus the backlog drain B/(L − m) exceeds the four new
// instances' κ sum, so the weights follow κ and every instance takes load.
func TestCapacityOverloadWeightsDrainTheBacklog(t *testing.T) {
	kappa := []float64{42.01676682274319, 48.2933028258133, 114.5538148895834, 144.47154833104395}
	p := &capacityPlanner{ctx: context.Background(), period: 147, settings: capacityTestSettings}
	p.ws = []capacityWorkload{{d: &Definition{UID: "w"}, rate: 12.8, queued: 160, slack: 1 - 0.139}}
	choice := []int{}
	for n, k := range kappa {
		p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{UID: fmt.Sprint("n", n)}})
		keep := &capacityPlan{keep: true, items: [][]capacityItem{nil}, retained: [][]capacityItem{nil}}
		add := &capacityPlan{items: [][]capacityItem{{{id: fmt.Sprint("i", n), kappa: k, e: float64(n + 1)}}}, retained: [][]capacityItem{nil}}
		p.plans = append(p.plans, []*capacityPlan{keep, add})
		p.compared = append(p.compared, true)
		choice = append(choice, 1)
	}
	res := p.evaluate(choice, 15.926122415)
	j := res.judgments[0]
	if math.Abs(j.backlog-363.85436691200005) > 1e-6 || math.Abs(res.sums[0][0]-349.3354328691838) > 1e-9 {
		t.Fatalf("incident judgment B=%g κ_new=%g", j.backlog, res.sums[0][0])
	}
	want := map[string]int32{"i0": 120, "i1": 138, "i2": 328, "i3": 414}
	if !maps.Equal(res.weights[0], want) || res.weightsExceeded {
		t.Fatalf("weights %v, want κ-proportional %v", res.weights[0], want)
	}
	// Without backlog the λ fill stays on the cheapest instance.
	p.ws[0].queued = 0
	p.ws[0].rate = 0.5
	if res = p.evaluate(choice, 0); !maps.Equal(res.weights[0], map[string]int32{"i0": 1000}) {
		t.Fatalf("no backlog: %v", res.weights[0])
	}
}

// A batch limit change keeps the instance: the Pod initializes every measured
// batch, so the route applies the new firstfloor policy cap with no remove or
// prepare (m4 r5 swapped genesis3 q2 b1↔b2 by stop, drain, remove, prepare).
func TestCapacityBatchLimitChangeIsPolicyRoute(t *testing.T) {
	s, snap := schedulerFixture()
	profile := *snap.Instances[0].Profile
	profile.Batches = append(slices.Clone(profile.Batches), BatchProfile{Batch: 2, DurationNanos: []float64{float64(15 * time.Millisecond)}, EnergyPerInputJoules: 0.8, MemoryBytes: 100})
	snap.Instances[0].Profile = &profile
	option := snap.Instances[0]
	option.MaxBatch = 2
	snap.Options = []SchedulerOption{poolsOption(option)}
	n1 := snap.Instances[0].Node
	snap.Costs = nil
	capacityLoad(s, &snap, snap.Workloads[0], 150)
	rows := capacityRowsForTest(s, snap, func(o SchedulerOption) float64 { return 100 * float64(o.Instance.MaxBatch) })
	d := capacityDecideForTest(t, s, snap, rows)
	plans := poolsPlans(d, n1)
	i := slices.IndexFunc(plans, func(p SchedulerNodePlan) bool { return p.Key == "profile/b2/w1" })
	if i < 0 || !plans[i].Eligible || plans[i].Pre != 0 || plans[i].Post != 0 || plans[i].KappaRetained["w1"] != 100 {
		t.Fatalf("b2 plan must reuse i1 with no chain: %+v", plans)
	}
	if d.Target == nil || len(d.Target.Instances) != 1 || d.Target.Instances[0].ID != "i1" || d.Target.Instances[0].MaxBatch != 2 ||
		slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return a.Kind != schedulerActionRoute }) {
		t.Fatalf("target must route i1 with batch 2 only: %+v", d.Target)
	}
}

// The measured prepare cost carries the bare measured peak; the planned
// allocation adds servingMemoryBytes. The cost still matches: memory is a
// placement constraint, not part of the measured transition condition.
func TestCapacityPrepareCostIgnoresQueueMemory(t *testing.T) {
	s, snap := schedulerFixture()
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	measured := poolsInstance(snap.Instances[0], "add", n2, "fixed", "w1")
	planned := measured
	planned.Allocation.MemoryBytes += 500
	snap.Options = append(snap.Options, poolsOption(planned))
	snap.Costs = []OperationCost{poolsCost(schedulerActionPrepare, n2, "unloaded", schedulerState(measured), "fixed")}
	capacityLoad(s, &snap, snap.Workloads[0], 150)
	d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
	if d.Target == nil || !poolsOn(*d.Target, n2) || slices.ContainsFunc(poolsPlans(d, n2), func(p SchedulerNodePlan) bool { return p.Reason == schedulerPlanMissingOperationCost }) {
		t.Fatalf("prepare cost missed on memory: %+v plans %+v", d.Target, poolsPlans(d, n2))
	}
	if !schedulerEqualState([]string{schedulerState(planned)}, []string{schedulerState(measured)}) {
		t.Fatal("replay loaded-state witness compares memory")
	}
}

// m4 r2: a preparation whose instance died fails the goal; while that failure
// stays in snapshot.Actions (one observation window) no plan prepares the same
// state on that Node again.
func TestCapacityFailedPreparationWaitsOneWindow(t *testing.T) {
	s, snap := schedulerFixture()
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	add := poolsInstance(snap.Instances[0], "add", n2, "fixed", "w1")
	snap.Options = append(snap.Options, poolsOption(add))
	snap.Costs = []OperationCost{poolsCost(schedulerActionPrepare, n2, "unloaded", schedulerState(add), "fixed")}
	capacityLoad(s, &snap, snap.Workloads[0], 150)
	rows := capacityRowsForTest(s, snap, nil)
	if d := capacityDecideForTest(t, s, snap, rows); d.Target == nil || !poolsOn(*d.Target, n2) {
		t.Fatalf("fixture must prepare on n2: %+v", d.Target)
	}
	snap.Actions = []Action{{ID: "prepare-lost", Kind: schedulerActionPrepare, Node: n2, Instance: "target-w1-" + add.Profile.ID + "-1",
		From: "unloaded", To: schedulerState(add), Status: schedulerActionFailed, Reason: "completed effect lost"}}
	d := capacityDecideForTest(t, s, snap, rows)
	if d.Target == nil || poolsOn(*d.Target, n2) || slices.Contains(d.Reasons, schedulerTransitionInProgress) {
		t.Fatalf("failed preparation planned again: %+v", d.Target)
	}
	if !slices.ContainsFunc(poolsPlans(d, n2), func(p SchedulerNodePlan) bool { return p.Reason == schedulerPlanPreparationFailed }) {
		t.Fatalf("exclusion not recorded: %+v", poolsPlans(d, n2))
	}
}

// §9 6 search: request energy splits at the route: [0, r] by the retained instance
// up to κ_ret, its overflow and [r, T] by the target fill.
func TestCapacityRequestEnergySplitsAtRoute(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ComparisonPeriod, s.MaxObservationAge = time.Second, time.Minute, time.Hour
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	add := poolsInstance(snap.Instances[0], "add", n2, "fixed", "w1")
	profile := *add.Profile
	profile.Batches = []BatchProfile{{Batch: 1, DurationNanos: []float64{float64(time.Millisecond)}, EnergyPerInputJoules: 3, MemoryBytes: 100}}
	add.Profile = &profile
	snap.Options = append(snap.Options, poolsOption(add))
	prepare := poolsCost(schedulerActionPrepare, n2, "unloaded", schedulerState(add), "fixed")
	prepare.Trials = []OperationTrial{{Duration: 10 * time.Second, Joules: 0}}
	snap.Costs = []OperationCost{prepare}
	capacityLoad(s, &snap, snap.Workloads[0], 150)
	p := capacityPlannerForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
	choice := capacityChoiceForTest(t, p, map[string]string{"n2": add.Profile.ID + "/b1/w1"})
	res := p.evaluate(choice, 10)
	node := p.plans[1][choice[1]].energy(10, p.period)
	// [0,10]: 100 req/s on i1 (e 1) = 1,000 J; overflow 50 × 10 at the target mean
	// e 250/150; [10,60]: 100 on i1 + 50 on add (e 3) = 250 W × 50 s.
	want := 1000 + 500*250.0/150 + 250*50
	if p.period != 60 || math.Abs(res.joules-node-want) > 1e-6 {
		t.Fatalf("request energy %g, want %g (T %g)", res.joules-node, want, p.period)
	}
}

// §9 6 기간과 기록: a 197 s chain with a 60 s horizon sets one T for keep and
// every plan, and the plan pays its whole trial energy.
func TestCapacityPeriodCoversTheLongestChain(t *testing.T) {
	s, snap := schedulerFixture()
	s.ComparisonPeriod = time.Minute
	reentry := poolsAddNode(&snap, "n3", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6})
	snap.Nodes[1].PowerMode = schedulerTestEcoMode // already lowered, as in E1
	option := poolsInstance(snap.Instances[0], "opt", reentry, "fixed", "w1")
	snap.Options = append(snap.Options, poolsOption(option))
	power := poolsCost(schedulerActionPower, reentry, schedulerTestEcoMode, "fixed", "")
	power.Trials = []OperationTrial{{Duration: 134500 * time.Millisecond, Joules: 1968.96}}
	prepare := poolsCost(schedulerActionPrepare, reentry, "unloaded", schedulerState(option), "fixed")
	prepare.Trials = []OperationTrial{{Duration: 62600 * time.Millisecond, Joules: 600}}
	snap.Costs = []OperationCost{power, prepare}
	rows := capacityRowsForTest(s, snap, nil)
	d := capacityDecideForTest(t, s, snap, rows)
	if (d.Capacity.Period - 197100*time.Millisecond).Abs() > time.Microsecond {
		t.Fatalf("T %v, want 197.1 s", d.Capacity.Period)
	}
	for _, plan := range d.Capacity.Plans {
		if strings.Contains(plan.Reason, "Horizon") {
			t.Fatalf("plan excluded by the horizon: %+v", plan)
		}
	}
	p := capacityPlannerForTest(t, s, snap, rows)
	keep := p.evaluate(make([]int, len(p.plans)), 0)
	plan := p.evaluate(capacityChoiceForTest(t, p, map[string]string{"n3": option.Profile.ID + "/b1/w1"}), 197.1)
	// The chain fills T: the plan costs its trial joules where keep idles at eco (6 W).
	if want := 1968.96 + 600 - 6*197.1; math.Abs(plan.joules-keep.joules-want) > 1e-6 {
		t.Fatalf("re-entry over keep %g J, want %g J", plan.joules-keep.joules, want)
	}
}

// §9 6 기간과 기록 (D-22): among 40 measured options the re-entry plan is a candidate.
func TestCapacityReentryAmongFortyOptions(t *testing.T) {
	s, snap := schedulerFixture()
	s.MaxReplicas, s.MaxInstances = 8, 16
	base := snap.Instances[0]
	snap.Instances, snap.Options = nil, nil
	nodes := []NodeReference{snap.Nodes[0].Node}
	for n := range 7 {
		nodes = append(nodes, poolsAddNode(&snap, fmt.Sprint("n", n+2), map[string]float64{"fixed": 10}))
	}
	for k := range 39 {
		option := poolsInstance(base, fmt.Sprint("option-", k), nodes[k%len(nodes)], "fixed", "w1")
		profile := *option.Profile
		profile.ID += fmt.Sprint("-cpu", k%4, "-", k)
		profile.Condition.CPU.AffinityCPUs = []int{k % 4}
		metadata := *profile.RuntimeMetadata
		metadata.CPUAffinity = []int{k % 4}
		profile.RuntimeMetadata = &metadata
		option.Profile, option.Allocation.CPUs = &profile, []int{k % 4}
		snap.Options = append(snap.Options, poolsOption(option))
		snap.Costs = append(snap.Costs, poolsCost(schedulerActionPrepare, option.Node, "unloaded", schedulerState(option), "fixed"))
		snap.Costs[len(snap.Costs)-1].ID += fmt.Sprint("-", k)
	}
	reentry := poolsAddNode(&snap, "z-reentry", map[string]float64{schedulerTestEcoMode: 6, "fixed": 10})
	option := poolsInstance(base, "reentry", reentry, "fixed", "w1")
	snap.Options = append(snap.Options, poolsOption(option))
	snap.Costs = append(snap.Costs, poolsCost(schedulerActionPower, reentry, schedulerTestEcoMode, "fixed", ""), poolsCost(schedulerActionPrepare, reentry, "unloaded", schedulerState(option), "fixed"))
	capacityLoad(s, &snap, snap.Workloads[0], 1000)
	rows := capacityRowsForTest(s, snap, capacityKappaByProfile(map[string]float64{option.Profile.ID: 2000}))
	for k := range rows {
		if rows[k].Input.ProfileID != option.Profile.ID {
			*rows[k].Kappa = 1
		}
	}
	began := time.Now()
	d := capacityDecideForTest(t, s, snap, rows)
	if d.Status != schedulerStatusFeasible || d.Target == nil || !poolsOn(*d.Target, reentry) {
		t.Fatalf("the re-entry plan must be reachable among 40 options: %+v", d.Status)
	}
	t.Logf("40 options: %d node plans, Tick %v", d.Search.Examined, time.Since(began))
}

// settings.excludedNodes: an excluded Node is never planned, even when its
// option is the only way to meet the load, and every decision records it.
func TestCapacityExcludedNodeIsNeverPlanned(t *testing.T) {
	s, snap := schedulerFixture()
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 1})
	n3 := poolsAddNode(&snap, "n3", map[string]float64{"fixed": 1})
	add := poolsInstance(snap.Instances[0], "add", n2, "fixed", "w1")
	snap.Options = append(snap.Options, poolsOption(add))
	snap.Costs = []OperationCost{poolsCost(schedulerActionPrepare, n2, "unloaded", schedulerState(add), "fixed")}
	capacityLoad(s, &snap, snap.Workloads[0], 150)
	rows := capacityRowsForTest(s, snap, nil)
	if d := capacityDecideForTest(t, s, snap, rows); d.Target == nil || !poolsOn(*d.Target, n2) {
		t.Fatalf("fixture must plan n2: %+v", d.Target)
	}
	snap.Nodes[1].Reasons = []string{NodeExcluded}
	snap.Nodes[2].Reasons = []string{NodeExcluded}
	d := capacityDecideForTest(t, s, snap, rows)
	if d.Target != nil && poolsOn(*d.Target, n2) || d.Status != SchedulerStatusOverload ||
		slices.ContainsFunc(d.Keep.Actions, func(a Action) bool { return a.Node == n2 || a.Node == n3 }) {
		t.Fatalf("an excluded Node was planned: %s %+v", d.Status, d.Target)
	}
	for _, node := range []NodeReference{n2, n3} {
		if !slices.ContainsFunc(poolsPlans(d, node), func(p SchedulerNodePlan) bool { return p.Reason == NodeExcluded }) {
			t.Fatalf("%s not recorded as excluded: %+v", node.Name, poolsPlans(d, node))
		}
	}
	s.ExcludedNodes = []string{"node-n2", "node-n2"}
	if s.Validate() == nil {
		t.Fatal("duplicate excluded Node accepted")
	}
}

// §9 6 기간과 기록: every excluded node plan is recorded with its reason.
func TestCapacityRecordsExcludedPlans(t *testing.T) {
	s, snap := schedulerFixture()
	poolsAddWorkload(&snap, "w2")
	base := snap.Instances[0]
	add := func(uid, mode string, option bool) (NodeReference, SchedulerInstance) {
		node := poolsAddNode(&snap, uid, map[string]float64{"fixed": 10})
		i := poolsInstance(base, "opt-"+uid, node, mode, "w1")
		if option {
			snap.Options = append(snap.Options, poolsOption(i))
		}
		return node, i
	}
	_, noCost := add("no-cost", "fixed", true)
	_, pending := add("pending", "fixed", true)
	_, rejected := add("rejected", "fixed", true)
	// One option alone over the Node's CPUs: CPU 7 is not online.
	offline, first := add("offline-cpu", "fixed", false)
	second := first
	profile := *first.Profile
	profile.ID += "-second"
	profile.Condition.CPU.AffinityCPUs = []int{7}
	metadata := *profile.RuntimeMetadata
	metadata.CPUAffinity = []int{7}
	profile.RuntimeMetadata = &metadata
	second.Profile, second.Allocation.CPUs = &profile, []int{7}
	snap.Options = append(snap.Options, poolsOption(second))
	fixed, _ := add("fixed-workload", "fixed", true)
	disabled := poolsInstance(base, "disabled", fixed, "fixed", "w2")
	snap.Instances = append(snap.Instances, disabled)
	snap.Routing = map[string]map[string]int32{"w1": {"i1": 1}, "w2": {"disabled": 1}}
	inventory, _ := add("inventory", "fixed", true)
	snap.Nodes[len(snap.Nodes)-1].Reasons = []string{NodePlacementInventoryMissing}
	for _, i := range []SchedulerInstance{pending, rejected, second} {
		snap.Costs = append(snap.Costs, poolsCost(schedulerActionPrepare, i.Node, "unloaded", schedulerState(i), "fixed"))
	}
	rows := capacityRowsForTest(s, snap, nil)
	rows = slices.DeleteFunc(rows, func(r CapacityRow) bool { return r.Input.ProfileID == pending.Profile.ID })
	for k := range rows {
		if rows[k].Input.ProfileID == rejected.Profile.ID {
			rows[k].Status, rows[k].Reason, rows[k].Kappa = CapacityRejected, CapacityNonMonotone, nil
		}
	}
	d := capacityDecideForTest(t, s, snap, rows)
	for node, reason := range map[NodeReference]string{
		noCost.Node: schedulerPlanMissingOperationCost, pending.Node: CapacityPending, rejected.Node: CapacityRejected,
		offline: schedulerPlanInfeasibleResources, fixed: schedulerPlanFixedWorkload, inventory: NodePlacementInventoryMissing,
	} {
		if !slices.ContainsFunc(poolsPlans(d, node), func(p SchedulerNodePlan) bool { return !p.Eligible && p.Reason == reason }) {
			t.Fatalf("%s: no plan excluded with %s: %+v", node.UID, reason, poolsPlans(d, node))
		}
	}
}

// A plan that changes the mode checks its CPUs against the target mode's.
func TestCapacityModeChangePlansUseTargetModeCPUs(t *testing.T) {
	s, snap := schedulerFixture()
	base := snap.Instances[0]
	add := func(uid, mode string, modeCPUs map[string][]int) NodeReference {
		node := poolsAddNode(&snap, uid, map[string]float64{"fixed": 10, schedulerTestEcoMode: 6})
		snap.Nodes[len(snap.Nodes)-1].ModeCPUs = modeCPUs
		i := poolsInstance(base, "opt-"+uid, node, mode, "w1")
		snap.Options = append(snap.Options, poolsOption(i))
		snap.Costs = append(snap.Costs, poolsCost(schedulerActionPrepare, node, "unloaded", schedulerState(i), mode))
		return node
	}
	twoCore := add("two-core", schedulerTestEcoMode, map[string][]int{schedulerTestEcoMode: {1}})
	undeclared := add("undeclared", schedulerTestEcoMode, nil)
	current := add("current", "fixed", map[string][]int{schedulerTestEcoMode: {1}})
	d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
	for node, reason := range map[NodeReference]string{twoCore: schedulerPlanInfeasibleResources, undeclared: NodePlacementInventoryMissing} {
		if !slices.ContainsFunc(poolsPlans(d, node), func(p SchedulerNodePlan) bool { return !p.Eligible && p.Reason == reason }) {
			t.Fatalf("%s: no plan excluded with %s: %+v", node.UID, reason, poolsPlans(d, node))
		}
	}
	if slices.ContainsFunc(poolsPlans(d, current), func(p SchedulerNodePlan) bool {
		return p.Reason == schedulerPlanInfeasibleResources || p.Reason == NodePlacementInventoryMissing
	}) {
		t.Fatalf("current-mode plan excluded: %+v", poolsPlans(d, current))
	}
}

// §9 6 기간과 기록: the decision does not depend on input order.
func TestCapacityDecisionIgnoresInputOrder(t *testing.T) {
	s, snap, rows, _ := capacityE1Fixture()
	poolsAddNode(&snap, "idle", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6})
	want := capacityDecideForTest(t, s, snap, rows)
	want.Capacity.Search.Duration = 0 // the search time is measured, not decided
	for seed := range 10 {
		rng := rand.New(rand.NewPCG(uint64(seed), 3))
		shuffled := snap
		shuffled.Nodes, shuffled.Instances, shuffled.Options = slices.Clone(snap.Nodes), slices.Clone(snap.Instances), slices.Clone(snap.Options)
		shuffled.Costs, shuffled.Baselines, shuffled.Arrivals = slices.Clone(snap.Costs), slices.Clone(snap.Baselines), slices.Clone(snap.Arrivals)
		rng.Shuffle(len(shuffled.Nodes), func(i, j int) { shuffled.Nodes[i], shuffled.Nodes[j] = shuffled.Nodes[j], shuffled.Nodes[i] })
		rng.Shuffle(len(shuffled.Options), func(i, j int) { shuffled.Options[i], shuffled.Options[j] = shuffled.Options[j], shuffled.Options[i] })
		rng.Shuffle(len(shuffled.Costs), func(i, j int) { shuffled.Costs[i], shuffled.Costs[j] = shuffled.Costs[j], shuffled.Costs[i] })
		rng.Shuffle(len(shuffled.Baselines), func(i, j int) {
			shuffled.Baselines[i], shuffled.Baselines[j] = shuffled.Baselines[j], shuffled.Baselines[i]
		})
		rng.Shuffle(len(shuffled.Arrivals), func(i, j int) {
			shuffled.Arrivals[i], shuffled.Arrivals[j] = shuffled.Arrivals[j], shuffled.Arrivals[i]
		})
		shuffledRows := slices.Clone(rows)
		rng.Shuffle(len(shuffledRows), func(i, j int) { shuffledRows[i], shuffledRows[j] = shuffledRows[j], shuffledRows[i] })
		got := capacityDecideForTest(t, s, shuffled, shuffledRows)
		got.Capacity.Search.Duration = 0
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d: shuffled input changed the decision:\n%+v\n%+v", seed, got.Target, want.Target)
		}
	}
}

// §9 6 기간과 기록: an exact tie keeps the current configuration.
func TestCapacityExactTieKeepsCurrent(t *testing.T) {
	p := &capacityPlanner{ctx: context.Background(), period: 60, settings: capacityTestSettings}
	p.ws = []capacityWorkload{{d: &Definition{UID: "w"}, rate: 50, slack: 1}}
	p.snapshot.Nodes = []SchedulerNode{{Node: NodeReference{UID: "n0"}}}
	item := capacityItem{id: "i", kappa: 100, e: 1, weight: 1}
	keep := &capacityPlan{keep: true, items: [][]capacityItem{{item}}, retained: [][]capacityItem{{item}}, before: 10}
	twin := item
	twin.id = "a-twin"
	same := &capacityPlan{items: [][]capacityItem{{twin}}, retained: [][]capacityItem{{item}}, after: 10}
	p.sum(keep)
	p.sum(same)
	keep.record.Eligible, same.record.Eligible, same.record.Key = true, true, "a-same"
	p.plans, p.compared = [][]*capacityPlan{{keep, same}}, []bool{true}
	if best := capacitySearchForTest(t, p); !best.keep || best.choice[0] != 0 {
		t.Fatalf("exact tie must keep: %+v", best)
	}
}

// m4 r4 D-R4-4: at 2 req/s genesis3 q2 and orangepi q1 stayed Ready but
// unrouted. A node plan that takes such an instance back for the Workload is
// physically keep, with the same energy and routing, yet it carried more κ, so
// the node DP pruned keep and the route-only plan was re-issued 22 times. Keep wins.
func TestCapacityUnroutedTwinKeepsCurrent(t *testing.T) {
	s, snap := schedulerFixture()
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	idle := poolsInstance(snap.Instances[0], "i2", n2, "fixed", "")
	idle.TargetID = "slot-i2"
	snap.Instances = append(snap.Instances, idle)
	served := idle
	served.WorkloadUID, served.TargetID = "w1", ""
	snap.Options = append(snap.Options, poolsOption(served))
	snap.Routing = map[string]map[string]int32{"w1": {"i1": schedulerRouteUnits}}
	capacityLoad(s, &snap, snap.Workloads[0], 20)
	d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
	if d.Status != schedulerStatusKeep || d.Target == nil || !d.Target.Keep || slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return !a.Fixed }) {
		t.Fatalf("an equal-energy relabel of keep was chosen: %s %+v", d.Status, d.Target)
	}
}

// m4 r4 D-R4-1: at 40 req/s the weights switched almost every Tick between the
// backlog drain shape (genesis3 takes load) and the energy shape (rasp5 q1 at
// exactly its κ 25.3), 181 route actions in 75 min. Current weights now stay
// while each instance's share of λ plus the backlog drain is within its κ, so
// the r4 queue sequence changes the weights once.
func TestCapacityFeasibleWeightsStay(t *testing.T) {
	ids := []string{"q1", "lut", "g3", "opi"}
	kappa := []float64{25.288198615188904, 25.68831509322024, 70.93509923703007, 7.53273799179367}
	p := &capacityPlanner{ctx: context.Background(), period: 152, settings: capacityTestSettings}
	p.ws = []capacityWorkload{{d: &Definition{UID: "w"}, slack: 1 - 0.029}}
	p.keep.Routing = map[string]map[string]int32{"w": {"q1": 643, "lut": 357}}
	for n, id := range ids {
		p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{UID: fmt.Sprint("n", n)}})
		item := capacityItem{id: id, kappa: kappa[n], e: float64(n + 1)}
		p.plans = append(p.plans, []*capacityPlan{{keep: true, items: [][]capacityItem{{item}}, retained: [][]capacityItem{{item}}}})
		p.compared = append(p.compared, true)
	}
	changes := 0
	for _, tick := range []struct {
		rate   float64
		queued int
	}{{39.32, 0}, {40.1, 52}, {39.32, 0}, {39.43, 3}, {40.73, 0}, {39.83, 1}, {40.23, 2}} {
		p.ws[0].rate, p.ws[0].queued = tick.rate, tick.queued
		res := p.evaluate(make([]int, len(ids)), 0)
		if !res.feasible {
			t.Fatalf("λ %g q %d infeasible: %+v", tick.rate, tick.queued, res.judgments[0])
		}
		if !maps.Equal(res.weights[0], p.keep.Routing["w"]) {
			changes++
			p.keep.Routing["w"] = res.weights[0]
		}
	}
	// Only the backlog of 52 moves load off rasp5 q1 (0.643 × 40.1 > κ 25.3).
	if changes != 1 || p.keep.Routing["w"]["g3"] == 0 {
		t.Fatalf("%d weight changes, last %v", changes, p.keep.Routing["w"])
	}
}

// capacityR5Fixture is the m4 r5 low-load state: rasp5 i1 routed at 1,000 and
// genesis3 q2 and orangepi q1 Ready with weight 0, at λ 2 over a 60 s window.
func capacityR5Fixture() (SchedulerSettings, SchedulerSnapshot, []CapacityRow, NodeReference, NodeReference) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod, s.MaxObservationAge = time.Minute, 10*time.Second, 10*time.Minute, time.Hour
	genesis3 := poolsAddNode(&snap, "genesis3", map[string]float64{"fixed": 49.97})
	orangepi := poolsAddNode(&snap, "orangepi", map[string]float64{"fixed": 2.73})
	g3 := poolsInstance(snap.Instances[0], "g3", genesis3, "fixed", "w1")
	opi := poolsInstance(snap.Instances[0], "opi", orangepi, "fixed", "w1")
	for _, i := range []*SchedulerInstance{&g3, &opi} {
		profile := *i.Profile
		profile.IdleBaselineWatts = map[string]float64{"g3": 49.80, "opi": 2.71}[i.ID]
		i.Profile = &profile
	}
	snap.Instances = append(snap.Instances, g3, opi)
	snap.Options = append(snap.Options, poolsOption(g3), poolsOption(opi))
	snap.Routing = map[string]map[string]int32{"w1": {"i1": schedulerRouteUnits}}
	snap.Costs = []OperationCost{
		poolsCost(schedulerActionRemove, genesis3, schedulerState(g3), "unloaded", "fixed"),
		poolsCost(schedulerActionRemove, orangepi, schedulerState(opi), "unloaded", "fixed"),
	}
	capacityLoad(s, &snap, snap.Workloads[0], 2)
	return s, snap, capacityRowsForTest(s, snap, nil), genesis3, orangepi
}

// m4 r5 D-R5-2: genesis3 q2 and orangepi q1 stayed at weight 0 after P2; their
// measured idle power is below the empty Node's, so energy never removes them.
// Unrouted for one observation window, they are removed; not before.
func TestCapacityIdleInstancesAreRemovedAfterOneWindow(t *testing.T) {
	s, snap, rows, genesis3, orangepi := capacityR5Fixture()
	since := snap.At.Add(-30 * time.Second)
	for k := 1; k < 3; k++ {
		snap.Instances[k].UnroutedSince = &since
	}
	if d := capacityDecideForTest(t, s, snap, rows); d.Status != schedulerStatusKeep || slices.ContainsFunc(d.Reasons, func(r string) bool { return strings.HasPrefix(r, schedulerIdleInstanceRemoved) }) {
		t.Fatalf("unrouted for 30 s removed: %s %v", d.Status, d.Reasons)
	}
	since = snap.At.Add(-time.Minute)
	d := capacityDecideForTest(t, s, snap, rows)
	removes := func(node NodeReference) bool {
		return slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return a.Kind == schedulerActionRemove && a.Node == node })
	}
	if d.Target == nil || poolsOn(*d.Target, genesis3) || poolsOn(*d.Target, orangepi) || !removes(genesis3) || !removes(orangepi) ||
		!slices.Contains(d.Reasons, schedulerIdleInstanceRemoved+":g3") || !slices.Contains(d.Reasons, schedulerIdleInstanceRemoved+":opi") {
		t.Fatalf("idle instances kept: %+v %v", d.Target, d.Reasons)
	}
	until := snap.At.Add(time.Minute)
	snap.Instances[1].HeldUntil = &until
	if d = capacityDecideForTest(t, s, snap, rows); d.Target == nil || !poolsOn(*d.Target, genesis3) || poolsOn(*d.Target, orangepi) {
		t.Fatalf("a held instance was removed as idle: %+v", d.Target)
	}
}

// m4 r6: the unrouted genesis3 q2 and orangepi q1 serve no Workload (capture
// workloadUID "") and, prepared before the planner recorded owners, have no
// owner; only their slot names the Workload. An instance owned by an
// autonomous Workload, or owned by nobody and routed by nobody, is removed
// after one unrouted window. One a disabled Workload routes to stays.
func TestCapacityUnroutedPlannerInstancesAreRemoved(t *testing.T) {
	s, snap, rows, genesis3, orangepi := capacityR5Fixture()
	since := snap.At.Add(-time.Minute)
	for k := 1; k < 3; k++ {
		snap.Instances[k].WorkloadUID, snap.Instances[k].TargetID, snap.Instances[k].UnroutedSince = "", "target-w1-"+snap.Instances[k].Profile.ID+"-1", &since
	}
	snap.Instances[1].OwnerWorkloadUID = "w1" // prepared with the planner's owner
	removed := func(d SchedulerDecision, node NodeReference) bool {
		return d.Target != nil && !poolsOn(*d.Target, node) &&
			slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return a.Kind == schedulerActionRemove && a.Node == node })
	}
	if d := capacityDecideForTest(t, s, snap, rows); !removed(d, genesis3) || !removed(d, orangepi) {
		t.Fatalf("unrouted planner instances kept: %s %+v %v", d.Status, d.Target, d.Reasons)
	}
	other := poolsAddWorkload(&snap, "w2")
	other.Model.UID = "m2"
	snap.Routing["w2"] = map[string]int32{"opi": 1}
	snap.Instances[2].UnroutedSince = nil
	if d := capacityDecideForTest(t, s, snap, rows); !removed(d, genesis3) || removed(d, orangepi) {
		t.Fatalf("an instance a disabled Workload routes to was removed: %+v", d.Target)
	}
}

// m4 r5 D-R5-1: genesis2 was prepared at 04:28:48 while keep was in Overload
// and removed 50 s later. Its hold keeps it for one observation window even
// when removal saves energy.
func TestCapacityHeldInstanceIsNotRemoved(t *testing.T) {
	s, snap, rows, genesis3, _ := capacityR5Fixture()
	snap.Instances[1].Profile.IdleBaselineWatts = 60 // removal saves 10 W
	d := capacityDecideForTest(t, s, snap, rows)
	if d.Target == nil || poolsOn(*d.Target, genesis3) {
		t.Fatalf("fixture must remove genesis3 for energy: %+v", d.Target)
	}
	until := snap.At.Add(time.Minute)
	snap.Instances[1].HeldUntil = &until
	d = capacityDecideForTest(t, s, snap, rows)
	if d.Target == nil || !poolsOn(*d.Target, genesis3) || !slices.ContainsFunc(poolsPlans(d, genesis3), func(p SchedulerNodePlan) bool { return p.Reason == schedulerPlanHeld }) {
		t.Fatalf("held genesis3 removed: %+v %+v", d.Target, poolsPlans(d, genesis3))
	}
}

// A replacement repays its transition over T at the least load of the
// observation window. n2's instance a (e 2) can be replaced by b (e 1) for a
// 1,000 J chain: at a steady 30 req/s the saving (≈30 W over 60 s) repays it,
// with the same mean but one 10 s interval at 5 req/s it does not.
func TestCapacityReplacementRepaysAtLeastLoad(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod, s.MaxObservationAge = time.Minute, 10*time.Second, time.Minute, time.Hour
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	energy := func(i *SchedulerInstance, e float64) {
		profile := *i.Profile
		profile.Batches = slices.Clone(profile.Batches)
		profile.Batches[0].EnergyPerInputJoules, profile.IdleBaselineWatts = e, 12
		i.Profile = &profile
	}
	energy(&snap.Instances[0], 3)
	a, b := poolsInstance(snap.Instances[0], "a", n2, "fixed", "w1"), schedulerTestCPU(poolsInstance(snap.Instances[0], "b", n2, "fixed", "w1"))
	energy(&a, 2)
	energy(&b, 1)
	b.Profile.ID = "profile-b"
	snap.Instances = append(snap.Instances, a)
	snap.Options = append(snap.Options, poolsOption(a), poolsOption(b))
	snap.Routing = map[string]map[string]int32{"w1": {"i1": 400, "a": 600}}
	prepare := poolsCost(schedulerActionPrepare, n2, "unloaded", schedulerState(b), "fixed")
	prepare.Trials = []OperationTrial{{Duration: time.Second, Joules: 1000}}
	snap.Costs = []OperationCost{poolsCost(schedulerActionRemove, n2, schedulerState(a), "unloaded", "fixed"), prepare}
	rows := capacityRowsForTest(s, snap, capacityKappaByProfile(map[string]float64{"profile": 100, a.Profile.ID: 50, b.Profile.ID: 50}))
	load := func(rates ...float64) {
		d := snap.Workloads[0]
		snap.Arrivals = nil
		start := snap.ObservationEnd.Add(-s.ObservationWindow)
		for k, rate := range rates {
			n := int(rate * 10)
			for j := range n {
				at := start.Add(time.Duration(k)*10*time.Second + time.Duration(j)*10*time.Second/time.Duration(n))
				snap.Arrivals = append(snap.Arrivals, SchedulerArrival{ID: fmt.Sprint(k, "-", j), At: at, Definition: d, Accepted: true, Compute: true, WorkloadUID: d.UID})
			}
		}
	}
	replaced := func(d SchedulerDecision) bool {
		return d.Target != nil && slices.ContainsFunc(d.Target.Actions, func(x Action) bool { return x.Kind == schedulerActionPrepare && x.Node == n2 })
	}
	load(30, 30, 30, 30, 30, 30)
	if d := capacityDecideForTest(t, s, snap, rows); !replaced(d) {
		t.Fatalf("steady 30 req/s must replace a: %+v %v", d.Target, d.Reasons)
	}
	load(35, 35, 35, 35, 35, 5)
	if d := capacityDecideForTest(t, s, snap, rows); replaced(d) || !slices.Contains(d.Reasons, schedulerReplacementUnpaid+":n2") {
		t.Fatalf("replacement unpaid at 5 req/s was chosen: %+v %v", d.Target, d.Reasons)
	}
}
