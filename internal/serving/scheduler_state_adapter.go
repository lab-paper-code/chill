package serving

import (
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

func (p *capacityPlanner) scalarProblem(t *planTable, deadline time.Time) protocol.Problem {
	problem := p.searchProblem(deadline)
	t.initSearch(p)
	table := &protocol.Table{ByPlan: t.byPlan, Index: t.index, Kappa: t.kappa, KappaE: t.kappaE, KappaRet: t.kappaRet,
		Count: t.count, Keep: t.keep, Searchable: t.searchable, KeyParts: t.keyParts, Compared: p.compared,
		Mid: t.mid, After: t.after, TPre: t.tPre, Period: p.period, Energy: t.energy, EnergyLowerBound: t.energyLowerBound, Pruned: t.pruned,
		NodeUID: make([]string, len(p.plans)), Options: make([]string, len(t.index)), Modes: make([]string, len(t.index)), PlanKeys: make([]string, len(t.index)), Nonnegative: make([]bool, len(t.index))}
	for n := range p.plans {
		table.NodeUID[n] = p.snapshot.Nodes[n].Node.UID
	}
	for q, n := range t.node {
		plan := p.plans[n][t.index[q]]
		table.Options[q], table.Modes[q], table.PlanKeys[q] = plan.option, plan.mode, plan.record.Key
		table.Nonnegative[q] = true
		for _, lists := range [][][]capacityItem{t.items[q], t.retained[q]} {
			for _, items := range lists {
				for _, item := range items {
					table.Nonnegative[q] = table.Nonnegative[q] && item.e >= 0
				}
			}
		}
	}
	problem.Table = table
	problem.NewState = func(choice []int) protocol.State {
		return &capacityState{state: newSearchState(p, t, choice), table: table}
	}
	return problem
}

// capacityState exposes scalar move results; CHILL retains integer routing and SLO math.
type capacityState struct {
	state *searchState
	table *protocol.Table
}

func (s *capacityState) Choice() []int         { return s.state.choice }
func (s *capacityState) Reset(choice []int)    { s.state.reset(choice) }
func (s *capacityState) Apply(n, q int)        { s.state.apply(n, q) }
func (s *capacityState) RouteTimes() []float64 { return s.state.tPre }
func (s *capacityState) Delta(n, q int) protocol.Rank {
	return capacityRank(s.state.delta(n, q), s.table)
}
func capacityRank(key orderKey, table *protocol.Table) protocol.Rank {
	return protocol.Rank{Feasible: key.feasible, Final: key.final, Keep: key.keep, WeightsExceeded: key.weightsExceeded,
		UnroutedPrepare: key.unroutedPrepare, Searchable: key.searchable, Bounded: key.bounded, Joules: key.joules, Route: key.route,
		KeepJoules: key.keepJoules, Period: key.period, Excesses: key.excesses, Demand: key.demand, Supply: key.supply, Satisfied: key.satisfied,
		Instances: key.instances, CountExcess: key.countExcess, Choice: key.choice, Table: table}
}
func (s *capacityState) MoveTerms(w, n, q int) (protocol.Workload, float64, float64, float64) {
	terms := s.state.workloadTerms(w, n, q)
	t := s.state.table
	return protocol.Workload{Rate: terms.demand.rate, Queued: terms.demand.queued, Slack: terms.demand.slack}, terms.kret,
		s.state.sums[w].kappa - t.kappa[s.state.choice[n]][w], s.state.sums[w].kappaE - t.kappaE[s.state.choice[n]][w]
}
func (s *capacityState) Overloaded() bool {
	for _, terms := range s.state.workloads {
		satisfied, _, _ := capacitySearchJudge(terms.demand, terms.knew, terms.kret, terms.present, s.state.r)
		if !satisfied || !terms.ok {
			return true
		}
	}
	return false
}
