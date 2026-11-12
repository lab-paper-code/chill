package serving

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

func (p *capacityPlanner) scalarProblem(t *planTable, deadline time.Time) protocol.Problem {
	problem := p.searchProblem(deadline)
	t.initSearch(p)
	table := &protocol.Table{ByPlan: t.byPlan, Index: t.index, RoutingRate: t.routingRate, RoutingRateServiceNanos: t.routingRateServiceNanos, RetainedRoutingRate: t.retainedRoutingRate,
		Count: t.count, Keep: t.keep, Searchable: t.searchable, KeyParts: t.keyParts, Compared: p.compared,
		Mid: t.mid, After: t.after, TPre: t.tPre, Period: p.periodSeconds, Energy: t.energy, EnergyLowerBound: t.energyLowerBound, Pruned: t.pruned,
		NodeUID: make([]string, len(p.plans)), Options: make([]string, len(t.index)), Modes: make([]string, len(t.index)), PlanKeys: make([]string, len(t.index)), Placement: make([]string, len(t.index)), Nonnegative: t.nonnegative}
	for n := range p.plans {
		table.NodeUID[n] = p.snapshot.Nodes[n].Node.UID
	}
	for q, n := range t.node {
		plan := p.plans[n][t.index[q]]
		table.Options[q], table.Modes[q], table.PlanKeys[q] = plan.option, plan.mode, plan.record.Key
		if len(plan.instances) > 0 {
			parts := make([]string, len(plan.instances))
			for j, i := range plan.instances {
				profile := i.ProfileID
				if i.Profile != nil {
					profile = i.Profile.ID
				}
				parts[j] = fmt.Sprintf("%q/%v/%d/%q/%q", profile, i.Allocation, i.MaxWait, i.WorkloadUID, plan.mode)
			}
			slices.Sort(parts)
			table.Placement[q] = strings.Join(parts, ";")
		} else {
			table.Placement[q] = fmt.Sprintf("%q/%q", plan.option, plan.mode)
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

func (s *capacityState) Choice() []int      { return s.state.choice }
func (s *capacityState) Reset(choice []int) { s.state.reset(choice) }
func (s *capacityState) Apply(n, q int)     { s.state.apply(n, q) }
func (s *capacityState) Delta(n, q int) protocol.Rank {
	return capacityRank(s.state.delta(n, q), s.table)
}
func capacityRank(key orderKey, table *protocol.Table) protocol.Rank {
	return protocol.Rank{Feasible: key.feasible, Final: key.final, Keep: key.keep, WeightsExceeded: key.weightsExceeded,
		UnroutedPrepare: key.unroutedPrepare, Searchable: key.searchable, Bounded: key.bounded, Joules: key.joules, Route: key.route,
		KeepJoules: key.keepJoules, Period: key.period, Excesses: key.excesses, Demand: key.demand, Supply: key.supply, Satisfied: key.satisfied,
		Instances: key.instances, CountExcess: key.countExcess, Choice: key.choice, Table: table}
}
