package serving

import (
	"testing"
	"time"
)

func v2TestSearch(p *capacityPlanner, active int) *v2Search {
	tab := p.planTable()
	choice := make([]int, len(p.plans))
	for n := range choice {
		choice[n] = tab.byPlan[n][0]
	}
	c := newV2Search(p, tab, choice, 500000, 16, active, time.Time{})
	c.start = capacitySearchStartKeep
	key, _ := c.evaluate(0, choice[0])
	key.copyInto(&c.current)
	return c
}

func TestCapacityCoverSatisfiedAndOverload(t *testing.T) {
	for _, test := range []struct {
		name     string
		rates    map[int]float64
		feasible bool
	}{
		{"recover", map[int]float64{0: 110, 1: 15}, true},
		{"overload", map[int]float64{0: 1000000}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := e5Planner(test.rates, evaluationNodes(), e5Start, nil)
			c := v2TestSearch(p, 4)
			if c.current.feasible {
				t.Fatal("fixture must start insufficient")
			}
			c.cover()
			if c.current.feasible != test.feasible {
				t.Fatalf("feasible=%v, want %v: %+v", c.current.feasible, test.feasible, c.current)
			}
			if c.stopped() {
				t.Fatal("cover exhausted budget")
			}
			before := c.stats.surrogateEvaluated
			c.cover()
			if c.current.feasible && c.stats.surrogateEvaluated != before {
				t.Fatal("satisfied cover must stop without evaluation")
			}
			t.Logf("feasible=%v evaluations=%d J=%.9f", c.current.feasible, c.stats.surrogateEvaluated, c.current.joules)
		})
	}
}
