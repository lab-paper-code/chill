package serving



// planTable owns the Tick's scalar columns. Search needs no capacityPlan reads.
// Workload columns and item lists are indexed [table plan][Workload].
type planTable struct {
	byPlan                       [][]int
	node, index                  []int
	routingRate, routingRateServiceNanos, retainedRoutingRate [][]float64
	weightRet, energyWeightRet   [][]float64
	energyRate, weight, weightedEnergy [][]float64
	count                        [][]int
	pending                      [][]int // replica occupancy; not capacity or extra total occupancy
	nonnegative []bool
	others                       []int
	before, mid, after []float64
	preJ, postJ                  []float64
	tauPre, tauPost, tPre []float64
	keep []bool
	eligible                     []bool
	pruned                       map[string]int
	// Search-only columns are initialized once with the reusable search state.
	searchable           []bool
	excessIndex []int
	priorityCount int
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
	routingRate, service, retainedRate := make([]float64, k), make([]float64, k), make([]float64, k)
	weightRet, energyWeightRet := make([]float64, k), make([]float64, k)
	energyRate, weight, weightedEnergy := make([]float64,k), make([]float64,k), make([]float64,k)
	count:=make([]int,k)
	pending:=make([]int,k)
	nonnegative:=true
	for w := range ws {
		if w < len(plan.items) {count[w]=len(plan.items[w])}
		if w < len(plan.pendingReplicas) {
			pending[w] = plan.pendingReplicas[w]
		}
		var items []capacityItem
		if w<len(plan.items) {items=plan.items[w]}
		for _, item := range items {
			routingRate[w] += float64(item.routingWeight)
			service[w] += float64(item.routingWeight) * item.serviceNanos
			energyRate[w] += float64(item.routingWeight)*item.e
			weight[w] += float64(item.weight)
			weightedEnergy[w] += float64(item.weight)*item.e
			nonnegative=nonnegative && item.e>=0
		}
		if w < len(plan.retained) {
			for _, item := range plan.retained[w] {
				retainedRate[w] += float64(item.routingWeight)
				weightRet[w] += float64(item.weight)
				energyWeightRet[w] += item.e * float64(item.weight)
				nonnegative=nonnegative && item.e>=0
			}
		}
	}
	t.node, t.index = append(t.node, n), append(t.index, j)
	t.routingRate, t.routingRateServiceNanos, t.retainedRoutingRate = append(t.routingRate, routingRate), append(t.routingRateServiceNanos, service), append(t.retainedRoutingRate, retainedRate)
	t.energyRate,t.weight,t.weightedEnergy=append(t.energyRate,energyRate),append(t.weight,weight),append(t.weightedEnergy,weightedEnergy)
	t.weightRet, t.energyWeightRet = append(t.weightRet, weightRet), append(t.energyWeightRet, energyWeightRet)
	t.count,t.others,t.nonnegative=append(t.count,count),append(t.others,plan.others),append(t.nonnegative,nonnegative)
	t.pending = append(t.pending, pending)
	t.preJ, t.postJ = append(t.preJ, plan.preJ), append(t.postJ, plan.postJ)
	t.before, t.mid, t.after = append(t.before, plan.before), append(t.mid, plan.mid), append(t.after, plan.after)
	t.tauPre, t.tauPost = append(t.tauPre, plan.preMean), append(t.tauPost, plan.postMean)
	t.tPre=append(t.tPre,plan.preMax)
	t.keep=append(t.keep,plan.keep)
	t.eligible=append(t.eligible,plan.record.Eligible)
	return q
}
