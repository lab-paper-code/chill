package v2

import (
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

// Each cap variant keeps its own heuristic projection in preallocated buffers.
// Active-product selection uses the same Workload priority as every other move.
type v2Merit struct {
	q   int
	key protocol.Rank
}

func (c *v2Search) rankMove(n, q int, key protocol.Rank) {
	if c.allActive || c.active == 0 || !key.Searchable || key.UnroutedPrepare {
		return
	}
	ranked := c.ranked[n]
	at := 0
	for at < len(ranked) && !key.Better(&ranked[at].key) {
		at++
	}
	if at >= c.active {
		return
	}
	if len(ranked) < c.active {
		ranked = ranked[:len(ranked)+1]
	}
	slot := ranked[len(ranked)-1]
	copy(ranked[at+1:], ranked[at:len(ranked)-1])
	slot.q = q
	key.CopyInto(&slot.key)
	ranked[at] = slot
	c.ranked[n] = ranked
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
		c.moves[n] = c.movePlans(n, c.moves[n])
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
				// Every searchable second variant remains reachable after the first move.
				c.secondMovePlans = c.movePlans(other, c.secondMovePlans)
				for _, second := range c.secondMovePlans {
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
