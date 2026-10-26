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
	if got := observe(5, 6); got.Allowed || got.Elapsed != 0 {
		t.Fatalf("rise below baseline: %+v", got)
	}
	if got := observe(8, 6); !got.Allowed {
		t.Fatalf("new boundary: %+v", got)
	}
	if got := observe(30, 5); got.Allowed || got.DownSince != nil {
		t.Fatalf("gap: %+v", got)
	}
	observe(31, 4)
	s.ScaleDownStabilization = 4 * time.Second
	if got := observe(32, 4); got.Allowed || got.DownSince != nil {
		t.Fatalf("settings reset: %+v", got)
	}
	tempo = SchedulerTempo{}
	if got := observe(33, 3); got.Allowed || got.DownSince != nil {
		t.Fatalf("restart: %+v", got)
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
