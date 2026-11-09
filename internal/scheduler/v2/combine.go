package v2

import "slices"

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
	c.combineAt(0)
}

// Profile routing sums and heuristic feasibility do not bound exact failure or
// delay. Enumerate the configured active product under the existing budgets.
func (c *v2Search) combineAt(depth int) {
	if c.expired() {
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

