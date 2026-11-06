package serving

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

func TestCapacityCapSelection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		e          float64
		state      capacityCapState
		variants   []int
		candidates []int
		preferred  int
	}{
		{"cheap", 1, capacityCapState{10, 20, 12, false, false}, []int{0, 1, 2}, []int{2, 1}, 2},
		{"expensive", 3, capacityCapState{10, 20, 12, false, false}, []int{0, 1, 2}, []int{2, 1}, 1},
		{"equal-energy", 2, capacityCapState{10, 20, 12, false, false}, []int{0, 1, 2}, []int{2, 1}, 2},
		{"no-others", 3, capacityCapState{0, 0, 2, false, false}, []int{0, 1, 2}, []int{2, 1}, 2},
		{"no-satisfying", 3, capacityCapState{10, 20, 15, false, false}, []int{0, 1, 2}, []int{2}, 2},
		{"overload-backlog", 3, capacityCapState{10, 20, 12, true, true}, []int{0, 1, 2}, []int{2}, 2},
		{"overload-zero-backlog", 3, capacityCapState{10, 20, 12, true, false}, []int{0, 1, 2}, []int{2, 1}, 1},
		{"one", 3, capacityCapState{10, 20, 12, false, false}, []int{1}, []int{1}, 1},
		{"same-endpoint", 3, capacityCapState{10, 20, 14, false, false}, []int{0, 1, 2}, []int{2}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tab := &planTable{kappa: [][]float64{{1}, {2}, {4}}, kappaE: [][]float64{{tc.e}, {2 * tc.e}, {4 * tc.e}}}
			got := tab.selectCaps(capacityPlanEntry{workload: 0, variants: tc.variants}, tc.state)
			if !slices.Equal(got.candidates[:got.count], tc.candidates) || got.preferred != tc.preferred {
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
			for _, entry := range tab.entries {
				current := p.plans[entry.node][choice[entry.node]]
				first := entry.variants[0]
				if current.keep == tab.keep[first] && current.option == entry.option && current.mode == entry.mode {
					continue
				}
				state := capacityCapMoveState(p, tab, choice, entry)
				selected := tab.selectCaps(entry, state)
				best := capacityCapBest(p, tab, choice, entry.node, entry.variants)
				if best == nil { // bounded rejects all variants of this option
					continue
				}
				got := capacityCapBest(p, tab, choice, entry.node, selected.candidates[:selected.count])
				preferred := capacityCapBest(p, tab, choice, entry.node, []int{selected.preferred})
				moves++
				if capacityCapEquivalent(preferred, best) {
					preferredMatches++
				}
				if !capacityCapEquivalent(got, best) {
					mismatches++
					reason := "non-overload"
					if state.overload {
						reason = "overload"
					}
					if got.joules == best.joules && got.feasible == best.feasible && got.final == best.final && slices.Equal(got.excesses, best.excesses) {
						reason += "-key"
					}
					failures[reason]++
					if failures[reason] == 1 {
						t.Errorf("reason=%s mix=%d rates=%v state=%d choice=%v node=%d option=%q input=%+v candidates=%v got={key:%s feasible:%t final:%t excess:%v J:%.17g} all={key:%s feasible:%t final:%t excess:%v J:%.17g}",
							reason, mix, rates, stateIndex, choice, entry.node, entry.option, state, selected.candidates[:selected.count], got.key, got.feasible, got.final, got.excesses, got.joules, best.key, best.feasible, best.final, best.excesses, best.joules)
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
func capacityCapMoveState(p *capacityPlanner, tab *planTable, choice []int, entry capacityPlanEntry) capacityCapState {
	moved := slices.Clone(choice)
	moved[entry.node] = tab.index[entry.variants[0]]
	r := p.routeOf(moved)
	state := capacityCapState{}
	for w, ws := range p.ws {
		kappa, retained, kappaE := 0.0, 0.0, 0.0
		for n, j := range moved {
			q := tab.byPlan[n][j]
			retained += tab.kappaRet[q][w]
			if n != entry.node {
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
		if w == entry.workload {
			state.kappaOthers, state.kappaEOthers, state.demand = kappa, kappaE, demand
			state.backlog = backlog > 0
		} else {
			q := entry.variants[0]
			state.overload = state.overload || kappa+tab.kappa[q][w] < demand
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
