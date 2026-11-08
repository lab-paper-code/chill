package v2

import (
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

// v2Merit ranks positive-coverage moves by ΔE/Δcov. Nonpositive coverage
// follows every positive-coverage move, ordered by ΔE, then logical plan key.
// Each cap variant is a separate active item, not an option-level proxy.
type v2Merit struct {
	q                int
	Energy, coverage float64
}

func (c *v2Search) rankMove(n, q int, key protocol.Rank) {
	if c.active == 0 {
		return
	}
	move := v2Merit{q: q, Energy: key.Joules - c.current.Joules, coverage: coverageDelta(c.current, key)}
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
	av, bv := a.Energy, b.Energy
	if a.coverage > 0 {
		av, bv = av/a.coverage, bv/b.coverage
	}
	if av != bv {
		return av < bv
	}
	return c.s.Table.KeyParts[a.q] < c.s.Table.KeyParts[b.q]
}

func (c *v2Search) polish() {
	for !c.expired() {
		evaluated, stepStart := c.stats.SurrogateEvaluated, time.Now()
		phase := protocol.Polish
		c.current.CopyInto(&c.best)
		c.sweepOne()
		if !c.expired() {
			c.sweepTwo()
		}
		if !c.best.Better(&c.current) && !c.expired() {
			// The unsuccessful k=1/2 sweep belongs to trailing evaluations.
			evaluated, stepStart = c.stats.SurrogateEvaluated, time.Now()
			phase = protocol.Combine
			c.combine()
		}
		if !c.best.Better(&c.current) {
			return
		}
		c.recordStep(c.best, c.current.Choice, phase, c.stats.SurrogateEvaluated-evaluated, stepStart)
		c.s.reset(c.best.Choice)
		c.best.CopyInto(&c.current)
		c.stats.steps++
		c.finalists.add(c.current, c.Start)
	}
}

func (c *v2Search) sweepOne() {
	for _, n := range c.s.Table.NodeOrder {
		c.ranked[n] = c.ranked[n][:0]
		c.moves[n] = c.movePlans(n, !c.current.Feasible, c.moves[n])
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
	nodes := c.s.Table.NodeOrder
	for i, n := range nodes {
		old := c.s.Choice[n]
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

func (c *v2Search) overloaded() bool { return c.s.port.Overloaded() }
