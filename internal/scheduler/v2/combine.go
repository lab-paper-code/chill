package v2

import (
	"math"
	"slices"
)

func (c *v2Search) combine() {
	t := c.s.Table
	for _, n := range t.NodeOrder {
		plans := c.activePlans[n][:0]
		if c.allActive {
			for _, q := range t.ByPlan[n] {
				if t.Searchable[q] {
					plans = append(plans, q)
				}
			}
		} else {
			for _, move := range c.ranked[n] {
				plans = append(plans, move.q)
			}
		}
		for _, q := range []int{t.ByPlan[n][0], c.s.Choice[n]} {
			if !slices.Contains(plans, q) {
				plans = append(plans, q)
			}
		}
		slices.SortFunc(plans, func(a, b int) int {
			if t.KeyParts[a] < t.KeyParts[b] {
				return -1
			}
			if t.KeyParts[a] > t.KeyParts[b] {
				return 1
			}
			return 0
		})
		c.activePlans[n] = plans
	}
	for depth := len(t.NodeOrder) - 1; depth >= 0; depth-- {
		n := t.NodeOrder[depth]
		for w := range c.problem.Workloads {
			largest := 0.0
			for _, q := range c.activePlans[n] {
				largest = max(largest, t.Kappa[q][w])
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
	n := c.s.Table.NodeOrder[depth]
	old := c.s.Choice[n]
	for _, q := range c.activePlans[n] {
		if depth+1 == len(c.s.Choice) {
			key, ok := c.evaluate(n, q)
			if !ok {
				return
			}
			c.consider(key)
		} else {
			c.s.apply(n, q)
			c.combineAt(depth + 1)
			c.s.apply(n, old)
			if c.stopped() {
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
	t, p := c.s.Table, c.problem
	possible, finalPossible := true, true
	for w, ws := range p.Workloads {
		capacity := c.suffix[depth][w]
		for _, n := range t.NodeOrder[:depth] {
			capacity += t.Kappa[c.s.Choice[n]][w]
		}
		// Account for addition-order rounding without rejecting boundary supply.
		capacity += math.Abs(capacity) * 1e-12
		finalPossible = finalPossible && capacity >= ws.Rate
		demand, _ := CoverDemand(ws, math.Inf(1), 0)
		possible = possible && capacity >= demand
		excess := math.Inf(1)
		if capacity > 0 {
			excess = demand/capacity - 1
		} else if demand == 0 {
			excess = 0
		}
		// B=0 clamps the exact transition excess at zero.
		if ws.Queued == 0 {
			excess = max(0, excess)
		}
		c.optimistic[w] = excess
	}
	if c.best.Feasible {
		if !possible {
			return true
		}
		return c.energyPruned(depth)
	}
	if c.best.Final && !finalPossible {
		return true
	}
	if !possible && (c.best.Final == finalPossible || !finalPossible) {
		slices.SortFunc(c.optimistic, func(a, b float64) int {
			if a > b {
				return -1
			}
			if a < b {
				return 1
			}
			return 0
		})
		return slices.Compare(c.optimistic, c.best.Excesses) > 0
	}
	return false
}

// Cache independent clamped-term minima over [0, maximum product route].
// Each duration is nonnegative. Nonnegative watts and request energy make
// their sum a safe lower bound, including routes beyond the fixed horizon.
func (c *v2Search) prepareEnergyBounds() {
	t, p := c.s.Table, c.problem
	c.energyBoundValid = true
	for depth := len(t.NodeOrder) - 1; depth >= 0; depth-- {
		n := t.NodeOrder[depth]
		c.suffixRoute[depth] = c.suffixRoute[depth+1]
		for _, q := range c.activePlans[n] {
			if !t.Keep[q] {
				c.energyBoundValid = c.energyBoundValid && t.Mid[q] >= 0 && t.After[q] >= 0
				c.suffixRoute[depth] = max(c.suffixRoute[depth], t.TPre[q])
			}
			c.energyBoundValid = c.energyBoundValid && t.Nonnegative[q]
		}
	}
	for depth := len(t.NodeOrder) - 1; depth >= 0; depth-- {
		n, minimum := t.NodeOrder[depth], 0.0
		if p.Table.Compared[n] {
			minimum = math.Inf(1)
			for _, q := range c.activePlans[n] {
				minimum = min(minimum, t.EnergyLowerBound(q, 0, c.suffixRoute[0], p.Table.Period))
			}
		}
		c.suffixEnergy[depth] = c.suffixEnergy[depth+1] + minimum
	}
}

// Only feasible incumbents permit J pruning. Fixed nodes require r>=their
// maximum pre; remaining-node pre maxima bound r above. Minimize the two
// clamped idle terms independently; never use an affine endpoint assumption.
func (c *v2Search) energyPruned(depth int) bool {
	if !c.energyBoundValid {
		return false
	}
	t, p := c.s.Table, c.problem
	lowRoute := 0.0
	for _, n := range t.NodeOrder[:depth] {
		if q := c.s.Choice[n]; !t.Keep[q] {
			lowRoute = max(lowRoute, t.TPre[q])
		}
	}
	highRoute := max(lowRoute, c.suffixRoute[depth])
	bound := c.suffixEnergy[depth]
	for _, n := range t.NodeOrder[:depth] {
		if !p.Table.Compared[n] {
			continue
		}
		bound += t.EnergyLowerBound(c.s.Choice[n], lowRoute, highRoute, p.Table.Period)
	}
	return bound > c.best.Joules+max(1, math.Abs(c.best.Joules))*1e-12
}
