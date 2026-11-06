package serving

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"testing"
	"time"
)

func TestCapacityV2FinalistsDistinctAndOrdered(t *testing.T) {
	tab := &planTable{byPlan: [][]int{{0, 1, 2}}, keyParts: []string{"n=a", "n=b", "n=c"}}
	f := newV2Finalists(tab, 0, 2)
	key := newV2Key(tab, 0)
	for _, q := range []int{2, 1, 1, 0, 2} {
		key.choice[0], key.joules = q, float64(q)
		f.add(key, "keep")
	}
	if f.used != 2 || f.ordered[0].key.choice[0] != 0 || f.ordered[1].key.choice[0] != 1 {
		t.Fatalf("bad finalist set: %+v", f)
	}
}

func TestCapacityV2CoreQualityAndPlanOrder(t *testing.T) {
	for _, space := range []struct {
		variants, modes int
		v1Worst         float64
	}{{1, 0, 0.03985}, {2, 0, 0.10043}, {1, 2, 0.01860}} {
		t.Run(fmt.Sprintf("v%d_m%d", space.variants, space.modes), func(t *testing.T) {
			if space.modes > 0 && os.Getenv("CHILL_PLAN_SEARCH") == "" {
				t.Skip("8-node exhaustive oracle; set CHILL_PLAN_SEARCH=1")
			}
			measurements := []struct {
				active                                 int
				name                                   string
				worst                                  float64
				maxEvaluations, totalEvaluations, cuts int
				longest, total                         time.Duration
			}{{active: 4, name: "4"}, {active: 8, name: "8"}, {active: 0, name: "all"}}
			for mix, rates := range e5Mixes {
				p := e5Planner(rates, evaluationScaled(space.variants, space.modes), e5Start, nil)
				exact := e5Brute(p) // One oracle per mix, shared across active limits.
				for i := range measurements {
					m := &measurements[i]
					budget := 500000
					if m.active <= 0 {
						budget = 0
					} // Test the documented full-neighborhood inclusion, without a cutoff.
					started := time.Now()
					candidates, stats := p.searchV2Core(p.planTable(), budget, 16, m.active, time.Time{})
					elapsed := time.Since(started)
					if len(candidates) == 0 {
						t.Fatalf("mix=%d m=%s has no finalists", mix, m.name)
					}
					got := p.evaluate(candidates[0].choice, p.routeOf(candidates[0].choice))
					if got.feasible != (exact != nil) {
						t.Errorf("mix=%d m=%s feasibility=%v, exhaustive=%v", mix, m.name, got.feasible, exact != nil)
					}
					gap := 0.0
					if exact != nil {
						gap = (got.joules - exact.joules) / exact.joules
						// §7.8.0 reports percentages to three decimals: allow half its final digit.
						if gap > space.v1Worst+0.000005 || m.active <= 0 && gap > 0.005 {
							t.Errorf("mix=%d m=%s rates=%v gap=%.6f%% exceeds gate: got=%s exact=%s", mix, m.name, rates, gap*100, got.key, exact.key)
						}
					}
					if stats.budgetCut {
						m.cuts++
					}
					if math.Abs(got.joules-candidates[0].joules) > 1e-8 {
						t.Errorf("mix=%d m=%s surrogate=%g exact=%g", mix, m.name, candidates[0].joules, got.joules)
					}
					shuffled := *p
					shuffled.plans = make([][]*capacityPlan, len(p.plans))
					rng := rand.New(rand.NewPCG(uint64(mix+20261107), 4))
					for n, plans := range p.plans {
						shuffled.plans[n] = slices.Clone(plans)
						rng.Shuffle(len(plans)-1, func(a, b int) {
							shuffled.plans[n][a+1], shuffled.plans[n][b+1] = shuffled.plans[n][b+1], shuffled.plans[n][a+1]
						})
					}
					again, repeated := shuffled.searchV2Core(shuffled.planTable(), budget, 16, m.active, time.Time{})
					if len(again) == 0 {
						t.Fatalf("mix=%d m=%s shuffled finalists empty", mix, m.name)
					}
					other := shuffled.evaluate(again[0].choice, shuffled.routeOf(again[0].choice))
					if got.key != other.key || got.joules != other.joules || stats.surrogateEvaluated != repeated.surrogateEvaluated {
						t.Errorf("mix=%d m=%s order changed key/J/evaluations: %s %g %d vs %s %g %d", mix, m.name, got.key, got.joules, stats.surrogateEvaluated, other.key, other.joules, repeated.surrogateEvaluated)
					}
					m.worst, m.maxEvaluations = max(m.worst, gap), max(m.maxEvaluations, stats.surrogateEvaluated)
					m.totalEvaluations += stats.surrogateEvaluated
					m.longest, m.total = max(m.longest, elapsed), m.total+elapsed
					t.Logf("mix=%02d m=%s rates=%v gap=%.6f%% feasible=%v J=%.9f evaluations=%d duration=%s steps=%d budgetCut=%v", mix, m.name, rates, gap*100, got.feasible, got.joules, stats.surrogateEvaluated, elapsed, stats.steps, stats.budgetCut)
				}
			}
			for _, m := range measurements {
				t.Logf("CORE variants=%d modes=%d m=%s mixes=%d worstGap=%.6f%% maxEvaluations=%d totalEvaluations=%d longest=%s total=%s budgetCuts=%d", space.variants, space.modes, m.name, len(e5Mixes), m.worst*100, m.maxEvaluations, m.totalEvaluations, m.longest, m.total, m.cuts)
			}
		})
	}
}

func TestCapacityV2CoreBudgetAndDeadline(t *testing.T) {
	p := e5Planner(e5Mixes[0], evaluationNodes(), e5Start, nil)
	got, stats := p.searchV2Core(p.planTable(), 10, 1, 4, time.Time{})
	if len(got) == 0 || !stats.budgetCut || stats.surrogateEvaluated != 10 {
		t.Fatalf("evaluation budget: finalists=%d stats=%+v", len(got), stats)
	}
	got, stats = p.searchV2Core(p.planTable(), 500000, 1, 4, time.Now().Add(-time.Second))
	if len(got) != 0 || !stats.budgetCut || stats.surrogateEvaluated != 0 {
		t.Fatalf("deadline: finalists=%d stats=%+v", len(got), stats)
	}
}
