package serving

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

const capacitySearchStartKeep = "keep"

func capacityDecideForTest(t testing.TB, s SchedulerSettings, snap SchedulerSnapshot) SchedulerDecision {
	t.Helper()
	// These fixtures model an acknowledged route by their serving weights unless
	// the case supplies an explicit routing snapshot.
	if snap.Routing == nil {
		snap.Routing = schedulerInstanceRouting(context.Background(), snap.Workloads, snap.Instances)
	}
	capacityFeedbackForTest(s, &snap)
	d, err := EvaluateScheduler(context.Background(), s, snap)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// capacityServiceRatesForTest supplies deterministic paired raw service laws.
// Rates are optimistic profile support, never tested-rate capacity certificates.
func capacityServiceRatesForTest(snapshot *SchedulerSnapshot, rates map[string]float64) {
	profiles := map[string]*Profile{}
	update := func(instance *SchedulerInstance) {
		if instance.Profile == nil {
			return
		}
		rate, ok := rates[instance.Profile.ID]
		if !ok {
			return
		}
		if profile := profiles[instance.Profile.ID]; profile != nil {
			instance.Profile = profile
			return
		}
		profile := *instance.Profile
		profile.Batches = slices.Clone(profile.Batches)
		for k, batch := range profile.Batches {
			profile.Batches[k] = schedulerTestBatchProfile(batch, []float64{float64(time.Second) * float64(batch.Batch) / rate})
		}
		profiles[profile.ID] = &profile
		instance.Profile = &profile
	}
	for k := range snapshot.Instances {
		update(&snapshot.Instances[k])
	}
	for k := range snapshot.Options {
		update(&snapshot.Options[k].Instance)
	}
}

// capacityFeedbackForTest captures the current timing input explicitly.
func capacityFeedbackForTest(settings SchedulerSettings, snapshot *SchedulerSnapshot) {
	for _, definition := range snapshot.Workloads {
		if slices.ContainsFunc(snapshot.PathFeedback, func(feedback PathFeedback) bool { return feedback.WorkloadUID == definition.UID }) {
			continue
		}
		snapshot.PathFeedback = append(snapshot.PathFeedback, BuildPathFeedback(definition.UID, nil, definition.SLO, time.Duration(settings.CompletionMarginMS*float64(time.Millisecond))))
	}
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
func capacityPlannerForTest(t testing.TB, s SchedulerSettings, snap SchedulerSnapshot) *capacityPlanner {
	t.Helper()
	ctx := context.Background()
	capacityFeedbackForTest(s, &snap)
	snap = schedulerCanonical(snap)
	if snap.Routing == nil {
		snap.Routing = schedulerInstanceRouting(ctx, snap.Workloads, snap.Instances)
	}
	keep := SchedulerCandidate{ID: "keep", Keep: true, Nodes: schedulerCommonNodes(snap), Instances: snap.Instances, Routing: schedulerInstanceRouting(ctx, snap.Workloads, snap.Instances)}
	for uid, routing := range snap.Routing {
		keep.Routing[uid] = routing
	}
	decision := SchedulerDecision{Search: schedulerSearchPopulation(ctx, s, snap), Workloads: schedulerWorkloadStates(s, snap)}
	p := newCapacityPlanner(ctx, s, snap, keep, decision)
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

// capacityE1Fixture gives each option paired raw service evidence and measured preparation costs.
func capacityE1Fixture() (SchedulerSettings, SchedulerSnapshot, NodeReference) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ComparisonPeriod, s.CompletionMarginMS = time.Second, time.Minute, 177
	d := snap.Workloads[0]
	// Reject leaves 88/2400 failures on the slow expansion and 34/2400 on
	// the fast one. p98 keeps this fixture's finite-demand recovery distinction.
	d.SLO = SLO{Percentile: 98, TargetLatency: time.Second}
	d.Admission.Policy = AdmissionPolicyReject
	// E1 placed q1-lut beside q1 on rasp5; with one instance per Workload per
	// Node (D-R8-1) it stands on a Node of its own with the same service law and chain.
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
	capacityServiceRatesForTest(&snap, map[string]float64{"profile": 34.3, lut.Profile.ID: 34.9, q2.Profile.ID: 109.7})
	capacityFeedbackForTest(s, &snap)
	return s, snap, genesis3
}

// Exact finite-demand outcomes outrank a cheaper route with slower preparation.
func TestCapacityTransientRuleRejectsCheaperSlowRoute(t *testing.T) {
	s, snap, genesis3 := capacityE1Fixture()
	d := capacityDecideForTest(t, s, snap)
	if d.Target == nil || !poolsOn(*d.Target, genesis3) || poolsOn(*d.Target, NodeReference{Name: "node-lut", UID: "lut"}) {
		t.Fatalf("finite-demand recovery lost faster route: target=%+v exclusions=%v", d.Target, d.Capacity.Search.Excluded)
	}
	if len(d.Target.Feedback) != 1 || d.Target.Feedback[0].SuccessQuantileNanos == nil || !schedulerFinite(*d.Target.Feedback[0].SuccessQuantileNanos) {
		t.Fatalf("selected route lacks finite-success evidence: %+v", d.Target.Feedback)
	}
	p := capacityPlannerForTest(t, s, snap)
	slow := p.evaluate(capacityChoiceForTest(t, p, map[string]string{"lut": "profile@lut-fixed/b1/w1"}), 20.2)
	fast := p.evaluate(capacityChoiceForTest(t, p, map[string]string{"genesis3": "profile@genesis3-fixed/b1/w1"}), 10.7)
	if slow.joules >= fast.joules || !fast.better(&slow) {
		t.Fatalf("direct outcome tuple must outrank cheaper slow route: slow=%+v fast=%+v", slow, fast)
	}
}

func TestCapacityRejectAtP95PrefersCheaperFeasibleExpansion(t *testing.T) {
	s, snap, genesis3 := capacityE1Fixture()
	snap.Workloads[0].SLO.Percentile = 95
	p := capacityPlannerForTest(t, s, snap)
	slow := p.evaluate(capacityChoiceForTest(t, p, map[string]string{"lut": "profile@lut-fixed/b1/w1"}), 20.2)
	fast := p.evaluate(capacityChoiceForTest(t, p, map[string]string{"genesis3": "profile@genesis3-fixed/b1/w1"}), 10.7)
	for _, tc := range []struct {
		result   capacityResult
		rejected int
	}{{slow, 88}, {fast, 34}} {
		if !tc.result.feasible || len(tc.result.feedback) != 1 || !tc.result.feedback[0].Satisfied ||
			tc.result.feedback[0].Population != 2400 || tc.result.feedback[0].Rejected != tc.rejected ||
			tc.result.feedback[0].OnTime+tc.result.feedback[0].Late != 2400-tc.rejected {
			t.Fatalf("p95 Reject outcome changed: %+v", tc.result)
		}
	}
	if slow.joules >= fast.joules || !slow.better(&fast) {
		t.Fatalf("both feasible expansions must retain energy preference: slow=%+v fast=%+v", slow, fast)
	}
	decision := capacityDecideForTest(t, s, snap)
	if decision.Target == nil || !poolsOn(*decision.Target, NodeReference{Name: "node-lut", UID: "lut"}) || poolsOn(*decision.Target, genesis3) {
		t.Fatalf("p95 Reject must choose the cheaper feasible route: target=%+v exclusions=%v", decision.Target, decision.Capacity.Search.Excluded)
	}
}

func TestCapacityMinimumDemandPreservesCapacityAtNoLoad(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod = time.Minute, 10*time.Second, time.Minute
	snap.ObservedSince = snap.ObservationEnd.Add(-time.Hour)
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	i2 := poolsInstance(snap.Instances[0], "i2", n2, "fixed", "w1")
	profile := *i2.Profile
	profile.IdleBaselineWatts = 20 // removing i2 saves 10 W
	i2.Profile = &profile
	snap.Instances = append(snap.Instances, i2)
	snap.Options = append(snap.Options, poolsOption(i2))
	snap.Routing = map[string]map[string]int32{"w1": {"i1": 500, "i2": 500}}
	snap.Costs = []OperationCost{poolsCost(schedulerActionRemove, n2, schedulerState(i2), "unloaded", "fixed")}
	capacityServiceRatesForTest(&snap, map[string]float64{snap.Instances[0].Profile.ID: 25, i2.Profile.ID: 25})

	// An empty replay has no SLO obligation to preserve a particular profile rate.
	cheap := capacityDecideForTest(t, s, snap)
	if cheap.Target == nil || cheap.Capacity == nil || cheap.Capacity.Workloads[0].Rate != 0 ||
		cheap.Target.ExpectedJoules == nil || cheap.Keep.ExpectedJoules == nil || *cheap.Target.ExpectedJoules >= *cheap.Keep.ExpectedJoules {
		t.Fatalf("empty demand must allow a cheaper configuration without a capacity floor: %+v", cheap)
	}
	if len(cheap.Target.Feedback) != 1 || cheap.Target.Feedback[0].Population != 0 || !cheap.Target.Feedback[0].Satisfied || cheap.Target.Feedback[0].SuccessQuantileNanos != nil {
		t.Fatalf("empty demand must not fabricate a successful latency observation: %+v", cheap.Target.Feedback)
	}
	s.MinimumDemand = map[string]float64{"w1": 40}
	floored := capacityDecideForTest(t, s, snap)
	if floored.Target == nil || !poolsOn(*floored.Target, n2) || floored.Capacity == nil {
		t.Fatalf("minimum demand must retain the second instance at no load: %+v", floored)
	}
	w := floored.Capacity.Workloads[0]
	if w.MinimumDemand != 40 || w.Rate != 40 || w.RoutingRate < w.Rate {
		t.Fatalf("effective demand and selected capacity must cover the floor: %+v", w)
	}
	if len(floored.Target.Feedback) != 1 || floored.Target.Feedback[0].MinimumDemandArrivals != 2400 || floored.Target.Feedback[0].OnTime != 2400 || !floored.Target.Feedback[0].Satisfied {
		t.Fatalf("the 40 req/s floor must be consumed as a successful finite replay population: %+v", floored.Target.Feedback)
	}
}

func TestSchedulerMinimumDemandValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		demand map[string]float64
		valid  bool
	}{
		{name: "unset", valid: true},
		{name: "zero", demand: map[string]float64{"w1": 0}, valid: true},
		{name: "positive", demand: map[string]float64{"w1": 40}, valid: true},
		{name: "empty UID", demand: map[string]float64{"": 40}},
		{name: "negative", demand: map[string]float64{"w1": -1}},
		{name: "NaN", demand: map[string]float64{"w1": math.NaN()}},
		{name: "positive infinity", demand: map[string]float64{"w1": math.Inf(1)}},
		{name: "negative infinity", demand: map[string]float64{"w1": math.Inf(-1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := schedulerFixture()
			s.MinimumDemand = tc.demand
			if err := s.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate() = %v, valid = %t", err, tc.valid)
			}
		})
	}
}

// m4 r6: 20 s after a ChillServer restart under 40 req/s the 60 s window held
// only 20 s of arrivals, λ read 13.3 and the planner removed instances under
// load. λ is measured over the part of the window observed since the process
// started; under one control interval of it the decision waits.
func TestCapacityRateCoversOnlyTheObservedWindow(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval = time.Minute, 10*time.Second
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
	capacityServiceRatesForTest(&snap, map[string]float64{snap.Instances[0].Profile.ID: 25})
	d := capacityDecideForTest(t, s, snap)
	if d.Target == nil || !poolsOn(*d.Target, n2) || !d.Search.WindowStart.Equal(snap.ObservedSince) ||
		d.Capacity == nil || math.Abs(d.Capacity.Workloads[0].Rate-40) > 1e-9 {
		t.Fatalf("partial window read as the whole window: %s %+v %+v", d.Status, d.Search, d.Capacity)
	}
	snap.ObservedSince = snap.ObservationEnd.Add(-5 * time.Second)
	if d = capacityDecideForTest(t, s, snap); d.Status != schedulerStatusPending || !slices.Contains(d.Reasons, schedulerPartialObservation) {
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
	s.ObservationWindow, s.ControlInterval, s.MaxReplicas = time.Minute, 10*time.Second, 3
	s.ComparisonPeriod = time.Minute // Include the partially observed trace in the finite replay cohort.
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
	capacityServiceRatesForTest(&snap, map[string]float64{"profile": 25.69, i2.Profile.ID: 29.30, i3.Profile.ID: 70.94})
	observe := func(rate float64, since time.Duration) SchedulerDecision {
		capacityLoad(s, &snap, snap.Workloads[0], rate)
		snap.ObservedSince = snap.ObservationEnd.Add(-since)
		snap.Arrivals = slices.DeleteFunc(snap.Arrivals, func(a SchedulerArrival) bool { return a.At.Before(snap.ObservedSince) })
		return capacityDecideForTest(t, s, snap)
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

// A mature observation process still sees a new step through its recent window.
// The same λ reaches capacity, transient delay, route shares and request energy.
func TestCapacityRecentDemandExpandsWithOutstandingOnce(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod = time.Minute, 10*time.Second, time.Minute
	snap.ObservedSince = snap.ObservationEnd.Add(-time.Hour)
	d := snap.Workloads[0]
	d.SLO.TargetLatency = time.Second
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	i2 := poolsInstance(snap.Instances[0], "i2", n2, "fixed", d.UID)
	snap.Options = append(snap.Options, poolsOption(i2))
	snap.Costs = []OperationCost{poolsCost(schedulerActionPrepare, n2, "unloaded", schedulerState(i2), "fixed")}
	capacityLoad(s, &snap, d, 50)
	recent := snap.ObservationEnd.Add(-s.ControlInterval)
	snap.Arrivals = slices.DeleteFunc(snap.Arrivals, func(a SchedulerArrival) bool { return a.At.Before(recent) })
	for j := range 6 {
		snap.Outstanding = append(snap.Outstanding, SchedulerRequest{ID: fmt.Sprint("q", j), Definition: d, ReceivedAt: snap.At, Phase: phasePending})
	}
	capacityServiceRatesForTest(&snap, map[string]float64{"profile": 25, i2.Profile.ID: 100})
	got := capacityDecideForTest(t, s, snap)
	if got.Target == nil || !poolsOn(*got.Target, n2) || got.Capacity == nil {
		t.Fatalf("recent demand must expand: %s %v %+v", got.Status, got.Reasons, got.Target)
	}
	load, w := got.Search.Loads[0], got.Capacity.Workloads[0]
	rate := 50.1 // 500/10 + 6/60, rather than (500+6)/60
	if !got.Search.RecentWindowStart.Equal(recent) || load.RecentComputeInputs != 500 ||
		math.Abs(load.InputsPerSecond-500.0/60) > 1e-9 || load.RecentInputsPerSecond != 50 ||
		math.Abs(w.Rate-rate) > 1e-9 || w.Queued != 6 {
		t.Fatalf("demand inputs: %+v %+v %+v", got.Search, load, w)
	}
	total := int32(0)
	for _, weight := range w.Weights {
		total += weight
	}
	if total != schedulerRouteUnits || len(got.Target.Feedback) != 1 {
		t.Fatalf("demand lost integer route or exact outcomes: %+v", got.Target)
	}
	p := capacityPlannerForTest(t, s, snap)
	choice := capacityChoiceForTest(t, p, map[string]string{"n2": "profile@n2-fixed/b1/w1"})
	res := p.evaluate(choice, got.Capacity.Route.Seconds())
	background := 0.0
	for n, j := range choice {
		if p.compared[n] {
			background += p.plans[n][j].energy(res.route, res.period)
		}
	}
	// Both instances share batch-1 energy, so every λW input has that cost.
	want := rate * res.period * capacityInputEnergy(snap.Instances[0])
	if math.Abs(res.joules-background-want) > 1e-8 {
		t.Fatalf("energy used another rate: request J %g want %g", res.joules-background, want)
	}
}

func TestCapacityHighDemandProtectsOnlyItsServingCapacity(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod = time.Minute, 10*time.Second, time.Minute
	snap.ObservedSince = snap.ObservationEnd.Add(-time.Hour)
	w1, w2 := snap.Workloads[0], poolsAddWorkload(&snap, "w2")
	s.AutonomousWorkloads = append(s.AutonomousWorkloads, w2.UID)
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	n3 := poolsAddNode(&snap, "n3", map[string]float64{"fixed": 10})
	n4 := poolsAddNode(&snap, "n4", map[string]float64{"fixed": 10})
	i2 := poolsInstance(snap.Instances[0], "i2", n2, "fixed", w1.UID)
	i3 := poolsInstance(snap.Instances[0], "i3", n3, "fixed", w2.UID)
	i4 := poolsInstance(snap.Instances[0], "i4", n4, "fixed", w2.UID)
	for _, i := range []SchedulerInstance{i2, i3, i4} {
		snap.Instances = append(snap.Instances, i)
		snap.Options = append(snap.Options, poolsOption(i))
		snap.Costs = append(snap.Costs, poolsCost(schedulerActionRemove, i.Node, schedulerState(i), "unloaded", "fixed"))
	}
	snap.Costs = append(snap.Costs, poolsCost(schedulerActionRemove, snap.Instances[0].Node, schedulerState(snap.Instances[0]), "unloaded", "fixed"))
	snap.Routing = map[string]map[string]int32{w1.UID: {"i1": 500, "i2": 500}, w2.UID: {"i3": 500, "i4": 500}}
	capacityLoad(s, &snap, w1, 120)
	capacityLoad(s, &snap, w2, 10)
	recent := snap.ObservationEnd.Add(-s.ControlInterval)
	snap.Arrivals = slices.DeleteFunc(snap.Arrivals, func(a SchedulerArrival) bool { return a.WorkloadUID == w1.UID && a.At.Before(recent) })
	got := capacityDecideForTest(t, s, snap)
	if got.Target == nil || len(got.Target.Routing[w1.UID]) != 2 ||
		!poolsOn(*got.Target, snap.Nodes[0].Node) || !poolsOn(*got.Target, n2) ||
		len(got.Target.Routing[w2.UID]) != 1 {
		t.Fatalf("current demand must protect w1 and allow w2 to shrink: %s %+v", got.Status, got.Target)
	}
	// A lower current demand permits the same existing capacity to shrink.
	capacityLoad(s, &snap, w1, 20)
	got = capacityDecideForTest(t, s, snap)
	if got.Target == nil || len(got.Target.Routing[w1.UID]) != 1 {
		t.Fatalf("steady demand remained frozen: %s %+v", got.Status, got.Target)
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
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod, s.CompletionMarginMS = time.Minute, 10*time.Second, 10*time.Minute, 29
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
	capacityServiceRatesForTest(&snap, map[string]float64{lut.Profile.ID: 25.69, q2.Profile.ID: 29.30, g3.Profile.ID: 70.94})
	got := capacityDecideForTest(t, s, snap)
	if got.Target == nil {
		t.Fatalf("no target: %s %v", got.Status, got.Reasons)
	}
	for _, a := range got.Target.Actions {
		if a.Kind == schedulerActionPrepare && !schedulerInstanceRouted(got.Target.Routing, a.Instance) {
			t.Fatalf("prepared %s at weight 0: routing %v", a.Instance, got.Target.Routing)
		}
	}
	if got.Status != schedulerStatusFeasible {
		t.Fatalf("profile-proportional routing must be feasible: %s %v", got.Status, got.Reasons)
	}
}

// The historical replacement stopped routed q2 before a 68 s prepare. A short
// Ready-only bridge cannot serve this finite cohort within its SLO. Every stop
// must follow the route that hands its work off, including intermediate routes.
func TestCapacityStopsRoutedInstancesOnlyAfterTheRoute(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod, s.CompletionMarginMS = time.Minute, 10*time.Second, 3*time.Second, 29
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
	snap.Nodes[len(snap.Nodes)-1].MemoryBytes = q2.Allocation.MemoryBytes
	snap.Options = append(snap.Options, poolsOption(q1))
	snap.Routing = map[string]map[string]int32{"w1": {rasp5.ID: 468, q2.ID: 532}}
	snap.Costs = []OperationCost{
		poolsCost(schedulerActionRemove, genesis3, schedulerState(q2), "unloaded", "fixed"),
		poolsCost(schedulerActionPrepare, genesis3, "unloaded", schedulerState(q1), "fixed"),
	}
	snap.Costs[1].Trials[0].Duration = 68 * time.Second
	capacityLoad(s, &snap, d, 38.6)
	capacityServiceRatesForTest(&snap, map[string]float64{rasp5.Profile.ID: 25.69, q2.Profile.ID: 70.94, q1.Profile.ID: 65.1})
	got := capacityDecideForTest(t, s, snap)
	if got.Target == nil {
		t.Fatalf("no target: %s %v", got.Status, got.Reasons)
	}
	for _, a := range got.Target.Actions {
		if a.Kind == schedulerActionStop && schedulerInstanceRouted(snap.Routing, a.Instance) && !slices.ContainsFunc(got.Target.Actions, func(route Action) bool {
			handoff := got.Target
			if route.RouteTarget != nil {
				handoff = route.RouteTarget
			}
			return route.Kind == schedulerActionRoute && slices.Contains(a.DependsOn, route.ID) &&
				!schedulerInstanceRouted(handoff.Routing, a.Instance)
		}) {
			t.Fatalf("stopped routed %s without its handoff route: %+v", a.Instance, got.Target.Actions)
		}
	}
	if !slices.ContainsFunc(poolsPlans(got, genesis3), func(p SchedulerNodePlan) bool {
		return p.Key == q1.Profile.ID+"/b1/w1" && len(p.RequiresBridge) > 0
	}) {
		t.Fatalf("replacement lost its routed-blocker condition: %+v", poolsPlans(got, genesis3))
	}
	if slices.ContainsFunc(got.Target.Instances, func(i SchedulerInstance) bool { return i.Profile.ID == q1.Profile.ID }) {
		t.Fatal("insufficient Ready bridge admitted the unsafe replacement")
	}
}

// The finite comparison horizon changes the arrival cohort, while preserving
// captured outstanding requests. Exact outcomes may therefore change the choice.
func TestCapacityComparisonPeriodBoundsFiniteDemandPopulation(t *testing.T) {
	settings, snapshot, _ := capacityE1Fixture()
	settings.ObservationWindow = time.Minute
	definition := snapshot.Workloads[0]
	capacityLoad(settings, &snapshot, definition, 40)
	for q := range 161 {
		snapshot.Outstanding = append(snapshot.Outstanding, SchedulerRequest{ID: fmt.Sprint("queued-", q), Definition: definition, ReceivedAt: snapshot.At, Phase: phasePending})
	}
	previous := 0
	for _, period := range []time.Duration{time.Minute, 10 * time.Minute} {
		settings.ComparisonPeriod = period
		decision := capacityDecideForTest(t, settings, snapshot)
		if decision.Target == nil || decision.Capacity == nil || decision.Capacity.Period != period || len(decision.Target.Feedback) != 1 {
			t.Fatalf("T=%v lost common finite comparison: %+v", period, decision)
		}
		population := decision.Target.Feedback[0].Population
		if population <= previous || population < 161 {
			t.Fatalf("T=%v did not preserve outstanding plus finite arrival population: %+v", period, decision.Target.Feedback)
		}
		previous = population
	}
}

// Exact infeasible ordering uses failure, finite-success excess, then potential.
func TestCapacityExactOutcomeTupleIgnoresFinalFlag(t *testing.T) {
	s, snap, _ := capacityE1Fixture()
	p := capacityPlannerForTest(t, s, snap)
	quantile, potential := float64(2*time.Second), .25
	worse := capacityResult{final: true, joules: 1, feedback: []FeedbackWorkloadScore{{UID: "w1", Population: 10, OnTime: 5, Rejected: 5, FailureFraction: .5, SuccessQuantileNanos: &quantile, SuccessExcess: 1, PotentialExcess: &potential}}}
	better := capacityResult{joules: 100, feedback: []FeedbackWorkloadScore{{UID: "w1", Population: 10, OnTime: 8, Rejected: 2, FailureFraction: .2, SuccessQuantileNanos: &quantile, SuccessExcess: 1, PotentialExcess: &potential}}}
	p.rankFeedback(&worse)
	p.rankFeedback(&better)
	worse.final = true
	if better.feasible || worse.feasible || !better.better(&worse) || worse.better(&better) {
		t.Fatalf("Final flag overrode failure tuple: better=%+v worse=%+v", better, worse)
	}
	worse.feedback[0].FailureFraction = .2
	worse.feedback[0].OnTime, worse.feedback[0].Rejected = 8, 2
	lowerQuantile := float64(1500 * time.Millisecond)
	better.feedback[0].SuccessQuantileNanos = &lowerQuantile
	better.feedback[0].SuccessExcess = .5
	p.rankFeedback(&worse)
	p.rankFeedback(&better)
	if !better.better(&worse) {
		t.Fatal("finite success excess did not break failure tie")
	}
	worse.feedback[0].SuccessQuantileNanos = &lowerQuantile
	worse.feedback[0].SuccessExcess = .5
	largerPotential := .75
	worse.feedback[0].PotentialExcess = &largerPotential
	p.rankFeedback(&worse)
	if !better.better(&worse) {
		t.Fatal("conditional potential did not break outcome tie")
	}
}

// A stopping mode change loses retained profile rate; free-CPU replacement retains it.
func TestCapacityRetainedRoutingRateFollowsTheChain(t *testing.T) {
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
	capacityServiceRatesForTest(&snap, map[string]float64{current.Profile.ID: 50})
	d := capacityDecideForTest(t, s, snap)
	retained := map[string]float64{}
	for _, plan := range poolsPlans(d, n1) {
		if plan.Eligible {
			retained[plan.Key] = plan.RetainedRoutingRate["w1"]
		}
	}
	if k, ok := retained[eco.Profile.ID+"/b1/w1"]; !ok || k != 0 {
		t.Fatalf("mode change retained κ: %v", retained)
	}
	if k, ok := retained[other.Profile.ID+"/b1/w1"]; !ok || k != 50 {
		t.Fatalf("replacement removed after the route lost keep κ: %v", retained)
	}
}

// A stopping mode change retains no live worker during its measured outage.
func TestCapacitySingleNodeStoppingModeRaiseJudgesActualOutage(t *testing.T) {
	for _, duration := range []time.Duration{time.Millisecond, 100 * time.Millisecond} {
		t.Run(duration.String(), func(t *testing.T) {
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
			for i := range snap.Costs {
				snap.Costs[i].Trials[0].Duration = duration
			}
			capacityLoad(s, &snap, snap.Workloads[0], 30)
			capacityServiceRatesForTest(&snap, map[string]float64{current.Profile.ID: 10, raised.Profile.ID: 50})
			p := capacityPlannerForTest(t, s, snap)
			choice := capacityChoiceForTest(t, p, map[string]string{n1.UID: raised.Profile.ID + "/b1/w1"})
			route := p.plans[0][choice[0]].record.Pre
			replacement := p.evaluate(choice, route.Seconds())
			if route != 3*duration || replacement.sums[0][1] != 0 {
				t.Fatalf("stopping replacement retained nonexistent supply: route=%v sums=%v", route, replacement.sums)
			}
			if len(replacement.feedback) != 1 || replacement.feedback[0].Population == 0 {
				t.Fatalf("outage lost finite demand population: %+v", replacement)
			}
			score := replacement.feedback[0]
			if score.SuccessQuantileNanos != nil && !schedulerFinite(*score.SuccessQuantileNanos) {
				t.Fatalf("outage produced nonfinite success latency: %+v", score)
			}
			if replacement.exclusion == "" && !p.admits(&replacement) {
				t.Fatalf("physical target rejected merely for SLO infeasibility: %+v", replacement)
			}
		})
	}
}

// Infeasible direct outcomes rank before energy.
func TestCapacityOverloadOrdersByWorstExceedance(t *testing.T) {
	s, snap := schedulerFixture()
	s.MaxReplicas = 3
	s.ComparisonPeriod = time.Second // The finite cohort must still overload all available workers.
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
	capacityServiceRatesForTest(&snap, map[string]float64{a.Profile.ID: 150, b.Profile.ID: 250})
	d := capacityDecideForTest(t, s, snap)
	if d.Status != SchedulerStatusOverload || d.Target == nil || !poolsOn(*d.Target, small) || !poolsOn(*d.Target, large) ||
		!slices.Contains(d.Reasons, schedulerNoPlanMeetsSLO) {
		t.Fatalf("overload must retain the best exact finite-demand recovery: %+v", d)
	}
}

// Each Workload uses its own SLO and current frozen path feedback.
func TestCapacityJudgesEachWorkloadWithItsSLO(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ComparisonPeriod, s.CompletionMarginMS = time.Second, time.Minute, 100
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
	p := capacityPlannerForTest(t, s, snap)
	both := p.evaluate(capacityChoiceForTest(t, p, map[string]string{"n3": "profile@n3-fixed/b1/w1", "n4": "profile@n4-fixed/b1/w2"}), 10)
	if len(both.feedback) != 2 {
		t.Fatalf("missing workload outcomes: %+v", both)
	}
	for _, score := range both.feedback {
		definition := relaxed
		if score.UID == tight.UID {
			definition = tight
		}
		if score.SuccessQuantileNanos == nil || !schedulerFinite(*score.SuccessQuantileNanos) {
			t.Fatalf("%s lost finite success quantile: %+v", score.UID, score)
		}
		want := score.FailureFraction == 0 && *score.SuccessQuantileNanos <= float64(definition.SLO.TargetLatency)
		if score.Satisfied != want {
			t.Fatalf("%s judged against another SLO: %+v", score.UID, score)
		}
	}

}

// A reservation fixes its own scope, not the capacity planner.
func TestCapacityReservationKeepsScopeWithoutGlobalBarrier(t *testing.T) {
	s, snap := schedulerFixture()
	goal := SchedulerCandidate{ID: "goal", Nodes: snap.Nodes, Instances: snap.Instances, Routing: map[string]map[string]int32{"w1": {"i1": 1}}}
	reserveTargetForTest(&snap, &goal)
	d := capacityDecideForTest(t, s, snap)
	if d.Target == nil || !d.Target.Keep || d.Target.ID == goal.ID || len(d.Workloads) == 0 || d.Workloads[0].State != "Fixed" {
		t.Fatalf("reservation became a global continuation: %+v", d)
	}
	if d.Target.ExpectedJoules != nil {
		t.Fatal("fixed scope was presented as a verified whole estimate")
	}
}

func TestCapacityUncreatedReservationConsumesGlobalLimitAC39(t *testing.T) {
	for _, physical := range []string{"absent", "exact", "slot", "pod UID"} {
		t.Run(physical, func(t *testing.T) {
			s, snap := schedulerFixture()
			wanted := snap.Instances[0]
			wanted.PodUID = "reserved-pod"
			goal := SchedulerCandidate{ID: "reserved-goal", Instances: []SchedulerInstance{wanted},
				Routing: map[string]map[string]int32{wanted.WorkloadUID: {wanted.ID: 1000}}}
			snap.Instances, snap.Routing = nil, nil
			reserveTargetForTest(&snap, &goal)
			// Repeated views of an owner's frozen intent must not count twice.
			snap.Reservations = append(snap.Reservations, snap.Reservations[0])
			if physical != "absent" {
				actual := wanted
				switch physical {
				case "slot":
					actual.ID, actual.TargetID, actual.PodUID = "reserved-pod/container", wanted.ID, ""
				case "pod UID":
					actual.ID = "reserved-pod/container"
				}
				snap.Instances = []SchedulerInstance{actual}
			}
			w2 := *snap.Workloads[0]
			w2.Name, w2.UID = "independent", "w2"
			snap.Workloads = append(snap.Workloads, &w2)
			s.AutonomousWorkloads = append(s.AutonomousWorkloads, w2.UID)
			s.MinimumDemand = map[string]float64{w2.UID: 10}
			node := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
			next := poolsInstance(wanted, "independent-slot", node, "fixed", w2.UID)
			next.PodUID = ""
			snap.Options = append(snap.Options, poolsOption(next))
			snap.Costs = append(snap.Costs, poolsCost(schedulerActionPrepare, node, "unloaded", schedulerState(next), "fixed"))
			for _, limit := range []int{1, 2} {
				s.MaxInstances = limit
				decision := capacityDecideForTest(t, s, snap)
				expanded := decision.Target != nil && poolsOn(*decision.Target, node)
				if expanded != (limit == 2) {
					t.Fatalf("%s limit=%d expanded=%v reasons=%v target=%+v", physical, limit, expanded, decision.Reasons, decision.Target)
				}
			}
		})
	}
}

// Routing control off preserves integer weights even above optimistic profile support.
func TestCapacityRoutingOffKeepsWeightsWithoutRateVeto(t *testing.T) {
	items := []capacityItem{{id: "a", routingWeight: 20, weight: 1}, {id: "b", routingWeight: 100, weight: 3}}
	weights, offered, ok := capacityFixedWeights(100, items)
	if !ok || !slices.Equal(weights, []int32{1, 3}) || !slices.Equal(offered, []float64{25, 75}) {
		t.Fatalf("fixed route changed or vetoed overload: weights=%v rates=%v ok=%v", weights, offered, ok)
	}
	s, snap := schedulerFixture()
	s.Controls.Routing = false
	snap.Routing = map[string]map[string]int32{"w1": {"i1": 7}}
	d := capacityDecideForTest(t, s, snap)
	if d.Target != nil && d.Target.Routing["w1"]["i1"] != 7 {
		t.Fatalf("routing off launched new weights: %+v", d.Target.Routing)
	}
}

func TestCapacityUnconfirmedInstanceDoesNotFreezeWorkload(t *testing.T) {
	for _, routing := range []bool{true, false} {
		t.Run(fmt.Sprint("routing=", routing), func(t *testing.T) {
			s, snap := schedulerFixture()
			s.Controls.Routing = routing
			bad := snap.Instances[0]
			bad.ID, bad.Node, bad.Ready = "unconfirmed", poolsAddNode(&snap, "n2", map[string]float64{"fixed": 1}), false
			bad.Reasons = []string{"probe timed out"}
			bad.ObservedAt = time.Time{} // No successful native observation is usable capacity.
			bad.OwnerWorkloadUID = bad.WorkloadUID
			snap.Instances = append(snap.Instances, bad)
			snap.Nodes[1].Reasons = []string{NodeInstanceUnavailable}
			snap.Routing = map[string]map[string]int32{"w1": {"i1": 1, bad.ID: 1}}
			capacityLoad(s, &snap, snap.Workloads[0], 10)
			d := capacityDecideForTest(t, s, snap)
			if d.Workloads[0].State != WorkloadControlled {
				t.Fatalf("one unconfirmed instance froze autonomous control: %+v", d.Workloads)
			}
			if d.Target == nil || d.Target.Routing["w1"][bad.ID] != 0 || d.Target.Routing["w1"]["i1"] <= 0 {
				t.Fatalf("unconfirmed capacity remains routed: %+v", d.Target)
			}
			if len(d.Capacity.Workloads) != 1 || d.Capacity.Workloads[0].RoutingRate != 100 {
				t.Fatalf("unconfirmed capacity counted as serving: %+v", d.Capacity.Workloads)
			}
			schedulerRequireReason(t, d.Reasons, "UnconfirmedInstance:"+bad.ID+":"+bad.Reasons[0])
		})
	}
}

// Offline row lifecycle is not a scheduler input.
func TestCapacityOfflinePendingRowsDoNotGateScheduler(t *testing.T) {
	s, snap := schedulerFixture()
	offline := []CapacityRow{{Status: CapacityPending}}
	before := capacityDecideForTest(t, s, snap)
	offline[0].Status = CapacityRejected
	after := capacityDecideForTest(t, s, snap)
	if before.Target == nil || after.Target == nil || before.Target.ID != after.Target.ID || before.Workloads[0].State != WorkloadControlled || slices.Contains(before.Reasons, CapacityPending+":w1") {
		t.Fatalf("unrelated offline rows gated scheduling: before=%+v after=%+v rows=%+v", before, after, offline)
	}
}

// capacityTestSettings bound synthetic planners loosely, replicas ON.
var capacityTestSettings = SchedulerSettings{ComparisonPeriod: time.Minute, MaxReplicas: 100, MaxInstances: 100, PlanSearchWorkers: 2, PlanSearchStrategy: "v1", PlanSearchEvaluations: 500000, PlanExactFinalists: 16, PlanPolishActive: 4, Controls: SchedulerControls{Replicas: true, Routing: true}}

// capacitySearchForTest runs the plan search without a time budget.
func capacitySearchForTest(t testing.TB, p *capacityPlanner) capacityResult {
	t.Helper()
	best, err := p.search(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	assertCapacitySearchSteps(t, p.searched, len(p.plans))
	return best
}

func assertCapacitySearchSteps(t testing.TB, search SchedulerPlanSearch, nodes int) {
	t.Helper()
	v2 := search.Strategy == protocol.V2
	wantEvaluated := search.Evaluated
	if v2 {
		wantEvaluated = search.SurrogateEvaluated
	}
	evaluated, duration, k := search.TrailingEvaluated, time.Duration(0), 0
	last := map[string]SchedulerPlanSearchStep{}
	for i, step := range search.Steps {
		evaluated += step.Evaluated
		duration += step.Duration
		if !v2 && step.K < k || step.Duration < 0 || step.BudgetCut != (search.BudgetCut && i == len(search.Steps)-1) {
			t.Fatalf("step order, time or cut flag: %+v", search)
		}
		k = step.K
		if previous, ok := last[step.Start]; ok {
			assertCapacitySearchMove(t, step, previous, v2, nodes)
		} else if step.K != 0 || step.Evaluated != 0 || len(step.ChangedNodes) != 0 || step.Phase != "" {
			t.Fatalf("missing origin (excluded from aggregate evaluation count): %+v", step)
		}
		last[step.Start] = step
	}
	if len(last) == 0 || evaluated != wantEvaluated || search.TrailingEvaluated < 0 || duration > search.Duration {
		t.Fatalf("step accounting: evaluated=%d duration=%s search=%+v", evaluated, duration, search)
	}
	wantReason := protocol.NoImprovement
	if search.BudgetCut {
		wantReason = protocol.BudgetCut
		if v2 {
			wantReason = protocol.Deadline
		}
	} else if v2 && search.TerminationReason == protocol.EvaluationBudget {
		wantReason = protocol.EvaluationBudget
	}
	if search.TerminationReason != wantReason {
		t.Fatalf("termination: got %q want %q", search.TerminationReason, wantReason)
	}
}

func assertCapacitySearchMove(t testing.TB, step, previous SchedulerPlanSearchStep, v2 bool, nodes int) {
	t.Helper()
	minK, maxK := 2, 3
	if v2 {
		minK, maxK = 1, nodes
	}
	if step.K < minK || step.K > maxK || len(step.ChangedNodes) == 0 || len(step.ChangedNodes) > step.K ||
		v2 && len(step.ChangedNodes) != step.K || step.Evaluated <= 0 {
		t.Fatalf("invalid accepted move: %+v after %+v", step, previous)
	}
	if v2 && (step.Phase != protocol.Cover && step.Phase != protocol.Polish && step.Phase != protocol.Combine ||
		step.Phase == protocol.Cover && previous.Phase != "" && previous.Phase != protocol.Cover) {
		t.Fatalf("invalid v2 phase: %+v after %+v", step, previous)
	}
	// Polish/combine follow better's priority order. Cover can lose
	// feasibility or increase energy while recovering count limits.
	if (!v2 || step.Phase != protocol.Cover) && (previous.Feasible && !step.Feasible ||
		previous.Feasible && step.Feasible && step.Joules-step.KeepJoules > previous.Joules-previous.KeepJoules) {
		t.Fatalf("accepted point worsened: %+v after %+v", step, previous)
	}
}

// capacityRawPlannerForTest gives hand-built search plans real replay inputs.
// Scalar routing terms remain distinct from exact demand outcomes.
func capacityRawPlannerForTest(p *capacityPlanner) {
	settings, base := schedulerFixture()
	if p.settings.ObservationWindow <= 0 {
		p.settings.ObservationWindow = time.Second
	}
	if p.settings.ControlInterval <= 0 {
		p.settings.ControlInterval = time.Second
	}
	p.settings.CapacityWindow = settings.CapacityWindow
	p.settings.CapacityWindows = settings.CapacityWindows
	p.settings.CapacityPassingWindows = settings.CapacityPassingWindows
	p.settings.CapacityMinimumRequests = settings.CapacityMinimumRequests
	p.settings.DefaultOperationCost = settings.DefaultOperationCost
	p.settings.DefaultIdlePower = settings.DefaultIdlePower
	p.snapshot.At, p.snapshot.ObservationEnd = base.At, base.At
	p.snapshot.ObservedSince = base.At.Add(-time.Hour)
	p.snapshot.Workloads, p.snapshot.Options, p.snapshot.Instances = nil, nil, nil
	p.snapshot.Baselines = nil
	p.settings.AutonomousWorkloads = nil
	p.feedbackCache = sync.Map{}
	p.feedbackCohort = capacityArrivalMemo{}
	p.snapshot.Arrivals, p.snapshot.Outstanding = nil, nil
	p.snapshot.PathFeedback = nil
	p.index = map[string]int{}
	p.keep = SchedulerCandidate{ID: "keep", Keep: true, Routing: map[string]map[string]int32{}}
	for w := range p.ws {
		ws := &p.ws[w]
		definition := *base.Workloads[0]
		definition.UID = ws.d.UID
		if ws.d.SLO.TargetLatency > 0 {
			definition.SLO.TargetLatency = ws.d.SLO.TargetLatency
		}
		ws.d = &definition
		p.index[definition.UID] = w
		p.snapshot.Workloads = append(p.snapshot.Workloads, ws.d)
		p.settings.AutonomousWorkloads = append(p.settings.AutonomousWorkloads, definition.UID)
		capacityLoad(p.settings, &p.snapshot, ws.d, ws.rate)
		for q := range ws.queued {
			p.snapshot.Outstanding = append(p.snapshot.Outstanding, SchedulerRequest{ID: fmt.Sprintf("%s-q%d", definition.UID, q), Definition: ws.d, ReceivedAt: base.At, Phase: phasePending})
		}
	}
	for n := range p.plans {
		node := base.Nodes[0]
		node.Node = p.snapshot.Nodes[n].Node
		if node.Node.Name == "" {
			node.Node.Name = node.Node.UID
		}
		node.MemoryBytes = 1 << 30
		p.snapshot.Nodes[n] = node
		baseline := base.Baselines[0]
		baseline.ID, baseline.Node, baseline.Meter = "unloaded-"+node.Node.UID, node.Node, "wall-"+node.Node.UID
		p.snapshot.Baselines = append(p.snapshot.Baselines, baseline)
		for j, plan := range p.plans[n] {
			plan.mode = node.PowerMode
			plan.instances = nil
			for w, items := range plan.items {
				for k := range items {
					item := &plan.items[w][k]
					if item.routingWeight <= 0 {
						continue
					}
					instance := poolsInstance(base.Instances[0], item.id, node.Node, node.PowerMode, p.ws[w].d.UID)
					instance.Profile.ID += "-" + item.id
					metadata := *instance.Profile.RuntimeMetadata
					instance.Profile.RuntimeMetadata = &metadata
					instance.Profile.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1, EnergyPerInputJoules: item.e, MemoryBytes: 100}, []float64{float64(time.Second) / float64(item.routingWeight)})}
					item.serviceNanos = float64(time.Second) / float64(item.routingWeight)
					instance.Weight = item.weight
					if instance.Weight <= 0 {
						instance.Weight = 1
					}
					instance.Profile.RuntimeMetadata.CPUAffinity = []int{w*16 + k}
					plan.instances = append(plan.instances, instance)
					p.snapshot.Options = append(p.snapshot.Options, poolsOption(instance))
					if j == 0 {
						p.snapshot.Instances = append(p.snapshot.Instances, instance)
					}
				}
			}
			p.sum(plan)
		}
	}
	p.keep.Nodes = slices.Clone(p.snapshot.Nodes)
	p.keep.Instances = slices.Clone(p.snapshot.Instances)
	p.snapshot.Routing = schedulerInstanceRouting(p.ctx, p.snapshot.Workloads, p.snapshot.Instances)
	p.keep.Routing = p.snapshot.Routing
	capacityFeedbackForTest(p.settings, &p.snapshot)
	p.keepWatts = p.evaluateEnergyForRoutes(make([]int, len(p.plans)), 0, nil, 1, p.keep.Routing).joules
}

// capacityRandomPlanner builds a planner over synthetic node plans.
func capacityRandomPlanner(rng *rand.Rand, workloads, nodes int) *capacityPlanner {
	p := &capacityPlanner{ctx: context.Background(), settings: capacityTestSettings}
	// Keep all 300 exhaustive plan spaces, with a one-second finite cohort.
	p.settings.ComparisonPeriod = time.Second
	for w := range workloads {
		rate := 0.0
		if rng.IntN(4) > 0 {
			rate = 10 + 90*rng.Float64()
		}
		ws := capacityWorkload{d: &Definition{UID: fmt.Sprint("w", w), SLO: SLO{TargetLatency: time.Duration((0.2 + 2*rng.Float64()) * float64(time.Second))}}, rate: rate, queued: rng.IntN(6)}
		p.ws = append(p.ws, ws)
	}
	for n := range nodes {
		p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{UID: fmt.Sprint("n", n)}})
		var plans []*capacityPlan
		for j := range 1 + rng.IntN(4) {
			plan := &capacityPlan{keep: j == 0, items: make([][]capacityItem, workloads), retained: make([][]capacityItem, workloads), before: 1 + 10*rng.Float64()}
			plan.record.Eligible, plan.record.Key = true, fmt.Sprint("plan-", j)
			for w := range workloads {
				for k := range rng.IntN(3) {
					item := capacityItem{id: fmt.Sprint(n, "-", j, "-", w, "-", k), routingWeight: profileRoutingWeight(60 * rng.Float64()), e: rng.Float64(), weight: 1}
					item.throughput = float64(item.routingWeight)
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
			}
			p.sum(plan)
			plans = append(plans, plan)
		}
		p.plans = append(p.plans, plans)
		p.compared = append(p.compared, len(plans) > 1)
	}
	p.periodSeconds = p.settings.ComparisonPeriod.Seconds()
	capacityRawPlannerForTest(p)
	return p
}

// capacityBruteForce evaluates every plan set within the instance bounds that
// gives no new instance weight 0, at its own route time, and returns the
// cheapest feasible one (nil when none is feasible).
func capacityBruteForce(p *capacityPlanner) *capacityResult {
	var best *capacityResult
	choice := make([]int, len(p.plans))
	for {
		if res := p.evaluate(slices.Clone(choice), p.routeOf(choice)); p.bounded(choice) && p.admits(&res) && res.feasible && res.better(best) {
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
// every plan set, so the search equals the exact finite-demand brute force.
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
	p := &capacityPlanner{ctx: context.Background(), periodSeconds: 60, settings: capacityTestSettings}
	p.ws = []capacityWorkload{{d: &Definition{UID: "w", SLO: SLO{TargetLatency: time.Second}}, rate: 29}}
	for n := range 3 {
		p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{UID: fmt.Sprint("n", n)}})
		item := capacityItem{id: fmt.Sprint("i", n), routingWeight: 10, throughput: 10, e: 1}
		keep := &capacityPlan{keep: true, items: [][]capacityItem{{item}}, retained: [][]capacityItem{{item}}, before: 10}
		other := &capacityPlan{items: [][]capacityItem{nil}, retained: [][]capacityItem{{item}}}
		if n == 0 {
			other.items, other.after = [][]capacityItem{{{id: "big", routingWeight: 30, throughput: 30, e: 1}}}, 25
		}
		keep.record.Eligible, other.record.Eligible, other.record.Key = true, true, "swap"
		p.sum(keep)
		p.sum(other)
		p.plans = append(p.plans, []*capacityPlan{keep, other})
		p.compared = append(p.compared, true)
	}
	capacityRawPlannerForTest(p)
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
	steps := p.searched.Steps
	assertCapacitySearchSteps(t, p.searched, len(p.plans))
	if !slices.ContainsFunc(steps, func(step SchedulerPlanSearchStep) bool {
		return step.K > 0 && step.Feasible && step.Joules == best.joules && step.Joules < keep.joules
	}) {
		t.Fatalf("search lost its accepted lower-energy result: %+v", steps)
	}
	record, err := json.Marshal(p.searched)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded search: %s", record)
}

// capacityTwinSwap saves gain J over T=60s and carries a deterministic
// operation bound. Profiles supply nominal energy only.
func capacityTwinSwap(gain, bound float64) *capacityPlanner {
	p := &capacityPlanner{ctx: context.Background(), periodSeconds: 60, settings: capacityTestSettings}
	p.ws = []capacityWorkload{{d: &Definition{UID: "w", SLO: SLO{TargetLatency: time.Second}}, rate: 10}}
	p.snapshot.Nodes = []SchedulerNode{{Node: NodeReference{UID: "n0"}}}
	a, b := SchedulerInstance{ID: "a"}, SchedulerInstance{ID: "b"}
	keep := &capacityPlan{keep: true, instances: []SchedulerInstance{a}, items: [][]capacityItem{{{id: "a", routingWeight: 50, throughput: 50, e: 1}}}, retained: [][]capacityItem{{{id: "a", routingWeight: 50, throughput: 50, e: 1}}}, before: 10}
	swap := &capacityPlan{instances: []SchedulerInstance{b}, items: [][]capacityItem{{{id: "b", routingWeight: 50, throughput: 50, e: 1}}}, retained: [][]capacityItem{{{id: "a", routingWeight: 50, throughput: 50, e: 1}}}, mid: 10, after: 10 - gain/60}
	source := schedulerBoundSource{reference: evidencestore.ObjectIdentity{Store: "raw", Key: "swap", Size: 1}, trial: 0}
	swap.operationBounds = schedulerEnergyBounds{source: bound}
	keep.record.Eligible, swap.record.Eligible, swap.record.Key = true, true, "swap"
	p.sum(keep)
	p.sum(swap)
	p.plans, p.compared = [][]*capacityPlan{{keep, swap}}, []bool{true}
	capacityRawPlannerForTest(p)
	return p
}

func TestCapacityTransitionNeedsGainBeyondDeterministicBound(t *testing.T) {
	for _, tc := range []struct {
		gain, bound float64
		keeps       bool
	}{{2.7, 0, false}, {2.7, 3, true}, {100, 100, true}, {100, 99, false}} {
		p := capacityTwinSwap(tc.gain, tc.bound)
		best := capacitySearchForTest(t, p)
		if best.keep || math.Abs(p.evaluate([]int{0}, 0).joules-best.joules-tc.gain) > 1e-6 {
			t.Fatalf("fixture must save %g J: %+v", tc.gain, best)
		}
		got := p.minimumGain(best)
		if got.keep != tc.keeps || got.withinUncertainty != tc.keeps || got.gainUncertainty != tc.bound {
			t.Fatalf("gain %g bound %g: %+v", tc.gain, tc.bound, got)
		}
	}
}

func capacityRecoverySwapForTest(gain, bound float64) *capacityPlanner {
	p := capacityTwinSwap(gain, bound)
	p.settings.ComparisonPeriod, p.periodSeconds = time.Second, 1
	p.plans[0][0].items[0][0].routingWeight = 1
	p.plans[0][0].items[0][0].throughput = 1
	capacityRawPlannerForTest(p)
	old, next := p.snapshot.Instances[0], p.plans[0][1].instances[0]
	mode := p.snapshot.Nodes[0].PowerMode
	// Whole-wall rows include the focal model and its co-resident replica.
	p.snapshot.Costs = []OperationCost{
		poolsCost(schedulerActionPrepare, next.Node, "unloaded", schedulerState(next), mode, schedulerState(old), schedulerState(next)),
		poolsCost(schedulerActionRemove, old.Node, schedulerState(old), "unloaded", mode, schedulerState(old), schedulerState(next)),
	}
	for k := range p.snapshot.Arrivals {
		p.snapshot.Arrivals[k].At = p.snapshot.Arrivals[k].At.Add(50 * time.Millisecond)
	}
	return p
}

func TestCapacityMinimumGainDoesNotBlockRecoveryFromInfeasibleKeep(t *testing.T) {
	p := capacityRecoverySwapForTest(2.7, 1e6)
	best := p.evaluate([]int{1}, 0)
	keep := p.evaluate([]int{0}, 0)
	if !best.feasible || keep.feasible {
		t.Fatalf("fixture requires feasible target and infeasible keep: target=%+v keep=%+v", best, keep)
	}
	if got := p.minimumGain(best); got.keep || !got.feasible || got.withinUncertainty || got.gainUncertainty != 0 {
		t.Fatalf("deterministic bound blocked capacity recovery: %+v", got)
	}
}

// The real immutable-cost -> node chain -> minimumGain flow must not turn B
// into nominal energy or pretend its deterministic error is a statistical CI.
func TestCapacityOperationUncertaintyChangesMinimumGain(t *testing.T) {
	var nominal float64
	for _, bound := range []float64{0, 1e6} {
		s, snap, node, _ := capacityR5Fixture()
		snap.Instances[1].Profile.IdleBaselineWatts = 60 // removing saves about 10 W
		for i := range snap.Costs {
			snap.Costs[i].Trials[0].EnergyUncertaintyJoules = bound
			// A mean of two equally uncertain trials still has bound B, not B/sqrt(2).
			snap.Costs[i].Trials = append(snap.Costs[i].Trials, snap.Costs[i].Trials[0])
		}
		p := capacityPlannerForTest(t, s, snap)
		choice := make([]int, len(p.plans))
		n := slices.IndexFunc(p.snapshot.Nodes, func(n SchedulerNode) bool { return n.Node == node })
		choice[n] = slices.IndexFunc(p.plans[n], func(plan *capacityPlan) bool {
			return !plan.keep && plan.record.Eligible && len(plan.instances) == 0
		})
		if choice[n] < 0 {
			t.Fatal("missing measured removal plan")
		}
		best := p.evaluate(choice, p.routeOf(choice))
		if !best.feasible || best.keep {
			t.Fatalf("fixture must have a feasible changed candidate: %+v", best)
		}
		if bound == 0 {
			nominal = best.joules
		} else if best.joules != nominal {
			t.Fatalf("B changed nominal energy: %g != %g", best.joules, nominal)
		}
		if got := p.plans[n][choice[n]].record.EnergyUncertaintyJoules; got != bound {
			t.Fatalf("chain lost immutable trial B: %g != %g", got, bound)
		}
		got := p.minimumGain(best)
		if got.keep != (bound > 0) || got.withinUncertainty != (bound > 0) || got.gainUncertainty != bound {
			t.Fatalf("same nominal, B=%g: keep=%v threshold=%g", bound, got.keep, got.gainUncertainty)
		}
	}
}

func TestCapacityMinimumGainExactBaselineIgnoresProfileCI(t *testing.T) {
	s, snap, node, _ := capacityR5Fixture()
	baseline := slices.IndexFunc(snap.Baselines, func(b NodeBaseline) bool { return b.Node == node })
	if baseline < 0 {
		t.Fatal("missing unloaded baseline")
	}
	loaded := snap.Baselines[baseline]
	loaded.ID, loaded.State = "exact-loaded", []string{schedulerState(snap.Instances[1])}
	loaded.Watts, loaded.WattsUncertainty = 60, 0.001
	loaded.Reference = evidencestore.ObjectIdentity{Store: "raw", Key: "exact-loaded", Size: 1}
	snap.Baselines = append(snap.Baselines, loaded)
	var nominalKeep, nominalBest, threshold float64
	for trial, confidence := range []float64{0.5, 0.999999} {
		// Profile CI confidence and its unselected fallback mean are irrelevant
		// when the exact loaded-state measurement supplies nominal idle power.
		snap.Instances[1].Profile.ConfidenceLevel = confidence
		snap.Instances[1].Profile.IdleBaselineWatts = 1 + float64(trial)*1e6
		p := capacityPlannerForTest(t, s, snap)
		n := slices.IndexFunc(p.snapshot.Nodes, func(n SchedulerNode) bool { return n.Node == node })
		watts, _, sources, _ := schedulerBaseline(schedulerEstimateSettingsForTest(), p.snapshot.Baselines, p.snapshot.Nodes[n], []SchedulerInstance{snap.Instances[1]}, p.snapshot.Options)
		if watts != loaded.Watts || !slices.Contains(sources.References, loaded.ID) {
			t.Fatalf("fixture did not select exact loaded evidence: %g %v", watts, sources)
		}
		choice := make([]int, len(p.plans))
		choice[n] = slices.IndexFunc(p.plans[n], func(plan *capacityPlan) bool {
			return !plan.keep && plan.record.Eligible && len(plan.instances) == 0
		})
		if choice[n] < 0 {
			t.Fatal("missing measured removal plan")
		}
		best := p.evaluate(choice, p.routeOf(choice))
		keep := p.evaluate(make([]int, len(p.plans)), 0)
		got := p.minimumGain(best)
		if !best.feasible || got.keep || got.withinUncertainty {
			t.Fatalf("unselected profile statistics blocked measured gain: %+v", got)
		}
		if trial == 0 {
			nominalKeep, nominalBest, threshold = keep.joules, best.joules, got.gainUncertainty
		} else if keep.joules != nominalKeep || best.joules != nominalBest || got.gainUncertainty != threshold {
			t.Fatalf("unselected profile CI changed nominal energy or B: keep=%g best=%g B=%g", keep.joules, best.joules, got.gainUncertainty)
		}
	}
}

func TestCapacityDeterministicBoundsComposeAndCancelSharedEvidence(t *testing.T) {
	source := schedulerBoundSource{reference: evidencestore.ObjectIdentity{Store: "raw", Key: "operation", Size: 1}, trial: 0}
	plan := &capacityPlan{operationBounds: schedulerEnergyBounds{source: 3}}
	bounds := schedulerEnergyBounds{}
	// Two actions reusing one measured trial have 6 J, not sqrt(2)*3 J.
	plan.addBounds(bounds, 0, 60, 1)
	plan.addBounds(bounds, 0, 60, 1)
	if got := bounds.halfWidth(); got != 6 {
		t.Fatalf("repeated evidence was treated as independent: %g", got)
	}
	// A shared action in the compared candidate cancels exactly once.
	plan.addBounds(bounds, 0, 60, -1)
	if got := bounds.halfWidth(); got != 3 {
		t.Fatalf("shared evidence did not cancel: %g", got)
	}
	plan.addBounds(bounds, 0, 60, -1)
	if got := bounds.halfWidth(); got != 0 {
		t.Fatalf("identical candidates retained uncertainty: %g", got)
	}
	distinct := source
	distinct.reference.Key = "other-operation"
	bounds[source], bounds[distinct] = 3, -4
	if got := bounds.halfWidth(); got != 7 {
		t.Fatalf("distinct deterministic errors must sum absolute bounds: %g", got)
	}
}

func TestCapacityBaselineUncertaintyPreservesSignedReconstruction(t *testing.T) {
	_, snap := schedulerFixture()
	snap.Baselines[0].WattsUncertainty = 2
	snap.Instances[0].Profile.IdleBaselineWatts = 8 // negative loaded-minus-unloaded delta
	instances := []SchedulerInstance{}
	for count := 0; count <= 2; count++ {
		watts, _, sources, _ := schedulerBaseline(schedulerEstimateSettingsForTest(), snap.Baselines, snap.Nodes[0], instances, snap.Options)
		bound := schedulerBaselineUncertainty(snap.Baselines, sources, count)
		if watts != 10-2*float64(count) || bound.watts != 2*(1-float64(count)) {
			t.Fatalf("%d instances: nominal %g bound %+v", count, watts, bound)
		}
		instances = append(instances, snap.Instances[0])
	}
	loaded := snap.Baselines[0]
	loaded.ID, loaded.State, loaded.Watts, loaded.WattsUncertainty = "loaded", []string{schedulerState(snap.Instances[0])}, 8, 3
	loaded.Reference.Key = "loaded-idle"
	snap.Baselines = append(snap.Baselines, loaded)
	_, _, sources, _ := schedulerBaseline(schedulerEstimateSettingsForTest(), snap.Baselines, snap.Nodes[0], snap.Instances, snap.Options)
	if got := schedulerBaselineUncertainty(snap.Baselines, sources, 1); got.watts != 3 || got.reference != loaded.Reference {
		t.Fatalf("exact loaded-state evidence must replace, not add unloaded uncertainty: %+v", got)
	}
	// Signed operation delta adds the SAME stored baseline back exactly once.
	base := 10.0
	cost := poolsCost(schedulerActionPrepare, snap.Nodes[0].Node, "unloaded", "loaded", "fixed")
	cost.EnergyScope, cost.SubtractedWatts = schedulerTargetBaselineDelta, &base
	cost.Trials = []OperationTrial{{Duration: time.Second, Joules: -4, EnergyUncertaintyJoules: 2}}
	_, _, joules, ok := capacityOperationCost(&cost)
	state := schedulerReplayState{rng: rand.New(rand.NewPCG(1, 0))}
	action := schedulerReplayAction{}
	if !ok || joules != 6 || !state.sampleOperation(&action, &cost) || math.Abs(action.wallRate*1e9-6) > 1e-12 || action.trial.EnergyUncertaintyJoules != 2 {
		t.Fatalf("signed wall reconstruction/B incoherent: %g %+v", joules, action)
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
	d := capacityDecideForTest(t, s, snap)
	if d.Target == nil || !slices.ContainsFunc(d.Target.Instances, func(i SchedulerInstance) bool { return i.ID == "i1" }) {
		t.Fatalf("replicas OFF removed the Workload's only instance: %+v %v", d.Target, d.Reasons)
	}
}

// §9 6 search: four Nodes serve λ 39.5 with κ 10 each at 10 W; Node 0 can
// instead hold κ 40 at 35 W. That saves 5 W only with all three others
// emptied, four changed Nodes, beyond k = 3 from keep. The second start, every
// Node emptied, reaches it in one step.
func TestCapacitySearchEmptyStartFindsFourNodeChange(t *testing.T) {
	p := &capacityPlanner{ctx: context.Background(), periodSeconds: 60, settings: capacityTestSettings}
	p.ws = []capacityWorkload{{d: &Definition{UID: "w", SLO: SLO{TargetLatency: time.Second}}, rate: 39.5}}
	for n := range 4 {
		p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{UID: fmt.Sprint("n", n)}})
		i := SchedulerInstance{ID: fmt.Sprint("i", n), WorkloadUID: "w"}
		item := capacityItem{id: i.ID, routingWeight: 10, throughput: 10, e: 1}
		keep := &capacityPlan{keep: true, instances: []SchedulerInstance{i}, items: [][]capacityItem{{item}}, retained: [][]capacityItem{{item}}, before: 10}
		empty := &capacityPlan{items: [][]capacityItem{nil}, retained: [][]capacityItem{{item}}}
		plans := []*capacityPlan{keep, empty}
		if n == 0 {
			big := SchedulerInstance{ID: "big", WorkloadUID: "w"}
			plans = append(plans, &capacityPlan{instances: []SchedulerInstance{big}, items: [][]capacityItem{{{id: "big", routingWeight: 40, throughput: 40, e: 1}}}, retained: [][]capacityItem{{item}}, after: 35})
		}
		for j, plan := range plans {
			plan.record.Eligible, plan.record.Key = true, fmt.Sprint("plan-", j)
			p.sum(plan)
		}
		p.plans = append(p.plans, plans)
		p.compared = append(p.compared, true)
	}
	capacityRawPlannerForTest(p)
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
	for _, step := range p.searched.Steps {
		if step.K != 0 || len(step.ChangedNodes) != 0 {
			t.Fatalf("spent budget recorded an unaccepted move: %+v", step)
		}
	}
	assertCapacitySearchSteps(t, p.searched, len(p.plans))
}

// Delay the first changed candidate's replay so it completes after the move
// budget. The next sweep must retain that accepted exact result.
type capacitySearchDeadlineContext struct {
	context.Context
	deadline   time.Time
	planner    *capacityPlanner
	changedKey string
	delayed    bool
}

func (c *capacitySearchDeadlineContext) Err() error {
	replaying := false
	c.planner.feedbackCache.Range(func(key, _ any) bool {
		replaying = key.(capacityReplayKey).plans == c.changedKey
		return !replaying
	})
	if replaying && !c.delayed {
		c.delayed = true
		time.Sleep(max(0, time.Until(c.deadline)) + time.Millisecond)
	}
	return nil
}

func TestCapacitySearchBudgetCutRetainsAcceptedSteps(t *testing.T) {
	p := capacityTwinSwap(100, 0)
	p.settings.PlanSearchWorkers = 1
	deadline := time.Now().Add(time.Second)
	changed := p.evaluateEnergyAtWindow([]int{1}, 0, nil, p.periodSeconds)
	p.ctx = &capacitySearchDeadlineContext{Context: context.Background(), deadline: deadline, planner: p, changedKey: changed.key}
	best, err := p.search(deadline)
	if err != nil || best.keep || !p.searched.BudgetCut {
		t.Fatalf("expected accepted move followed by budget cut: %+v %v %+v", best, err, p.searched)
	}
	steps := p.searched.Steps
	if len(steps) != 2 || steps[0].K != 0 || steps[1].K != 2 ||
		steps[1].Joules != best.joules ||
		!slices.Equal(steps[1].ChangedNodes, []NodeReference{p.snapshot.Nodes[0].Node}) {
		t.Fatalf("budget cut lost the accepted prefix: %+v", steps)
	}
	assertCapacitySearchSteps(t, p.searched, len(p.plans))
}

// §9 6 search: the decision records the search scope with the capacity judgment.
func TestCapacityDecisionRecordsSearch(t *testing.T) {
	s, snap, _ := capacityE1Fixture()
	planner := capacityPlannerForTest(t, s, snap)
	before := time.Now()
	best := capacitySearchForTest(t, planner)
	d := planner.decide(SchedulerDecision{Keep: planner.keep}, best)
	after := time.Now()
	if d.Capacity == nil || d.Capacity.Search == nil || d.Capacity.Search.Evaluated == 0 {
		t.Fatalf("decision search record: %+v", d.Capacity)
	}
	if len(d.Capacity.Search.Steps) == 0 {
		t.Fatal("decision lost live search steps")
	}
	search := d.Capacity.Search
	if search.StartedAt.Before(before) || search.FinishedAt.After(after) || search.FinishedAt.Before(search.StartedAt) {
		t.Fatalf("search capture escaped actual evaluation: before=%s search=[%s,%s] after=%s", before, search.StartedAt, search.FinishedAt, after)
	}
	// The recorded interval must enclose the real work, not two timestamps
	// taken together before or after the sweep.
	var swept time.Duration
	for _, step := range search.Steps {
		swept += step.Duration
	}
	if swept <= 0 || search.FinishedAt.Sub(search.StartedAt) < swept {
		t.Fatalf("search interval does not enclose measured sweep work: search=%+v work=%s", search, swept)
	}
}

func TestCapacitySearchStepRecordSize(t *testing.T) {
	// Record shape needs accepted multi-option moves, not the historical
	// ten-minute E5 exact-replay benchmark.
	p := capacityV2MultiOptionFixture()
	p.settings.PlanSearchStrategy = protocol.V2
	if _, err := p.search(time.Time{}); err != nil {
		t.Fatal(err)
	}
	assertCapacitySearchSteps(t, p.searched, len(p.plans))
	starts := map[string]bool{}
	for _, step := range p.searched.Steps {
		starts[step.Start] = true
	}
	if len(starts) != 2 {
		t.Fatalf("expected keep and empty trajectories: %+v", starts)
	}
	withSteps, err := json.Marshal(p.searched)
	if err != nil {
		t.Fatal(err)
	}
	without := p.searched
	without.Steps = nil
	without.TrailingEvaluated = 0
	without.TerminationReason = ""
	withoutSteps, err := json.Marshal(without)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(withoutSteps), `"steps"`) {
		t.Fatal("absent steps must preserve the old optional JSON shape")
	}
	t.Logf("finite-cohort decision: nodes=%d workloads=%d steps=%d searchBytes=%d bytesAdded=%d budgetCut=%t",
		len(p.plans), len(p.ws), len(p.searched.Steps), len(withSteps), len(withSteps)-len(withoutSteps), p.searched.BudgetCut)
}

func TestCapacitySearchStepsPreserveFeasibilityBeforeEnergy(t *testing.T) {
	p := capacityRecoverySwapForTest(-100, 0)
	best := capacitySearchForTest(t, p)
	steps := p.searched.Steps
	if len(steps) != 2 || steps[0].Feasible || !steps[1].Feasible || steps[1].Joules <= steps[0].Joules || !best.feasible {
		t.Fatalf("recovery must record the real higher-energy point: %+v", steps)
	}
}

// §9 6 weights: route weights sum to 1,000 and stay within κ after rounding.
func TestCapacityRouteWeightsUseProfileSupport(t *testing.T) {
	for _, row := range []struct {
		rate    float64
		support []float64
		want    []int32
		valid   bool
	}{
		{100, []float64{99.6, .4}, []int32{996, 4}, true},
		{100, []float64{34, 34, 34}, []int32{334, 333, 333}, true},
		{0, []float64{1, 2, 3}, []int32{167, 333, 500}, true},
		{1000, []float64{33.35, 33.35, 33.3}, []int32{334, 333, 333}, true},
		{100, []float64{.06, 100}, []int32{0, 1000}, true},
		{10, []float64{0, 10, 0}, []int32{0, 1000, 0}, true},
		{0, []float64{0, 0, 0}, nil, false},
	} {
		items := make([]capacityItem, len(row.support))
		for k, support := range row.support {
			items[k] = capacityItem{id: fmt.Sprint(k), routingWeight: profileRoutingWeight(support)}
		}
		weights, offered, ok := capacityWeights(row.rate, items)
		if ok != row.valid || row.valid && !slices.Equal(weights, row.want) {
			t.Fatalf("support=%v route=%v valid=%v", row.support, weights, ok)
		}
		total := int32(0)
		for k, weight := range weights {
			total += weight
			if offered[k] != row.rate*float64(weight)/schedulerRouteUnits {
				t.Fatalf("nominal share differs: %v", offered)
			}
		}
		if row.valid && total != schedulerRouteUnits {
			t.Fatalf("route units=%d", total)
		}
	}
}

func TestCapacityIntegerWeightsIgnoreRate(t *testing.T) {
	items := []capacityItem{{routingWeight: .06}, {routingWeight: 100}}
	for _, rate := range []float64{50, 100} {
		weights, _, ok := capacityWeights(rate, items)
		if !ok || !slices.Equal(weights, []int32{0, 1000}) {
			t.Fatalf("λ %g changed minimax route: %v ok=%t", rate, weights, ok)
		}
	}
}

// full-E5-3m-12: all destinations share the same profile-rate-normalized utilization.
func TestCapacityRouteWeights3m12(t *testing.T) {
	items := []capacityItem{
		{id: "nano", routingWeight: 10.191, e: 1},
		{id: "rasp5", routingWeight: 26.498, e: 2},
		{id: "jetsonx", routingWeight: 15.976, e: 3},
		{id: "orangepi", routingWeight: 36.859, e: 4},
	}
	for _, rate := range []float64{73.8, 0, 100} {
		weights, y, ok := capacityWeights(rate, items)
		if !ok || !slices.Equal(weights, []int32{114, 296, 178, 412}) {
			t.Fatalf("3m-12 at λ %g: profile-proportional weights %v ok=%t", rate, weights, ok)
		}
		for k, weight := range weights {
			if want := rate * float64(weight) / schedulerRouteUnits; y[k] != want {
				t.Fatalf("%s request load %g, want integer-weight load %g", items[k].id, y[k], want)
			}
		}
	}
}

func TestCapacityEnergyLoadUsesActualDemand(t *testing.T) {
	for _, rate := range []float64{73.8, 100, 200} {
		weights, offered, ok := capacityWeights(rate, []capacityItem{{routingWeight: 100}, {routingWeight: 200}})
		if !ok || !slices.Equal(weights, []int32{333, 667}) {
			t.Fatalf("profile route changed with demand: %v", weights)
		}
		for k, weight := range weights {
			if want := rate * float64(weight) / schedulerRouteUnits; math.Abs(offered[k]-want) > 1e-12 {
				t.Fatalf("energy load=%g, want actual nominal share=%g", offered[k], want)
			}
		}
	}
}

// Outstanding requests retain their actual finite-demand completion outcomes.
func TestCapacityQueuedBacklogFailsKeep(t *testing.T) {
	s, snap, _ := capacityE1Fixture()
	s.ComparisonPeriod = time.Second
	snap.Workloads[0].SLO.TargetLatency = 100 * time.Millisecond
	snap.Workloads[0].Admission.Policy = AdmissionPolicyDelay
	for q := range 160 {
		snap.Outstanding = append(snap.Outstanding, SchedulerRequest{ID: fmt.Sprint("queued-", q), Definition: snap.Workloads[0], ReceivedAt: snap.At, Phase: phasePending})
	}
	p := capacityPlannerForTest(t, s, snap)
	keep := p.evaluate(make([]int, len(p.plans)), 0)
	if keep.feasible || len(keep.feedback) != 1 || keep.feedback[0].Population < 160 || keep.feedback[0].SuccessQuantileNanos == nil || *keep.feedback[0].SuccessQuantileNanos <= float64(snap.Workloads[0].SLO.TargetLatency) {
		t.Fatalf("actual backlog lost finite-demand overload: %+v", keep)
	}
}

// m4 r2: λ 12.8 lagged a 40 req/s load with 160 queued and no instance left
// (κ_ret 0). profile-proportional weights use every new instance, independently of
// backlog; queued requests affect the SLO judgment, not request energy.
func TestCapacityBacklogDoesNotChangeWeightsOrRequestEnergy(t *testing.T) {
	support := []float64{42.01676682274319, 48.2933028258133, 114.5538148895834, 144.47154833104395}
	p := &capacityPlanner{ctx: context.Background(), periodSeconds: 147, settings: capacityTestSettings}
	p.ws = []capacityWorkload{{d: &Definition{UID: "w", SLO: SLO{TargetLatency: 861 * time.Millisecond}}, rate: 12.8, queued: 160}}
	choice := []int{}
	for n, k := range support {
		p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{UID: fmt.Sprint("n", n)}})
		keep := &capacityPlan{keep: true, items: [][]capacityItem{nil}, retained: [][]capacityItem{nil}}
		add := &capacityPlan{items: [][]capacityItem{{{id: fmt.Sprint("i", n), routingWeight: profileRoutingWeight(k), throughput: k, e: float64(n + 1)}}}, retained: [][]capacityItem{nil}}
		p.plans = append(p.plans, []*capacityPlan{keep, add})
		p.compared = append(p.compared, true)
		choice = append(choice, 1)
	}
	capacityRawPlannerForTest(p)
	res := p.evaluate(choice, 15.926122415)
	if math.Abs(res.sums[0][0]-349.3354328691838) > 1e-9 || len(res.feedback) != 1 {
		t.Fatalf("raw profile route or exact outcomes missing: %+v", res)
	}
	want := map[string]int32{"i0": 120, "i1": 138, "i2": 328, "i3": 414}
	if !maps.Equal(res.weights[0], want) || res.weightsExceeded {
		t.Fatalf("weights %v, want profile-proportional %v", res.weights[0], want)
	}
	// Removing backlog changes neither the proportional route nor request joules.
	withBacklog := res.joules
	p.ws[0].queued = 0
	p.snapshot.Outstanding = nil
	p.feedbackCache = sync.Map{}
	if res = p.evaluate(choice, 15.926122415); !maps.Equal(res.weights[0], want) || res.joules != withBacklog {
		t.Fatalf("backlog changed route or request energy: %v, %g vs %g J", res.weights[0], res.joules, withBacklog)
	}
}

// A batch limit change keeps the instance: the Pod initializes every measured
// batch, so the route applies the new firstfloor policy cap with no remove or
// prepare (m4 r5 swapped genesis3 q2 b1↔b2 by stop, drain, remove, prepare).
func TestCapacityBatchLimitChangeIsPolicyRoute(t *testing.T) {
	s, snap := schedulerFixture()
	s.ComparisonPeriod = time.Second
	profile := *snap.Instances[0].Profile
	metadata := *profile.RuntimeMetadata
	metadata.InitializedBatches = []int{1, 2}
	profile.RuntimeMetadata = &metadata
	profile.Condition.PlannedBatchSizes = []int{1, 2}
	profile.Condition.Firstfloor.MaxBatchSize = 2
	profile.Batches = append(slices.Clone(profile.Batches), schedulerTestBatchProfile(BatchProfile{Batch: 2, EnergyPerInputJoules: 0.8, MemoryBytes: 100}, []float64{float64(15 * time.Millisecond)}))
	snap.Instances[0].Profile = &profile
	option := snap.Instances[0]
	option.MaxBatch = 2
	snap.Options = []SchedulerOption{poolsOption(option)}
	n1 := snap.Instances[0].Node
	snap.Costs = nil
	capacityLoad(s, &snap, snap.Workloads[0], 150)
	d := capacityDecideForTest(t, s, snap)
	plans := poolsPlans(d, n1)
	i := slices.IndexFunc(plans, func(p SchedulerNodePlan) bool { return p.Key == "profile/b2/w1" })
	if i < 0 || !plans[i].Eligible || plans[i].Pre != 0 || plans[i].Post != 0 || plans[i].RetainedRoutingRate["w1"] != 100 {
		t.Fatalf("b2 plan must reuse i1 with no chain: %+v", plans)
	}
	if d.Target == nil || len(d.Target.Instances) != 1 || d.Target.Instances[0].ID != "i1" || d.Target.Instances[0].MaxBatch != 2 ||
		slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return a.Kind != schedulerActionRoute }) {
		t.Fatalf("target must route i1 with batch 2 only: %+v", d.Target)
	}
	capacityLoad(s, &snap, snap.Workloads[0], 50)
	capacityServiceRatesForTest(&snap, map[string]float64{snap.Instances[0].Profile.ID: 100})
	energyOnly := capacityDecideForTest(t, s, snap)
	for _, plan := range poolsPlans(energyOnly, n1) {
		if plan.Key == "profile/b2/w1" && !plan.Eligible {
			t.Fatalf("safe policy change was excluded: %+v", plan)
		}
	}
}

// The measured prepare cost carries the bare measured peak; the planned
// allocation adds servingMemoryBytes. The cost still matches: memory is a
// placement constraint, not part of the measured transition condition.
func TestCapacityPrepareCostIgnoresQueueMemory(t *testing.T) {
	s, snap := schedulerFixture()
	s.ComparisonPeriod = time.Second
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	measured := poolsInstance(snap.Instances[0], "add", n2, "fixed", "w1")
	planned := measured
	planned.Allocation.MemoryBytes += 500
	snap.Options = append(snap.Options, poolsOption(planned))
	snap.Costs = []OperationCost{poolsCost(schedulerActionPrepare, n2, "unloaded", schedulerState(measured), "fixed")}
	capacityLoad(s, &snap, snap.Workloads[0], 150)
	d := capacityDecideForTest(t, s, snap)
	if d.Target == nil || !poolsOn(*d.Target, n2) {
		t.Fatalf("prepare cost missed on memory: %+v plans %+v", d.Target, poolsPlans(d, n2))
	}
	if !measurementEqualState([]string{schedulerState(planned)}, []string{schedulerState(measured)}, map[string]string{planned.Profile.ID: MeasurementProfileIdentity(planned.Profile)}) {
		t.Fatal("replay loaded-state witness compares memory")
	}
}

// A concrete failed-start fact, rather than a generic action error, excludes
// this physical placement without freezing its Workload.
func TestCapacityPreparationFailureFactExcludesPlacement(t *testing.T) {
	s, snap := schedulerFixture()
	s.ComparisonPeriod = time.Second
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	add := poolsInstance(snap.Instances[0], "add", n2, "fixed", "w1")
	snap.Options = append(snap.Options, poolsOption(add))
	snap.Costs = []OperationCost{poolsCost(schedulerActionPrepare, n2, "unloaded", schedulerState(add), "fixed")}
	capacityLoad(s, &snap, snap.Workloads[0], 150)
	if d := capacityDecideForTest(t, s, snap); d.Target == nil || !poolsOn(*d.Target, n2) {
		t.Fatalf("fixture must prepare on n2: %+v", d.Target)
	}
	snap.Actions = []Action{{ID: "prepare-lost", Kind: schedulerActionPrepare, Node: n2, Instance: "target-w1-" + add.Profile.ID + "-1",
		From: "unloaded", To: schedulerState(add), Status: schedulerActionFailed, Reason: "completed effect lost"}}
	snap.Actions[0].PreparationFailure = NewPreparationFailure(add, "failed-pod",
		PreparationFailureObservation{Classification: PreparationDeterministic, ReasonKey: "process-exit:firstfloor:OOMKilled:137", Reason: "OOMKilled"})
	d := capacityDecideForTest(t, s, snap)
	if d.Target == nil || poolsOn(*d.Target, n2) {
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
	s.ObservationWindow, s.ComparisonPeriod = time.Second, time.Minute
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	add := poolsInstance(snap.Instances[0], "add", n2, "fixed", "w1")
	profile := *add.Profile
	profile.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1, EnergyPerInputJoules: 3, MemoryBytes: 100}, []float64{float64(time.Millisecond)})}
	add.Profile = &profile
	snap.Options = append(snap.Options, poolsOption(add))
	prepare := poolsCost(schedulerActionPrepare, n2, "unloaded", schedulerState(add), "fixed")
	prepare.Trials = []OperationTrial{{Duration: 10 * time.Second, Joules: 0}}
	snap.Costs = []OperationCost{prepare}
	capacityLoad(s, &snap, snap.Workloads[0], 150)
	p := capacityPlannerForTest(t, s, snap)
	choice := capacityChoiceForTest(t, p, map[string]string{"n2": add.Profile.ID + "/b1/w1"})
	res := p.evaluate(choice, 10)
	node := 0.0
	for n, j := range choice {
		if p.compared[n] {
			node += p.plans[n][j].energy(res.route, res.period)
		}
	}
	// The target runs for the full configured steady window after routing.
	mean := 0.0
	for id, weight := range res.weights[0] {
		energy := capacityInputEnergy(snap.Instances[0])
		if id != snap.Instances[0].ID {
			energy = capacityInputEnergy(add)
		}
		mean += float64(weight) / schedulerRouteUnits * energy
	}
	retained := min(150.0, res.sums[0][1])
	want := retained*10*capacityInputEnergy(snap.Instances[0]) + (150-retained)*10*mean + 150*(res.period-10)*mean
	if p.periodSeconds != 60 || math.Abs(res.joules-node-want) > 1e-6 {
		t.Fatalf("request energy %g, want %g (T %g)", res.joules-node, want, p.periodSeconds)
	}
}

// A long unselected chain cannot extend keep, but remains an eligible estimate.
func TestCapacityPeriodScoresTheLongestChain(t *testing.T) {
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
	d := capacityDecideForTest(t, s, snap)
	if d.Capacity.Period != s.ComparisonPeriod {
		t.Fatalf("H %v, want %v", d.Capacity.Period, s.ComparisonPeriod)
	}
	excluded := false
	for _, plan := range d.Capacity.Plans {
		if plan.Node == reentry && plan.Reason == "TransitionExceedsHorizon" {
			excluded = true
			if !plan.Eligible {
				t.Fatal("long transition rejected instead of scored")
			}
		}
	}
	if !excluded {
		t.Fatal("missing explicit horizon annotation")
	}
	without := snap
	without.Options = slices.DeleteFunc(slices.Clone(snap.Options), func(o SchedulerOption) bool { return o.Instance.Node == reentry })
	common := capacityDecideForTest(t, s, without)
	// Enabling a candidate adds its Node's measured idle energy to the
	// comparison universe, but its unselected chain must not extend W.
	extraIdle := 6 * d.Capacity.Period.Seconds()
	if common.Capacity.Period != d.Capacity.Period || math.Abs(*d.Keep.ExpectedJoules-*common.Keep.ExpectedJoules-extraIdle) > 1e-8 {
		t.Fatalf("unselected chain changed window or idle accounting: %v/%v", common.Keep.ExpectedJoules, d.Keep.ExpectedJoules)
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
		metadata := *profile.RuntimeMetadata
		metadata.CPUAffinity = []int{k % 4}
		profile.RuntimeMetadata = &metadata
		option.Profile = &profile
		snap.Options = append(snap.Options, poolsOption(option))
		snap.Costs = append(snap.Costs, poolsCost(schedulerActionPrepare, option.Node, "unloaded", schedulerState(option), "fixed"))
		snap.Costs[len(snap.Costs)-1].ID += fmt.Sprint("-", k)
	}
	reentry := poolsAddNode(&snap, "z-reentry", map[string]float64{schedulerTestEcoMode: 6, "fixed": 10})
	option := poolsInstance(base, "reentry", reentry, "fixed", "w1")
	snap.Options = append(snap.Options, poolsOption(option))
	snap.Costs = append(snap.Costs, poolsCost(schedulerActionPower, reentry, schedulerTestEcoMode, "fixed", ""), poolsCost(schedulerActionPrepare, reentry, "unloaded", schedulerState(option), "fixed"))
	capacityLoad(s, &snap, snap.Workloads[0], 1000)
	capacityServiceRatesForTest(&snap, map[string]float64{option.Profile.ID: 2000})
	began := time.Now()
	d := capacityDecideForTest(t, s, snap)
	if d.Status != schedulerStatusFeasible || d.Target == nil || !poolsOn(*d.Target, reentry) {
		t.Fatalf("the re-entry plan must be reachable among 40 options: %+v", d.Status)
	}
	t.Logf("40 options: %d node plans, Tick %v", d.Search.Examined, time.Since(began))
}

// settings.excludedNodes: an excluded Node is never planned, even when its
// option is the only way to meet the load, and every decision records it.
func TestCapacityExcludedNodeIsNeverPlanned(t *testing.T) {
	s, snap := schedulerFixture()
	s.ComparisonPeriod = time.Second
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 1})
	n3 := poolsAddNode(&snap, "n3", map[string]float64{"fixed": 1})
	add := poolsInstance(snap.Instances[0], "add", n2, "fixed", "w1")
	snap.Options = append(snap.Options, poolsOption(add))
	snap.Costs = []OperationCost{poolsCost(schedulerActionPrepare, n2, "unloaded", schedulerState(add), "fixed")}
	capacityLoad(s, &snap, snap.Workloads[0], 150)
	if d := capacityDecideForTest(t, s, snap); d.Target == nil || !poolsOn(*d.Target, n2) {
		t.Fatalf("fixture must plan n2: %+v", d.Target)
	}
	snap.Nodes[1].Reasons = []string{NodeExcluded}
	snap.Nodes[2].Reasons = []string{NodeExcluded}
	d := capacityDecideForTest(t, s, snap)
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
	// A current GPU inventory absence blocks placement, not the measured row.
	offline, first := add("missing-device", "fixed", false)
	second := first
	profile := *first.Profile
	profile.ID += "-second"
	metadata := *profile.RuntimeMetadata
	metadata.CPUAffinity = []int{7}
	profile.RuntimeMetadata = &metadata
	second.Profile, second.Allocation.GPUUUID = &profile, "missing"
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
	d := capacityDecideForTest(t, s, snap)
	for node, reason := range map[NodeReference]string{
		offline: schedulerPlanInfeasibleResources, fixed: schedulerPlanFixedWorkload, inventory: NodePlacementInventoryMissing,
	} {
		if !slices.ContainsFunc(poolsPlans(d, node), func(p SchedulerNodePlan) bool { return !p.Eligible && p.Reason == reason }) {
			t.Fatalf("%s: no plan excluded with %s: %+v", node.UID, reason, poolsPlans(d, node))
		}
	}
	if !slices.ContainsFunc(poolsPlans(d, noCost.Node), func(p SchedulerNodePlan) bool {
		return p.Eligible && len(p.ActionCosts) > 0 && p.ActionCosts[0].Provenance != nil
	}) {
		t.Fatal("unmeasured operation excluded a physically usable plan")
	}
}

func TestCapacityModeChangePlansNeedNoCPUInventory(t *testing.T) {
	settings, snapshot := schedulerFixture()
	node := poolsAddNode(&snapshot, "target", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6})
	option := poolsInstance(snapshot.Instances[0], "eco-option", node, schedulerTestEcoMode, "w1")
	snapshot.Options = append(snapshot.Options, poolsOption(option))
	snapshot.Costs = append(snapshot.Costs, poolsCost(schedulerActionPrepare, node, "unloaded", schedulerState(option), schedulerTestEcoMode),
		poolsCost(schedulerActionPower, node, "fixed", schedulerTestEcoMode, ""))
	decision := capacityDecideForTest(t, settings, snapshot)
	if !slices.ContainsFunc(poolsPlans(decision, node), func(plan SchedulerNodePlan) bool {
		return plan.Eligible && strings.HasPrefix(plan.Key, option.Profile.ID+"/")
	}) {
		t.Fatalf("power plan required a CPU inventory: %+v", poolsPlans(decision, node))
	}
}

func TestCapacityTargetDAGProjectsOnlyPowerMode(t *testing.T) {
	const currentMode, targetMode = "two", "fixed"
	settings, snapshot, expanded := capacityE1Fixture()
	n := slices.IndexFunc(snapshot.Nodes, func(node SchedulerNode) bool { return node.Node == expanded })
	source := &snapshot.Nodes[n]
	source.PowerMode = currentMode
	for _, baseline := range snapshot.Baselines {
		if baseline.Node == expanded {
			baseline.ID, baseline.PowerMode = "two-current", currentMode
			snapshot.Baselines = append(snapshot.Baselines, baseline)
			break
		}
	}
	snapshot.Costs = append(snapshot.Costs, poolsCost(schedulerActionPower, expanded, currentMode, targetMode, ""))
	// Projection needs a completed search, independent of host CPU contention.
	planner := capacityPlannerForTest(t, settings, snapshot)
	best := capacitySearchForTest(t, planner)
	decision := planner.decide(SchedulerDecision{Keep: planner.keep}, best)
	if decision.Target == nil || !poolsOn(*decision.Target, expanded) {
		t.Fatalf("target mode expansion missing: status=%s reasons=%v", decision.Status, decision.Reasons)
	}
	target := decision.Target.Nodes[slices.IndexFunc(decision.Target.Nodes, func(node SchedulerNode) bool { return node.Node == expanded })]
	if target.PowerMode != targetMode || source.PowerMode != currentMode {
		t.Fatalf("target mode missing or source mutated: %+v", target)
	}
}

// Planner decision projection is input-order invariant when search runs to
// completion. Public EvaluateScheduler wall-deadline behavior is tested separately.
func TestCapacityPlannerDecisionIgnoresInputOrder(t *testing.T) {
	s, snap, _ := capacityE1Fixture()
	poolsAddNode(&snap, "idle", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6})
	decide := func(snapshot SchedulerSnapshot) SchedulerDecision {
		planner := capacityPlannerForTest(t, s, snapshot)
		best := capacitySearchForTest(t, planner)
		decision := planner.decide(SchedulerDecision{Keep: planner.keep}, best)
		decision.Capacity.Search = nil // Search timing/traversal diagnostics are not the chosen outcome.
		return decision
	}
	want := decide(snap)
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
		got := decide(shuffled)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d: shuffled input changed the decision:\n%+v\n%+v", seed, got.Target, want.Target)
		}
	}
}

// §9 6 기간과 기록: an exact tie keeps the current configuration.
func TestCapacityExactTieKeepsCurrent(t *testing.T) {
	p := &capacityPlanner{ctx: context.Background(), periodSeconds: 60, settings: capacityTestSettings}
	p.ws = []capacityWorkload{{d: &Definition{UID: "w", SLO: SLO{TargetLatency: time.Second}}, rate: 50}}
	p.snapshot.Nodes = []SchedulerNode{{Node: NodeReference{UID: "n0"}}}
	item := capacityItem{id: "i", routingWeight: 100, throughput: 100, e: 1, weight: 1}
	keep := &capacityPlan{keep: true, items: [][]capacityItem{{item}}, retained: [][]capacityItem{{item}}, before: 10}
	twin := item
	twin.id = "a-twin"
	same := &capacityPlan{items: [][]capacityItem{{twin}}, retained: [][]capacityItem{{item}}, after: 10}
	p.sum(keep)
	p.sum(same)
	keep.record.Eligible, same.record.Eligible, same.record.Key = true, true, "a-same"
	p.plans, p.compared = [][]*capacityPlan{{keep, same}}, []bool{true}
	capacityRawPlannerForTest(p)
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
	d := capacityDecideForTest(t, s, snap)
	if d.Status != schedulerStatusKeep || d.Target == nil || !d.Target.Keep || slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return !a.Fixed }) {
		t.Fatalf("an equal-energy relabel of keep was chosen: %s %+v", d.Status, d.Target)
	}
}

// m4 r4 D-R4-1: backlog and energy-greedy weights alternated 181 times in
// 75 min. profile-proportional weights replace the old route once and then stay
// independent of λ and backlog throughout the observed queue sequence.
func TestCapacityProportionalWeightsIgnoreDemandAndBacklog(t *testing.T) {
	ids := []string{"q1", "lut", "g3", "opi"}
	support := []float64{25.288198615188904, 25.68831509322024, 70.93509923703007, 7.53273799179367}
	p := &capacityPlanner{ctx: context.Background(), periodSeconds: 152, settings: capacityTestSettings}
	p.ws = []capacityWorkload{{d: &Definition{UID: "w", SLO: SLO{TargetLatency: 971 * time.Millisecond}}}}
	p.keep.Routing = map[string]map[string]int32{"w": {"q1": 643, "lut": 357}}
	for n, id := range ids {
		p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{UID: fmt.Sprint("n", n)}})
		item := capacityItem{id: id, routingWeight: profileRoutingWeight(support[n]), throughput: support[n], e: float64(n + 1)}
		p.plans = append(p.plans, []*capacityPlan{{keep: true, items: [][]capacityItem{{item}}, retained: [][]capacityItem{{item}}}})
		p.compared = append(p.compared, true)
	}
	capacityRawPlannerForTest(p)
	changes := 0
	for _, tick := range []struct {
		rate   float64
		queued int
	}{{39.32, 0}, {40.1, 52}, {39.32, 0}, {39.43, 3}, {40.73, 0}, {39.83, 1}, {40.23, 2}} {
		p.ws[0].rate, p.ws[0].queued = tick.rate, tick.queued
		res := p.evaluateEnergyAtWindow(make([]int, len(ids)), 0, nil, p.periodSeconds)
		if want := map[string]int32{"q1": 195, "lut": 198, "g3": 549, "opi": 58}; !maps.Equal(res.weights[0], want) {
			t.Fatalf("λ %g q %d: weights %v, want %v", tick.rate, tick.queued, res.weights[0], want)
		}
		if !maps.Equal(res.weights[0], p.keep.Routing["w"]) {
			changes++
			p.keep.Routing["w"] = res.weights[0]
		}
	}
	if changes != 1 || p.keep.Routing["w"]["g3"] == 0 {
		t.Fatalf("%d weight changes, last %v", changes, p.keep.Routing["w"])
	}
}

// capacityR5Fixture is the m4 r5 low-load state: rasp5 i1 routed at 1,000 and
// genesis3 q2 and orangepi q1 Ready with weight 0, at λ 2 over a 60 s window.
func capacityR5Fixture() (SchedulerSettings, SchedulerSnapshot, NodeReference, NodeReference) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod = time.Minute, 10*time.Second, 10*time.Minute
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
	capacityFeedbackForTest(s, &snap)
	return s, snap, genesis3, orangepi
}

// Idle time does not make a negative-H-gain retirement admissible.
func TestCapacityIdleInstancesWithNegativeGainStay(t *testing.T) {
	s, snap, genesis3, orangepi := capacityR5Fixture()
	// Routing-on now assigns these destinations positive κ shares. Leave routing
	// fixed here to exercise cleanup of instances that genuinely remain unrouted.
	s.Controls.Routing = false
	since := snap.At.Add(-30 * time.Second)
	for k := 1; k < 3; k++ {
		snap.Instances[k].UnroutedSince = &since
	}
	if d := capacityDecideForTest(t, s, snap); d.Status != schedulerStatusKeep {
		t.Fatalf("unrouted for 30 s removed: %s %v", d.Status, d.Reasons)
	}
	since = snap.At.Add(-time.Minute)
	d := capacityDecideForTest(t, s, snap)
	removes := func(node NodeReference) bool {
		return slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return a.Kind == schedulerActionRemove && a.Node == node })
	}
	if d.Target == nil || !poolsOn(*d.Target, genesis3) || !poolsOn(*d.Target, orangepi) || removes(genesis3) || removes(orangepi) {
		t.Fatalf("negative-gain idle retirement chosen: %+v %v", d.Target, d.Reasons)
	}
	until := snap.At.Add(time.Minute)
	snap.Instances[1].HeldUntil = &until
	if d = capacityDecideForTest(t, s, snap); d.Target == nil || !poolsOn(*d.Target, genesis3) || !poolsOn(*d.Target, orangepi) {
		t.Fatalf("held or negative-gain instance removed: %+v", d.Target)
	}
}

// m4 r6: the unrouted genesis3 q2 and orangepi q1 serve no Workload (capture
// workloadUID "") and, prepared before the planner recorded owners, have no
// owner; only their slot names the Workload. An instance owned by an
// autonomous Workload, or owned by nobody and routed by nobody, is removed
// after one unrouted window. One a disabled Workload routes to stays.
func TestCapacityUnroutedPlannerInstancesAreRemoved(t *testing.T) {
	s, snap, genesis3, orangepi := capacityR5Fixture()
	profitable := *snap.Instances[1].Profile
	profitable.IdleBaselineWatts = 60
	snap.Instances[1].Profile = &profitable
	snap.Options[1].Instance.Profile = &profitable
	// Otherwise routing may legitimately bind the Ready owned slot to w1,
	// making it active rather than an unrouted cleanup candidate.
	s.Controls.Routing = false
	since := snap.At.Add(-time.Minute)
	for k := 1; k < 3; k++ {
		snap.Instances[k].WorkloadUID, snap.Instances[k].TargetID, snap.Instances[k].UnroutedSince = "", "target-w1-"+snap.Instances[k].Profile.ID+"-1", &since
	}
	snap.Instances[1].OwnerWorkloadUID = "w1" // prepared with the planner's owner
	// The unowned runner draws more loaded power, so the search itself wants
	// to remove it, independently of the idle-cleanup postprocessing path.
	originalProfile := snap.Instances[2].Profile
	profile := *snap.Instances[2].Profile
	profile.IdleBaselineWatts = 60
	snap.Instances[2].Profile = &profile
	optionIndex := slices.IndexFunc(snap.Options, func(o SchedulerOption) bool { return o.Instance.ID == snap.Instances[2].ID })
	snap.Options[optionIndex].Instance.Profile = &profile
	removed := func(d SchedulerDecision, node NodeReference) bool {
		return d.Target != nil && !poolsOn(*d.Target, node) &&
			slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return a.Kind == schedulerActionRemove && a.Node == node })
	}
	if d := capacityDecideForTest(t, s, snap); !removed(d, genesis3) || !removed(d, orangepi) {
		t.Fatalf("unrouted instances: removed genesis3=%v orangepi=%v plans=%+v reasons=%v", removed(d, genesis3), removed(d, orangepi), poolsPlans(d, orangepi), d.Reasons)
	}
	snap.Instances[2].Profile = originalProfile
	snap.Options[optionIndex].Instance.Profile = originalProfile
	other := poolsAddWorkload(&snap, "w2")
	other.Model.UID = "m2"
	snap.Routing["w2"] = map[string]int32{"opi": 1}
	snap.Instances[2].UnroutedSince = nil
	if d := capacityDecideForTest(t, s, snap); !removed(d, genesis3) || removed(d, orangepi) {
		t.Fatalf("an instance a disabled Workload routes to was removed: %+v", d.Target)
	}
}

// m4 r5 D-R5-1: genesis2 was prepared at 04:28:48 while keep was in Overload
// and removed 50 s later. Its hold keeps it for one observation window even
// when removal saves energy.
func TestCapacityHeldInstanceIsNotRemoved(t *testing.T) {
	s, snap, genesis3, _ := capacityR5Fixture()
	snap.Instances[1].Profile.IdleBaselineWatts = 60 // removal saves 10 W
	d := capacityDecideForTest(t, s, snap)
	if d.Target == nil || poolsOn(*d.Target, genesis3) {
		t.Fatalf("fixture must remove genesis3 for energy: %+v", d.Target)
	}
	until := snap.At.Add(time.Minute)
	snap.Instances[1].HeldUntil = &until
	d = capacityDecideForTest(t, s, snap)
	if d.Target == nil || !poolsOn(*d.Target, genesis3) || !slices.ContainsFunc(poolsPlans(d, genesis3), func(p SchedulerNodePlan) bool { return p.Reason == schedulerPlanHeld }) {
		t.Fatalf("held genesis3 removed: %+v %+v", d.Target, poolsPlans(d, genesis3))
	}
}

// A replacement repays its transition over T at the least load of the
// observation window. n2's instance a (e 2) can be replaced by b (e 1) for a
// 500 J chain: at a steady 30 req/s its κ share saves ≈10 W over 60 s,
// with the same mean but one 10 s interval at 5 req/s it does not repay.
func TestCapacityReplacementRepaysAtLeastLoad(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod = time.Minute, 10*time.Second, time.Minute
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
	prepare.Trials = []OperationTrial{{Duration: time.Second, Joules: 500}}
	snap.Costs = []OperationCost{poolsCost(schedulerActionRemove, n2, schedulerState(a), "unloaded", "fixed"), prepare}
	capacityServiceRatesForTest(&snap, map[string]float64{"profile": 100, a.Profile.ID: 50, b.Profile.ID: 50})
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
	if d := capacityDecideForTest(t, s, snap); !replaced(d) {
		t.Fatalf("steady 30 req/s must replace a: %+v %v", d.Target, d.Reasons)
	}
	p := capacityPlannerForTest(t, s, snap)
	best := capacitySearchForTest(t, p)
	load(35, 35, 35, 35, 35, 5)
	if d := capacityDecideForTest(t, s, snap); replaced(d) || !slices.Contains(d.Reasons, schedulerReplacementUnpaid+":n2") {
		t.Fatalf("replacement unpaid at 5 req/s was chosen: %+v %v", d.Target, d.Reasons)
	}
	// Recovery changes shared profile pointers; test normal payback before that mutation.
	for _, plans := range p.plans {
		for w := range plans[0].items {
			for k := range plans[0].items[w] {
				plans[0].items[w][k].routingWeight = 1
				plans[0].items[w][k].throughput = 1
				plans[0].instances[k].Profile.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1, EnergyPerInputJoules: capacityInputEnergy(plans[0].instances[k]), MemoryBytes: 100}, []float64{float64(time.Second)})}
			}
		}
		p.sum(plans[0])
	}
	p.feedbackCache = sync.Map{}
	if keep := p.evaluate(make([]int, len(p.plans)), 0); keep.feasible {
		t.Fatal("recovery fixture keep must be infeasible")
	}
	if got := p.payback(best); !slices.Equal(got.choice, best.choice) {
		t.Fatalf("keep infeasible recovery must bypass optional payback: %+v", got)
	}
}

func capacityEmptyFixedFixture() (SchedulerSettings, SchedulerSnapshot, NodeReference) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval = time.Minute, 10*time.Second
	s.ComparisonPeriod, s.MaxReplicas = 600*time.Second, 1
	snap.ObservedSince = snap.ObservationEnd.Add(-time.Hour)
	snap.Routing = map[string]map[string]int32{"w1": {"i1": schedulerRouteUnits}}
	capacityLoad(s, &snap, snap.Workloads[0], 20)
	// 0.28 W steady saving with 867 J transition overhead breaks even in
	// 3096 s (~52 min), not within the common 600 s horizon.
	node := poolsAddNode(&snap, "jetsono", map[string]float64{"fixed": 10, schedulerTestEcoMode: 9.72})
	power := poolsCost(schedulerActionPower, node, "fixed", schedulerTestEcoMode, "")
	power.Trials = []OperationTrial{{Duration: 10 * time.Second, Joules: 964.2}}
	snap.Costs = []OperationCost{power}
	return s, snap, node
}

func TestCapacityEmptyFixedUsesCommonEnergyAdmission(t *testing.T) {
	for _, tc := range []struct {
		name        string
		joules      float64
		uncertainty float64
		gain        float64
		admitted    bool
	}{
		{name: "negative", joules: 964.2, gain: -699},
		{name: "positive", joules: 100, gain: 165.2, admitted: true},
		{name: "positive-below-minimumGain", joules: 100, uncertainty: 200, gain: 165.2},
		{name: "zero-gain", joules: 265.2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, snap, node := capacityEmptyFixedFixture()
			snap.Costs[0].Trials[0].Joules = tc.joules
			snap.Costs[0].Trials[0].EnergyUncertaintyJoules = tc.uncertainty
			p := capacityPlannerForTest(t, s, snap)
			choice := capacityChoiceForTest(t, p, map[string]string{node.UID: ""})
			n := slices.IndexFunc(p.snapshot.Nodes, func(n SchedulerNode) bool { return n.Node == node })
			plan := p.plans[n][choice[n]]
			if !p.compared[n] || !plan.reduces || math.Abs(plan.record.Joules-tc.joules) > 1e-9 {
				t.Fatalf("Fixed saving missing from comparison or chain costs: %+v", plan.record)
			}
			result := p.evaluate(choice, p.routeOf(choice))
			gain := result.keepJoules - result.joules
			if math.Abs(gain-tc.gain) > 1e-8 {
				t.Fatalf("gain=%g, want %g over the common W", gain, tc.gain)
			}
			paid := p.minimumGain(p.payback(p.evaluate(choice, p.routeOf(choice))))
			if (paid.choice[n] != 0) != tc.admitted {
				t.Fatalf("Fixed payback/minimumGain admission=%v, want %v", paid.choice[n] != 0, tc.admitted)
			}
			d := capacityDecideForTest(t, s, snap)
			if schedulerNodeMode(d.Keep.Nodes, node) != schedulerNodeMode(snap.Nodes, node) || len(d.Keep.Actions) != 0 {
				t.Fatalf("new saving was preapplied to keep: %+v", d.Keep)
			}
			admitted := d.Target != nil && slices.ContainsFunc(d.Target.Actions, func(a Action) bool {
				return a.Node == node && a.Kind == schedulerActionPower && a.Fixed
			})
			wantWindow := s.ComparisonPeriod
			if tc.admitted {
				wantWindow = time.Duration(result.period * 1e9)
			}
			if admitted != tc.admitted || d.Capacity == nil || d.Capacity.Period != wantWindow || !slices.Contains(d.Search.ComparedNodes, node) {
				t.Fatalf("admission=%v, want %v: target=%+v capacity=%+v", admitted, tc.admitted, d.Target, d.Capacity)
			}
		})
	}
}

func TestCapacityLosingEmptyFixedCannotHideInProfitableCombination(t *testing.T) {
	s, snap, losing := capacityEmptyFixedFixture()
	profitable := poolsAddNode(&snap, "profitable", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6})
	power := poolsCost(schedulerActionPower, profitable, "fixed", schedulerTestEcoMode, "")
	power.Trials = []OperationTrial{{Duration: time.Second, Joules: 1}}
	snap.Costs = append(snap.Costs, power)
	p := capacityPlannerForTest(t, s, snap)
	choice := capacityChoiceForTest(t, p, map[string]string{losing.UID: "", profitable.UID: ""})
	combined := p.evaluate(choice, p.routeOf(choice))
	keep := p.evaluate(make([]int, len(p.plans)), 0)
	if !combined.feasible || combined.joules >= keep.joules {
		t.Fatal("fixture must combine a losing Fixed with a larger positive gain")
	}
	paid := p.minimumGain(p.payback(combined))
	for n, node := range p.snapshot.Nodes {
		if node.Node == losing && paid.choice[n] != 0 || node.Node == profitable && paid.choice[n] == 0 {
			t.Fatalf("individual Fixed admission was hidden by total gain: %+v", paid)
		}
	}
	d := capacityDecideForTest(t, s, snap)
	if d.Target == nil || schedulerNodeMode(d.Target.Nodes, losing) != schedulerNodeMode(snap.Nodes, losing) ||
		schedulerNodeMode(d.Target.Nodes, profitable) != schedulerTestEcoMode {
		t.Fatalf("search included the losing saving or lost the profitable one: %+v", d.Target)
	}
}

func TestCapacityTargetlessKeepNeverIntroducesFixedSaving(t *testing.T) {
	s, snap, node := capacityEmptyFixedFixture()
	snap.Reasons = []string{"ObservationGap"}
	d := capacityDecideForTest(t, s, snap)
	if d.Target != nil || len(d.Keep.Actions) != 0 || schedulerNodeMode(d.Keep.Nodes, node) != schedulerNodeMode(snap.Nodes, node) {
		t.Fatalf("targetless keep bypassed comparison: %+v", d.Keep)
	}
}

func TestSchedulerReplayFixedPowerIsNotFreeOrInstant(t *testing.T) {
	_, snap, node := capacityEmptyFixedFixture()
	action := Action{ID: "new-fixed", Kind: schedulerActionPower, Node: node, From: schedulerNodeMode(snap.Nodes, node), To: schedulerTestEcoMode, Fixed: true, Status: schedulerActionPlanned}
	replay := schedulerReplayState{snap: snap, c: SchedulerCandidate{Actions: []Action{action}}, nodes: slices.Clone(snap.Nodes)}
	replay.initializeActions()
	if replay.actionByID[action.ID].done || schedulerNodeMode(replay.nodes, node) != schedulerNodeMode(snap.Nodes, node) {
		t.Fatal("Fixed action bypassed the normal replay transition")
	}
}

func TestSchedulerReadyExpansionCapacitySCH20AC38(t *testing.T) {
	const terminated = "terminated"
	for _, scenario := range []string{"admitted", "same-node", "other-workload-node", "not-ready", terminated, "wrong-allocation", "overload", "backlog", "routing-off", "future"} {
		t.Run(scenario, func(t *testing.T) {
			s, snap, _ := capacityE1Fixture()
			s.ComparisonPeriod = time.Second
			old := snap.Instances[0]
			added := snap.Options[len(snap.Options)-1].Instance
			added.ID, added.WorkloadUID = "ready-addition", old.WorkloadUID
			s.Controls.Routing = true
			capacityLoad(s, &snap, snap.Workloads[0], 40)
			snap.Routing = map[string]map[string]int32{old.WorkloadUID: {old.ID: schedulerRouteUnits}}
			switch scenario {
			case "same-node":
				added.Node = old.Node
			case "other-workload-node":
				other := added
				other.ID, other.WorkloadUID = schedulerTestOther, "other-workload"
				snap.Instances = append(snap.Instances, other)
				snap.Routing[other.WorkloadUID] = map[string]int32{other.ID: schedulerRouteUnits}
			case "not-ready":
				added.Ready = false
			case terminated:
				added.Terminated = true
			case "wrong-allocation":
				added.Allocation.GPUUUID = "different-device"
			case "overload":
				capacityLoad(s, &snap, snap.Workloads[0], 600)
			case "backlog":
				for q := range 100 {
					snap.Outstanding = append(snap.Outstanding, SchedulerRequest{ID: fmt.Sprint("backlog-", q), Definition: snap.Workloads[0], ReceivedAt: snap.At, Phase: phasePending})
				}
			case "routing-off":
				s.Controls.Routing = false
			case "future":
				snap.Nodes[0].ObservedAt = snap.At.Add(time.Second)
			}
			weights, ok := SchedulerReadyExpansion(context.Background(), s, snap, old.WorkloadUID, []SchedulerInstance{old}, []SchedulerInstance{added})
			// SLO-infeasible improvements are admissible, including existing backlog.
			want := scenario == "admitted" || scenario == "overload" || scenario == "backlog"
			if ok != want {
				t.Fatalf("admission=%v want=%v weights=%v", ok, want, weights)
			}
			if ok && (weights[old.ID] <= 0 || weights[added.ID] <= 0 || weights[old.ID]+weights[added.ID] != schedulerRouteUnits) {
				t.Fatalf("lost positive integer route: %v", weights)
			}
		})
	}
}
