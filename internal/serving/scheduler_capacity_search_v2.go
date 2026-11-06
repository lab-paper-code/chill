package serving

import "time"

const (
	schedulerPlanSearchV1     = "v1"
	schedulerPlanSearchV2     = "v2"
	schedulerPlanSearchShadow = "shadow"
)

func (p *capacityPlanner) searchV2(deadline time.Time) (capacityResult, error) {
	started := time.Now()
	t := p.planTable()
	candidates, core := p.searchV2Core(t, p.settings.PlanSearchEvaluations, p.settings.PlanExactFinalists, p.settings.PlanPolishActive, deadline)
	if err := p.ctx.Err(); err != nil {
		return capacityResult{}, err
	}
	stats := capacityFunnel{}
	best := p.funnel(candidates, &stats)
	fallback := ""
	if best == nil {
		fallback = "exactK1"
		best = p.exactK1(&stats)
		if best != nil && p.settings.PlanPostFilterFinalists {
			filtered := p.minimumGain(p.payback(*best))
			best = &filtered
		}
	}
	if best == nil {
		// Disable dispatch only for this synchronous fallback, then restore it.
		strategy := p.settings.PlanSearchStrategy
		p.settings.PlanSearchStrategy = schedulerPlanSearchV1
		res, err := p.search(deadline)
		p.settings.PlanSearchStrategy = strategy
		p.searched.Strategy, p.searched.Fallback = schedulerPlanSearchV2, schedulerPlanSearchV1
		if p.settings.PlanPostFilterFinalists {
			res = p.minimumGain(p.payback(res))
		}
		return res, err
	}
	pruned := make(map[string]int, len(t.pruned)+len(t.unsearchable))
	for reason, count := range t.pruned {
		pruned[reason] += count
	}
	for reason, count := range t.unsearchable {
		pruned[reason] += count
	}
	p.searched = SchedulerPlanSearch{
		Strategy: schedulerPlanSearchV2, Pruned: pruned, SurrogateEvaluated: core.surrogateEvaluated,
		Finalists: len(candidates), ExactEvaluated: stats.exact, Evaluated: stats.exact,
		SurrogateGapJoules: stats.gap, PostFilterReverts: stats.reverts, Fallback: fallback,
		BudgetCut: core.budgetCut, Duration: time.Since(started), Excluded: stats.excluded,
		ReserveRejections: stats.reserves,
	}
	return *best, p.ctx.Err()
}
