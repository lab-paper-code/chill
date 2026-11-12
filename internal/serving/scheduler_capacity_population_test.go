package serving

import (
	"fmt"
	"testing"
	"time"
)

func capacityDelayedArrivalPlanner(t *testing.T) *capacityPlanner {
	t.Helper()
	p := capacityDeltaTiny([]float64{1, 0}, 10)
	p.settings = capacityTestSettings
	p.settings.PlanSearchStrategy = "v2"
	p.settings.ComparisonPeriod, p.periodSeconds = time.Second, 1
	p.ws[0].d.SLO = SLO{Percentile: 95, TargetLatency: 300 * time.Millisecond}
	p.plans[1][0].items, p.plans[1][0].retained = [][]capacityItem{nil}, [][]capacityItem{nil}
	target := capacityItem{id: "target", routingWeight: 20, throughput: 20, weight: 1}
	next := &capacityPlan{items: [][]capacityItem{{target}}, retained: [][]capacityItem{nil}, record: SchedulerNodePlan{Eligible: true, Key: "target"}}
	p.plans[1] = append(p.plans[1], next)
	capacityRawPlannerForTest(p)
	p.ws[0].d.SLO.Percentile = 95
	p.snapshot.Arrivals = nil
	start := p.snapshot.ObservationEnd.Add(-p.settings.ObservationWindow)
	for j := range 10 {
		p.snapshot.Arrivals = append(p.snapshot.Arrivals, SchedulerArrival{ID: fmt.Sprint(j), Definition: p.ws[0].d, At: start.Add(200*time.Millisecond + time.Duration(j)*80*time.Millisecond), Compute: true, Accepted: true})
	}
	i := next.instances[0]
	prepare := poolsCost(schedulerActionPrepare, i.Node, "unloaded", schedulerState(i), p.snapshot.Nodes[1].PowerMode)
	prepare.Trials = []OperationTrial{{Duration: 100 * time.Millisecond, Joules: 1}}
	old := p.snapshot.Instances[0]
	remove := poolsCost(schedulerActionRemove, old.Node, schedulerState(old), "unloaded", p.snapshot.Nodes[0].PowerMode)
	remove.Trials = []OperationTrial{{Duration: time.Millisecond, Joules: 1}}
	p.snapshot.Costs = []OperationCost{prepare, remove}
	p.resolveCapacity()
	empty := p.plan(0, p.snapshot.Nodes[0].PowerMode, p.plans[0][0].instances, nil)
	c := capacityChoice{option: poolsOption(i), w: 0}
	c.routingWeight, c.throughput, c.serviceNanos, c.reason = p.profileTerms(i, p.ws[0].d)
	prepared := p.plan(1, p.snapshot.Nodes[1].PowerMode, nil, []capacityChoice{c})
	if empty == nil || prepared == nil {
		t.Fatal("production transition missing")
	}
	p.plans[0] = append(p.plans[0], empty)
	p.plans[1][1] = prepared
	p.compared = []bool{true, true}
	return p
}

func TestCapacityHeuristicUsesExactArrivalPopulation(t *testing.T) {
	p := capacityDelayedArrivalPlanner(t)
	choice := []int{1, 1}
	route := p.routeOf(choice)
	if route != .1 {
		t.Fatalf("prepare route=%g want .1", route)
	}
	exact := p.evaluate(choice, route)
	if exact.exclusion != "" || !exact.feasible {
		t.Fatalf("exact transition must serve delayed cohort: %+v", exact)
	}
	tab := p.planTable()
	flat := []int{tab.byPlan[0][1], tab.byPlan[1][1]}
	got := newSearchState(p, tab, flat).delta(0, flat[0])
	if !got.feasible {
		t.Fatalf("invented preparation arrivals excluded exact-feasible target: excess=%v exact=%+v", got.excesses, exact.feedback)
	}
}

func TestCapacityHeuristicBacklogUsesExactArrivalPopulation(t *testing.T) {
	p := capacityDelayedArrivalPlanner(t)
	p.ws[0].d.SLO.TargetLatency = 80 * time.Millisecond
	exact := p.evaluate([]int{1, 1}, .1)
	if exact.exclusion != "" || !exact.feasible {
		t.Fatalf("exact delayed cohort must finish in 50ms: %+v", exact)
	}
	tab := p.planTable()
	flat := []int{tab.byPlan[0][1], tab.byPlan[1][1]}
	got := newSearchState(p, tab, flat).delta(0, flat[0])
	if !got.feasible {
		t.Fatalf("invented preparation backlog excluded exact-feasible target: excess=%v", got.excesses)
	}
}
