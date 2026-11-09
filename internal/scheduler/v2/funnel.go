package v2

import (
	"slices"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

// FunnelStats counts exact candidate evaluations, excluding postfilter probes.
type FunnelStats struct {
	Exact         int
	Gap           float64
	Reverts       int
	Start, RawKey string
	RawJoules     float64
}

func funnelEvaluate[R any](exact protocol.Exact[R], choice []int, stats *FunnelStats) *R {
	if !exact.Bounded(choice) {
		return nil
	}
	res := exact.Evaluate(slices.Clone(choice))
	stats.Exact++
	exact.Observe(res, 0)
	if !exact.Admits(res) {
		return nil
	}
	return &res
}

// Funnel selects on native exact results and optional CHILL postfilters.
func Funnel[R any](p protocol.Problem, exact protocol.Exact[R], candidates []protocol.Candidate, stats *FunnelStats) *R {
	var rawBest, filteredBest, selected *R
	var rawFiltered R
	var rawGap, filteredGap float64
	var rawStart, filteredStart string
	anySurvivor := false
	for _, candidate := range candidates {
		res := funnelEvaluate(exact, candidate.Choice, stats)
		if res == nil {
			continue
		}
		filtered := *res
		if p.PostFilter {
			filtered = exact.PostFilter(*res)
			if !exact.FilterKept(filtered) {
				anySurvivor = true
			}
		}
		if exact.Better(*res, rawBest) {
			rawBest, rawFiltered = res, filtered
			rawGap = candidate.Joules - exact.Point(*res).Joules
			rawStart = candidate.Start
		}
		if p.PostFilter && exact.Better(filtered, filteredBest) {
			filteredBest = &filtered
			filteredGap = candidate.Joules - exact.Point(filtered).Joules
			filteredStart = candidate.Start
			if p.PostFilter {
				point := exact.Point(*res)
				stats.RawKey, stats.RawJoules = point.Key, point.Joules
			}
		}
	}
	if p.PostFilter && rawBest != nil && !exact.FilterKept(*rawBest) && exact.FilterKept(rawFiltered) && anySurvivor {
		stats.Reverts++
	}
	selected, stats.Gap, stats.Start = rawBest, rawGap, rawStart
	if rawBest != nil && !p.PostFilter {
		point := exact.Point(*rawBest)
		stats.RawKey, stats.RawJoules = point.Key, point.Joules
	}
	if p.PostFilter {
		selected, stats.Gap, stats.Start = filteredBest, filteredGap, filteredStart
	}
	return selected
}

// ExactK1 includes keep and every eligible variant without surrogate pruning.
func ExactK1[R any](p protocol.Problem, exact protocol.Exact[R], stats *FunnelStats) *R {
	choice := make([]int, len(p.Plans))
	best := funnelEvaluate(exact, choice, stats)
	for n, plans := range p.Plans {
		for j, plan := range plans {
			if j == 0 || !plan.Eligible {
				continue
			}
			choice[n] = j
			res := funnelEvaluate(exact, choice, stats)
			if res != nil && exact.Better(*res, best) {
				best = res
			}
		}
		choice[n] = 0
	}
	return best
}
