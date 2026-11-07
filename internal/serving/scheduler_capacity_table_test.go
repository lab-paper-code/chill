package serving

import (
	"reflect"
	"slices"
	"testing"
)

func TestCapacityTableZeroCapacity(t *testing.T) {
	makePlan := func(keep, eligible bool, w int) *capacityPlan {
		items := make([][]capacityItem, 2)
		items[w] = []capacityItem{{id: "zero", prepare: true}}
		return &capacityPlan{keep: keep, option: string(rune('a' + w)), items: items, record: SchedulerNodePlan{Eligible: eligible}}
	}
	p := &capacityPlanner{ws: []capacityWorkload{{rate: 2}, {rate: 0}}, plans: [][]*capacityPlan{{
		makePlan(true, true, 0), makePlan(false, true, 0), makePlan(false, true, 1), makePlan(false, false, 0),
	}}}
	tab := p.planTable()
	if len(tab.entries) != 2 || tab.pruned[capacityZeroCapacity] != 1 {
		t.Fatalf("entries=%d pruned=%v", len(tab.entries), tab.pruned)
	}
	for j, plan := range p.plans[0] {
		want := ""
		if j == 1 {
			want = capacityZeroCapacity
		}
		if plan.record.Dominated != want || tab.dominated[tab.byPlan[0][j]] != want {
			t.Errorf("plan %d: dominated=%q want=%q", j, plan.record.Dominated, want)
		}
		if plan.record.Eligible != (j != 3) {
			t.Errorf("plan %d changed Eligible", j)
		}
	}
}

func TestCapacityTableScalars(t *testing.T) {
	item := capacityItem{id: "new", kappa: 3, e: 5, weight: 7, prepare: true}
	retained := capacityItem{id: "old", kappa: 11, e: 13, weight: 17}
	plan := &capacityPlan{option: "option", mode: "mode", items: [][]capacityItem{{item}}, retained: [][]capacityItem{{retained}},
		others: 2, preMean: 19, preMax: 23, postMean: 29, postMax: 31, preJ: 37, postJ: 41,
		before: 43, mid: 47, after: 53, replaces: true, reduces: true,
		record: SchedulerNodePlan{Eligible: true, RequiresBridge: []string{"bridge"}}}
	p := &capacityPlanner{ws: []capacityWorkload{{rate: 1}}, plans: [][]*capacityPlan{{{keep: true}, plan}}}
	tab := p.planTable()
	q := tab.byPlan[0][1]
	got := []float64{tab.kappa[q][0], tab.kappaE[q][0], tab.kappaRet[q][0], tab.weightRet[q][0], tab.energyWeightRet[q][0],
		tab.joules[q], tab.before[q], tab.mid[q], tab.after[q], tab.tauPre[q], tab.tPre[q], tab.tauPost[q], tab.tPost[q]}
	want := []float64{3, 15, 11, 17, 221, 78, 43, 47, 53, 19, 23, 29, 31}
	if !slices.Equal(got, want) || tab.count[q][0] != 1 || tab.others[q] != 2 || tab.items[q][0][0] != item ||
		!tab.replaces[q] || !tab.bridge[q] || !tab.slow[q] {
		t.Fatalf("scalar extraction: got=%v want=%v table=%+v", got, want, tab)
	}
}

func TestCapacityTableVariants(t *testing.T) {
	for mix, rates := range e5Mixes {
		p := e5Planner(rates, evaluationScaled(2, 0), e5Start, nil)
		tab := p.planTable()
		for _, entry := range tab.entries {
			first := p.plans[entry.node][tab.index[entry.variants[0]]]
			previousK, previousJ := -1.0, -1
			for _, q := range entry.variants {
				plan := p.plans[entry.node][tab.index[q]]
				if entry.workload >= 0 {
					k := tab.kappa[q][entry.workload]
					if k < previousK || k == previousK && tab.index[q] < previousJ {
						t.Fatalf("mix %d entry %+v not sorted", mix, entry)
					}
					previousK, previousJ = k, tab.index[q]
				}
				a := []float64{first.preMean, first.preMax, first.postMean, first.postMax, first.preJ, first.postJ, first.before, first.mid, first.after}
				b := []float64{plan.preMean, plan.preMax, plan.postMean, plan.postMax, plan.preJ, plan.postJ, plan.before, plan.mid, plan.after}
				if !slices.Equal(a, b) || !reflect.DeepEqual(first.retained, plan.retained) || len(first.instances) != len(plan.instances) || first.others != plan.others {
					t.Fatalf("mix %d option %q: cap invariant differs: first=%+v next=%+v", mix, entry.option, first, plan)
				}
				for w := range first.items {
					if len(first.items[w]) != len(plan.items[w]) {
						t.Fatalf("mix %d option %q: item count differs", mix, entry.option)
					}
					for k, item := range first.items[w] {
						other := plan.items[w][k]
						if item.e != other.e || item.weight != other.weight || item.prepare != other.prepare {
							t.Fatalf("mix %d option %q: non-cap item fields differ: %+v vs %+v", mix, entry.option, item, other)
						}
					}
				}
			}
		}
	}
}
