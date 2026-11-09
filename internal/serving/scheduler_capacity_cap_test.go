package serving

import (
	"math"
	"slices"
	"testing"
	"time"

	v2 "github.com/lab-paper-code/chill/internal/scheduler/v2"
)

func TestCapacityEveryProfileVariantRetained(t *testing.T) {
	p := capacityDeltaTiny([]float64{50}, 10)
	for _, mu := range []float64{0, 25, 50, 75} {
		plan := *p.plans[0][0]
		plan.keep, plan.option, plan.record.Key = false, "same-profile-option", time.Duration(mu).String()
		item := plan.items[0][0]
		item.routingWeight = profileRoutingWeight(mu)
		plan.items = [][]capacityItem{{item}}
		p.plans[0] = append(p.plans[0], &plan)
	}
	tab := p.planTable()
	problem := p.scalarProblem(tab, time.Time{})
	v2.Group(problem.Table)
	seen := make(map[int]bool)
	for _, entry := range problem.Table.Entries {
		for _, q := range entry.Variants {
			if seen[q] {
				t.Fatalf("duplicate variant %d", q)
			}
			seen[q] = true
		}
	}
	for _, q := range tab.byPlan[0] {
		if !seen[q] || !tab.searchable[q] {
			t.Fatalf("profile variant %d removed before heuristic scoring", q)
		}
	}
	if len(tab.pruned) != 0 {
		t.Fatalf("unjustified scalar pruning: %v", tab.pruned)
	}
}

func TestCapacityDeltaFrozen130And190ms(t *testing.T) {
	p := capacityDeltaTiny([]float64{50}, 10)
	p.period, p.ws[0].queued, p.ws[0].totalNanos = 1, 2, 20e6
	p.ws[0].d.SLO.TargetLatency = 100 * time.Millisecond
	base := p.plans[0][0]
	for _, spec := range []struct{ mu, service float64 }{{50, 20e6}, {25, 40e6}} {
		plan := *base
		plan.keep, plan.option, plan.preMean, plan.preMax = false, "profile", .05, .05
		item := base.items[0][0]
		item.routingWeight, item.serviceNanos = profileRoutingWeight(spec.mu), spec.service
		plan.items = [][]capacityItem{{item}}
		retained := item
		retained.routingWeight = 10
		plan.retained = [][]capacityItem{{retained}}
		p.plans[0] = append(p.plans[0], &plan)
	}
	tab := p.planTable()
	s := newSearchState(p, tab, []int{tab.byPlan[0][0]})
	problem := p.scalarProblem(tab, time.Time{})
	a := capacityDeltaSaved(capacityRank(s.delta(0, tab.byPlan[0][1]), problem.Table))
	b := capacityDeltaSaved(capacityRank(s.delta(0, tab.byPlan[0][2]), problem.Table))
	if math.Abs(a.Excesses[0]-.3) > 1e-14 || math.Abs(b.Excesses[0]-.9) > 1e-14 || !a.Better(&b) || b.Better(&a) {
		t.Fatalf("frozen h130/h190 ordering: A=%+v B=%+v", a, b)
	}
	s.apply(0, tab.byPlan[0][2])
	if !slices.Equal(s.choice, []int{tab.byPlan[0][2]}) || s.sums[0].routingRate != 25 || s.sums[0].serviceNanos != 25*40e6 {
		t.Fatalf("apply lost frozen sums: %+v", s.sums)
	}
}

func TestCapacityDeltaFrozenWorkloadPriority(t *testing.T) {
	for _, strategy := range []string{SLOPriorityExcess, SLOPriorityWorkload} {
		p := capacityDeltaTiny([]float64{10}, 1)
		p.ws[0].d.UID = "b"
		p.ws[0].d.SLO.TargetLatency = 100 * time.Millisecond
		a := *p.ws[0].d
		a.UID = "a"
		p.ws = append(p.ws, capacityWorkload{d: &a, rate: 1})
		p.index = map[string]int{"b": 0, "a": 1}
		p.settings.SLOPriorityStrategy, p.settings.WorkloadPriority = strategy, []string{"a"}
		makePlan := func(bService, aService float64, key string) *capacityPlan {
			return &capacityPlan{items: [][]capacityItem{
				{{id: "b", routingWeight: 10, serviceNanos: bService}},
				{{id: "a", routingWeight: 10, serviceNanos: aService}},
			}, retained: make([][]capacityItem, 2), record: SchedulerNodePlan{Key: key, Eligible: true}}
		}
		p.plans[0] = []*capacityPlan{makePlan(190e6, 130e6, "A"), makePlan(150e6, 140e6, "B")}
		tab := p.planTable()
		problem := p.scalarProblem(tab, time.Time{})
		s := newSearchState(p, tab, []int{tab.byPlan[0][0]})
		first := capacityDeltaSaved(capacityRank(s.delta(0, tab.byPlan[0][0]), problem.Table))
		second := capacityDeltaSaved(capacityRank(s.delta(0, tab.byPlan[0][1]), problem.Table))
		if first.Better(&second) != (strategy == SLOPriorityWorkload) {
			t.Fatalf("strategy=%s A=%v B=%v", strategy, first.Excesses, second.Excesses)
		}
	}
}
