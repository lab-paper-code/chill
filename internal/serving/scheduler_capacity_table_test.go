package serving

import (
	"context"
	"math"
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
	p := &capacityPlanner{ctx: context.Background(), ws: []capacityWorkload{{d: &Definition{UID: "a", SLO: SLO{TargetLatency: time.Second}}, rate: 2}, {d: &Definition{UID: "b", SLO: SLO{TargetLatency: time.Second}}}}, plans: [][]*capacityPlan{{
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
	item := capacityItem{id: "new", routingWeight: 3, throughput: 2, serviceNanos: 7e6, order: 5, model: schedulerTestModel(5), weight: 7, prepare: true}
	retained := capacityItem{id: "old", routingWeight: 11, throughput: 10, order: 13, model: schedulerTestModel(13), weight: 17}
	plan := &capacityPlan{option: "option", mode: "mode", items: [][]capacityItem{{item}}, retained: [][]capacityItem{{retained}},
		others: 2, preMean: 19, preMax: 23, postMean: 29, postMax: 31, preJ: 37, postJ: 41,
		before: 43, mid: 47, after: 53, replaces: true, reduces: true,
		record: SchedulerNodePlan{Eligible: true, RequiresBridge: []string{"bridge"}}}
	p := &capacityPlanner{ws: []capacityWorkload{{rate: 1}}, plans: [][]*capacityPlan{{{keep: true}, plan}}}
	tab := p.planTable()
	q := tab.byPlan[0][1]
	got := []float64{tab.routingRate[q][0], tab.routingRateServiceNanos[q][0], tab.retainedRoutingRate[q][0], tab.weightRet[q][0],
		tab.before[q], tab.mid[q], tab.after[q], tab.tauPre[q], tab.tPre[q], tab.tauPost[q]}
	want := []float64{2, 14e6, 10, 17, 43, 47, 53, 19, 23, 29}
	// The load columns keep each instance's model in its own slot: the final
	// entry (throughput 2, weight 7) and the retained one (weight 17).
	load := []tableLoad{tab.loadFinal[q][0][0], tab.loadRetained[q][0][0]}
	if !slices.Equal(got, want) || tab.count[q][0] != 1 || tab.others[q] != 2 || len(tab.loadModels[q]) != 2 ||
		load[0] != (tableLoad{slot: 0, throughput: 2, weight: 7}) || load[1] != (tableLoad{slot: 1, throughput: 10, weight: 17}) ||
		tab.loadModels[q][0] != item.model || tab.loadModels[q][1] != retained.model || !slices.Equal(tab.loadWorkloads[q], []int{0}) {
		t.Fatalf("scalar extraction: got=%v want=%v table=%+v", got, want, tab)
	}
}

func TestCapacityProfileTermsThroughputUsesMeanAndOccupancy(t *testing.T) {
	profile := &Profile{Batches: []BatchProfile{
		schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{10e6, 30e6}),
		schedulerTestBatchProfile(BatchProfile{Batch: 2}, []float64{10e6, 50e6}),
	}}
	p := &capacityPlanner{settings: SchedulerSettings{DefaultDispatchOccupancyNanos: int64(10 * time.Millisecond)}}
	d := &Definition{SLO: SLO{Percentile: 99}}
	for _, maxBatch := range []int{1, 2} {
		i := SchedulerInstance{Profile: profile, MaxBatch: maxBatch}
		_, got, _, reason := p.profileTerms(i, d)
		want := 1e9 / (20e6 + 10e6)
		if maxBatch == 2 {
			want = math.Max(want, 2e9/(30e6+2*10e6))
		}
		if reason != "" || math.Abs(got-want) > 1e-12 {
			t.Fatalf("maxBatch=%d throughput=%g want=%g reason=%q", maxBatch, got, want, reason)
		}
		if got == capacityThroughput(profile, maxBatch) {
			t.Fatalf("maxBatch=%d throughput still uses fastest sample: %g", maxBatch, got)
		}
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
						if item.order != other.order || item.weight != other.weight || item.prepare != other.prepare {
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
