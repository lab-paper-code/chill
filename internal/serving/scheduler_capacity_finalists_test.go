package serving

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	v2 "github.com/lab-paper-code/chill/internal/scheduler/v2"
)

func TestCapacityV2CorePlanOrder(t *testing.T) {
	for _, space := range []struct{ variants, modes int }{{1, 0}, {2, 0}, {1, 2}} {
		p := e5Planner(map[int]float64{0: 60, 1: 15, 2: 1.2}, evaluationScaled(space.variants, space.modes), e5Start)
		problem := p.scalarProblem(p.planTable(), time.Time{})
		got, stats := v2.Candidates(problem, problem.Table, 50000, 16, 4, time.Time{})
		if len(got) == 0 || stats.Deadline {
			t.Fatalf("no heuristic finalists: %+v", stats)
		}
		keys := func(planner *capacityPlanner, choices []int) string {
			tab := planner.planTable()
			global := make([]int, len(choices))
			for n, j := range choices {
				global[n] = tab.byPlan[n][j]
			}
			state := newSearchState(planner, tab, global)
			return capacityDeltaKey(state.delta(0, global[0]))
		}
		want := keys(p, got[0].Choice)
		shuffled := e5Planner(map[int]float64{0: 60, 1: 15, 2: 1.2}, evaluationScaled(space.variants, space.modes), e5Start)
		rng := rand.New(rand.NewPCG(20261109, 4))
		for n, plans := range p.plans {
			shuffled.plans[n] = slices.Clone(plans)
			rng.Shuffle(len(plans)-1, func(a, b int) {
				shuffled.plans[n][a+1], shuffled.plans[n][b+1] = shuffled.plans[n][b+1], shuffled.plans[n][a+1]
			})
		}
		other := shuffled.scalarProblem(shuffled.planTable(), time.Time{})
		again, repeated := v2.Candidates(other, other.Table, 50000, 16, 4, time.Time{})
		if len(again) == 0 || keys(shuffled, again[0].Choice) != want || got[0].Joules != again[0].Joules || stats.SurrogateEvaluated != repeated.SurrogateEvaluated {
			t.Fatalf("plan order changed heuristic endpoint: first=%+v again=%+v", got, again)
		}
	}
}

func TestCapacityV2CoreBudgetAndDeadline(t *testing.T) {
	p := e5Planner(e5Mixes[0], evaluationNodes(), e5Start)
	problem := p.scalarProblem(p.planTable(), time.Time{})
	got, stats := v2.Candidates(problem, problem.Table, 10, 1, 4, time.Time{})
	if len(got) == 0 || !stats.Exhausted || stats.Deadline || stats.SurrogateEvaluated != 10 {
		t.Fatalf("evaluation budget: finalists=%d stats=%+v", len(got), stats)
	}
	got, stats = v2.Candidates(problem, problem.Table, 500000, 1, 4, time.Now().Add(-time.Second))
	if len(got) != 0 || !stats.Deadline || stats.Exhausted || stats.SurrogateEvaluated != 0 {
		t.Fatalf("deadline: finalists=%d stats=%+v", len(got), stats)
	}
}

func TestCapacityExactFeedbackTupleOrdering(t *testing.T) {
	p := capacityDeltaTiny([]float64{10}, 1)
	score := func(failure, success, potential float64) FeedbackWorkloadScore {
		return FeedbackWorkloadScore{UID: "w", Population: 10, OnTime: 10 - int(failure*10), Rejected: int(failure * 10), FailureFraction: failure, SuccessQuantileNanos: &success, SuccessExcess: max(0, success/1e9-1), PotentialExcess: &potential, Satisfied: failure == 0 && success <= 1e9}
	}
	for _, tc := range []struct {
		name string
		a, b FeedbackWorkloadScore
	}{
		{"failure-before-success", score(.1, 9e9, 100), score(.2, 1e9, 0)},
		{"success-before-potential", score(.2, 2e9, 100), score(.2, 3e9, 0)},
		{"potential-breaks-outcome-tie", score(.2, 2e9, .1), score(.2, 2e9, .2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := capacityResult{feedback: []FeedbackWorkloadScore{tc.a}, joules: 1000}
			b := capacityResult{feedback: []FeedbackWorkloadScore{tc.b}, joules: 1}
			p.rankFeedback(&a)
			p.rankFeedback(&b)
			a.final, b.final = false, true
			if !a.better(&b) || b.better(&a) {
				t.Fatalf("tuple ordering lost to Final/energy: A=%+v B=%+v", a, b)
			}
		})
	}
	// Feasible candidates compare energy without rewarding unused SLO margin.
	a := capacityResult{feedback: []FeedbackWorkloadScore{score(0, 900e6, 0)}, joules: 1}
	b := capacityResult{feedback: []FeedbackWorkloadScore{score(0, 100e6, 0)}, joules: 2}
	p.rankFeedback(&a)
	p.rankFeedback(&b)
	if !a.better(&b) {
		t.Fatal("feasible spare margin overrode energy")
	}
	a.exclusion = "NoRoute:w"
	if a.better(&b) {
		t.Fatal("physical impossibility outranked an eligible finalist")
	}
}

func TestCapacityExactFeedbackWorkloadPriority(t *testing.T) {
	p := capacityDeltaTiny([]float64{10}, 1)
	d := *p.ws[0].d
	d.UID = schedulerTestOther
	p.ws = append(p.ws, capacityWorkload{d: &d, rate: 1})
	for _, strategy := range []string{SLOPriorityExcess, SLOPriorityWorkload} {
		p.settings.SLOPriorityStrategy, p.settings.WorkloadPriority = strategy, []string{"w"}
		a := capacityResult{feedback: []FeedbackWorkloadScore{{UID: "w", Population: 10, OnTime: 9, FailureFraction: .1}, {UID: schedulerTestOther, Population: 10, OnTime: 1, FailureFraction: .9}}}
		b := capacityResult{feedback: []FeedbackWorkloadScore{{UID: "w", Population: 10, OnTime: 8, FailureFraction: .2}, {UID: schedulerTestOther, Population: 10, OnTime: 7, FailureFraction: .3}}}
		p.rankFeedback(&a)
		p.rankFeedback(&b)
		if a.better(&b) != (strategy == SLOPriorityWorkload) {
			t.Fatalf("strategy=%s A=%v B=%v", strategy, a.excesses, b.excesses)
		}
	}
}
func TestCapacityV2TinyBudgetPreservesFeasibleKeep(t *testing.T) {
	p := capacityThreeNodeSwap()
	p.settings.PlanSearchStrategy, p.settings.PlanSearchEvaluations = "v2", 1
	p.settings.ComparisonPeriod, p.periodSeconds = time.Second, 1
	p.ws[0].d.SLO.TargetLatency = 500 * time.Millisecond
	p.resolveCapacity()
	for n := range 2 {
		empty := p.plan(n, p.snapshot.Nodes[n].PowerMode, p.plans[n][0].instances, nil)
		if empty == nil {
			t.Fatal("production empty plan missing")
		}
		p.plans[n] = []*capacityPlan{p.plans[n][0], empty}
	}
	// The third node is fixed, so the empty start retains a real route while
	// removing enough supply to violate the SLO (rather than NoRoute).
	p.plans[2] = p.plans[2][:1]
	keep := p.evaluate([]int{0, 0, 0}, 0)
	if !keep.feasible {
		t.Fatalf("keep must meet SLO: %+v", keep)
	}
	got, err := p.search(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.feasible || !got.keep {
		t.Fatalf("tiny budget replaced feasible keep: %+v", got)
	}
}
func TestCapacityFinalistsKeepAndCapsSharePhysicalPlacement(t *testing.T) {
	p := capacityTwinSwap(100, 0)
	same := *p.plans[0][0]
	same.keep, same.record.Key = false, "cap-policy"
	same.instances = slices.Clone(same.instances)
	same.instances[0].MaxBatch++
	i := same.instances[0]
	same.option = fmt.Sprintf("%q/%v/%d/%q/%q", i.Profile.ID, i.Allocation, i.MaxWait, i.WorkloadUID, same.mode)
	p.plans[0] = append(p.plans[0], &same)
	problem := p.scalarProblem(p.planTable(), time.Time{})
	tab := problem.Table
	// Confirm production normalization, not the search grouping's keep entry.
	if tab.Placement[0] != tab.Placement[2] {
		t.Fatalf("keep/cap physical identity differs: placement=%v", tab.Placement)
	}
	candidates, _ := v2.Candidates(problem, tab, 0, 2, 0, time.Time{})
	seen := map[string]bool{}
	for _, c := range candidates {
		if c.Start != "keep" {
			continue
		} // Endpoints are intentionally retained outside K.
		key := tab.Placement[tab.ByPlan[0][c.Choice[0]]]
		if seen[key] {
			t.Fatalf("physical placement repeated in shortlist: %+v", candidates)
		}
		seen[key] = true
	}
}
