package serving

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	v2 "github.com/lab-paper-code/chill/internal/scheduler/v2"
)

func TestCapacityV2CorePlanOrder(t *testing.T) {
	for _, space := range []struct{ variants, modes int }{{1, 0}, {2, 0}, {1, 2}} {
		p := e5Planner(map[int]float64{0: 60, 1: 15, 2: 1.2}, evaluationScaled(space.variants, space.modes), e5Start, nil)
		problem := p.scalarProblem(p.planTable(), time.Time{})
		got, stats := v2.Candidates(problem, problem.Table, 50000, 16, 4, time.Time{})
		if len(got) == 0 || stats.Deadline { t.Fatalf("no heuristic finalists: %+v", stats) }
		keys := func(planner *capacityPlanner, choices []int) string {
			tab := planner.planTable()
			global := make([]int, len(choices))
			for n, j := range choices { global[n] = tab.byPlan[n][j] }
			state := newSearchState(planner, tab, global)
			return capacityDeltaKey(state.delta(0, global[0]))
		}
		want := keys(p, got[0].Choice)
		shuffled := *p
		shuffled.plans = make([][]*capacityPlan, len(p.plans))
		rng := rand.New(rand.NewPCG(20261109, 4))
		for n, plans := range p.plans {
			shuffled.plans[n] = slices.Clone(plans)
			rng.Shuffle(len(plans)-1, func(a, b int) { shuffled.plans[n][a+1], shuffled.plans[n][b+1] = shuffled.plans[n][b+1], shuffled.plans[n][a+1] })
		}
		other := shuffled.scalarProblem(shuffled.planTable(), time.Time{})
		again, repeated := v2.Candidates(other, other.Table, 50000, 16, 4, time.Time{})
		if len(again) == 0 || keys(&shuffled, again[0].Choice) != want || got[0].Joules != again[0].Joules || stats.SurrogateEvaluated != repeated.SurrogateEvaluated {
			t.Fatalf("plan order changed heuristic endpoint: first=%+v again=%+v", got, again)
		}
	}
}

func TestCapacityV2CoreBudgetAndDeadline(t *testing.T) {
	p := e5Planner(e5Mixes[0], evaluationNodes(), e5Start, nil)
	problem := p.scalarProblem(p.planTable(), time.Time{})
	got, stats := v2.Candidates(problem, problem.Table, 10, 1, 4, time.Time{})
	if len(got) == 0 || !stats.Exhausted || stats.Deadline || stats.SurrogateEvaluated != 10 { t.Fatalf("evaluation budget: finalists=%d stats=%+v", len(got), stats) }
	got, stats = v2.Candidates(problem, problem.Table, 1, 1, 4, time.Time{})
	if len(got) != 1 || !slices.Equal(got[0].Choice, make([]int, len(p.plans))) || !stats.Exhausted || stats.Deadline || stats.SurrogateEvaluated != 1 || len(stats.SearchSteps) != 1 || stats.SearchSteps[0].Start != capacitySearchStartKeep || stats.SearchSteps[0].K != 0 {
		t.Fatalf("origin-only budget lost endpoint: finalists=%+v stats=%+v", got, stats)
	}
	got, stats = v2.Candidates(problem, problem.Table, 500000, 1, 4, time.Now().Add(-time.Second))
	if len(got) != 0 || !stats.Deadline || stats.Exhausted || stats.SurrogateEvaluated != 0 { t.Fatalf("deadline: finalists=%d stats=%+v", len(got), stats) }
}

func TestCapacityExactFeedbackTupleOrdering(t *testing.T) {
	p := capacityDeltaTiny([]float64{10}, 1)
	score := func(failure, success, potential float64) FeedbackWorkloadScore {
		return FeedbackWorkloadScore{UID: "w", Population: 10, FailureFraction: failure, SuccessQuantileNanos: &success, PotentialExcess: &potential, Satisfied: failure == 0 && success <= 1e9}
	}
	for _, tc := range []struct { name string; a, b FeedbackWorkloadScore }{
		{"failure-before-success", score(.1, 9e9, 100), score(.2, 1e9, 0)},
		{"success-before-potential", score(.2, 2e9, 100), score(.2, 3e9, 0)},
		{"potential-breaks-outcome-tie", score(.2, 2e9, .1), score(.2, 2e9, .2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := capacityResult{feedback: []FeedbackWorkloadScore{tc.a}, joules: 1000}
			b := capacityResult{feedback: []FeedbackWorkloadScore{tc.b}, joules: 1}
			p.rankFeedback(&a); p.rankFeedback(&b)
			a.final, b.final = false, true
			if !a.better(&b) || b.better(&a) { t.Fatalf("tuple ordering lost to Final/energy: A=%+v B=%+v", a, b) }
		})
	}
	// Feasible candidates compare energy without rewarding unused SLO margin.
	a := capacityResult{feedback: []FeedbackWorkloadScore{score(0, 900e6, 0)}, joules: 1}
	b := capacityResult{feedback: []FeedbackWorkloadScore{score(0, 100e6, 0)}, joules: 2}
	p.rankFeedback(&a); p.rankFeedback(&b)
	if !a.better(&b) { t.Fatal("feasible spare margin overrode energy") }
	a.exclusion = "NoRoute:w"
	if a.better(&b) { t.Fatal("physical impossibility outranked an eligible finalist") }
}

func TestCapacityExactFeedbackWorkloadPriority(t *testing.T) {
	p := capacityDeltaTiny([]float64{10}, 1)
	d := *p.ws[0].d
	d.UID = "other"
	p.ws = append(p.ws, capacityWorkload{d: &d, rate: 1})
	for _, strategy := range []string{SLOPriorityExcess, SLOPriorityWorkload} {
		p.settings.SLOPriorityStrategy, p.settings.WorkloadPriority = strategy, []string{"w"}
		a := capacityResult{feedback: []FeedbackWorkloadScore{{UID: "w", FailureFraction: .1}, {UID: "other", FailureFraction: .9}}}
		b := capacityResult{feedback: []FeedbackWorkloadScore{{UID: "w", FailureFraction: .2}, {UID: "other", FailureFraction: .3}}}
		p.rankFeedback(&a); p.rankFeedback(&b)
		if a.better(&b) != (strategy == SLOPriorityWorkload) { t.Fatalf("strategy=%s A=%v B=%v", strategy, a.excesses, b.excesses) }
	}
}
