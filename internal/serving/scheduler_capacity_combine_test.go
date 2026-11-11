package serving

import (
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
	v2 "github.com/lab-paper-code/chill/internal/scheduler/v2"
)

// This exhaustive oracle ranks the frozen scalar problem, not replay outcomes.
func capacityHeuristicBrute(p *capacityPlanner, tab *planTable) protocol.Rank {
	problem := p.scalarProblem(tab, time.Time{})
	choice := make([]int, len(p.plans))
	for n := range choice {
		choice[n] = tab.byPlan[n][0]
	}
	s := newSearchState(p, tab, choice)
	var best protocol.Rank
	found := false
	var visit func(int)
	visit = func(n int) {
		if n < len(choice) {
			for _, q := range tab.byPlan[n] {
				if tab.searchable[q] {
					choice[n] = q
					visit(n + 1)
				}
			}
			return
		}
		s.reset(choice)
		rank := capacityRank(s.delta(0, choice[0]), problem.Table)
		if !rank.Searchable || !rank.Bounded {
			return
		}
		if !found || rank.Better(&best) {
			best, found = capacityDeltaSaved(rank), true
		}
	}
	visit(0)
	return best
}

func capacityScalarProductFixture() *capacityPlanner {
	p := capacityDeltaTiny([]float64{10, 10, 10}, 29)
	for n := range p.plans {
		keep := p.plans[n][0]
		keep.before = 10
		keep.items[0][0].e, keep.retained[0][0].e = 0, 0
		plan := *keep
		plan.keep, plan.record.Key = false, "move"
		if n == 0 {
			item := keep.items[0][0]
			item.routingWeight, item.throughput = 30, 30
			plan.items, plan.after = [][]capacityItem{{item}}, 25
		} else {
			plan.items, plan.after = [][]capacityItem{{}}, 0
		}
		p.plans[n] = append(p.plans[n], &plan)
	}
	p.keepWatts = 30
	return p
}

func TestCapacityCombineFrozenProduct(t *testing.T) {
	p := capacityScalarProductFixture()
	problem := p.scalarProblem(p.planTable(), time.Time{})
	candidates, stats := v2.Candidates(problem, problem.Table, 0, 16, 0, time.Time{})
	if len(candidates) == 0 || !slices.Equal(candidates[0].Choice, []int{1, 1, 1}) || stats.Deadline || stats.Exhausted {
		t.Fatalf("three-node scalar product missing: candidates=%+v stats=%+v", candidates, stats)
	}
	if !slices.ContainsFunc(stats.SearchSteps, func(step protocol.Step) bool {
		return step.Start == capacitySearchStartKeep && step.Phase == protocol.Combine && step.K == 3 && step.Evaluated > 0
	}) {
		t.Fatalf("missing product move: %+v", stats.SearchSteps)
	}
}

func TestCapacityCombineMatchesFrozenExhaustiveOracle(t *testing.T) {
	for _, horizon := range []float64{1, 10, 30} {
		p := e5Planner(map[int]float64{0: 20, 1: 40}, evaluationNodes(), e5Start)
		p.periodSeconds = horizon
		tab := p.planTable()
		problem := p.scalarProblem(tab, time.Time{})
		candidates, stats := v2.Candidates(problem, problem.Table, 0, 16, 0, time.Time{})
		if len(candidates) == 0 || stats.Deadline || stats.Exhausted {
			t.Fatalf("H=%g missing scalar endpoint: %+v", horizon, stats)
		}
		want := capacityHeuristicBrute(p, tab)
		choice := make([]int, len(p.plans))
		for n, j := range candidates[0].Choice {
			choice[n] = tab.byPlan[n][j]
		}
		state := newSearchState(p, tab, choice)
		got := capacityRank(state.delta(0, choice[0]), problem.Table)
		if got.Better(&want) || want.Better(&got) {
			t.Fatalf("H=%g scalar exhaustive mismatch: got=%+v want=%+v", horizon, got, want)
		}
	}
}

func TestCapacityCombineAllActiveIncludesEveryProfileVariant(t *testing.T) {
	for _, active := range []int{0, -1} {
		p := e5Planner(map[int]float64{0: 20}, evaluationScaled(2, 0), e5Start)
		problem := p.scalarProblem(p.planTable(), time.Time{})
		v2.Group(problem.Table)
		multiple := false
		for n, plans := range problem.Table.ByPlan {
			for _, q := range plans {
				if !problem.Table.Searchable[q] {
					continue
				}
				entry := problem.Table.Entries[problem.Table.EntryOf[q]]
				if entry.Node != n || !slices.Contains(entry.Variants, q) {
					t.Fatalf("active=%d omitted profile variant %d", active, q)
				}
				multiple = multiple || len(entry.Variants) > 1
			}
		}
		if !multiple {
			t.Fatal("fixture needs multiple profile variants")
		}
		candidates, stats := v2.Candidates(problem, problem.Table, 0, 16, active, time.Time{})
		if len(candidates) == 0 || stats.Deadline || stats.Exhausted {
			t.Fatalf("active=%d missing endpoint: %+v", active, stats)
		}
	}
}
