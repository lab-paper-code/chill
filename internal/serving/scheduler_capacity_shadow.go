package serving

import "time"

// searchShadow runs v2 first with its evaluation budget and the shared wall-clock
// safety deadline. Its runtime reduces v1's remaining deadline: shadow operation
// pays that cost, while v1 alone still decides and records its own search time.
func (p *capacityPlanner) searchShadow(deadline time.Time) (capacityResult, error) {
	keepWatts := p.keepWatts
	_, err := p.searchV2WithFallback(deadline, false)
	if err != nil {
		return capacityResult{}, err
	}
	v2 := p.searched
	shadow := &SchedulerPlanSearchShadow{
		Key: v2.Key, Joules: v2.Joules,
		Evaluated: v2.SurrogateEvaluated, ExactEvaluated: v2.ExactEvaluated,
		Duration: v2.Duration, BudgetCut: v2.BudgetCut, Fallback: v2.Fallback,
		TerminationReason: v2.TerminationReason,
	}
	p.searched = SchedulerPlanSearch{}
	p.keepWatts = keepWatts
	strategy := p.settings.PlanSearchStrategy
	p.settings.PlanSearchStrategy = schedulerPlanSearchV1
	defer func() { p.settings.PlanSearchStrategy = strategy }()
	// v1 reseeds keepWatts from the same keep choice before its first sweep.
	best, err := p.search(deadline)
	p.searched.Strategy, p.searched.Shadow = schedulerPlanSearchShadow, shadow
	return best, err
}
