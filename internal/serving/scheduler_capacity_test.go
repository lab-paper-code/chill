package serving

import (
	"context"
	"fmt"
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
				in := NewCapacityInput(s, v, w)
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
	s.ObservationWindow, s.Horizon, s.MaxObservationAge, s.CompletionMarginMS = time.Second, time.Minute, time.Hour, 177
	d := snap.Workloads[0]
	d.SLO = SLO{Percentile: 95, TargetLatency: time.Second}
	rasp5 := snap.Nodes[0].Node
	lut := schedulerTestCPU(snap.Instances[0])
	lut.ID = "q1-lut"
	genesis3 := poolsAddNode(&snap, "genesis3", map[string]float64{"fixed": 30})
	q2 := poolsInstance(snap.Instances[0], "q2", genesis3, "fixed", "w1")
	profile := *q2.Profile
	profile.IdleBaselineWatts = 60
	q2.Profile = &profile
	snap.Options = append(snap.Options, poolsOption(lut), poolsOption(q2))
	prepareLUT := poolsCost(schedulerActionPrepare, rasp5, "unloaded", schedulerState(lut), "fixed")
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
	if d.Status != schedulerStatusFeasible || d.Target == nil || !poolsOn(*d.Target, genesis3) || slices.ContainsFunc(d.Target.Instances, func(i SchedulerInstance) bool { return i.ID == "target-w1-profile-other-cpu-1" }) {
		t.Fatalf("genesis3 q2 must be the target: %+v", d)
	}
	w := d.Capacity.Workloads[0]
	if math.Abs(w.Rate-40) > 1e-9 || math.Abs(w.KappaNew-144) > 1e-9 || math.Abs(w.KappaRetained-34.3) > 1e-9 || math.Abs(w.Backlog-5.7*10.7) > 1e-6 ||
		w.WaitSeconds == nil || math.Abs(*w.WaitSeconds-5.7*10.7/144) > 1e-9 || d.Capacity.Route != 10700*time.Millisecond {
		t.Fatalf("genesis3 judgment: %+v route %v", w, d.Capacity.Route)
	}
	p := capacityPlannerForTest(t, s, snap, rows)
	slow := p.evaluate(capacityChoiceForTest(t, p, map[string]string{"n1": "profile-other-cpu/b1/w1+profile/b1/w1"}), 20.2)
	fast := p.evaluate(capacityChoiceForTest(t, p, map[string]string{"genesis3": "profile@genesis3-fixed/b1/w1"}), 10.7)
	j := slow.judgments[0]
	if slow.feasible || math.Abs(j.backlog-5.7*20.2) > 1e-6 || math.Abs(*j.wait-5.7*20.2/69.2) > 1e-9 || *j.wait <= 1-0.177 {
		t.Fatalf("q1-lut judgment B=%g W=%v", j.backlog, *j.wait)
	}
	if slow.joules >= fast.joules || !fast.feasible {
		t.Fatalf("fixture must make the slow route cheaper: %g vs %g", slow.joules, fast.joules)
	}
}

// §9 6 판정: a mode change removes the current instances before the route
// (κ_ret 0); a replacement on free CPUs removes after it (keep κ retained).
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
	if d.Status != schedulerStatusOverload || d.Target == nil || !poolsOn(*d.Target, small) || !poolsOn(*d.Target, large) ||
		!slices.Contains(d.Reasons, schedulerNoPlanMeetsSLO) || math.Abs(d.Target.WorstExceedance-0.2) > 1e-9 {
		t.Fatalf("overload must add every instance (exceedance 600/500 − 1): %+v", d)
	}
}

// §9 6 판정: two Workloads with different SLOs are judged each with its own L − m.
func TestCapacityJudgesEachWorkloadWithItsSLO(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.Horizon, s.MaxObservationAge, s.CompletionMarginMS = time.Second, time.Minute, time.Hour, 100
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
	// The 10 s route backs up both Workloads, so the current instances (final
	// 150/100 − 1) win; only their weights take the 1,000 scale.
	if d := capacityDecideForTest(t, s, snap, rows); d.Status != schedulerStatusOverload || d.Target == nil || poolsOn(*d.Target, n3) || poolsOn(*d.Target, n4) || math.Abs(d.Target.WorstExceedance-0.5) > 1e-9 {
		t.Fatalf("current instances must win at worst exceedance 0.5: %+v", d.Target)
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

// §9 6 판정: an instance captured in two Workloads makes the decision wait.
func TestCapacitySharedInstanceWaits(t *testing.T) {
	s, snap := schedulerFixture()
	snap.Reasons = []string{SchedulerSharedInstance + ":i1"}
	d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
	if d.Status != SchedulerSharedInstance || d.Target != nil || d.Capacity != nil || !slices.Equal(d.Reasons, snap.Reasons) {
		t.Fatalf("shared instance must wait: %+v", d)
	}
}

// §4: a non-Fixed action of the running goal holds planning; keep continues it.
func TestCapacityTransitionInProgressContinuesGoal(t *testing.T) {
	s, snap := schedulerFixture()
	goal := SchedulerCandidate{ID: "goal", Nodes: snap.Nodes, Instances: snap.Instances, Routing: map[string]map[string]int32{"w1": {"i1": 2}},
		Actions: []Action{{ID: "route", Kind: schedulerActionRoute, Status: phaseRunning}}}
	snap.InProgressTarget = &goal
	d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
	if d.Status != schedulerStatusKeep || d.Target == nil || d.Target.ID != "goal" || !d.Target.Keep || !slices.Contains(d.Reasons, schedulerTransitionInProgress) || d.Capacity != nil {
		t.Fatalf("in-progress goal must continue: %+v", d)
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
var capacityTestSettings = SchedulerSettings{Horizon: time.Minute, MaxReplicas: 100, MaxInstances: 100, Controls: SchedulerControls{Replicas: true}}

// capacityRandomPlanner builds a planner over synthetic node plans.
func capacityRandomPlanner(rng *rand.Rand, workloads, nodes, cells int) *capacityPlanner {
	p := &capacityPlanner{ctx: context.Background(), cells: cells, settings: capacityTestSettings}
	for w := range workloads {
		rate := 0.0
		if rng.IntN(4) > 0 {
			rate = 10 + 90*rng.Float64()
		}
		ws := capacityWorkload{d: &Definition{UID: fmt.Sprint("w", w)}, rate: rate, queued: rng.IntN(6), slack: 0.2 + 2*rng.Float64()}
		if rate > 0 {
			ws.cells, ws.unit = cells, rate/float64(cells)
		}
		p.ws = append(p.ws, ws)
	}
	routes := map[float64]bool{0: true}
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
				routes[plan.preMax] = true
				pre, post = max(pre, plan.preMax), max(post, plan.postMax)
			}
			p.sum(plan)
			plans = append(plans, plan)
		}
		p.plans = append(p.plans, plans)
		p.compared = append(p.compared, len(plans) > 1)
	}
	p.period = max(60, pre+post)
	for r := range routes {
		p.routes = append(p.routes, r)
	}
	slices.Sort(p.routes)
	for _, plans := range p.plans {
		for _, plan := range plans {
			p.serveTable(plan)
		}
	}
	return p
}

// capacityBruteForce evaluates every plan set at its own route time.
func capacityBruteForce(p *capacityPlanner) (bool, float64) {
	feasible, best := false, math.Inf(1)
	choice := make([]int, len(p.plans))
	for {
		r := 0.0
		for n, j := range choice {
			if !p.plans[n][j].keep {
				r = max(r, p.plans[n][j].preMax)
			}
		}
		if res := p.evaluate(slices.Clone(choice), r); res.feasible {
			feasible, best = true, min(best, res.joules)
		}
		n := 0
		for ; n < len(choice); n++ {
			if choice[n]++; choice[n] < len(p.plans[n]) {
				break
			}
			choice[n] = 0
		}
		if n == len(choice) {
			return feasible, best
		}
	}
}

// §9 6 DP: on random small fixtures (1–3 Workloads) the DP judgment equals the
// exact-κ brute force, for every cell count; the Pareto set sizes are logged.
func TestCapacityDPMatchesBruteForce(t *testing.T) {
	pareto, gap := 0, 0.0
	for trial := range 150 {
		workloads := 1 + trial%3
		for _, cells := range []int{2, 7, 50} {
			if workloads == 3 && cells == 50 {
				cells = 12 // three Workloads at the largest count take minutes of brute-force DP time
			}
			p := capacityRandomPlanner(rand.New(rand.NewPCG(uint64(trial), 7)), workloads, 1+trial%4, cells)
			best, err := p.solve()
			if err != nil {
				t.Fatal(err)
			}
			want, cheapest := capacityBruteForce(p)
			if best.feasible != want {
				t.Fatalf("trial %d cells %d: DP feasible=%v, brute force %v", trial, cells, best.feasible, want)
			}
			if want {
				if best.joules < cheapest-1e-6 {
					t.Fatalf("trial %d: DP energy %g below the exact minimum %g", trial, best.joules, cheapest)
				}
				if cheapest > 0 {
					gap = max(gap, (best.joules-cheapest)/cheapest)
				}
			}
			pareto = max(pareto, p.paretoMax)
		}
	}
	t.Logf("largest Pareto set of one load-cell state %d; largest DP energy above the exact minimum %.3g%%", pareto, 100*gap)
}

// §9 6 DP: three instances of κ 0.34λ meet λ at any cell count.
func TestCapacityThreeThirdsMeetTheLoad(t *testing.T) {
	for _, cells := range []int{3, 50} {
		p := &capacityPlanner{ctx: context.Background(), cells: cells, period: 60, routes: []float64{0}, settings: capacityTestSettings}
		p.ws = []capacityWorkload{{d: &Definition{UID: "w"}, rate: 100, slack: 1, cells: cells, unit: 100 / float64(cells)}}
		for n := range 3 {
			p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{UID: fmt.Sprint("n", n)}})
			keep := &capacityPlan{keep: true, items: [][]capacityItem{nil}, retained: [][]capacityItem{nil}, before: 1}
			item := capacityItem{id: fmt.Sprint("i", n), kappa: 34, e: 1}
			add := &capacityPlan{items: [][]capacityItem{{item}}, retained: [][]capacityItem{nil}, mid: 2, after: 2}
			p.sum(keep)
			p.sum(add)
			keep.record.Eligible, add.record.Eligible, add.record.Key = true, true, "add"
			p.plans = append(p.plans, []*capacityPlan{keep, add})
			p.compared = append(p.compared, true)
			p.serveTable(keep)
			p.serveTable(add)
		}
		best, err := p.solve()
		if err != nil || !best.feasible || !slices.Equal(best.choice, []int{1, 1, 1}) {
			t.Fatalf("cells %d: %+v %v", cells, best, err)
		}
	}
}

// §9 6 DP: route weights sum to 1,000 and stay within κ after rounding.
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
		weights, _, ok := capacityWeights(c.rate, items(c.kappa...))
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

// §9 6 DP: changing an instance's batch limit is a replacement, remove and prepare.
func TestCapacityBatchLimitChangeIsReplacement(t *testing.T) {
	s, snap := schedulerFixture()
	profile := *snap.Instances[0].Profile
	profile.Batches = append(slices.Clone(profile.Batches), BatchProfile{Batch: 2, DurationNanos: []float64{float64(15 * time.Millisecond)}, EnergyPerInputJoules: 0.8, MemoryBytes: 100})
	snap.Instances[0].Profile = &profile
	option := snap.Instances[0]
	option.MaxBatch = 2
	snap.Options = []SchedulerOption{poolsOption(option)}
	n1, state := snap.Instances[0].Node, schedulerState(option)
	remove, prepare := poolsCost(schedulerActionRemove, n1, state, "unloaded", "fixed"), poolsCost(schedulerActionPrepare, n1, "unloaded", state, "fixed")
	prepare.Trials = []OperationTrial{{Duration: 2 * time.Millisecond, Joules: 0.02}}
	snap.Costs = []OperationCost{remove, prepare}
	capacityLoad(s, &snap, snap.Workloads[0], 150)
	rows := capacityRowsForTest(s, snap, func(o SchedulerOption) float64 { return 100 * float64(o.Instance.MaxBatch) })
	d := capacityDecideForTest(t, s, snap, rows)
	plans := poolsPlans(d, n1)
	i := slices.IndexFunc(plans, func(p SchedulerNodePlan) bool { return p.Key == "profile/b2/w1" })
	if i < 0 || !plans[i].Eligible || (plans[i].Pre-3*time.Millisecond).Abs() > time.Microsecond || plans[i].KappaRetained["w1"] != 0 {
		t.Fatalf("b2 plan must remove then prepare before the route: %+v", plans)
	}
	if d.Target == nil || !slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return a.Kind == schedulerActionRemove && a.Instance == "i1" }) ||
		!slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return a.Kind == schedulerActionPrepare && a.Instance == "target-w1-profile-2" }) {
		t.Fatalf("target must replace i1: %+v", d.Target)
	}
}

// §9 6 DP: request energy splits at the route: [0, r] by the retained instance
// up to κ_ret, its overflow and [r, T] by the target fill.
func TestCapacityRequestEnergySplitsAtRoute(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.Horizon, s.MaxObservationAge = time.Second, time.Minute, time.Hour
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
	s.Horizon = time.Minute
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

// §9 6 기간과 기록 (D-22): among 40 measured options the re-entry plan is in the DP.
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
	overlap, first := add("overlap", "fixed", true)
	second := first
	profile := *first.Profile
	profile.ID += "-second"
	profile.Condition.CPU.AffinityCPUs = []int{0, 1}
	metadata := *profile.RuntimeMetadata
	metadata.CPUAffinity = []int{0, 1}
	profile.RuntimeMetadata = &metadata
	second.Profile, second.Allocation.CPUs = &profile, []int{0, 1}
	snap.Options = append(snap.Options, poolsOption(second))
	fixed, _ := add("fixed-workload", "fixed", true)
	disabled := poolsInstance(base, "disabled", fixed, "fixed", "w2")
	snap.Instances = append(snap.Instances, disabled)
	snap.Routing = map[string]map[string]int32{"w1": {"i1": 1}, "w2": {"disabled": 1}}
	inventory, _ := add("inventory", "fixed", true)
	snap.Nodes[len(snap.Nodes)-1].Reasons = []string{NodePlacementInventoryMissing}
	for _, i := range []SchedulerInstance{pending, rejected, first, second} {
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
		overlap: schedulerPlanInfeasibleResources, fixed: schedulerPlanFixedWorkload, inventory: NodePlacementInventoryMissing,
	} {
		if !slices.ContainsFunc(poolsPlans(d, node), func(p SchedulerNodePlan) bool { return !p.Eligible && p.Reason == reason }) {
			t.Fatalf("%s: no plan excluded with %s: %+v", node.UID, reason, poolsPlans(d, node))
		}
	}
}

// §9 6 기간과 기록: the decision does not depend on input order.
func TestCapacityDecisionIgnoresInputOrder(t *testing.T) {
	s, snap, rows, _ := capacityE1Fixture()
	poolsAddNode(&snap, "idle", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6})
	want := capacityDecideForTest(t, s, snap, rows)
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
		if got := capacityDecideForTest(t, s, shuffled, shuffledRows); !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d: shuffled input changed the decision:\n%+v\n%+v", seed, got.Target, want.Target)
		}
	}
}

// §9 6 기간과 기록: an exact tie keeps the current configuration.
func TestCapacityExactTieKeepsCurrent(t *testing.T) {
	p := &capacityPlanner{ctx: context.Background(), cells: 10, period: 60, routes: []float64{0}, settings: capacityTestSettings}
	p.ws = []capacityWorkload{{d: &Definition{UID: "w"}, rate: 50, slack: 1, cells: 10, unit: 5}}
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
	p.serveTable(keep)
	p.serveTable(same)
	best, err := p.solve()
	if err != nil || !best.keep || best.choice[0] != 0 {
		t.Fatalf("exact tie must keep: %+v %v", best, err)
	}
}
