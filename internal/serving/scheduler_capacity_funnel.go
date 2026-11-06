package serving

import "slices"

// capacityFunnel records only exact candidate evaluations, not postfilter probes.
type capacityFunnel struct {
	exact    int
	excluded map[string]int
	reserves map[string]SchedulerTransitionReserve
	gap      float64
	reverts  int
}

func (p *capacityPlanner) funnelEvaluate(choice []int, stats *capacityFunnel) *capacityResult {
	if !p.bounded(choice) {
		return nil
	}
	res := p.evaluate(slices.Clone(choice), p.routeOf(choice))
	stats.exact++
	stats.reserves = reserveRejections(stats.reserves, res.reserves, res.key)
	if res.exclusion != "" {
		if stats.excluded == nil {
			stats.excluded = map[string]int{}
		}
		stats.excluded[res.exclusion]++
	}
	if !p.admits(&res) {
		return nil
	}
	return &res
}

// funnel preserves v1 exact scoring and admission; postfilter comparison is optional.
func (p *capacityPlanner) funnel(candidates []v2Candidate, stats *capacityFunnel) *capacityResult {
	var rawBest, filteredBest, selected *capacityResult
	var rawFiltered capacityResult
	var rawGap, filteredGap float64
	anySurvivor := false
	for _, candidate := range candidates {
		res := p.funnelEvaluate(candidate.choice, stats)
		if res == nil {
			continue
		}
		filtered := p.minimumGain(p.payback(*res))
		if !capacityFilterKept(filtered) {
			anySurvivor = true
		}
		if res.better(rawBest) {
			rawBest, rawFiltered = res, filtered
			rawGap = candidate.joules - res.joules
		}
		if filtered.better(filteredBest) {
			filteredBest = &filtered
			filteredGap = candidate.joules - filtered.joules
		}
	}
	if rawBest != nil && !capacityFilterKept(*rawBest) && capacityFilterKept(rawFiltered) && anySurvivor {
		stats.reverts++
	}
	selected, stats.gap = rawBest, rawGap
	if p.settings.PlanPostFilterFinalists {
		selected, stats.gap = filteredBest, filteredGap
	}
	return selected
}

func capacityFilterKept(res capacityResult) bool {
	return res.keep || res.withinUncertainty || len(res.unpaid) > 0 && slices.IndexFunc(res.choice, func(j int) bool { return j != 0 }) < 0
}

// exactK1 includes keep and every eligible cap variant, without table pruning.
func (p *capacityPlanner) exactK1(stats *capacityFunnel) *capacityResult {
	choice := make([]int, len(p.plans))
	best := p.funnelEvaluate(choice, stats)
	for n, plans := range p.plans {
		for j, plan := range plans {
			if j == 0 || !plan.record.Eligible {
				continue
			}
			choice[n] = j
			res := p.funnelEvaluate(choice, stats)
			if res != nil && res.better(best) {
				best = res
			}
		}
		choice[n] = 0
	}
	return best
}
