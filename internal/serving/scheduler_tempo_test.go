package serving

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestSchedulerTwoTempoSettings(t *testing.T) {
	s, _ := schedulerFixture()
	margin := 0.0
	in := SchedulerSettingsInput{SchedulerSettings: s, CompletionMarginMS: &margin}
	got, err := in.Settings()
	if err != nil || got.ScaleDownStabilization != 300*time.Second || got.TransitionReserveFraction != 0 {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	for _, d := range []time.Duration{-1, 0, 1, 120 * time.Second, 300 * time.Second, time.Duration(math.MaxInt64)} {
		in.ScaleDownStabilization = &d
		got, err = in.Settings()
		if (err == nil) != (d >= 0) || err == nil && got.ScaleDownStabilization != d {
			t.Fatalf("duration %v: %+v %v", d, got, err)
		}
	}
	for _, f := range []float64{0, 1, .25, -.1, 1.1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		s.TransitionReserveFraction = f
		if (s.Validate() == nil) != (schedulerFinite(f) && f >= 0 && f <= 1) {
			t.Fatalf("fraction %v", f)
		}
	}
	for _, value := range []string{"9223372036854775808", "1.5"} {
		if json.Unmarshal([]byte(`{"scaleDownStabilizationNanos":`+value+`}`), &in) == nil {
			t.Fatalf("accepted %s", value)
		}
	}
	s.TransitionReserveFraction = 0
	s.ComparisonPeriod = 0
	if s.Validate() == nil {
		t.Fatal("accepted zero horizon")
	}
}

func TestSchedulerTempoContinuousDecline(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow = time.Second
	s.ControlInterval = time.Second
	s.MaxObservationAge = 10 * time.Second
	s.ScaleDownStabilization = 3 * time.Second
	snap.Outstanding = nil
	start := snap.At
	var tempo SchedulerTempo
	observe := func(second int, rate float64) SchedulerDownState {
		snap.At = start.Add(time.Duration(second) * time.Second)
		snap.ObservationEnd = snap.At
		snap.ObservedSince = start.Add(-time.Hour)
		snap.Arrivals = nil
		capacityLoad(s, &snap, snap.Workloads[0], rate)
		tempo.Observe(s, &snap)
		return snap.ScaleDown["w1"]
	}
	if got := observe(0, 10); got.Allowed || got.DownSince != nil {
		t.Fatalf("initial: %+v", got)
	}
	if got := observe(1, 5); got.Allowed || got.DownSince == nil {
		t.Fatalf("drop: %+v", got)
	}
	if got := observe(3, 5); got.Allowed {
		t.Fatalf("before boundary: %+v", got)
	}
	if got := observe(4, 5); !got.Allowed || !snap.EmptyNodeScaleDownAllowed {
		t.Fatalf("boundary: %+v", got)
	}
	if got := observe(5, 6); !got.Allowed || got.Elapsed != 4*time.Second || snap.EmptyNodeScaleDownAllowed {
		t.Fatalf("rise below baseline interrupted UID decline or opened vector gate: %+v", got)
	}
	if got := observe(6, 10); got.Allowed || got.DownSince != nil || got.Baseline != 10 {
		t.Fatalf("return to baseline: %+v", got)
	}
	if got := observe(7, 12); got.Allowed || got.DownSince != nil || got.Baseline != 12 {
		t.Fatalf("rise above baseline: %+v", got)
	}
	if got := observe(8, 6); got.Allowed || got.Elapsed != 0 || got.DownSince == nil {
		t.Fatalf("new decline: %+v", got)
	}
	if got := observe(30, 5); got.Allowed || got.Elapsed != 0 || got.ResetReason != "ObservationGap" {
		t.Fatalf("gap: %+v", got)
	}
	observe(31, 4)
	s.ScaleDownStabilization = 4 * time.Second
	if got := observe(32, 4); got.Allowed || got.Elapsed != 0 || got.ResetReason != "SettingsChanged" {
		t.Fatalf("settings reset: %+v", got)
	}
	tempo = SchedulerTempo{}
	if got := observe(33, 3); got.Allowed || got.DownSince != nil {
		t.Fatalf("restart: %+v", got)
	}
}

func TestSchedulerTempoDeclineSurvivesLowRateJitter(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval = time.Second, time.Second
	s.MaxObservationAge = 10 * time.Second
	s.ScaleDownStabilization = 120 * time.Second
	snap.Outstanding = nil
	start := snap.At
	var tempo SchedulerTempo
	observe := func(second int, rate float64) SchedulerDownState {
		snap.At = start.Add(time.Duration(second) * time.Second)
		snap.ObservationEnd, snap.ObservedSince = snap.At, start.Add(-time.Hour)
		snap.Arrivals = nil
		capacityLoad(s, &snap, snap.Workloads[0], rate)
		tempo.Observe(s, &snap)
		return snap.ScaleDown["w1"]
	}
	observe(0, 100)
	downSince := start.Add(time.Second)
	rates := []float64{20, 18, 22, 19, 21}
	for second := 1; second <= 301; second++ {
		got := observe(second, rates[(second-1)%len(rates)])
		elapsed := time.Duration(second-1) * time.Second
		if got.Baseline != 100 || got.DownSince == nil || !got.DownSince.Equal(downSince) ||
			got.Elapsed != elapsed || got.Allowed != (elapsed >= s.ScaleDownStabilization) || got.ResetReason != "" {
			t.Fatalf("t=%ds: low-rate jitter restarted decline: %+v", second, got)
		}
	}
	if got := observe(302, 100); got.Allowed || got.DownSince != nil || got.Reason != "DemandNotDecreasing" {
		t.Fatalf("baseline return did not cancel stabilized decline: %+v", got)
	}
	if got := observe(303, 20); got.Allowed || got.DownSince == nil || got.Elapsed != 0 {
		t.Fatalf("new decline did not restart after baseline return: %+v", got)
	}
}

func TestSchedulerTempoBlocksIdleRemovalAndEmptyPower(t *testing.T) {
	s, snap, rows, _, _ := capacityR5Fixture()
	s.ScaleDownStabilization = time.Minute
	snap.ScaleDown = map[string]SchedulerDownState{"w1": {}}
	since := snap.At.Add(-time.Minute)
	for k := 1; k < len(snap.Instances); k++ {
		snap.Instances[k].UnroutedSince = &since
	}
	got := capacityDecideForTest(t, s, snap, rows)
	if got.Target != nil && len(got.Target.Instances) < len(snap.Instances) {
		t.Fatal("idle removal bypassed stabilization")
	}
	for _, n := range schedulerCommonNodes(s, snap) {
		for _, old := range snap.Nodes {
			if n.Node == old.Node && n.PowerMode != old.PowerMode {
				t.Fatal("empty power bypassed stabilization")
			}
		}
	}
	snap.ScaleDown["w1"] = SchedulerDownState{Allowed: true}
	got = capacityDecideForTest(t, s, snap, rows)
	if got.Target == nil || len(got.Target.Instances) >= len(snap.Instances) {
		t.Fatalf("stabilized idle removal missing: %s", got.Status)
	}
}

func TestCapacityCombinedHorizonBoundary(t *testing.T) {
	p := &capacityPlanner{period: 60, settings: capacityTestSettings}
	for n := range 2 {
		p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{UID: string(rune('a' + n))}})
		p.plans = append(p.plans, []*capacityPlan{{keep: true}, {record: SchedulerNodePlan{Eligible: true}}})
		p.compared = append(p.compared, true)
	}
	p.plans[0][1].preMax = 10
	p.plans[1][1].postMax = 51
	for _, choice := range [][]int{{1, 0}, {0, 1}} {
		res := p.evaluate(choice, p.routeOf(choice))
		if res.exclusion != "" {
			t.Fatalf("individual chain rejected: %s", res.exclusion)
		}
	}
	choice := []int{1, 1}
	res := p.evaluate(choice, p.routeOf(choice))
	if res.exclusion != "TransitionExceedsHorizon" || p.admits(&res) {
		t.Fatal("combined chain beyond H admitted")
	}
	p.plans[1][1].postMax = 50
	res = p.evaluate(choice, p.routeOf(choice))
	if res.exclusion != "" || !p.admits(&res) {
		t.Fatal("exact H boundary rejected")
	}
}

func TestTransitionReserveActualSupply(t *testing.T) {
	uid := "w"
	a, b := NodeReference{UID: "a"}, NodeReference{UID: "b"}
	p := &capacityPlanner{settings: SchedulerSettings{TransitionReserveFraction: .5}, ws: []capacityWorkload{{d: &Definition{UID: uid}, rate: 10}}}
	p.snapshot.Nodes = []SchedulerNode{{Node: a, PowerMode: "fixed"}, {Node: b, PowerMode: "fixed"}}
	p.snapshot.Instances = []SchedulerInstance{{ID: "a", Node: a, WorkloadUID: uid, Ready: true, Routable: map[string]bool{uid: true}}, {ID: "b", Node: b, WorkloadUID: uid, Ready: true, Routable: map[string]bool{uid: true}}}
	itemA, itemB := capacityItem{id: "a", kappa: 10}, capacityItem{id: "b", kappa: 20}
	p.plans = [][]*capacityPlan{
		{{keep: true, mode: "fixed", items: [][]capacityItem{{itemA}}, retained: [][]capacityItem{{itemA}}}, {mode: "fixed", items: [][]capacityItem{{}}, retained: [][]capacityItem{{itemA}}}},
		{{keep: true, mode: "fixed", items: [][]capacityItem{{itemB}}, retained: [][]capacityItem{{itemB}}}},
	}
	choice := []int{1, 0}
	check := func(want bool) {
		t.Helper()
		records := p.transitionReserve(choice)
		if len(records) != 1 || records[0].Allowed != want || records[0].Required != 15 {
			t.Fatalf("reserve want %v: %+v", want, records)
		}
	}
	check(true)
	p.snapshot.Instances[1].Ready = false
	check(false)
	p.snapshot.Instances[1].Ready = true
	p.snapshot.Instances[1].Routable[uid] = false
	check(false)
	p.snapshot.Instances[1].Routable[uid] = true
	p.snapshot.Reservations = []SchedulerReservation{{Owner: "other", Instances: []string{"b"}}}
	check(false)
	p.snapshot.Reservations = nil
	p.plans[1][0].items[0][0].prepare = true
	check(false)
	p.settings.TransitionReserveFraction = 0
	if p.transitionReserve(choice) != nil {
		t.Fatal("zero reserve enabled an extra constraint")
	}
}

func TestSchedulerTempoUIDIndependenceAndVectorGate(t *testing.T) {
	s, snap := schedulerFixture()
	w2 := poolsAddWorkload(&snap, "w2")
	s.AutonomousWorkloads = append(s.AutonomousWorkloads, w2.UID)
	snap.Options[0].CompatibleUIDs = append(snap.Options[0].CompatibleUIDs, w2.UID)
	s.ObservationWindow, s.ControlInterval, s.MaxObservationAge = time.Second, time.Second, time.Hour
	s.ScaleDownStabilization = 2 * time.Second
	snap.Outstanding = nil
	start := snap.At
	var tempo SchedulerTempo
	observe := func(second int, a, b float64) {
		snap.At = start.Add(time.Duration(second) * time.Second)
		snap.ObservationEnd, snap.ObservedSince = snap.At, start.Add(-time.Hour)
		snap.Arrivals = nil
		capacityLoad(s, &snap, snap.Workloads[0], a)
		capacityLoad(s, &snap, w2, b)
		tempo.Observe(s, &snap)
	}
	observe(0, 10, 10)
	observe(1, 5, 10)
	observe(2, 5, 12)
	observe(3, 5, 14)
	if !snap.ScaleDown["w1"].Allowed || snap.ScaleDown["w2"].Allowed || snap.EmptyNodeScaleDownAllowed {
		t.Fatalf("UIDs were globally synchronized or increases offset: %+v", snap.ScaleDown)
	}
	snap.Reasons = []string{"ObservationGap"}
	observe(4, 4, 14)
	if snap.ScaleDown["w1"].DownSince != nil || snap.EmptyNodeScaleDownAllowed {
		t.Fatal("observation gap retained decline")
	}
}

func TestSchedulerTempoFixedPowerGateIncludesTargetlessKeep(t *testing.T) {
	s, snap := schedulerFixture()
	s.ScaleDownStabilization = time.Minute
	node := poolsAddNode(&snap, "empty", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6, "boost": 12})
	snap.ScaleDown = map[string]SchedulerDownState{"w1": {}}
	snap.Reasons = []string{"ObservationGap"}
	for _, allowed := range []bool{false, true} {
		snap.EmptyNodeScaleDownAllowed = allowed
		got := capacityDecideForTest(t, s, snap, nil)
		if got.Target != nil {
			t.Fatal("fixture must exercise targetless Keep.Actions")
		}
		fixed := false
		for _, action := range got.Keep.Actions {
			fixed = fixed || action.Node == node && action.Fixed
		}
		if fixed != allowed {
			t.Fatalf("targetless Fixed gate: allowed=%v actions=%+v", allowed, got.Keep.Actions)
		}
		target := SchedulerCandidate{Nodes: append([]SchedulerNode(nil), snap.Nodes...), Instances: snap.Instances, Routing: got.Keep.Routing}
		for index := range target.Nodes {
			if target.Nodes[index].Node == node {
				target.Nodes[index].PowerMode = "boost"
			}
		}
		fixed = false
		for _, action := range schedulerPlan(snap, target) {
			fixed = fixed || action.Node == node && action.Fixed
		}
		if fixed != allowed {
			t.Fatalf("changeNodes prepended forbidden Fixed lowering: allowed=%v", allowed)
		}
	}
}

func TestSchedulerTempoSelectedRebasesOwnerOnlyRemoval(t *testing.T) {
	since := time.Unix(100, 0)
	declining := SchedulerDownState{Baseline: 10, Previous: 5, Rate: 5, DownSince: &since, Elapsed: time.Minute, Allowed: true}
	tempo := SchedulerTempo{states: map[string]SchedulerDownState{"w1": declining, "w2": declining}}
	unrouted := SchedulerInstance{ID: "owned-unrouted", OwnerWorkloadUID: "w1"}
	decision := SchedulerDecision{Keep: SchedulerCandidate{Instances: []SchedulerInstance{unrouted}}, Target: &SchedulerCandidate{Instances: []SchedulerInstance{unrouted}}}
	tempo.Selected(decision)
	if state := tempo.states["w1"]; state.Baseline != 10 || state.DownSince == nil {
		t.Fatalf("retained owner-only runner reset the timer: %+v", state)
	}
	decision.Target.Instances = nil
	tempo.Selected(decision)
	if state := tempo.states["w1"]; state.Baseline != 5 || state.DownSince != nil || state.Elapsed != 0 || state.Allowed {
		t.Fatalf("owner-only removal did not rebase selection: %+v", state)
	}
	if state := tempo.states["w2"]; state.Baseline != 10 || state.DownSince == nil || !state.Allowed {
		t.Fatalf("unrelated Workload was rebased: %+v", state)
	}
}

func TestSchedulerTempoRecordsEarlyDecisionReset(t *testing.T) {
	for _, reason := range []string{"partial-window", "snapshot-gap"} {
		t.Run(reason, func(t *testing.T) {
			s, snapshot := schedulerFixture()
			s.ScaleDownStabilization = time.Minute
			snapshot.ObservedSince = snapshot.At
			if reason == "snapshot-gap" {
				snapshot.Reasons = []string{schedulerObservationGap}
			}
			var tempo SchedulerTempo
			tempo.Observe(s, &snapshot)
			rows := capacityRowsForTest(s, snapshot, nil)
			decision := capacityDecideForTest(t, s, snapshot, rows)
			if decision.Target != nil || decision.Capacity == nil || decision.Capacity.Search != nil || len(decision.Capacity.Workloads) != 1 {
				t.Fatalf("fixture must return before plan search with timer evidence: %+v", decision)
			}
			down := decision.Capacity.Workloads[0].ScaleDown
			if down == nil || down.ResetReason != "FirstObservation" || down.Allowed || decision.Capacity.Period != s.ComparisonPeriod {
				t.Fatalf("early reset evidence missing: %+v", decision.Capacity)
			}
			// A later observed gap records the interruption too, not just startup.
			snapshot.ScaleDown = nil
			snapshot.At = snapshot.At.Add(time.Second)
			snapshot.ObservationEnd = snapshot.At
			snapshot.Reasons = []string{schedulerObservationGap}
			tempo.Observe(s, &snapshot)
			decision = capacityDecideForTest(t, s, snapshot, rows)
			down = decision.Capacity.Workloads[0].ScaleDown
			if down.ResetReason != schedulerObservationGap || down.DownSince != nil {
				t.Fatalf("observation interruption disappeared from decision: %+v", down)
			}
		})
	}
}
