package serving

import (
	"cmp"
	"math"
	"slices"
)

type orderKey struct {
	feasible, final, keep, weightsExceeded bool
	unroutedPrepare, searchable, bounded   bool
	joules, route, keepJoules, period      float64
	excesses, demand, supply               []float64
	satisfied                              []bool
	instances, countExcess                 int
	choice                                 []int
	table                                  *planTable
}

type searchSum struct {
	routingRate, retainedRoutingRate, serviceNanos                     float64
	energyRate, weight, weightedEnergy, retainedWeight, retainedEnergy float64
	count, pending                                                     int
}

// searchState keeps additive Tick-fixed terms. Delta never queries raw service
// distributions, allocates integer routes, or invokes demand replay.
type searchState struct {
	planner *capacityPlanner
	table   *planTable
	choice  []int
	sums    []searchSum
	total   int
	tPre    []float64
	r       float64
	result  orderKey
}

func newSearchState(p *capacityPlanner, t *planTable, choice []int) *searchState {
	t.initSearch(p)
	n, w := len(p.plans), len(p.ws)
	s := &searchState{planner: p, table: t, choice: make([]int, n), sums: make([]searchSum, w), tPre: make([]float64, n),
		result: orderKey{choice: make([]int, n), excesses: make([]float64, w), demand: make([]float64, w), supply: make([]float64, w), satisfied: make([]bool, w), table: t}}
	s.reset(choice)
	return s
}

func (s *searchState) reset(choice []int) { copy(s.choice, choice); s.recount() }
func (s *searchState) recount() {
	clear(s.sums)
	s.total, s.r = 0, 0
	for n, q := range s.choice {
		s.tPre[n] = s.table.tPre[q]
		if s.table.keep[q] {
			s.tPre[n] = 0
		}
		s.r = max(s.r, s.tPre[n])
		s.total += s.table.others[q]
		for w := range s.sums {
			s.addPlan(&s.sums[w], q, w, 1)
			s.total += s.table.count[q][w]
		}
	}
}
func (s *searchState) addPlan(sum *searchSum, q, w int, sign float64) {
	t := s.table
	sum.routingRate += sign * t.routingRate[q][w]
	sum.retainedRoutingRate += sign * t.retainedRoutingRate[q][w]
	sum.serviceNanos += sign * t.routingRateServiceNanos[q][w]
	sum.energyRate += sign * t.energyRate[q][w]
	sum.weight += sign * t.weight[q][w]
	sum.weightedEnergy += sign * t.weightedEnergy[q][w]
	sum.retainedWeight += sign * t.weightRet[q][w]
	sum.retainedEnergy += sign * t.energyWeightRet[q][w]
	sum.count += int(sign) * t.count[q][w]
	sum.pending += int(sign) * t.pending[q][w]
}

// Rebase committed moves in canonical order. This avoids accumulated roundoff
// changing a choice's rank after Reset; hot Delta uses additive scalars only.
func (s *searchState) apply(n, q int) {
	if s.choice[n] == q {
		return
	}
	s.choice[n] = q
	s.recount()
}

func (s *searchState) delta(n, q int) orderKey {
	p, t := s.planner, s.table
	key := s.result
	key.feasible, key.final, key.keep, key.searchable, key.bounded = true, true, true, true, true
	key.weightsExceeded, key.unroutedPrepare = false, false
	key.joules, key.route, key.countExcess = 0, 0, 0
	key.period, key.keepJoules = p.period, p.keepWatts*p.period
	for node, current := range s.choice {
		if node == n {
			current = q
		}
		key.choice[node] = current
		key.keep = key.keep && t.keep[current]
		key.searchable = key.searchable && t.searchable[current]
		if !t.keep[current] {
			key.route = max(key.route, t.tPre[current])
		}
	}
	for node, current := range key.choice {
		if p.compared[node] {
			key.joules += t.energy(current, key.route, p.period)
		}
	}
	total := s.total + t.others[q] - t.others[s.choice[n]]
	for w, ws := range p.ws {
		sum := s.sums[w]
		s.addPlan(&sum, s.choice[n], w, -1)
		s.addPlan(&sum, q, w, 1)
		total += sum.count - s.sums[w].count
		count := sum.count + sum.pending
		key.bounded = key.bounded && count <= p.settings.MaxReplicas && (p.settings.Controls.Replicas || count == ws.current)
		key.countExcess += max(0, count-p.settings.MaxReplicas)
		excess, eligible := capacityHeuristic(ws, sum.routingRate, sum.retainedRoutingRate, sum.serviceNanos, key.route, p.period)
		key.excesses[t.excessIndex[w]], key.satisfied[w] = excess, excess == 0 && eligible
		key.demand[w], key.supply[w] = ws.rate, sum.routingRate
		key.feasible = key.feasible && key.satisfied[w]
		key.final = key.feasible
		key.searchable = key.searchable && eligible
		mean := 0.0
		if p.settings.Controls.Routing && sum.routingRate > 0 {
			mean = sum.energyRate / sum.routingRate
		} else if sum.weight > 0 {
			mean = sum.weightedEnergy / sum.weight
		}
		initial := 0.0
		if sum.retainedWeight > 0 {
			initial = min(ws.rate, sum.retainedRoutingRate) * sum.retainedEnergy / sum.retainedWeight
		}
		before := min(key.route, p.period)
		key.joules += initial*before + max(0, ws.rate-sum.retainedRoutingRate)*before*mean + ws.rate*mean*max(0, p.period-key.route)
	}
	key.bounded = key.bounded && total <= p.settings.MaxInstances
	key.instances = total
	key.countExcess += max(0, total-p.settings.MaxInstances)
	if key.keep {
		key.keepJoules = key.joules
	}
	slices.SortFunc(key.excesses[t.priorityCount:], func(a, b float64) int { return cmp.Compare(b, a) })
	return key
}

// capacityHeuristic is an optimistic fluid search score, not predicted E2E.
func capacityHeuristic(w capacityWorkload, mu, retained, service, route, period float64) (float64, bool) {
	if w.rate == 0 && w.queued == 0 {
		return 0, true
	}
	if mu <= 0 {
		return math.Inf(1), false
	}
	backlog := max(0, float64(w.queued)+(w.rate-retained)*route)
	backlog += max(w.rate-mu, 0) * max(period-route, 0)
	h := w.totalNanos + 1e9*route + 1e9*backlog/mu + service/mu
	return max(0, h/float64(w.d.SLO.TargetLatency)-1), true
}
