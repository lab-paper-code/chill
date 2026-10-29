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

func TestSchedulerReplacementDwellSettings(t *testing.T) {
	s, _ := schedulerFixture()
	margin := 0.0
	in := SchedulerSettingsInput{SchedulerSettings: s, CompletionMarginMS: &margin}
	if got, err := in.Settings(); err != nil || got.ReplacementDwell != 0 {
		t.Fatalf("omitted dwell must reuse comparison period: %+v %v", got, err)
	}
	for _, d := range []time.Duration{-1, 0, 17 * time.Second, time.Duration(math.MaxInt64)} {
		in.ReplacementDwell = &d
		got, err := in.Settings()
		if (err == nil) != (d >= 0) || err == nil && got.ReplacementDwell != d {
			t.Fatalf("dwell %v: %+v %v", d, got, err)
		}
	}
	for _, value := range []string{"9223372036854775808", "1.5"} {
		if json.Unmarshal([]byte(`{"replacementDwellNanos":`+value+`}`), &in) == nil {
			t.Fatalf("accepted dwell %s", value)
		}
	}
}

func TestSchedulerTempoDemandWindow(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval = time.Second, time.Second
	s.MaxObservationAge = 10 * time.Second
	s.ScaleDownStabilization = 3 * time.Second
	snap.Outstanding = nil
	snap.Routing = map[string]map[string]int32{"w1": {"i1": 1000}}
	snap.Instances[0].Routable = map[string]bool{"w1": true}
	start := snap.At
	var tempo SchedulerTempo
	observe := func(second int, rate float64) SchedulerDownState {
		snap.At = start.Add(time.Duration(second) * time.Second)
		snap.ObservationEnd, snap.ObservedSince = snap.At, start.Add(-time.Hour)
		snap.Arrivals = nil
		capacityLoad(s, &snap, snap.Workloads[0], rate)
		rows := capacityRowsForTest(s, snap, nil)
		tempo.Observe(s, &snap, rows)
		return snap.ScaleDown["w1"]
	}
	// Supply is measured, never inferred from a local demand peak.
	first := observe(0, 100)
	if first.Supply <= 0 {
		t.Fatalf("missing measured supply: %+v", first)
	}
	for second := 1; second < 10; second++ {
		got := observe(second, first.Supply+float64(second%2))
		if got.Allowed || snap.EmptyNodeScaleDownAllowed {
			t.Fatalf("constant noisy demand opened gate: %+v", got)
		}
	}
	high := observe(10, first.Supply+10)
	observe(11, 1)
	if got := observe(12, 1); got.Allowed || got.WindowMax != high.WindowMax {
		t.Fatalf("high sample expired early: %+v", got)
	}
	if got := observe(13, 1); !got.Allowed || got.WindowMax != 1 || !snap.EmptyNodeScaleDownAllowed {
		t.Fatalf("left boundary not expired: %+v", got)
	}
	if got := observe(30, 1); got.Allowed || got.ResetReason != schedulerObservationGap {
		t.Fatalf("gap: %+v", got)
	}
	observe(31, 1)
	s.ScaleDownStabilization = 4 * time.Second
	if got := observe(32, 1); got.Allowed || got.ResetReason != "SettingsChanged" {
		t.Fatalf("settings: %+v", got)
	}
	tempo = SchedulerTempo{}
	if got := observe(33, 1); got.Allowed || got.ResetReason != schedulerFirstObservation {
		t.Fatalf("restart: %+v", got)
	}
	s.ScaleDownStabilization = 0
	if got := observe(34, 1); !got.Allowed {
		t.Fatalf("zero window delayed: %+v", got)
	}
	if got := observe(35, first.Supply+10); got.Allowed {
		t.Fatalf("zero window bypassed supply: %+v", got)
	}
	s.ScaleDownStabilization = 2 * time.Second
	observe(36, 1)
	snap.Reasons = []string{schedulerObservationGap}
	observe(37, 1)
	snap.Reasons = nil
	if got := observe(38, 1); got.Allowed {
		t.Fatalf("invalid interval counted as coverage: %+v", got)
	}
	if got := observe(39, 1); got.Allowed {
		t.Fatalf("recovery window completed early: %+v", got)
	}
	if got := observe(40, 1); !got.Allowed {
		t.Fatalf("recovery window failed to complete: %+v", got)
	}
	if got := observe(39, 1); got.Allowed || got.ResetReason != schedulerObservationGap {
		t.Fatalf("clock reversal retained window: %+v", got)
	}
}

func TestSchedulerTempoAllowsSpareSupplyAboveWindowMaximum(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval = time.Second, time.Second
	s.ScaleDownStabilization = 3 * time.Second
	snap.Routing = map[string]map[string]int32{"w1": {"i1": schedulerRouteUnits}}
	start := snap.At
	var tempo SchedulerTempo
	for second, rate := range []float64{110, 110, 20, 20} {
		snap.At = start.Add(time.Duration(second) * time.Second)
		snap.ObservationEnd, snap.ObservedSince = snap.At, start.Add(-time.Hour)
		capacityLoad(s, &snap, snap.Workloads[0], rate)
		rows := capacityRowsForTest(s, snap, func(SchedulerOption) float64 { return 160 })
		tempo.Observe(s, &snap, rows)
	}
	state := snap.ScaleDown["w1"]
	if state.Rate != 20 || state.WindowMax != 110 || state.Supply != 160 || !state.Allowed || !snap.EmptyNodeScaleDownAllowed {
		t.Fatalf("spare capacity gate did not preserve past maximum: %+v", state)
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
	for _, n := range schedulerCommonNodes(snap) {
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
	snap.Routing = map[string]map[string]int32{"w1": {"i1": 1000}}
	snap.Instances[0].Routable = map[string]bool{"w1": true}
	start := snap.At
	var tempo SchedulerTempo
	observe := func(second int, a, b float64) {
		snap.At = start.Add(time.Duration(second) * time.Second)
		snap.ObservationEnd, snap.ObservedSince = snap.At, start.Add(-time.Hour)
		snap.Arrivals = nil
		capacityLoad(s, &snap, snap.Workloads[0], a)
		capacityLoad(s, &snap, w2, b)
		tempo.Observe(s, &snap, capacityRowsForTest(s, snap, nil))
	}
	observe(0, 10, 10)
	observe(1, 1, 10)
	observe(2, 1, 12)
	observe(3, 1, 14)
	if !snap.ScaleDown["w1"].Allowed || snap.ScaleDown["w2"].Allowed || snap.EmptyNodeScaleDownAllowed {
		t.Fatalf("UID gates coupled: %+v", snap.ScaleDown)
	}
	snap.Reasons = []string{schedulerObservationGap}
	observe(4, 1, 14)
	if snap.ScaleDown["w1"].Allowed || len(tempo.windows["w1"].samples) != 0 || snap.EmptyNodeScaleDownAllowed {
		t.Fatal("invalid observation retained window")
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
		if fixed {
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
		if fixed {
			t.Fatalf("changeNodes prepended forbidden Fixed lowering: allowed=%v", allowed)
		}
	}
}

func TestSchedulerTempoUIDReplacementResetsVector(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval = time.Second, time.Second
	s.ScaleDownStabilization = time.Second
	snap.Outstanding = nil
	snap.Routing = map[string]map[string]int32{"w1": {"i1": 1000}}
	snap.Instances[0].Routable = map[string]bool{"w1": true}
	start := snap.At
	for second := range 2 {
		snap.At = start.Add(time.Duration(second) * time.Second)
		snap.ObservationEnd, snap.ObservedSince = snap.At, start.Add(-time.Hour)
		snap.Arrivals = nil
		capacityLoad(s, &snap, snap.Workloads[0], 1)
	}
	var tempo SchedulerTempo
	tempo.Observe(s, &snap, capacityRowsForTest(s, snap, nil))
	snap.Workloads[0].UID = "replacement"
	s.AutonomousWorkloads = []string{"replacement"}
	snap.At = snap.At.Add(time.Second)
	snap.ObservationEnd = snap.At
	tempo.Observe(s, &snap, capacityRowsForTest(s, snap, nil))
	if snap.EmptyNodeScaleDownAllowed || snap.ScaleDown["replacement"].Allowed {
		t.Fatal("new UID reused prior coverage")
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
			tempo.Observe(s, &snapshot, nil)
			rows := capacityRowsForTest(s, snapshot, nil)
			decision := capacityDecideForTest(t, s, snapshot, rows)
			if decision.Target != nil || decision.Capacity == nil || decision.Capacity.Search != nil || len(decision.Capacity.Workloads) != 1 {
				t.Fatalf("fixture must return before plan search with timer evidence: %+v", decision)
			}
			down := decision.Capacity.Workloads[0].ScaleDown
			if down == nil || down.ResetReason != schedulerFirstObservation || down.Allowed || decision.Capacity.Period != s.ComparisonPeriod {
				t.Fatalf("early reset evidence missing: %+v", decision.Capacity)
			}
			// A later observed gap records the interruption too, not just startup.
			snapshot.ScaleDown = nil
			snapshot.At = snapshot.At.Add(time.Second)
			snapshot.ObservationEnd = snapshot.At
			snapshot.Reasons = []string{schedulerObservationGap}
			tempo.Observe(s, &snapshot, rows)
			decision = capacityDecideForTest(t, s, snapshot, rows)
			down = decision.Capacity.Workloads[0].ScaleDown
			if down.ResetReason != schedulerObservationGap || down.Allowed {
				t.Fatalf("observation interruption disappeared from decision: %+v", down)
			}
		})
	}
}
