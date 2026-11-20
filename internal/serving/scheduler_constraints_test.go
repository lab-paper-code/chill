package serving

import (
	"math"
	"testing"
)

func TestSchedulerTransitionReserveSettings(t *testing.T) {
	s, _ := schedulerFixture()
	margin := 0.0
	in := SchedulerSettingsInput{SchedulerSettings: s, CompletionMarginMS: &margin}
	got, err := in.Settings()
	if err != nil || got.TransitionReserveFraction != 0 {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	for _, f := range []float64{0, 1, .25, -.1, 1.1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		s.TransitionReserveFraction = f
		if (s.Validate() == nil) != (schedulerFinite(f) && f >= 0 && f <= 1) {
			t.Fatalf("fraction %v", f)
		}
	}
	s.TransitionReserveFraction = 0
	s.ComparisonPeriod = 0
	if s.Validate() == nil {
		t.Fatal("accepted zero horizon")
	}
}

func TestCapacityCombinedHorizonBoundary(t *testing.T) {
	p := &capacityPlanner{periodSeconds: 60, settings: capacityTestSettings}
	for n := range 2 {
		p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{UID: string(rune('a' + n))}})
		p.plans = append(p.plans, []*capacityPlan{{keep: true, record: SchedulerNodePlan{Eligible: true}}, {record: SchedulerNodePlan{Eligible: true}}})
		p.compared = append(p.compared, true)
	}
	p.plans[0][1].preMax = 10
	p.plans[1][1].postMax = 51
	for _, choice := range [][]int{{1, 0}, {0, 1}} {
		res := p.evaluateEnergyAtWindow(choice, p.routeOf(choice), nil, p.periodSeconds)
		if res.exclusion != "" {
			t.Fatalf("individual chain rejected: %s", res.exclusion)
		}
	}
	choice := []int{1, 1}
	res := p.evaluateEnergyAtWindow(choice, p.routeOf(choice), nil, p.periodSeconds)
	if res.exclusion != "" || !p.admits(&res) || res.period != 60 {
		t.Fatal("combined chain changed the common horizon or eligibility")
	}
	p.plans[1][1].postMax = 50
	res = p.evaluateEnergyAtWindow(choice, p.routeOf(choice), nil, p.periodSeconds)
	if res.exclusion != "" || !p.admits(&res) || res.period != 60 {
		t.Fatal("exact horizon boundary changed the common window rule")
	}
}

func TestTransitionReserveEstimatedSurvivingRate(t *testing.T) {
	uid := "w"
	a, b := NodeReference{UID: "a"}, NodeReference{UID: "b"}
	p := &capacityPlanner{settings: SchedulerSettings{TransitionReserveFraction: .5}, ws: []capacityWorkload{{d: &Definition{UID: uid}, rate: 10}}}
	p.snapshot.Nodes = []SchedulerNode{{Node: a, PowerMode: "fixed"}, {Node: b, PowerMode: "fixed"}}
	p.snapshot.Instances = []SchedulerInstance{{ID: "a", Node: a, WorkloadUID: uid, Ready: true, Routable: map[string]bool{uid: true}}, {ID: "b", Node: b, WorkloadUID: uid, Ready: true, Routable: map[string]bool{uid: true}}}
	itemA, itemB := capacityItem{id: "a", routingWeight: 10, throughput: 10}, capacityItem{id: "b", routingWeight: 20, throughput: 20}
	p.plans = [][]*capacityPlan{
		{{keep: true, mode: "fixed", items: [][]capacityItem{{itemA}}, retained: [][]capacityItem{{itemA}}}, {mode: "fixed", items: [][]capacityItem{{}}, retained: [][]capacityItem{{itemA}}}},
		{{keep: true, mode: "fixed", items: [][]capacityItem{{itemB}}, retained: [][]capacityItem{{itemB}}}},
	}
	choice := []int{1, 0}
	check := func(want bool) {
		t.Helper()
		records := p.transitionReserve(choice)
		if len(records) != 1 || records[0].Allowed != want || records[0].RequiredRate != 15 || records[0].BeforeRouteRate < 0 || records[0].AfterRouteRate < 0 {
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
