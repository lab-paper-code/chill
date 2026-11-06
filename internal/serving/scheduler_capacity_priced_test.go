package serving

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

func TestCapacityPricedDeterminism(t *testing.T) {
	var want []string
	var wantDual float64
	for trial := range 6 {
		p := e5Planner(map[int]float64{0: 60, 1: 15, 2: 1.2}, evaluationScaled(2, 2), e5Start, nil)
		if trial > 0 {
			rng := rand.New(rand.NewPCG(uint64(trial), 31))
			for _, plans := range p.plans {
				rng.Shuffle(len(plans)-1, func(i, j int) { plans[i+1], plans[j+1] = plans[j+1], plans[i+1] })
			}
		}
		tab := p.planTable()
		tab.initSearch(p)
		choice, dual := p.pricedStart(tab)
		if len(choice) != len(p.plans) || math.IsNaN(dual) || math.IsInf(dual, 0) {
			t.Fatalf("trial=%d choice=%v dual=%g", trial, choice, dual)
		}
		keys := make([]string, len(choice))
		for n, q := range choice {
			if q < 0 || q >= len(tab.index) || tab.node[q] != n {
				t.Fatalf("trial=%d node=%d invalid table index=%d", trial, n, q)
			}
			keys[n] = tab.keyParts[q]
		}
		if trial == 0 {
			want, wantDual = keys, dual
		} else if !slices.Equal(keys, want) || dual != wantDual {
			t.Fatalf("trial=%d keys=%v want=%v dual=%.17g want=%.17g", trial, keys, want, dual, wantDual)
		}
	}
}

func TestCapacityPricedSkip(t *testing.T) {
	for _, kind := range []string{"no-plans", "zero-capacity", "zero-cost"} {
		t.Run(kind, func(t *testing.T) {
			p := e5Planner(map[int]float64{0: 60}, evaluationScaled(1, 0), e5Start, nil)
			if kind == "no-plans" {
				p.plans, p.compared, p.snapshot.Nodes = nil, nil, nil
			}
			for _, plans := range p.plans {
				for _, plan := range plans {
					if kind == "zero-cost" {
						plan.before, plan.mid, plan.after, plan.preJ, plan.postJ = 0, 0, 0, 0, 0
					}
					for w := range plan.items {
						for i := range plan.items[w] {
							if kind == "zero-capacity" {
								plan.items[w][i].kappa = 0
							} else {
								plan.items[w][i].e = 0
							}
						}
					}
					p.sum(plan)
				}
			}
			tab := p.planTable()
			tab.initSearch(p)
			if choice, dual := p.pricedStart(tab); choice != nil || dual != 0 {
				t.Fatalf("skip returned choice=%v dual=%g", choice, dual)
			}
		})
	}
}

func TestCapacityPricedFixedMultiWorkloadKeep(t *testing.T) {
	current := make(map[string]bool, len(e5Start)+1)
	for id, present := range e5Start {
		current[id] = present
	}
	current["rasp5/plc/q1"] = true
	p := e5Planner(map[int]float64{0: 60, 1: 15, 2: 1.2}, evaluationScaled(2, 0), current, nil)
	p.compared[0] = false
	tab := p.planTable()
	tab.initSearch(p)
	keep := tab.byPlan[0][0]
	if tab.kappa[keep][0] <= 0 || tab.kappa[keep][1] <= 0 || tab.kappaE[keep][0] <= 0 || tab.kappaE[keep][1] <= 0 {
		t.Fatal("fixture must retain capacity and energy terms for both Workloads")
	}
	choice, dual := p.pricedStart(tab)
	if len(choice) != len(p.plans) || choice[0] != keep || math.IsNaN(dual) || math.IsInf(dual, 0) {
		t.Fatalf("uncompared multi-Workload Node must keep: choice=%v keep=%d dual=%g", choice, keep, dual)
	}
	c := capacityPrices{
		planner: p, table: tab, choice: make([]int, len(p.plans)),
		mu: []float64{2, 3, 5}, rho: []float64{0.25, 0.5, 0.75},
		demand: make([]float64, len(p.ws)), supply: make([]float64, len(p.ws)),
		retained: make([]float64, len(p.ws)), route: 7,
	}
	want := tab.energy(keep, c.route, p.period)
	for _, w := range []int{0, 1} {
		want += (p.period-c.route)*c.rho[w]*tab.kappaE[keep][w] - c.mu[w]*tab.kappa[keep][w]
	}
	if got := c.planCost(keep); got != want {
		t.Fatalf("multi-Workload keep cost=%.17g want=%.17g", got, want)
	}
	if allocations := testing.AllocsPerRun(100, func() {
		c.selectPlans()
		c.aggregate()
	}); allocations != 0 {
		t.Fatalf("price iteration allocated %g times", allocations)
	}
}

func TestCapacityPricedStage4Gaps(t *testing.T) {
	for _, tc := range []struct {
		name            string
		variants, modes int
		rates           map[int]float64
	}{
		{"5-b2", 2, 0, map[int]float64{0: 60, 1: 15, 2: 1.2}},
		{"8-b1-modes2", 1, 2, map[int]float64{0: 110, 2: 1.2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			brute := e5Brute(e5Planner(tc.rates, evaluationScaled(tc.variants, tc.modes), e5Start, nil))
			if brute == nil {
				t.Fatal("brute optimum missing")
			}
			for _, active := range []int{4, 8} {
				t.Run(fmt.Sprint("m", active), func(t *testing.T) {
					p := e5Planner(tc.rates, evaluationScaled(tc.variants, tc.modes), e5Start, nil)
					p.settings.PlanSearchStrategy = "v2"
					p.settings.PlanPricedStart = true
					p.settings.PlanSearchEvaluations = int(^uint(0) >> 1)
					p.settings.PlanPolishActive = active
					got := capacitySearchForTest(t, p)
					if p.searched.BudgetCut {
						t.Fatal("unlimited evaluation search hit budget")
					}
					t.Logf("J=%.17g key=%q optimumJ=%.17g optimumKey=%q reachesOptimum=%v sameKey=%v", got.joules, got.key, brute.joules, brute.key, got.feasible == brute.feasible && math.Abs(got.joules-brute.joules) <= 1e-9, got.key == brute.key)
					if active == 8 && (got.key != brute.key || got.joules != brute.joules || got.feasible != brute.feasible) {
						t.Fatal("m8 price-enabled search lost the observed exhaustive optimum")
					}
				})
			}
		})
	}
}
