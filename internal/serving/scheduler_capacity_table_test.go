package serving

import (
	"reflect"
	"slices"
	"testing"
	"time"

	v2 "github.com/lab-paper-code/chill/internal/scheduler/v2"
)

func TestCapacityTableZeroRoutingDoesNotPruneProfile(t *testing.T) {
	makePlan := func(keep, eligible bool, w int) *capacityPlan {
		items := make([][]capacityItem, 2)
		items[w] = []capacityItem{{id: "zero", prepare: true}}
		return &capacityPlan{keep: keep, option: string(rune('a' + w)), items: items, record: SchedulerNodePlan{Eligible: eligible}}
	}
	p := &capacityPlanner{ws: []capacityWorkload{{d: &Definition{UID: "a", SLO: SLO{TargetLatency: time.Second}}, rate: 2}, {d: &Definition{UID: "b", SLO: SLO{TargetLatency: time.Second}}}}, plans: [][]*capacityPlan{{
		makePlan(true, true, 0), makePlan(false, true, 0), makePlan(false, true, 1), makePlan(false, false, 0),
	}}}
	p.snapshot.Nodes = []SchedulerNode{{}}
	p.compared = []bool{true}
	tab := p.planTable()
	problem := p.scalarProblem(tab, time.Time{})
	v2.Group(problem.Table)
	if len(problem.Table.Entries) != 3 || len(tab.pruned) != 0 {
		t.Fatalf("entries=%d pruned=%v", len(problem.Table.Entries), tab.pruned)
	}
	for j, plan := range p.plans[0] {
		if plan.record.Eligible != (j != 3) {
			t.Errorf("plan %d changed Eligible", j)
		}
	}
}

func TestCapacityTableScalars(t *testing.T) {
	item := capacityItem{id: "new", routingWeight: 3, serviceNanos: 7e6, e: 5, weight: 7, prepare: true}
	retained := capacityItem{id: "old", routingWeight: 11, e: 13, weight: 17}
	plan := &capacityPlan{option: "option", mode: "mode", items: [][]capacityItem{{item}}, retained: [][]capacityItem{{retained}},
		others: 2, preMean: 19, preMax: 23, postMean: 29, postMax: 31, preJ: 37, postJ: 41,
		before: 43, mid: 47, after: 53, replaces: true, reduces: true,
		record: SchedulerNodePlan{Eligible: true, RequiresBridge: []string{"bridge"}}}
	p := &capacityPlanner{ws: []capacityWorkload{{rate: 1}}, plans: [][]*capacityPlan{{{keep: true}, plan}}}
	tab := p.planTable()
	q := tab.byPlan[0][1]
	got := []float64{tab.routingRate[q][0], tab.routingRateServiceNanos[q][0], tab.energyRate[q][0], tab.retainedRoutingRate[q][0], tab.weightRet[q][0], tab.energyWeightRet[q][0],
		tab.before[q], tab.mid[q], tab.after[q], tab.tauPre[q], tab.tPre[q], tab.tauPost[q]}
	want := []float64{3, 21e6, 15, 11, 17, 221, 43, 47, 53, 19, 23, 29}
	if !slices.Equal(got, want) || tab.count[q][0] != 1 || tab.others[q] != 2 || !tab.nonnegative[q] {
		t.Fatalf("scalar extraction: got=%v want=%v table=%+v", got, want, tab)
	}
}

func TestCapacityTableVariants(t *testing.T) {
	for mix, rates := range e5Mixes {
		p := e5Planner(rates, evaluationScaled(2, 0), e5Start)
		tab := p.planTable()
		problem := p.scalarProblem(tab, time.Time{})
		v2.Group(problem.Table)
		seen := make([]int, len(tab.index))
		for _, entry := range problem.Table.Entries {
			first := p.plans[entry.Node][tab.index[entry.Variants[0]]]
			for _, q := range entry.Variants {
				plan := p.plans[entry.Node][tab.index[q]]
				seen[q]++
				a := []float64{first.preMean, first.preMax, first.postMean, first.postMax, first.preJ, first.postJ, first.before, first.mid, first.after}
				b := []float64{plan.preMean, plan.preMax, plan.postMean, plan.postMax, plan.preJ, plan.postJ, plan.before, plan.mid, plan.after}
				if !slices.Equal(a, b) || !reflect.DeepEqual(first.retained, plan.retained) || len(first.instances) != len(plan.instances) || first.others != plan.others {
					t.Fatalf("mix %d option %q: cap invariant differs: first=%+v next=%+v", mix, entry.Option, first, plan)
				}
				for w := range first.items {
					if len(first.items[w]) != len(plan.items[w]) {
						t.Fatalf("mix %d option %q: item count differs", mix, entry.Option)
					}
					for k, item := range first.items[w] {
						other := plan.items[w][k]
						if item.e != other.e || item.weight != other.weight || item.prepare != other.prepare {
							t.Fatalf("mix %d option %q: non-cap item fields differ: %+v vs %+v", mix, entry.Option, item, other)
						}
					}
				}
			}
		}
		for q, searchable := range problem.Table.Searchable {
			if searchable && seen[q] != 1 || !searchable && seen[q] != 0 {
				t.Fatalf("mix %d variant %d: searchable=%t visits=%d", mix, q, searchable, seen[q])
			}
		}
	}
}
