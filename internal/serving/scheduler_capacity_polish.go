package serving

// v2Merit ranks positive-coverage moves by ΔE/Δcov. Nonpositive coverage
// follows every positive-coverage move, ordered by ΔE, then logical plan key.
// Each cap variant is a separate active item, not an option-level proxy.
type v2Merit struct {
	q                int
	energy, coverage float64
}

func (c *v2Search) rankMove(n, q int, key orderKey) {
	if c.active == 0 {
		return
	}
	move := v2Merit{q: q, energy: key.joules - c.current.joules, coverage: capacityCoverageDelta(c.current, key)}
	ranked := c.ranked[n]
	at := 0
	for at < len(ranked) && !c.meritLess(move, ranked[at]) {
		at++
	}
	if at >= c.active {
		return
	}
	if len(ranked) < c.active {
		ranked = append(ranked, v2Merit{})
	}
	copy(ranked[at+1:], ranked[at:len(ranked)-1])
	ranked[at] = move
	c.ranked[n] = ranked
}

func (c *v2Search) meritLess(a, b v2Merit) bool {
	if (a.coverage > 0) != (b.coverage > 0) {
		return a.coverage > 0
	}
	av, bv := a.energy, b.energy
	if a.coverage > 0 {
		av, bv = av/a.coverage, bv/b.coverage
	}
	if av != bv {
		return av < bv
	}
	return c.s.table.keyParts[a.q] < c.s.table.keyParts[b.q]
}

func (c *v2Search) polish() {
	for !c.expired() {
		c.current.copyInto(&c.best)
		c.sweepOne()
		if !c.expired() {
			c.sweepTwo()
		}
		if !c.best.better(&c.current) && !c.expired() {
			c.combine()
		}
		if !c.best.better(&c.current) {
			return
		}
		c.s.reset(c.best.choice)
		c.best.copyInto(&c.current)
		c.stats.steps++
		c.finalists.add(c.current, c.start)
	}
}

func (c *v2Search) sweepOne() {
	for _, n := range c.s.table.nodeOrder {
		c.ranked[n] = c.ranked[n][:0]
		c.moves[n] = c.movePlans(n, !c.current.feasible, c.moves[n])
		for _, q := range c.moves[n] {
			key, ok := c.evaluate(n, q)
			if !ok {
				return
			}
			c.rankMove(n, q, key)
			c.consider(key)
		}
		if c.expired() {
			return
		}
	}
}

func (c *v2Search) sweepTwo() {
	nodes := c.s.table.nodeOrder
	for i, n := range nodes {
		old := c.s.choice[n]
		for _, q := range c.moves[n] {
			c.s.apply(n, q)
			for _, other := range nodes[i+1:] {
				// The second cap selection sees the first move, including r and κret.
				c.second = c.movePlans(other, c.overloaded(), c.second)
				for _, second := range c.second {
					key, ok := c.evaluate(other, second)
					if !ok {
						c.s.apply(n, old)
						return
					}
					c.consider(key)
				}
			}
			c.s.apply(n, old)
			if c.expired() {
				return
			}
		}
	}
}

func (c *v2Search) overloaded() bool {
	for _, terms := range c.s.workloads {
		satisfied, _, _ := capacitySearchJudge(terms.demand, terms.knew, terms.kret, terms.present, c.s.r)
		if !satisfied || !terms.ok {
			return true
		}
	}
	return false
}
