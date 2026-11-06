package serving

import (
	"math"
	"slices"
)

// capacitySearchWeights mirrors capacityWeights/capacityFixedWeights in caller
// storage. Keep its arithmetic order identical to the exact evaluator.
func capacitySearchWeights(rate float64, items []capacityItem, weights []int32, y []float64, routing bool) bool {
	if len(items) == 0 {
		return true
	}
	total := 0.0
	for _, item := range items {
		if routing {
			total += item.kappa
		} else {
			total += float64(item.weight)
		}
	}
	sum := int32(0)
	for k, item := range items {
		if routing {
			share := 1 / float64(len(items))
			if total > 0 {
				share = item.kappa / total
			}
			weights[k] = int32(math.Floor(share * schedulerRouteUnits))
		} else {
			weights[k] = item.weight
		}
		sum += weights[k]
	}
	if routing {
		for ; sum < schedulerRouteUnits; sum++ {
			to, next := -1, 0.0
			for k, item := range items {
				kappa := item.kappa
				if total == 0 {
					kappa = 1
				}
				if kappa <= 0 {
					continue
				}
				if ratio := float64(weights[k]+1) / kappa; to < 0 || ratio < next {
					to, next = k, ratio
				}
			}
			weights[to]++
		}
	}
	ok := routing || total > 0
	for k, item := range items {
		y[k] = 0
		if routing {
			y[k] = rate * float64(weights[k]) / schedulerRouteUnits
		} else if total > 0 {
			y[k] = rate * float64(weights[k]) / total
		}
		ok = ok && y[k] <= item.kappa*(1+1e-12)
	}
	return ok
}

// searchWorkload caches route-independent terms. Unchanged Workloads need only
// a new judgment and request-energy expression when another Node changes r.
type searchWorkload struct {
	demand                            capacityWorkload
	knew, kret, served, mean, initial float64
	weightExcess                      float64
	present, ok, unroutedPrepare      bool
}

func (s *searchState) workload(w, n, q int, r float64, key *orderKey) float64 {
	t, old := s.table, s.choice[n]
	terms := s.workloads[w]
	if !t.sameWorkload(old, q, w) {
		terms = s.workloadTerms(w, n, q)
	}
	satisfied, final, worst := capacitySearchJudge(terms.demand, terms.knew, terms.kret, terms.present, r)
	if !terms.ok {
		satisfied = false
		worst = max(worst, terms.weightExcess)
	}
	key.demand[w], _ = capacityCoverDemand(terms.demand, terms.kret, r)
	key.supply[w], key.satisfied[w] = terms.knew, satisfied
	key.feasible = key.feasible && satisfied
	key.final = key.final && terms.ok && final
	key.weightsExceeded = key.weightsExceeded || !terms.ok
	key.unroutedPrepare = key.unroutedPrepare || terms.unroutedPrepare
	key.excesses[w] = worst
	rate := s.planner.ws[w].rate
	return terms.initial*r + max(0, rate-terms.kret)*r*terms.mean + (terms.served*terms.mean+max(0, rate-terms.served)*terms.mean)*(s.planner.period-r)
}

// workloadTerms folds retained energy in the exact evaluator's Node/item order,
// rather than reassociating products. Its scratch buffers belong to the state.
func (s *searchState) workloadTerms(w, n, q int) searchWorkload {
	t := s.table
	result := searchWorkload{weightExcess: math.Inf(-1)}
	items := s.items[:0]
	kret, weightSum, slow, outage := 0.0, 0.0, false, false
	sameRetained := n >= 0 && slices.Equal(t.retained[s.choice[n]][w], t.retained[q][w])
	if sameRetained {
		kret, weightSum = s.sums[w].kappaRet, s.sums[w].weightRet
		result.initial = s.workloads[w].initial
	}
	for node, current := range s.choice {
		if node == n {
			current = q
		}
		items = capacitySearchInsert(items, t.items[current][w])
		if !sameRetained {
			for _, item := range t.retained[current][w] {
				kret += item.kappa
				weightSum += float64(item.weight)
			}
		}
		slow = slow || t.slowWorkload[current][w]
		outage = outage || t.outage[current][w]
	}
	ws := s.planner.ws[w]
	demand := ws
	recovery := slow && t.keepInsufficient[w] && !(kret < ws.rate && outage)
	if slow && !recovery {
		demand.rate = max(ws.rate, s.planner.snapshot.ScaleDown[ws.d.UID].WindowMax)
	}
	protected := demand.rate
	if slow && !recovery {
		protected *= 1 + s.planner.settings.ScaleDownHeadroomFraction
	}
	weights, y := s.weights[:len(items)], s.load[:len(items)]
	ok := capacitySearchWeights(protected, items, weights, y, s.planner.settings.Controls.Routing)
	capacityEnergyLoad(y, ws.rate, protected)
	units, present := 0.0, false
	for k, weight := range weights {
		units += float64(weight)
		if weight > 0 {
			present = true
		} else {
			result.unroutedPrepare = result.unroutedPrepare || items[k].prepare
		}
	}
	knew, marginOK := capacityPositiveSupply(items, weights, protected, demand.rate)
	ok = ok && marginOK
	if !ok {
		result.weightExcess = capacitySearchWeightExcess(items, weights, demand.rate, units)
	}
	served, mean := 0.0, 0.0
	for k, item := range items {
		served += y[k]
		mean += y[k] * item.e
	}
	if served > 0 {
		mean /= served
	}
	initial := result.initial
	if !sameRetained && weightSum > 0 {
		for node, current := range s.choice {
			if node == n {
				current = q
			}
			for _, item := range t.retained[current][w] {
				initial += min(ws.rate, kret) * float64(item.weight) / weightSum * item.e
			}
		}
	}
	result.demand, result.knew, result.kret = demand, knew, kret
	result.present, result.ok = present, ok
	result.initial, result.served, result.mean = initial, served, mean
	return result
}

func capacitySearchInsert(items, additions []capacityItem) []capacityItem {
	for _, item := range additions {
		at := len(items)
		items = items[:at+1]
		for at > 0 && (item.e < items[at-1].e || item.e == items[at-1].e && item.id < items[at-1].id) {
			items[at] = items[at-1]
			at--
		}
		items[at] = item
	}
	return items
}

func capacitySearchWeightExcess(items []capacityItem, weights []int32, demand, units float64) float64 {
	worst := math.Inf(-1)
	for k, item := range items {
		if weights[k] == 0 {
			continue
		}
		excess := math.Inf(1)
		if item.kappa > 0 {
			excess = demand*float64(weights[k])/units/item.kappa - 1
		}
		worst = max(worst, excess)
	}
	return worst
}

// capacitySearchJudge is capacityJudge's final/transition projection without
// its escaping *float64 detail fields. Division and B=0 clamping stay identical.
func capacitySearchJudge(w capacityWorkload, knew, kret float64, present bool, r float64) (satisfied, finalOK bool, worst float64) {
	backlog := float64(w.queued) + max(0, w.rate-kret)*r
	final, transition := math.Inf(1), math.Inf(1)
	if present {
		if knew > 0 {
			final = w.rate/knew - 1
		} else if w.rate == 0 {
			final = 0
		}
	}
	if backlog == 0 {
		transition = 0
	} else if knew > 0 && w.slack > 0 {
		transition = (backlog/knew)/w.slack - 1
	}
	finalOK = final <= 0
	return finalOK && transition <= 0, finalOK, max(final, transition)
}
