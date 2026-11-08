package serving

type orderKey struct {
	feasible, final, keep, weightsExceeded bool
	unroutedPrepare, searchable, bounded   bool
	joules, route, keepJoules, period      float64
	excesses, demand, supply               []float64
	satisfied                              []bool
	instances, countExcess                 int
	choice                                 []int // table indices, not v1 plan indices
	table                                  *planTable
}


type searchSum struct {
	kappa, kappaE, kappaRet, weightRet, energyWeightRet float64
	count, pending, slow                                int
}

// searchState owns all mutable storage for one sequential search. The table is
// immutable after initSearch. Scratch writes in delta never change its choice,
// sums, timing list or route. Item capacity covers even multi-instance keep.
type searchState struct {
	planner   *capacityPlanner
	table     *planTable
	choice    []int
	sums      []searchSum
	workloads []searchWorkload
	total     int
	tPre      []float64
	r         float64
	nodeJ     []float64
	items     []capacityItem
	weights   []int32
	load      []float64
	result    orderKey
}

func newSearchState(p *capacityPlanner, t *planTable, choice []int) *searchState {
	t.initSearch(p)
	n, w, size := len(p.plans), len(p.ws), 0
	for _, plans := range t.byPlan {
		largest := 0
		for _, q := range plans {
			for _, count := range t.count[q] {
				largest = max(largest, count)
			}
		}
		size += largest
	}
	s := &searchState{planner: p, table: t, choice: make([]int, n), sums: make([]searchSum, w),
		tPre: make([]float64, n), nodeJ: make([]float64, n), items: make([]capacityItem, size),
		weights: make([]int32, size), load: make([]float64, size), workloads: make([]searchWorkload, w),
		result: orderKey{choice: make([]int, n), excesses: make([]float64, w), table: t,
			demand: make([]float64, w), supply: make([]float64, w), satisfied: make([]bool, w)}}
	s.reset(choice)
	return s
}

func (s *searchState) reset(choice []int) {
	copy(s.choice, choice)
	s.recount()
}

// recount uses canonical Node/item order. Repeated subtract/add would accumulate
// rounding error, which can flip better on an otherwise exact energy tie.
func (s *searchState) recount() {
	s.total, s.r = 0, 0
	for n, q := range s.choice {
		s.tPre[n] = 0
		if !s.table.keep[q] {
			s.tPre[n] = s.table.tPre[q]
		}
		s.r = max(s.r, s.tPre[n])
		s.total += s.table.others[q]
	}
	for w := range s.sums {
		s.recountWorkload(w)
		s.total += s.sums[w].count
		s.workloads[w] = s.workloadTerms(w, -1, 0)
	}
	for n, q := range s.choice {
		if s.planner.compared[n] {
			s.nodeJ[n] = s.table.energy(q, s.r, s.planner.period)
		}
	}
}

func (s *searchState) recountWorkload(w int) {
	s.sums[w] = searchSum{}
	sum := &s.sums[w]
	for _, q := range s.choice {
		sum.pending += s.table.pending[q][w]
		for _, item := range s.table.items[q][w] {
			sum.kappa += item.kappa
			sum.kappaE += item.kappa * item.e
			sum.count++
		}
		for _, item := range s.table.retained[q][w] {
			sum.kappaRet += item.kappa
			sum.weightRet += float64(item.weight)
			sum.energyWeightRet += item.e * float64(item.weight)
		}
		if s.table.slowWorkload[q][w] {
			sum.slow++
		}
	}
}

func (s *searchState) apply(n, q int) {
	t, old, previousRoute := s.table, s.choice[n], s.r
	if old == q {
		return
	}
	s.choice[n] = q
	s.total += t.others[q] - t.others[old]
	for w := range s.sums {
		if t.sameWorkload(old, q, w) {
			continue
		}
		s.total += t.count[q][w] - t.count[old][w]
		s.recountWorkload(w)
		s.workloads[w] = s.workloadTerms(w, -1, 0)
	}
	s.tPre[n] = 0
	if !t.keep[q] {
		s.tPre[n] = t.tPre[q]
	}
	s.r = 0
	for _, pre := range s.tPre {
		s.r = max(s.r, pre)
	}
	for node, current := range s.choice {
		if s.planner.compared[node] && (node == n || s.r != previousRoute && !t.keep[current]) {
			s.nodeJ[node] = t.energy(current, s.r, s.planner.period)
		}
	}
}

// delta evaluates a one-Node move without allocation. r changes invalidate all
// transitioning Node energies. Otherwise only the moved Node is recalculated;
// the final fold always follows v1's order, avoiding a subtract/add ulp drift.
func (s *searchState) delta(n, q int) orderKey {
	t, p := s.table, s.planner
	key := s.result
	key.feasible, key.final, key.keep, key.searchable, key.bounded = true, true, true, true, true
	key.weightsExceeded, key.unroutedPrepare = false, false
	key.joules, key.route = 0, 0
	key.period, key.keepJoules = p.period, p.keepWatts*p.period
	key.countExcess = 0
	for node, pre := range s.tPre {
		if node == n {
			pre = 0
			if !t.keep[q] {
				pre = t.tPre[q]
			}
		}
		key.route = max(key.route, pre)
	}
	for node, current := range s.choice {
		if node == n {
			current = q
		}
		key.choice[node] = current
		key.keep = key.keep && t.keep[current]
		key.searchable = key.searchable && t.searchable[current]
		if p.compared[node] {
			energy := s.nodeJ[node]
			if node == n || key.route != s.r && !t.keep[current] {
				energy = t.energy(current, key.route, p.period)
			}
			key.joules += energy
		}
	}
	old, total := s.choice[n], s.total+t.others[q]-t.others[s.choice[n]]
	for w, ws := range p.ws {
		count := s.sums[w].count + s.sums[w].pending - t.count[old][w] - t.pending[old][w] + t.count[q][w] + t.pending[q][w]
		total += t.count[q][w] - t.count[old][w]
		key.bounded = key.bounded && count <= p.settings.MaxReplicas && (p.settings.Controls.Replicas || count == ws.current)
		key.countExcess += max(0, count-p.settings.MaxReplicas)
		key.joules += s.workload(w, n, q, key.route, &key)
	}
	if key.keep {
		key.keepJoules = key.joules
	}
	key.bounded = key.bounded && total <= p.settings.MaxInstances
	key.instances = total
	key.countExcess += max(0, total-p.settings.MaxInstances)
	for k := 1; k < len(key.excesses); k++ {
		value, at := key.excesses[k], k
		for at > 0 && value > key.excesses[at-1] {
			key.excesses[at] = key.excesses[at-1]
			at--
		}
		key.excesses[at] = value
	}
	return key
}
