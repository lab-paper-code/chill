package serving

import (
	"cmp"
	"math"
	"slices"
	"sort"
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
	routingRate, retainedRoutingRate, serviceNanos float64
	retainedServiceNanos                           float64
	weight, retainedWeight                         float64
	count, pending                                 int
}

// searchState keeps additive Tick-fixed Workload sums and each node's load
// watts for the current choice. A node's load watts are not additive per
// instance (the awake step is added once per node and is concave in load),
// so a move recomputes them with schedulerLoadWatts for every node whose
// inputs the move changes and keeps the others: the move's load term is the
// exact difference over those nodes. Delta never queries raw service
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
	// Per node, the load watts before and after the route for choice, and
	// whether they are identified.
	loadInitial, loadFinal     []float64
	loadInitialOK, loadFinalOK []bool
	// Scratch for one move: the moved Workload sums, which Workloads changed,
	// and one plan's instance input rates.
	next    []searchSum
	changed []bool
	rates   []float64
}

func newSearchState(p *capacityPlanner, t *planTable, choice []int) *searchState {
	t.initSearch(p)
	n, w := len(p.plans), len(p.ws)
	slots := 0
	for _, models := range t.loadModels {
		slots = max(slots, len(models))
	}
	s := &searchState{planner: p, table: t, choice: make([]int, n), sums: make([]searchSum, w), tPre: make([]float64, n),
		result:      orderKey{choice: make([]int, n), excesses: make([]float64, w), demand: make([]float64, w), supply: make([]float64, w), satisfied: make([]bool, w), table: t},
		loadInitial: make([]float64, n), loadFinal: make([]float64, n), loadInitialOK: make([]bool, n), loadFinalOK: make([]bool, n),
		next: make([]searchSum, w), changed: make([]bool, w), rates: make([]float64, slots)}
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
	for n, q := range s.choice {
		s.loadInitial[n], s.loadFinal[n], s.loadInitialOK[n], s.loadFinalOK[n] = s.loadWatts(q, s.sums)
	}
}
func (s *searchState) addPlan(sum *searchSum, q, w int, sign float64) {
	t := s.table
	sum.routingRate += sign * t.routingRate[q][w]
	sum.retainedRoutingRate += sign * t.retainedRoutingRate[q][w]
	sum.serviceNanos += sign * t.routingRateServiceNanos[q][w]
	sum.retainedServiceNanos += sign * t.retainedRoutingRateServiceNanos[q][w]
	sum.weight += sign * t.weight[q][w]
	sum.retainedWeight += sign * t.weightRet[q][w]
	sum.count += int(sign) * t.count[q][w]
	sum.pending += int(sign) * t.pending[q][w]
}

// finalShare is an entry's share of its Workload's inputs after the route:
// by profile throughput when routing is controlled, otherwise by route weight.
func (s *searchState) finalShare(sum *searchSum, l tableLoad) float64 {
	if s.planner.settings.Controls.Routing && sum.routingRate > 0 {
		return l.throughput / sum.routingRate
	}
	if sum.weight > 0 {
		return l.weight / sum.weight
	}
	return 0
}

// loadWatts is plan q's node load watts (schedulerLoadWatts) under the
// Workload sums, before the route (retained instances serve up to their
// routing rate, the rest follows the final shares) and after it.
func (s *searchState) loadWatts(q int, sums []searchSum) (initial, final float64, initialOK, finalOK bool) {
	t, p := s.table, s.planner
	models := t.loadModels[q]
	if len(models) == 0 {
		return 0, 0, true, true
	}
	rates := s.rates[:len(models)]
	clear(rates)
	for _, w := range t.loadWorkloads[q] {
		rate, sum := p.ws[w].rate, &sums[w]
		for _, l := range t.loadFinal[q][w] {
			rates[l.slot] += rate * s.finalShare(sum, l)
		}
	}
	final, finalOK = schedulerLoadWatts(models, rates)
	clear(rates)
	for _, w := range t.loadWorkloads[q] {
		rate, sum := p.ws[w].rate, &sums[w]
		if sum.retainedWeight > 0 {
			for _, l := range t.loadRetained[q][w] {
				rates[l.slot] += min(rate, sum.retainedRoutingRate) * l.weight / sum.retainedWeight
			}
		}
		overflow := max(0, rate-sum.retainedRoutingRate)
		for _, l := range t.loadFinal[q][w] {
			rates[l.slot] += overflow * s.finalShare(sum, l)
		}
	}
	initial, initialOK = schedulerLoadWatts(models, rates)
	return initial, final, initialOK, finalOK
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

//nolint:gocyclo // One pass ranks a move: idle, node load, then Workload terms.
func (s *searchState) delta(n, q int) orderKey {
	p, t := s.planner, s.table
	key := s.result
	key.feasible, key.final, key.keep, key.searchable, key.bounded = true, true, true, true, true
	key.weightsExceeded, key.unroutedPrepare = false, false
	key.joules, key.route, key.countExcess = 0, 0, 0
	key.period, key.keepJoules = p.periodSeconds, p.keepWatts*p.periodSeconds
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
			key.joules += t.energy(current, key.route, p.periodSeconds)
		}
	}
	old := s.choice[n]
	for w := range p.ws {
		s.changed[w] = t.routingRate[old][w] != t.routingRate[q][w] || t.weight[old][w] != t.weight[q][w] ||
			t.retainedRoutingRate[old][w] != t.retainedRoutingRate[q][w] || t.weightRet[old][w] != t.weightRet[q][w]
		s.next[w] = s.sums[w]
		if s.changed[w] {
			s.addPlan(&s.next[w], old, w, -1)
			s.addPlan(&s.next[w], q, w, 1)
		}
	}
	// Node load watts: recomputed where the move changes a node's plan or the
	// sums of a Workload it serves, cached elsewhere. A load whose watts are not
	// identified adds nothing here; a non-keep plan carrying one is not
	// searchable, and exact evaluation reports its missing cell.
	before, after := min(key.route, p.periodSeconds), max(0, p.periodSeconds-key.route)
	for node, current := range key.choice {
		initial, final, initialOK, finalOK := s.loadInitial[node], s.loadFinal[node], s.loadInitialOK[node], s.loadFinalOK[node]
		if node == n || s.affected(current) {
			initial, final, initialOK, finalOK = s.loadWatts(current, s.next)
		}
		if before > 0 {
			if initialOK {
				key.joules += initial * before
			} else if !t.keep[current] {
				key.searchable = false
			}
		}
		if after > 0 {
			if finalOK {
				key.joules += final * after
			} else if !t.keep[current] {
				key.searchable = false
			}
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
		excess, eligible := capacityHeuristic(ws, t.arrivals[w], sum.routingRate, sum.retainedRoutingRate, sum.serviceNanos, sum.retainedServiceNanos, key.route, p.periodSeconds)
		key.excesses[t.excessIndex[w]], key.satisfied[w] = excess, excess == 0 && eligible
		key.demand[w], key.supply[w] = ws.rate, sum.routingRate
		key.feasible = key.feasible && key.satisfied[w]
		key.final = key.feasible
		key.searchable = key.searchable && eligible
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

// affected reports whether plan q serves a Workload whose sums the move changes.
func (s *searchState) affected(q int) bool {
	for _, w := range s.table.loadWorkloads[q] {
		if s.changed[w] {
			return true
		}
	}
	return false
}

// capacityHeuristic uses the exact finite cohort for phase populations and
// fluid work at each phase's last arrival. The tail request's own service is
// excluded from queued work before adding its service scalar. Each phase assigns
// that last request's delay to the whole phase, so simultaneous arrival bursts
// can score worse than exact's configured quantile. This can lower shortlist
// rank; keep and every start's endpoint still receive exact evaluation.
// Captured queued requests' already-accrued wait is not added, and a drained
// queue can lose earlier waiting time, so this point score can also be optimistic.
// It is not an E2E bound. No retained supply adds route wait.
func capacityHeuristic(w capacityWorkload, arrivals []float64, mu, retained, service, retainedService, route, period float64) (float64, bool) {
	if len(arrivals) == 0 && w.queued == 0 {
		return 0, true
	}
	if mu <= 0 {
		return math.Inf(1), false
	}
	pre := min(route, period)
	preCount := sort.Search(len(arrivals), func(i int) bool { return arrivals[i] >= pre*1e9 })
	preMass, postMass := float64(w.queued+preCount), float64(len(arrivals)-preCount)
	preLast, postLast := 0.0, pre
	if preCount > 0 {
		preLast = min(pre, arrivals[preCount-1]/1e9)
	}
	if len(arrivals) > preCount {
		postLast = arrivals[len(arrivals)-1] / 1e9
	} else if route == 0 {
		postLast = 0
	}
	backlog := max(0, preMass-retained*preLast)
	atRoute := max(0, preMass-retained*pre)
	postBacklog := max(0, atRoute+postMass-mu*max(0, postLast-pre))
	hPost := w.totalNanos + 1e9*max(0, postBacklog-1)/mu + service/mu
	if route == 0 {
		preMass, postMass = 0, preMass+postMass
	}
	hPre := w.totalNanos + 1e9*route + 1e9*max(0, backlog-1)/mu + service/mu
	if retained > 0 {
		hPre = w.totalNanos + 1e9*max(0, backlog-1)/retained + retainedService/retained
	}
	h := hPost
	if preMass > 0 {
		h = hPre
		if postMass > 0 {
			low, high, mass := hPre, hPost, preMass
			if hPost < hPre {
				low, high, mass = hPost, hPre, postMass
			}
			h = low
			if mass < (preMass+postMass)*w.d.SLO.Percentile/100 {
				h = high
			}
		}
	}
	return max(0, h/float64(w.d.SLO.TargetLatency)-1), true
}
