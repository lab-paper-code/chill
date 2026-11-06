package serving

import (
	"math"
	"slices"
)

func (c *v2Search) combine() {
	t := c.s.table
	for _, n := range t.nodeOrder {
		plans := c.activePlans[n][:0]
		if c.allActive {
			for _, q := range t.byPlan[n] {
				if t.searchable[q] {
					plans = append(plans, q)
				}
			}
		} else {
			for _, move := range c.ranked[n] {
				plans = append(plans, move.q)
			}
		}
		for _, q := range []int{t.byPlan[n][0], c.s.choice[n]} {
			if !slices.Contains(plans, q) {
				plans = append(plans, q)
			}
		}
		slices.SortFunc(plans, func(a, b int) int {
			if t.keyParts[a] < t.keyParts[b] {
				return -1
			}
			if t.keyParts[a] > t.keyParts[b] {
				return 1
			}
			return 0
		})
		c.activePlans[n] = plans
	}
	for depth := len(t.nodeOrder) - 1; depth >= 0; depth-- {
		n := t.nodeOrder[depth]
		for w := range c.s.planner.ws {
			largest := 0.0
			for _, q := range c.activePlans[n] {
				largest = max(largest, t.kappa[q][w])
			}
			c.suffix[depth][w] = c.suffix[depth+1][w] + largest
		}
	}
	c.prepareEnergyBounds()
	c.combineAt(0)
}

func (c *v2Search) combineAt(depth int) {
	if c.expired() || c.combinePruned(depth) {
		return
	}
	n := c.s.table.nodeOrder[depth]
	old := c.s.choice[n]
	for _, q := range c.activePlans[n] {
		if depth+1 == len(c.s.choice) {
			key, ok := c.evaluate(n, q)
			if !ok {
				return
			}
			c.consider(key)
		} else {
			c.s.apply(n, q)
			c.combineAt(depth + 1)
			c.s.apply(n, old)
			if c.stats.budgetCut {
				return
			}
		}
	}
}

// Capacity bounds use λ, not the partial state's λ̃: remaining choices can
// lower r and raise retained capacity. In overload, feasibility-only pruning
// would discard all leximin improvements. Compare optimistic final/excess
// bounds instead; an overload branch can still win by improving final first.
func (c *v2Search) combinePruned(depth int) bool {
	t, p := c.s.table, c.s.planner
	possible, finalPossible := true, true
	for w, ws := range p.ws {
		capacity := c.suffix[depth][w]
		for _, n := range t.nodeOrder[:depth] {
			capacity += t.kappa[c.s.choice[n]][w]
		}
		// Account for addition-order rounding without rejecting boundary supply.
		capacity += math.Abs(capacity) * 1e-12
		finalPossible = finalPossible && capacity >= ws.rate
		demand, _ := capacityCoverDemand(ws, math.Inf(1), 0)
		possible = possible && capacity >= demand
		excess := math.Inf(1)
		if capacity > 0 {
			excess = demand/capacity - 1
		} else if demand == 0 {
			excess = 0
		}
		// B=0 clamps the exact transition excess at zero.
		if ws.queued == 0 {
			excess = max(0, excess)
		}
		c.optimistic[w] = excess
	}
	if c.best.feasible {
		if !possible {
			return true
		}
		return c.energyPruned(depth)
	}
	if c.best.final && !finalPossible {
		return true
	}
	if !possible && (c.best.final == finalPossible || !finalPossible) {
		slices.SortFunc(c.optimistic, func(a, b float64) int {
			if a > b {
				return -1
			}
			if a < b {
				return 1
			}
			return 0
		})
		return slices.Compare(c.optimistic, c.best.excesses) > 0
	}
	return false
}

// Cache remaining-node minima once per product, not once per branch. Every
// node's energy is affine in r, so its minimum is at zero or the product's
// largest pre time. Keeps retain their before*H baseline. The unclamped v1
// formula can give negative node energy; keep that value in the lower bound.
func (c *v2Search) prepareEnergyBounds() {
	t, p := c.s.table, c.s.planner
	c.energyBoundValid = true
	for depth := len(t.nodeOrder) - 1; depth >= 0; depth-- {
		n := t.nodeOrder[depth]
		c.suffixRoute[depth] = c.suffixRoute[depth+1]
		for _, q := range c.activePlans[n] {
			if !t.keep[q] {
				c.suffixRoute[depth] = max(c.suffixRoute[depth], t.tPre[q])
			}
			for _, lists := range [][][]capacityItem{t.items[q], t.retained[q]} {
				for _, items := range lists {
					for _, item := range items {
						c.energyBoundValid = c.energyBoundValid && item.e >= 0
					}
				}
			}
		}
	}
	c.energyBoundValid = c.energyBoundValid && c.suffixRoute[0] <= p.period
	for depth := len(t.nodeOrder) - 1; depth >= 0; depth-- {
		n, minimum := t.nodeOrder[depth], 0.0
		if p.compared[n] {
			minimum = math.Inf(1)
			for _, q := range c.activePlans[n] {
				r := 0.0
				if t.mid[q] < t.after[q] {
					r = c.suffixRoute[0]
				}
				minimum = min(minimum, t.energy(q, r, p.period))
			}
		}
		c.suffixEnergy[depth] = c.suffixEnergy[depth+1] + minimum
	}
}

// Only feasible incumbents permit J pruning. Request energy is nonnegative
// for 0<=r<=H and e>=0. Unfixed nodes' current pre times are NOT lower bounds:
// their eventual choices may remove those transitions. Fixed nodes require
// r>=their maximum pre; mid<after instead needs the remaining pre upper bound.
func (c *v2Search) energyPruned(depth int) bool {
	if !c.energyBoundValid {
		return false
	}
	t, p := c.s.table, c.s.planner
	lowRoute := 0.0
	for _, n := range t.nodeOrder[:depth] {
		if q := c.s.choice[n]; !t.keep[q] {
			lowRoute = max(lowRoute, t.tPre[q])
		}
	}
	highRoute := max(lowRoute, c.suffixRoute[depth])
	bound := c.suffixEnergy[depth]
	for _, n := range t.nodeOrder[:depth] {
		if !p.compared[n] {
			continue
		}
		q, r := c.s.choice[n], lowRoute
		if t.mid[q] < t.after[q] {
			r = highRoute
		}
		bound += t.energy(q, r, p.period)
	}
	return bound > c.best.joules+max(1, math.Abs(c.best.joules))*1e-12
}
