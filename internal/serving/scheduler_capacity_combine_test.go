package serving

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
	"github.com/lab-paper-code/chill/internal/scheduler/v2"
)

func TestCapacityCombineMatchesExact(t *testing.T) {
	p := e5Planner(map[int]float64{0: 20, 1: 40, 2: 0.3}, evaluationNodes(), e5Start, nil)
	problem := p.scalarProblem(p.planTable(), time.Time{})
	candidates, stats := v2.Candidates(problem, problem.Table, 0, 16, 0, time.Time{})
	exact := e5Brute(p)
	if len(candidates) == 0 || exact == nil || !candidates[0].Feasible || math.Abs(candidates[0].Joules-exact.joules) > 1e-8 {
		t.Fatalf("candidates=%+v exact=%+v", candidates, exact)
	}
	if stats.Deadline || stats.Exhausted {
		t.Fatalf("unlimited combine stopped early: %+v", stats)
	}
	// This fixture needs a three-node product move beyond the k=1/2 optimum.
	p = capacityThreeNodeSwap()
	// Direct Candidates callers seed the common-window baseline like Engine.Search.
	adapter := newCapacityExact(p)
	adapter.SeedKeep(adapter.Evaluate(make([]int, len(p.plans))))
	problem = p.scalarProblem(p.planTable(), time.Time{})
	candidates, stats = v2.Candidates(problem, problem.Table, 0, 16, 0, time.Time{})
	if len(candidates) == 0 || !slices.Equal(candidates[0].Choice, []int{1, 1, 1}) ||
		!slices.ContainsFunc(stats.SearchSteps, func(step protocol.Step) bool {
			return step.Start == "keep" && step.Phase == protocol.Combine && step.K == 3 && step.Evaluated > 0
		}) {
		t.Fatalf("missing accepted product move: candidates=%+v steps=%+v", candidates, stats.SearchSteps)
	}
}

func TestCapacityCombineClampedMatchesExact(t *testing.T) {
	for _, horizon := range []float64{1, 10, 30} {
		p := e5Planner(map[int]float64{0: 20, 1: 40, 2: .3}, evaluationNodes(), e5Start, nil)
		p.period = horizon
		problem := p.scalarProblem(p.planTable(), time.Time{})
		candidates, stats := v2.Candidates(problem, problem.Table, 0, 16, 0, time.Time{})
		exact := e5Brute(p)
		if len(candidates) == 0 || exact == nil || candidates[0].Feasible != exact.feasible || math.Abs(candidates[0].Joules-exact.joules) > 1e-8 {
			t.Fatalf("H=%g clamped bound discarded optimum: candidates=%+v exact=%+v", horizon, candidates, exact)
		}
		if stats.Deadline || stats.Exhausted {
			t.Fatalf("H=%g unlimited combine stopped early: %+v", horizon, stats)
		}
		t.Logf("H=%g candidate=exact J=%.17g", horizon, candidates[0].Joules)
	}
}

func TestCapacityCombineOverloadKeepsLeximinBranches(t *testing.T) {
	p := e5Planner(map[int]float64{0: 1000000, 1: 1000000}, evaluationNodes(), e5Start, nil)
	// On three nodes, v1's k=3 neighborhood is an independent exhaustive oracle,
	// including infeasible results that the feasible-only e5Brute omits.
	p.plans, p.compared, p.snapshot.Nodes = p.plans[:3], p.compared[:3], p.snapshot.Nodes[:3]
	p.settings.PlanSearchStrategy = protocol.V1
	exact, err := p.search(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if exact.feasible {
		t.Fatal("fixture must remain overloaded")
	}
	problem := p.scalarProblem(p.planTable(), time.Time{})
	candidates, stats := v2.Candidates(problem, problem.Table, 0, 16, 0, time.Time{})
	if len(candidates) == 0 || stats.Deadline || stats.Exhausted {
		t.Fatalf("overload combine stopped early: candidates=%d stats=%+v", len(candidates), stats)
	}
	got := p.evaluate(candidates[0].Choice, p.routeOf(candidates[0].Choice))
	if got.better(&exact) || exact.better(&got) {
		t.Fatalf("overload pruning discarded leximin optimum: %+v vs %+v", got, exact)
	}
}

func TestCapacityCombineAllActiveIncludesEveryCap(t *testing.T) {
	for _, active := range []int{0, -1} {
		p := e5Planner(map[int]float64{0: 20}, evaluationScaled(2, 0), e5Start, nil)
		problem := p.scalarProblem(p.planTable(), time.Time{})
		v2.Group(problem.Table)
		multiCap := false
		for n, plans := range problem.Table.ByPlan {
			for _, q := range plans {
				if !problem.Table.Searchable[q] {
					continue
				}
				entry := problem.Table.Entries[problem.Table.EntryOf[q]]
				if entry.Node != n || !slices.Contains(entry.Variants, q) {
					t.Fatalf("active=%d omitted searchable cap %d", active, q)
				}
				multiCap = multiCap || len(entry.Variants) > 1
			}
		}
		if !multiCap {
			t.Fatal("fixture must include multiple cap variants")
		}
		candidates, stats := v2.Candidates(problem, problem.Table, 0, 16, active, time.Time{})
		exact := e5Brute(p)
		if len(candidates) == 0 || exact == nil || !candidates[0].Feasible || math.Abs(candidates[0].Joules-exact.joules) > 1e-8 || stats.Deadline || stats.Exhausted {
			t.Fatalf("active=%d all-cap optimum missing: candidates=%+v exact=%+v stats=%+v", active, candidates, exact, stats)
		}
	}
}
