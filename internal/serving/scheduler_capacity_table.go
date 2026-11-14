package serving

// planTable owns the Tick's scalar columns. Search needs no capacityPlan reads.
// Workload columns and item lists are indexed [table plan][Workload].
// Rate columns sum surrogate profile throughput, independent of routing weights.
type planTable struct {
	byPlan                                                    [][]int
	node, index                                               []int
	routingRate, routingRateServiceNanos, retainedRoutingRate [][]float64
	retainedRoutingRateServiceNanos                           [][]float64
	weightRet, weight                                         [][]float64
	count                                                     [][]int
	pending                                                   [][]int // replica occupancy; not capacity or extra total occupancy
	others                                                    []int
	before, mid, after                                        []float64
	preJ, postJ                                               []float64
	tauPre, tauPost, tPre                                     []float64
	keep                                                      []bool
	eligible                                                  []bool
	pruned                                                    map[string]int
	// Load columns: a plan's instance power models (slots) and, per Workload,
	// the final and retained entries that route inputs to those slots.
	loadModels    [][]*schedulerLoadModel
	loadFinal     [][][]tableLoad
	loadRetained  [][][]tableLoad
	loadWorkloads [][]int
	// Search-only columns are initialized once with the reusable search state.
	searchable    []bool
	arrivals      [][]float64 // exact cohort's sorted arrival offsets, nanoseconds
	excessIndex   []int
	priorityCount int
	keyParts      []string
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

// tableLoad is one routed entry of a plan: its instance slot, profile
// throughput and route weight.
type tableLoad struct {
	slot               int
	throughput, weight float64
}

func (t *planTable) appendPlan(n, j int, plan *capacityPlan, ws []capacityWorkload) int {
	q, k := len(t.index), len(ws)
	routingRate, service, retainedRate := make([]float64, k), make([]float64, k), make([]float64, k)
	retainedService := make([]float64, k)
	weightRet, weight := make([]float64, k), make([]float64, k)
	count := make([]int, k)
	pending := make([]int, k)
	var models []*schedulerLoadModel
	slots := map[string]int{}
	slot := func(item capacityItem) int {
		s, ok := slots[item.id]
		if !ok {
			s = len(models)
			slots[item.id] = s
			models = append(models, item.model)
		}
		return s
	}
	final, retained := make([][]tableLoad, k), make([][]tableLoad, k)
	var workloads []int
	for w := range ws {
		if w < len(plan.items) {
			count[w] = len(plan.items[w])
		}
		if w < len(plan.pendingReplicas) {
			pending[w] = plan.pendingReplicas[w]
		}
		var items []capacityItem
		if w < len(plan.items) {
			items = plan.items[w]
		}
		for _, item := range items {
			routingRate[w] += item.throughput
			service[w] += item.throughput * item.serviceNanos
			weight[w] += float64(item.weight)
			final[w] = append(final[w], tableLoad{slot: slot(item), throughput: item.throughput, weight: float64(item.weight)})
		}
		if w < len(plan.retained) {
			for _, item := range plan.retained[w] {
				retainedRate[w] += item.throughput
				retainedService[w] += item.throughput * item.serviceNanos
				weightRet[w] += float64(item.weight)
				retained[w] = append(retained[w], tableLoad{slot: slot(item), throughput: item.throughput, weight: float64(item.weight)})
			}
		}
		if len(final[w]) > 0 || len(retained[w]) > 0 {
			workloads = append(workloads, w)
		}
	}
	t.node, t.index = append(t.node, n), append(t.index, j)
	t.routingRate, t.routingRateServiceNanos, t.retainedRoutingRate = append(t.routingRate, routingRate), append(t.routingRateServiceNanos, service), append(t.retainedRoutingRate, retainedRate)
	t.retainedRoutingRateServiceNanos = append(t.retainedRoutingRateServiceNanos, retainedService)
	t.weight, t.weightRet = append(t.weight, weight), append(t.weightRet, weightRet)
	t.loadModels, t.loadFinal, t.loadRetained = append(t.loadModels, models), append(t.loadFinal, final), append(t.loadRetained, retained)
	t.loadWorkloads = append(t.loadWorkloads, workloads)
	t.count, t.others = append(t.count, count), append(t.others, plan.others)
	t.pending = append(t.pending, pending)
	t.preJ, t.postJ = append(t.preJ, plan.preJ), append(t.postJ, plan.postJ)
	t.before, t.mid, t.after = append(t.before, plan.before), append(t.mid, plan.mid), append(t.after, plan.after)
	t.tauPre, t.tauPost = append(t.tauPre, plan.preMean), append(t.tauPost, plan.postMean)
	t.tPre = append(t.tPre, plan.preMax)
	t.keep = append(t.keep, plan.keep)
	t.eligible = append(t.eligible, plan.record.Eligible)
	return q
}
