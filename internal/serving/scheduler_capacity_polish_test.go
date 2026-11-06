package serving

import "testing"

func TestCapacityPolishStrictImprovementAndTermination(t *testing.T) {
	p := e5Planner(map[int]float64{0: 20, 1: 15}, evaluationNodes(), e5Start, nil)
	c := v2TestSearch(p, 4)
	before := newV2Key(c.s.table, len(p.ws))
	c.current.copyInto(&before)
	c.polish()
	if !c.current.better(&before) || c.stats.steps == 0 {
		t.Fatal("polish failed to make a strict improvement")
	}
	if c.stats.budgetCut {
		t.Fatal("polish exhausted budget")
	}
	c.current.copyInto(&before)
	steps := c.stats.steps
	c.polish()
	if c.current.better(&before) || before.better(&c.current) || c.stats.steps != steps {
		t.Fatal("polish moved from its strict local optimum")
	}
	t.Logf("steps=%d evaluations=%d J=%.9f", c.stats.steps, c.stats.surrogateEvaluated, c.current.joules)
}
