package serving

import (
	"cmp"
	"slices"
)

const capacityZeroCapacity = "ZeroCapacity"

// capacityPlanEntry is one node/option/mode. Keep is always a separate entry.
// Variants holds table indices in ascending κ, then original plan index order.
type capacityPlanEntry struct {
	node     int
	option   string
	mode     string
	workload int // -1 for empty or multi-Workload keep
	variants []int
}

// planTable owns the Tick's scalar columns. Search needs no capacityPlan reads.
// Workload columns and item lists are indexed [table plan][Workload].
type planTable struct {
	entries                      []capacityPlanEntry
	byPlan                       [][]int
	nodeOrder                    []int
	entryOrder                   [][]int
	entryOf                      []int
	node, index                  []int
	kappa, kappaE, kappaRet      [][]float64
	weightRet, energyWeightRet   [][]float64
	count                        [][]int
	items                        [][][]capacityItem
	retained                     [][][]capacityItem
	others                       []int
	joules, before, mid, after   []float64
	preJ, postJ                  []float64
	tauPre, tauPost, tPre, tPost []float64
	keep, replaces, bridge, slow []bool
	recovery                     [][]string
	eligible                     []bool
	dominated                    []string
	pruned                       map[string]int
	// Search-only columns are initialized once with the reusable search state.
	searchable           []bool
	slowWorkload, outage [][]bool
	keepInsufficient     []bool
	unsearchable         map[string]int
	keyParts             []string
}

func (p *capacityPlanner) planTable() *planTable {
	t := &planTable{byPlan: make([][]int, len(p.plans)), pruned: map[string]int{}}
	for n, plans := range p.plans {
		t.byPlan[n] = make([]int, len(plans))
		type identity struct{ option, mode string }
		entries := map[identity]int{}
		for j, plan := range plans {
			q := t.appendPlan(n, j, plan, p.ws)
			t.byPlan[n][j] = q
			if j != 0 && (!t.eligible[q] || t.dominated[q] != "") {
				continue
			}
			key := identity{plan.option, plan.mode}
			e, found := entries[key]
			if !found || j == 0 {
				e = len(t.entries)
				w := -1
				for k, count := range t.count[q] {
					if count > 0 {
						w = k
					}
				}
				if j == 0 {
					w = -1
				} else {
					entries[key] = e
				}
				t.entries = append(t.entries, capacityPlanEntry{node: n, option: plan.option, mode: plan.mode, workload: w})
			}
			t.entries[e].variants = append(t.entries[e].variants, q)
		}
	}
	for e := range t.entries {
		entry := &t.entries[e]
		slices.SortFunc(entry.variants, func(a, b int) int {
			ka, kb := 0.0, 0.0
			if entry.workload >= 0 {
				ka, kb = t.kappa[a][entry.workload], t.kappa[b][entry.workload]
			}
			return cmp.Or(cmp.Compare(ka, kb), cmp.Compare(p.plans[entry.node][t.index[a]].record.Key, p.plans[entry.node][t.index[b]].record.Key))
		})
	}
	t.nodeOrder, t.entryOrder, t.entryOf = make([]int, len(p.plans)), make([][]int, len(p.plans)), make([]int, len(t.index))
	for n := range t.nodeOrder {
		t.nodeOrder[n] = n
	}
	slices.SortFunc(t.nodeOrder, func(a, b int) int {
		return cmp.Compare(p.snapshot.Nodes[a].Node.UID, p.snapshot.Nodes[b].Node.UID)
	})
	for e, entry := range t.entries {
		t.entryOrder[entry.node] = append(t.entryOrder[entry.node], e)
		for _, q := range entry.variants {
			t.entryOf[q] = e
		}
	}
	for n := range t.entryOrder {
		slices.SortFunc(t.entryOrder[n], func(a, b int) int {
			ea, eb := t.entries[a], t.entries[b]
			qa, qb := ea.variants[0], eb.variants[0]
			return cmp.Or(cmp.Compare(ea.option, eb.option), cmp.Compare(ea.mode, eb.mode),
				cmp.Compare(p.plans[n][t.index[qa]].record.Key, p.plans[n][t.index[qb]].record.Key),
				cmp.Compare(t.index[qa], t.index[qb]))
		})
	}
	return t
}

func (t *planTable) appendPlan(n, j int, plan *capacityPlan, ws []capacityWorkload) int {
	q, k := len(t.index), len(ws)
	kappa, kappaE, kappaRet := make([]float64, k), make([]float64, k), make([]float64, k)
	weightRet, energyWeightRet := make([]float64, k), make([]float64, k)
	count, items := make([]int, k), make([][]capacityItem, k)
	retained := make([][]capacityItem, k)
	plan.record.Dominated = ""
	for w := range ws {
		if w < len(plan.items) {
			items[w] = slices.Clone(plan.items[w])
		}
		count[w] = len(items[w])
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
	t.retained = append(t.retained, retained)
	t.joules = append(t.joules, plan.preJ+plan.postJ)
	t.preJ, t.postJ = append(t.preJ, plan.preJ), append(t.postJ, plan.postJ)
	t.before, t.mid, t.after = append(t.before, plan.before), append(t.mid, plan.mid), append(t.after, plan.after)
	t.tauPre, t.tauPost = append(t.tauPre, plan.preMean), append(t.tauPost, plan.postMean)
	t.tPre, t.tPost = append(t.tPre, plan.preMax), append(t.tPost, plan.postMax)
	t.keep, t.replaces = append(t.keep, plan.keep), append(t.replaces, plan.replaces)
	t.bridge, t.slow = append(t.bridge, len(plan.record.RequiresBridge) > 0), append(t.slow, plan.record.Tempo == schedulerTempoSlow)
	t.recovery = append(t.recovery, slices.Clone(plan.record.RequiresRecovery))
	t.eligible, t.dominated = append(t.eligible, plan.record.Eligible), append(t.dominated, plan.record.Dominated)
	return q
}
