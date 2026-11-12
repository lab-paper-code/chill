package v2

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

// finiteState supplies the frozen scalar contract without serving profiles or
// replay. Engine tests exercise candidate reachability and rank preservation;
// the serving consumer tests own production Delta and exact outcome arithmetic.
type finiteState struct {
	table      *protocol.Table
	workloads  []protocol.Workload
	arrivals   [][]float64
	priority   []int
	choice     []int
	result     protocol.Rank
	raw        []float64
	percentile float64
}

func (s *finiteState) Choice() []int      { return s.choice }
func (s *finiteState) Reset(choice []int) { copy(s.choice, choice) }
func (s *finiteState) Apply(n, q int)     { s.choice[n] = q }
func (s *finiteState) Delta(n, q int) protocol.Rank {
	r, tab := s.result, s.table
	copy(r.Choice, s.choice)
	r.Choice[n] = q
	r.Feasible, r.Final, r.Keep, r.Searchable, r.Bounded = true, true, true, true, true
	r.Route, r.Joules = 0, 0
	for _, plan := range r.Choice {
		r.Keep = r.Keep && tab.Keep[plan]
		r.Searchable = r.Searchable && tab.Searchable[plan]
		if !tab.Keep[plan] {
			r.Route = max(r.Route, tab.TPre[plan])
		}
	}
	for node, plan := range r.Choice {
		if tab.Compared[node] {
			r.Joules += tab.Energy(plan, r.Route, tab.Period)
		}
	}
	for w, workload := range s.workloads {
		rate, retained, service, retainedService := 0.0, 0.0, 0.0, 0.0
		for node, plan := range r.Choice {
			rate += tab.RoutingRate[plan][w]
			retained += tab.RetainedRoutingRate[plan][w]
			service += tab.RoutingRateServiceNanos[plan][w]
			retainedService += tab.RoutingRateServiceNanos[tab.ByPlan[node][0]][w]
		}
		excess := 0.0
		if len(s.arrivals[w]) > 0 || workload.Queued > 0 {
			if rate <= 0 {
				excess, r.Searchable = math.Inf(1), false
			} else {
				pre := min(r.Route, tab.Period)
				preCount := sort.Search(len(s.arrivals[w]), func(i int) bool { return s.arrivals[w][i] >= pre*1e9 })
				preMass, postMass := float64(workload.Queued+preCount), float64(len(s.arrivals[w])-preCount)
				preLast, postLast := 0.0, pre
				if preCount > 0 {
					preLast = min(pre, s.arrivals[w][preCount-1]/1e9)
				}
				if len(s.arrivals[w]) > preCount {
					postLast = s.arrivals[w][len(s.arrivals[w])-1] / 1e9
				} else if r.Route == 0 {
					postLast = 0
				}
				backlog := max(0, preMass-retained*preLast)
				atRoute := max(0, preMass-retained*pre)
				postBacklog := max(0, atRoute+postMass-rate*max(0, postLast-pre))
				hPost := workload.TotalNanos + 1e9*max(0, postBacklog-1)/rate + service/rate
				if r.Route == 0 {
					preMass, postMass = 0, preMass+postMass
				}
				hPre := workload.TotalNanos + 1e9*r.Route + 1e9*max(0, backlog-1)/rate + service/rate
				if retained > 0 {
					hPre = workload.TotalNanos + 1e9*max(0, backlog-1)/retained + retainedService/retained
				}
				h := hPost
				if preMass > 0 {
					h = hPre
					if postMass > 0 {
						low, high, mass := hPre, hPost, preMass
						if hPost < hPre {
							low, high, mass = hPost, hPre, postMass
						}
						h = low
						if mass < (preMass+postMass)*s.percentile/100 {
							h = high
						}
					}
				}
				excess = max(0, h/workload.TargetNanos-1)
			}
		}
		s.raw[w] = excess
		r.Feasible = r.Feasible && excess == 0
		r.Final = r.Final && rate >= workload.Rate
	}
	copy(r.Excesses, s.raw)
	if len(s.priority) == 0 {
		slices.SortFunc(r.Excesses, func(a, b float64) int {
			if a > b {
				return -1
			}
			if a < b {
				return 1
			}
			return 0
		})
	} else {
		for k, w := range s.priority {
			r.Excesses[k] = s.raw[w]
		}
	}
	return r
}

func finiteProblem(nodes int, workloads []protocol.Workload, rates, services [][]float64, energies []float64, prepare float64, priority []int) protocol.Problem {
	tab := &protocol.Table{Period: 1}
	p := protocol.Problem{Context: context.Background(), Table: tab, Workloads: workloads, Plans: make([][]protocol.Plan, nodes)}
	for n := range nodes {
		tab.ByPlan = append(tab.ByPlan, nil)
		tab.NodeUID = append(tab.NodeUID, fmt.Sprintf("n%d", n))
		tab.Compared = append(tab.Compared, true)
		for j := range rates {
			q := len(tab.Index)
			tab.ByPlan[n] = append(tab.ByPlan[n], q)
			tab.Index = append(tab.Index, j)
			tab.Options = append(tab.Options, "option")
			tab.Modes = append(tab.Modes, "mode")
			tab.PlanKeys = append(tab.PlanKeys, fmt.Sprintf("p%d", j))
			tab.Placement = append(tab.Placement, fmt.Sprintf("p%d", j))
			tab.KeyParts = append(tab.KeyParts, fmt.Sprintf("n%d=p%d|", n, j))
			tab.Keep = append(tab.Keep, j == 0)
			tab.Searchable = append(tab.Searchable, true)
			tab.TPre = append(tab.TPre, prepare)
			tab.RoutingRate = append(tab.RoutingRate, slices.Clone(rates[j]))
			tab.RetainedRoutingRate = append(tab.RetainedRoutingRate, slices.Clone(rates[0]))
			terms, count := make([]float64, len(workloads)), make([]int, len(workloads))
			for w := range workloads {
				terms[w], count[w] = rates[j][w]*services[j][w], 1
			}
			tab.RoutingRateServiceNanos = append(tab.RoutingRateServiceNanos, terms)
			tab.Count = append(tab.Count, count)
			p.Plans[n] = append(p.Plans[n], protocol.Plan{Eligible: true})
		}
	}
	tab.Energy = func(q int, _, _ float64) float64 { return energies[tab.Index[q]] }
	arrivals := make([][]float64, len(workloads))
	for w, workload := range workloads {
		for j := range int(math.Ceil(workload.Rate * tab.Period)) {
			arrivals[w] = append(arrivals[w], float64(j)/workload.Rate*1e9)
		}
	}
	p.NewState = func(choice []int) protocol.State {
		return &finiteState{table: tab, workloads: workloads, arrivals: arrivals, priority: priority, percentile: 100, choice: slices.Clone(choice),
			result: newV2Key(tab, len(workloads)), raw: make([]float64, len(workloads))}
	}
	return p
}

func exhaustiveFiniteBest(p protocol.Problem) protocol.Rank {
	choice := make([]int, len(p.Plans))
	for n := range choice {
		choice[n] = p.Table.ByPlan[n][0]
	}
	s := p.NewState(choice)
	best, found := newV2Key(p.Table, len(p.Workloads)), false
	var visit func(int)
	visit = func(n int) {
		for _, q := range p.Table.ByPlan[n] {
			s.Apply(n, q)
			if n+1 < len(choice) {
				visit(n + 1)
				continue
			}
			key := s.Delta(n, q)
			if key.Searchable && (!found || key.Better(&best)) {
				key.CopyInto(&best)
				found = true
			}
		}
	}
	visit(0)
	return best
}

func TestCandidatesMatchSmallFiniteProducts(t *testing.T) {
	for nodes := 1; nodes <= 3; nodes++ {
		for _, trial := range []struct {
			name            string
			workloads       []protocol.Workload
			rates, services [][]float64
			energies        []float64
			prepare         float64
			priority        []int
		}{
			{"all-infeasible", []protocol.Workload{{Rate: 10, Queued: 2, TargetNanos: 100e6, TotalNanos: 20e6}},
				[][]float64{{10}, {25}, {50}, {80}}, [][]float64{{80e6}, {40e6}, {20e6}, {140e6}}, []float64{1, 5, 10, 2}, 0.05, nil},
			{"feasible-energy", []protocol.Workload{{Rate: 1, TargetNanos: 100e6}},
				[][]float64{{10}, {10}, {10}}, [][]float64{{200e6}, {80e6}, {10e6}}, []float64{1, 2, 4}, 0, nil},
			{"worst-first", []protocol.Workload{{Rate: 1, TargetNanos: 100e6}, {Rate: 1, TargetNanos: 50e6}},
				[][]float64{{10, 10}, {10, 10}, {10, 10}}, [][]float64{{500e6, 500e6}, {110e6, 80e6}, {140e6, 55e6}}, []float64{1, 2, 2}, 0, nil},
			{"workload-first", []protocol.Workload{{Rate: 1, TargetNanos: 100e6}, {Rate: 1, TargetNanos: 50e6}},
				[][]float64{{10, 10}, {10, 10}, {10, 10}}, [][]float64{{500e6, 500e6}, {110e6, 80e6}, {140e6, 55e6}}, []float64{1, 2, 2}, 0, []int{0, 1}},
		} {
			t.Run(fmt.Sprintf("%s/%d", trial.name, nodes), func(t *testing.T) {
				p := finiteProblem(nodes, trial.workloads, trial.rates, trial.services, trial.energies, trial.prepare, trial.priority)
				best := exhaustiveFiniteBest(p)
				got, stats := Candidates(p, p.Table, 0, 8, 0, time.Time{})
				want := make([]int, nodes)
				for n, q := range best.Choice {
					want[n] = p.Table.Index[q]
				}
				if len(got) == 0 || !slices.Equal(got[0].Choice, want) || stats.Exhausted || stats.Deadline {
					t.Fatalf("finite optimum lost: got=%v want=%v stats=%+v", got, want, stats)
				}
				if nodes == 1 {
					wantPlan := map[string]int{"all-infeasible": 0, "feasible-energy": 1, "worst-first": 2, "workload-first": 1}[trial.name]
					if want[0] != wantPlan {
						t.Fatalf("wrong semantic oracle: got=%v want=%d", want, wantPlan)
					}
				}
			})
		}
	}
}

func TestActiveMoveRankingPreservesPriorityWithoutAllocations(t *testing.T) {
	for _, priority := range [][]int{nil, {0, 1}} {
		p := finiteProblem(1, []protocol.Workload{{Rate: 1, TargetNanos: 100e6}, {Rate: 1, TargetNanos: 50e6}},
			[][]float64{{10, 10}, {10, 10}, {10, 10}}, [][]float64{{500e6, 500e6}, {110e6, 80e6}, {140e6, 55e6}}, []float64{1, 2, 2}, 0, priority)
		Group(p.Table)
		c := newV2Search(p, p.Table, []int{0}, 0, 4, 2, time.Time{})
		for _, q := range []int{1, 2} {
			c.rankMove(0, q, c.s.delta(0, q))
		}
		want := 2
		if len(priority) > 0 {
			want = 1
		}
		if c.ranked[0][0].q != want {
			t.Fatalf("priority=%v ranked=%v want=%d", priority, c.ranked[0], want)
		}
		if allocations := testing.AllocsPerRun(100, func() {
			c.ranked[0] = c.ranked[0][:0]
			for _, q := range []int{1, 2} {
				c.rankMove(0, q, c.s.delta(0, q))
			}
		}); allocations != 0 {
			t.Fatalf("active move ranking allocated %g times", allocations)
		}
	}
}

func TestCandidatesKeepEvaluationBudget(t *testing.T) {
	p := finiteProblem(3, []protocol.Workload{{Rate: 10, Queued: 2, TargetNanos: 100e6}},
		[][]float64{{5}, {10}, {50}}, [][]float64{{80e6}, {40e6}, {20e6}}, []float64{1, 2, 3}, 0, nil)
	_, stats := Candidates(p, p.Table, 5, 4, 0, time.Time{})
	if !stats.Exhausted || stats.SurrogateEvaluated != 5 {
		t.Fatalf("evaluation budget changed: %+v", stats)
	}
}

func TestCandidatesShareBudgetAcrossStarts(t *testing.T) {
	p := finiteProblem(3, []protocol.Workload{{Rate: 10, Queued: 2, TargetNanos: 100e6}},
		[][]float64{{5}, {0}, {50}}, [][]float64{{80e6}, {0}, {20e6}}, []float64{1, 0, 3}, 0, nil)
	for _, plans := range p.Table.ByPlan {
		p.Table.Count[plans[1]][0] = 0
	}
	got, stats := Candidates(p, p.Table, 6, 1, 0, time.Time{})
	starts := map[string]bool{}
	for _, step := range stats.SearchSteps {
		starts[step.Start] = true
	}
	if !starts["keep"] || !starts["empty"] || stats.SurrogateEvaluated != 6 || !stats.Exhausted {
		t.Fatalf("start starved: candidates=%+v stats=%+v", got, stats)
	}
	if !slices.ContainsFunc(got, func(c protocol.Candidate) bool { return c.Start == "empty" }) {
		t.Fatalf("empty endpoint lost outside K: %+v", got)
	}
}

func TestCandidatesKeepDeadline(t *testing.T) {
	p := finiteProblem(1, []protocol.Workload{{Rate: 10, TargetNanos: 100e6}},
		[][]float64{{5}, {50}}, [][]float64{{80e6}, {20e6}}, []float64{1, 2}, 0, nil)
	_, stats := Candidates(p, p.Table, 100, 4, 0, time.Now().Add(-time.Second))
	if !stats.Deadline || stats.SurrogateEvaluated != 0 {
		t.Fatalf("deadline changed: %+v", stats)
	}
}

func TestPricedStartHasNoCapacityOrDualGate(t *testing.T) {
	for _, total := range []float64{20e6, 120e6} {
		p := finiteProblem(2, []protocol.Workload{{Rate: 10, Queued: 2, TargetNanos: 100e6, TotalNanos: total}},
			[][]float64{{5}, {25}, {50}}, [][]float64{{80e6}, {40e6}, {20e6}}, []float64{1, 2, 3}, 0.05, nil)
		choice, bound := PricedStart(p, p.Table)
		if len(choice) != len(p.Plans) || bound != 0 {
			t.Fatalf("total=%g start=%v invented bound=%g", total, choice, bound)
		}
		for n, q := range choice {
			if !slices.Contains(p.Table.ByPlan[n], q) || !p.Table.Searchable[q] {
				t.Fatalf("total=%g invalid start=%v", total, choice)
			}
		}
		p.Priced = true
		want := exhaustiveFiniteBest(p)
		candidates, _ := Candidates(p, p.Table, 0, 8, 0, time.Time{})
		if len(candidates) == 0 {
			t.Fatal("price-guided start erased all finite candidates")
		}
		for n, q := range want.Choice {
			if candidates[0].Choice[n] != p.Table.Index[q] {
				t.Fatalf("total=%g priced candidates lost finite optimum: got=%v want=%v", total, candidates, want.Choice)
			}
		}
	}
}
