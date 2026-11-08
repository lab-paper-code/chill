package serving

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
	v2 "github.com/lab-paper-code/chill/internal/scheduler/v2"
)

func TestCapacityCapSelection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		e          float64
		state      v2.CapState
		variants   []int
		candidates []int
		preferred  int
	}{
		{"cheap", 1, v2.CapState{KappaOthers: 10, KappaEOthers: 20, Demand: 12}, []int{0, 1, 2}, []int{2, 1}, 2},
		{"expensive", 3, v2.CapState{KappaOthers: 10, KappaEOthers: 20, Demand: 12}, []int{0, 1, 2}, []int{2, 1}, 1},
		{"equal-energy", 2, v2.CapState{KappaOthers: 10, KappaEOthers: 20, Demand: 12}, []int{0, 1, 2}, []int{2, 1}, 2},
		{"no-others", 3, v2.CapState{Demand: 2}, []int{0, 1, 2}, []int{2, 1}, 2},
		{"no-satisfying", 3, v2.CapState{KappaOthers: 10, KappaEOthers: 20, Demand: 15}, []int{0, 1, 2}, []int{2}, 2},
		{"overload-backlog", 3, v2.CapState{KappaOthers: 10, KappaEOthers: 20, Demand: 12, Overload: true, Backlog: true}, []int{0, 1, 2}, []int{2}, 2},
		{"overload-zero-backlog", 3, v2.CapState{KappaOthers: 10, KappaEOthers: 20, Demand: 12, Overload: true}, []int{0, 1, 2}, []int{2, 1}, 1},
		{"one", 3, v2.CapState{KappaOthers: 10, KappaEOthers: 20, Demand: 12}, []int{1}, []int{1}, 1},
		{"same-endpoint", 3, v2.CapState{KappaOthers: 10, KappaEOthers: 20, Demand: 14}, []int{0, 1, 2}, []int{2}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tab := &protocol.Table{Kappa: [][]float64{{1}, {2}, {4}}, KappaE: [][]float64{{tc.e}, {2 * tc.e}, {4 * tc.e}}}
			got := v2.SelectCaps(tab, protocol.Entry{Workload: 0, Variants: tc.variants}, tc.state)
			if !slices.Equal(got.Candidates[:got.Count], tc.candidates) || got.Preferred != tc.preferred {
				t.Fatalf("got=%+v candidates=%v preferred=%d", got, tc.candidates, tc.preferred)
			}
		})
	}
}

func TestCapacityCapExact(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261107, 2))
	states, moves, mismatches, preferredMatches := 0, 0, 0, 0
	failures := map[string]int{}
	for mix, rates := range e5Mixes {
		p := e5Planner(rates, evaluationScaled(2, 0), e5Start, nil)
		tab := p.planTable()
		problem := p.scalarProblem(tab, time.Time{})
		v2.Group(problem.Table)
		choices := make([][]int, 34)
		for i := range choices {
			choice := make([]int, len(p.plans))
			for n, plans := range p.plans {
				switch i {
				case 0: // keep
				case 1: // empty, or keep when no empty plan exists
					for j, plan := range plans {
						if plan.record.Eligible && len(plan.instances) == 0 {
							choice[n] = j
							break
						}
					}
				default:
					eligible := []int{}
					for j, plan := range plans {
						if plan.record.Eligible {
							eligible = append(eligible, j)
						}
					}
					choice[n] = eligible[rng.IntN(len(eligible))]
				}
			}
			choices[i] = choice
		}
		for stateIndex, choice := range choices {
			states++
			for _, entry := range problem.Table.Entries {
				current := p.plans[entry.Node][choice[entry.Node]]
				first := entry.Variants[0]
				if current.keep == tab.keep[first] && current.option == entry.Option && current.mode == entry.Mode {
					continue
				}
				state := capacityCapMoveState(p, tab, choice, entry)
				selected := v2.SelectCaps(problem.Table, entry, state)
				best := capacityCapBest(p, tab, choice, entry.Node, entry.Variants)
				if best == nil { // bounded rejects all variants of this option
					continue
				}
				got := capacityCapBest(p, tab, choice, entry.Node, selected.Candidates[:selected.Count])
				preferred := capacityCapBest(p, tab, choice, entry.Node, []int{selected.Preferred})
				moves++
				if capacityCapEquivalent(preferred, best) {
					preferredMatches++
				}
				if !capacityCapEquivalent(got, best) {
					mismatches++
					reason := "non-overload"
					if state.Overload {
						reason = "overload"
					}
					if got.joules == best.joules && got.feasible == best.feasible && got.final == best.final && slices.Equal(got.excesses, best.excesses) {
						reason += "-key"
					}
					failures[reason]++
					if failures[reason] == 1 {
						t.Errorf("reason=%s mix=%d rates=%v state=%d choice=%v node=%d option=%q input=%+v candidates=%v got={key:%s feasible:%t final:%t excess:%v J:%.17g} all={key:%s feasible:%t final:%t excess:%v J:%.17g}",
							reason, mix, rates, stateIndex, choice, entry.Node, entry.Option, state, selected.Candidates[:selected.Count], got.key, got.feasible, got.final, got.excesses, got.joules, best.key, best.feasible, best.final, best.excesses, best.joules)
					}
				}
			}
		}
	}
	t.Logf("states=%d moves=%d mismatches=%d preferred-alone=%d/%d (%.2f%%)", states, moves, mismatches, preferredMatches, moves, 100*float64(preferredMatches)/float64(moves))
	t.Logf("mismatch classes=%v", failures)
	if mismatches > 0 {
		t.Fatalf("cap sub-selection disagrees with exact evaluator on %d moves", mismatches)
	}
}

// capacityCapMoveState uses table sums after the move. The route and retained
// supply do not depend on cap, so one variant gives the common λ̃.
func capacityCapMoveState(p *capacityPlanner, tab *planTable, choice []int, entry protocol.Entry) v2.CapState {
	moved := slices.Clone(choice)
	moved[entry.Node] = tab.index[entry.Variants[0]]
	r := p.routeOf(moved)
	state := v2.CapState{}
	for w, ws := range p.ws {
		kappa, retained, kappaE := 0.0, 0.0, 0.0
		for n, j := range moved {
			q := tab.byPlan[n][j]
			retained += tab.kappaRet[q][w]
			if n != entry.Node {
				kappa += tab.kappa[q][w]
				kappaE += tab.kappaE[q][w]
			}
		}
		demand := ws.rate
		backlog := math.Max(0, ws.rate-retained) * r // this gate has q=0
		if ws.slack > 0 {
			demand = math.Max(demand, backlog/ws.slack)
		} else if backlog > 0 {
			demand = math.Inf(1)
		}
		if w == entry.Workload {
			state.KappaOthers, state.KappaEOthers, state.Demand = kappa, kappaE, demand
			state.Backlog = backlog > 0
		} else {
			q := entry.Variants[0]
			state.Overload = state.Overload || kappa+tab.kappa[q][w] < demand
		}
	}
	return state
}

func capacityCapBest(p *capacityPlanner, tab *planTable, choice []int, n int, variants []int) *capacityResult {
	var best *capacityResult
	for _, q := range variants {
		moved := slices.Clone(choice)
		moved[n] = tab.index[q]
		if !p.bounded(moved) {
			continue
		}
		res := p.evaluate(moved, p.routeOf(moved))
		if res.better(best) {
			best = &res
		}
	}
	return best
}

func capacityCapEquivalent(a, b *capacityResult) bool {
	return a != nil && b != nil && (a.key == b.key || !a.better(b) && !b.better(a))
}
