package serving

import (
	"math"
	"time"
)

// capacityCoverDemand returns λ̃ and B>0, including the slack<=0 boundary.
func capacityCoverDemand(w capacityWorkload, retained, route float64) (float64, bool) {
	backlog := float64(w.queued) + max(0, w.rate-retained)*route
	if backlog == 0 {
		return w.rate, false
	}
	if w.slack <= 0 {
		return math.Inf(1), true
	}
	return max(w.rate, backlog/w.slack), true
}

func capacityCoverageDelta(before, after orderKey) float64 {
	change := 0.0
	for w := range before.demand {
		change += min(after.demand[w], after.supply[w]) - min(before.demand[w], before.supply[w])
	}
	return change
}

// movePlans excludes the current entry. Cap demand uses the moved entry's
// retained supply and route; all variants share those route-independent terms.
func (c *v2Search) movePlans(n int, overload bool, dst []int) []int {
	s, t := c.s, c.s.table
	dst = dst[:0]
	for _, e := range t.entryOrder[n] {
		if e == t.entryOf[s.choice[n]] {
			continue
		}
		entry := t.entries[e]
		state := capacityCapState{overload: overload}
		if w := entry.workload; w >= 0 {
			q := entry.variants[0]
			r := 0.0
			for node, pre := range s.tPre {
				if node == n {
					pre = 0
					if !t.keep[q] {
						pre = t.tPre[q]
					}
				}
				r = max(r, pre)
			}
			terms := s.workloadTerms(w, n, q)
			state.demand, state.backlog = capacityCoverDemand(terms.demand, terms.kret, r)
			state.kappaOthers = s.sums[w].kappa - t.kappa[s.choice[n]][w]
			state.kappaEOthers = s.sums[w].kappaE - t.kappaE[s.choice[n]][w]
		}
		caps := t.selectCaps(entry, state)
		if caps.count == 2 && t.keyParts[caps.candidates[1]] < t.keyParts[caps.candidates[0]] {
			caps.candidates[0], caps.candidates[1] = caps.candidates[1], caps.candidates[0]
		}
		for _, q := range caps.candidates[:caps.count] {
			if t.searchable[q] {
				dst = append(dst, q)
			}
		}
	}
	return dst
}

func (c *v2Search) cover() {
	for (!c.current.feasible || !c.current.bounded) && !c.expired() {
		evaluated, stepStart := c.stats.surrogateEvaluated, time.Now()
		found, bestEnergy, bestCoverage := false, 0.0, 0.0
		c.current.copyInto(&c.best)
		for _, n := range c.s.table.nodeOrder {
			c.moves[n] = c.movePlans(n, !c.current.feasible, c.moves[n])
			for _, q := range c.moves[n] {
				key, ok := c.evaluate(n, q)
				if !ok {
					break
				}
				coverage, energy := capacityCoverageDelta(c.current, key), key.joules-c.current.joules
				if !c.coverAllows(key, coverage) {
					continue
				}
				better := !found
				if found {
					if c.current.countExcess > 0 {
						better = coverage > bestCoverage
					} else if energy <= 0 || bestEnergy <= 0 {
						better = energy < bestEnergy || energy == bestEnergy && coverage > bestCoverage
					} else {
						better = energy/coverage < bestEnergy/bestCoverage
					}
					if coverage == bestCoverage && (c.current.countExcess > 0 || energy == bestEnergy) {
						better = c.moveLess(n, q, c.best)
					} else if c.current.countExcess == 0 && energy > 0 && bestEnergy > 0 && energy/coverage == bestEnergy/bestCoverage {
						better = c.moveLess(n, q, c.best)
					}
				}
				if better {
					key.copyInto(&c.best)
					found, bestEnergy, bestCoverage = true, energy, coverage
				}
			}
			if c.expired() {
				break
			}
		}
		if !found {
			return
		}
		c.recordStep(c.best, c.current.choice, schedulerPlanPhaseCover, c.stats.surrogateEvaluated-evaluated, stepStart)
		c.s.reset(c.best.choice)
		c.best.copyInto(&c.current)
		c.finalists.add(c.current, c.start)
	}
}

func (c *v2Search) coverAllows(key orderKey, coverage float64) bool {
	allowed := key.searchable && !key.unroutedPrepare
	if c.current.countExcess > 0 {
		allowed = allowed && key.instances < c.current.instances && key.countExcess < c.current.countExcess
	} else {
		allowed = allowed && key.bounded && coverage > 0
		for w, satisfied := range c.current.satisfied {
			allowed = allowed && (!satisfied || key.satisfied[w])
		}
	}
	return allowed
}

// UID order wins a cover tie before the changed plan's logical key.
func (c *v2Search) moveLess(n, q int, best orderKey) bool {
	for _, node := range c.s.table.nodeOrder {
		if best.choice[node] != c.current.choice[node] {
			if node != n {
				return c.s.planner.snapshot.Nodes[n].Node.UID < c.s.planner.snapshot.Nodes[node].Node.UID
			}
			return c.s.table.keyParts[q] < c.s.table.keyParts[best.choice[node]]
		}
	}
	return false
}
