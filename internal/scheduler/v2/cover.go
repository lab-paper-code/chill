package v2

import (
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

func (c *v2Search) movePlans(n int, dst []int) []int {
	s, t := c.s, c.s.Table
	dst = dst[:0]
	for _, e := range t.EntryOrder[n] {
		for _, q := range t.Entries[e].Variants {
			if q != s.Choice[n] && t.Searchable[q] {
				dst = append(dst, q)
			}
		}
	}
	return dst
}

// Cover repairs physical count excess first, then follows the same ordered
// heuristic as local search. No capacity certificate is required for a move.
func (c *v2Search) cover() {
	for (!c.current.Feasible || !c.current.Bounded) && !c.expired() {
		evaluated, stepStart := c.stats.SurrogateEvaluated, time.Now()
		found := false
		c.current.CopyInto(&c.best)
		for _, n := range c.s.Table.NodeOrder {
			c.moves[n] = c.movePlans(n, c.moves[n])
			for _, q := range c.moves[n] {
				key, ok := c.evaluate(n, q)
				if !ok {
					break
				}
				if key.Bounded && key.Searchable && !key.UnroutedPrepare {
					c.finalists.add(key, c.Start)
				}
				if !c.coverAllows(key) {
					continue
				}
				better := !found || key.Better(&c.best)
				if found && c.current.CountExcess > 0 && key.CountExcess != c.best.CountExcess {
					better = key.CountExcess < c.best.CountExcess
				}
				if better {
					key.CopyInto(&c.best)
					found = true
				}
			}
			if c.expired() {
				break
			}
		}
		if !found {
			return
		}
		c.recordStep(c.best, c.current.Choice, protocol.Cover, c.stats.SurrogateEvaluated-evaluated, stepStart)
		c.s.reset(c.best.Choice)
		c.best.CopyInto(&c.current)
		if c.current.Bounded {
			c.finalists.add(c.current, c.Start)
		}
	}
}

func (c *v2Search) coverAllows(key protocol.Rank) bool {
	if !key.Searchable || key.UnroutedPrepare {
		return false
	}
	if c.current.CountExcess > 0 {
		return key.CountExcess < c.current.CountExcess
	}
	return key.Bounded && key.Better(&c.current)
}
