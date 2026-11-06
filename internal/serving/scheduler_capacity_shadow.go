package serving

import "time"

// searchShadow keeps v1 authoritative and spends only its remaining deadline on v2.
func (p *capacityPlanner) searchShadow(deadline time.Time) (capacityResult, error) {
	strategy := p.settings.PlanSearchStrategy
	p.settings.PlanSearchStrategy = schedulerPlanSearchV1
	defer func() { p.settings.PlanSearchStrategy = strategy }()
	best, err := p.search(deadline)
	if err != nil {
		return best, err
	}
	searched := p.searched
	searched.Strategy, searched.Key = schedulerPlanSearchShadow, best.key
	defer func() { p.searched = searched }()
	shadow, err := p.searchV2WithFallback(deadline, false)
	if err != nil {
		return best, err
	}
	searched.Shadow = &SchedulerPlanSearchShadow{
		Key: shadow.key, Joules: shadow.joules,
		Evaluated: p.searched.SurrogateEvaluated, ExactEvaluated: p.searched.ExactEvaluated,
		Duration: p.searched.Duration, Fallback: p.searched.Fallback,
	}
	return best, nil
}
