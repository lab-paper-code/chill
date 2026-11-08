package serving

import (
	"slices"
)

const capacityZeroCapacity = "ZeroCapacity"

// planTable owns the Tick's scalar columns. Search needs no capacityPlan reads.
// Workload columns and item lists are indexed [table plan][Workload].
type planTable struct {
	byPlan                       [][]int
	node, index                  []int
	kappa, kappaE, kappaRet      [][]float64
	weightRet, energyWeightRet   [][]float64
	count                        [][]int
	pending                      [][]int // replica occupancy; not capacity or extra total occupancy
	items                        [][][]capacityItem
	retained                     [][][]capacityItem
	others                       []int
	joules, before, mid, after   []float64
	preJ, postJ                  []float64
	tauPre, tauPost, tPre, tPost []float64
	keep, replaces, bridge, slow []bool
	eligible                     []bool
	dominated                    []string
	pruned                       map[string]int
	// Search-only columns are initialized once with the reusable search state.
	searchable           []bool
	slowWorkload, outage [][]bool
	keepInsufficient     []bool
	keyParts             []string
}

func (p *capacityPlanner) planTable() *planTable {
	t := &planTable{byPlan: make([][]int, len(p.plans)), pruned: map[string]int{}}
	for n, plans := range p.plans {
		t.byPlan[n] = make([]int, len(plans))
		for j, plan := range plans {
			t.byPlan[n][j] = t.appendPlan(n, j, plan, p.ws)
		}
	}
	return t
}

func (t *planTable) appendPlan(n, j int, plan *capacityPlan, ws []capacityWorkload) int {
	q, k := len(t.index), len(ws)
	kappa, kappaE, kappaRet := make([]float64, k), make([]float64, k), make([]float64, k)
	weightRet, energyWeightRet := make([]float64, k), make([]float64, k)
	count, items := make([]int, k), make([][]capacityItem, k)
	pending := make([]int, k)
	retained := make([][]capacityItem, k)
	plan.record.Dominated = ""
	for w := range ws {
		if w < len(plan.items) {
			items[w] = slices.Clone(plan.items[w])
		}
		count[w] = len(items[w])
		if w < len(plan.pendingReplicas) {
			pending[w] = plan.pendingReplicas[w]
		}
		for _, item := range items[w] {
			kappa[w] += item.kappa
			kappaE[w] += item.kappa * item.e
			if !plan.keep && plan.record.Eligible && item.kappa == 0 && ws[w].rate > 0 {
				plan.record.Dominated = capacityZeroCapacity
			}
		}
		if w < len(plan.retained) {
			retained[w] = slices.Clone(plan.retained[w])
			for _, item := range plan.retained[w] {
				kappaRet[w] += item.kappa
				weightRet[w] += float64(item.weight)
				energyWeightRet[w] += item.e * float64(item.weight)
			}
		}
	}
	if plan.record.Dominated != "" {
		t.pruned[plan.record.Dominated]++
	}
	t.node, t.index = append(t.node, n), append(t.index, j)
	t.kappa, t.kappaE, t.kappaRet = append(t.kappa, kappa), append(t.kappaE, kappaE), append(t.kappaRet, kappaRet)
	t.weightRet, t.energyWeightRet = append(t.weightRet, weightRet), append(t.energyWeightRet, energyWeightRet)
	t.count, t.items, t.others = append(t.count, count), append(t.items, items), append(t.others, plan.others)
	t.pending = append(t.pending, pending)
	t.retained = append(t.retained, retained)
	t.joules = append(t.joules, plan.preJ+plan.postJ)
	t.preJ, t.postJ = append(t.preJ, plan.preJ), append(t.postJ, plan.postJ)
	t.before, t.mid, t.after = append(t.before, plan.before), append(t.mid, plan.mid), append(t.after, plan.after)
	t.tauPre, t.tauPost = append(t.tauPre, plan.preMean), append(t.tauPost, plan.postMean)
	t.tPre, t.tPost = append(t.tPre, plan.preMax), append(t.tPost, plan.postMax)
	t.keep, t.replaces = append(t.keep, plan.keep), append(t.replaces, plan.replaces)
	t.bridge, t.slow = append(t.bridge, len(plan.record.RequiresBridge) > 0), append(t.slow, plan.reduces)
	t.eligible, t.dominated = append(t.eligible, plan.record.Eligible), append(t.dominated, plan.record.Dominated)
	return q
}
