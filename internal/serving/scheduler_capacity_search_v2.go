package serving

import "time"

const (
	schedulerPlanSearchV1     = "v1"
	schedulerPlanSearchV2     = "v2"
	schedulerPlanSearchShadow = "shadow"
	schedulerPlanPhaseCover   = "cover"
	schedulerPlanPhasePolish  = "polish"
	schedulerPlanPhaseCombine = "combine"
	schedulerNoImprovement    = "noImprovement"
	schedulerBudgetCut        = "budgetCut"
	schedulerDeadline         = "deadline"
	schedulerEvaluationBudget = "evaluationBudget"
)

func (p *capacityPlanner) searchV2(deadline time.Time) (capacityResult, error) {
	return p.searchV2WithFallback(deadline, true)
}

// searchV2WithFallback skips v1 fallback when shadow runs v1 separately afterward.
func (p *capacityPlanner) searchV2WithFallback(deadline time.Time, allowV1 bool) (capacityResult, error) {
	started := time.Now()
	// Match v1's common-H keep baseline before exact admission/postfilters.
	keep := p.evaluate(make([]int, len(p.plans)), 0)
	p.keepWatts = keep.joules / keep.period
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
		if best != nil {
			stats.rawKey, stats.rawJoules = best.key, best.joules
		}
		if best != nil && p.settings.PlanPostFilterFinalists {
			filtered := p.minimumGain(p.payback(*best))
			best = &filtered
		}
	}
	if best == nil && allowV1 {
		// Disable dispatch only for this synchronous fallback, then restore it.
		strategy := p.settings.PlanSearchStrategy
		p.settings.PlanSearchStrategy = schedulerPlanSearchV1
		res, err := p.search(deadline)
		p.settings.PlanSearchStrategy = strategy
		p.searched.Strategy, p.searched.Fallback = schedulerPlanSearchV2, schedulerPlanSearchV1
		p.searched.StartedAt, p.searched.FinishedAt = started.UTC(), time.Now().UTC()
		p.searched.Duration = time.Since(started)
		p.recordV2Steps(core)
		if p.settings.PlanPostFilterFinalists {
			res = p.minimumGain(p.payback(res))
		}
		return res, err
	}
	if best == nil {
		fallback = schedulerPlanSearchV1
	}
	pruned := make(map[string]int, len(t.pruned))
	for reason, count := range t.pruned {
		pruned[reason] += count
	}
	p.searched = SchedulerPlanSearch{
		Strategy: schedulerPlanSearchV2, Pruned: pruned, SurrogateEvaluated: core.surrogateEvaluated,
		Key: stats.rawKey, Joules: stats.rawJoules,
		Finalists: len(candidates), ExactEvaluated: stats.exact, Evaluated: stats.exact,
		SurrogateGapJoules: stats.gap, PostFilterReverts: stats.reverts, Fallback: fallback,
		BudgetCut: core.deadline, Duration: time.Since(started), Excluded: stats.excluded,
		StartedAt: started.UTC(), FinishedAt: time.Now().UTC(),
		ReserveRejections: stats.reserves,
		DualBound:         core.dualBound, Start: stats.start,
	}
	p.recordV2Steps(core)
	if best == nil {
		return capacityResult{}, p.ctx.Err()
	}
	return *best, p.ctx.Err()
}

func (p *capacityPlanner) recordV2Steps(core v2CoreStats) {
	p.searched.Steps = core.searchSteps
	p.searched.SurrogateEvaluated = core.surrogateEvaluated
	p.searched.TrailingEvaluated = core.surrogateEvaluated
	for _, step := range core.searchSteps {
		p.searched.TrailingEvaluated -= step.Evaluated
	}
	p.searched.BudgetCut = core.deadline
	p.searched.TerminationReason = schedulerNoImprovement
	if core.deadline {
		p.searched.TerminationReason = schedulerDeadline
		if len(p.searched.Steps) > 0 {
			p.searched.Steps[len(p.searched.Steps)-1].BudgetCut = true
		}
	} else if core.exhausted {
		p.searched.TerminationReason = schedulerEvaluationBudget
	}
}
