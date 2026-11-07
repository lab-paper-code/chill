package serving

import (
	"math"
	"slices"
	"testing"
)

func v2UnprunedProduct(c *v2Search, depth int) {
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
			v2UnprunedProduct(c, depth+1)
			c.s.apply(n, old)
		}
	}
}

func TestCapacityCombineMatchesUnprunedAndExact(t *testing.T) {
	p := e5Planner(map[int]float64{0: 20, 1: 40, 2: 0.3}, evaluationNodes(), e5Start, nil)
	c := v2TestSearch(p, 0)
	c.current.copyInto(&c.best)
	c.sweepOne()
	c.combine()
	pruned, count := newV2Key(c.s.table, len(p.ws)), c.stats.surrogateEvaluated
	c.best.copyInto(&pruned)
	c.current.copyInto(&c.best)
	v2UnprunedProduct(c, 0)
	if c.best.better(&pruned) || pruned.better(&c.best) {
		t.Fatalf("pruned J=%g differs from exhaustive J=%g", pruned.joules, c.best.joules)
	}
	exact := e5Brute(p)
	if exact == nil || !pruned.feasible || math.Abs(pruned.joules-exact.joules) > 1e-8 {
		t.Fatalf("pruned J=%g feasible=%v, exact=%+v", pruned.joules, pruned.feasible, exact)
	}
	t.Logf("prunedEvaluations=%d unprunedEvaluations=%d J=%.9f", count, c.stats.surrogateEvaluated-count, pruned.joules)
}

func TestCapacityCombineClampedMatchesUnpruned(t *testing.T) {
	for _, horizon := range []float64{1, 10, 30} {
		p := e5Planner(map[int]float64{0: 20, 1: 40, 2: .3}, evaluationNodes(), e5Start, nil)
		p.period = horizon
		c := v2TestSearch(p, 0)
		c.current.copyInto(&c.best)
		c.sweepOne()
		c.combine()
		pruned := newV2Key(c.s.table, len(p.ws))
		c.best.copyInto(&pruned)
		c.current.copyInto(&c.best)
		v2UnprunedProduct(c, 0)
		if c.best.better(&pruned) || pruned.better(&c.best) {
			t.Fatalf("H=%g clamped bound discarded optimum: pruned=%g exhaustive=%g", horizon, pruned.joules, c.best.joules)
		}
		t.Logf("H=%g pruned=unpruned J=%.17g", horizon, pruned.joules)
	}
}

func TestCapacityCombineOverloadKeepsLeximinBranches(t *testing.T) {
	p := e5Planner(map[int]float64{0: 1000000, 1: 1000000}, evaluationNodes(), e5Start, nil)
	c := v2TestSearch(p, 0)
	c.current.copyInto(&c.best)
	c.sweepOne()
	c.combine()
	pruned := newV2Key(c.s.table, len(p.ws))
	c.best.copyInto(&pruned)
	c.current.copyInto(&c.best)
	v2UnprunedProduct(c, 0)
	if c.best.better(&pruned) || pruned.better(&c.best) {
		t.Fatalf("overload pruning discarded leximin optimum: %v vs %v", pruned.excesses, c.best.excesses)
	}
}

func TestCapacityCombineAllActiveIncludesEveryCap(t *testing.T) {
	for _, active := range []int{0, -1} {
		p := e5Planner(map[int]float64{0: 20}, evaluationScaled(2, 0), e5Start, nil)
		c := v2TestSearch(p, active)
		// A current non-keep entry must retain its other cap variants too.
		for _, entry := range c.s.table.entries {
			if len(entry.variants) > 1 {
				c.s.apply(entry.node, entry.variants[0])
				break
			}
		}
		key, _ := c.evaluate(0, c.s.choice[0])
		key.copyInto(&c.current)
		c.current.copyInto(&c.best)
		c.sweepOne()
		c.combine()
		for n, plans := range c.s.table.byPlan {
			for _, q := range plans {
				if c.s.table.searchable[q] && !slices.Contains(c.activePlans[n], q) {
					t.Fatalf("active=%d omitted searchable cap %d", active, q)
				}
			}
		}
	}
}
