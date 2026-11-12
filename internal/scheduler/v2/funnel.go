package v2

import (
	"cmp"
	"slices"
	"sync"
	"sync/atomic"

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
func Funnel[R any](p protocol.Problem, exact protocol.Exact[R], candidates []protocol.Candidate, stats *FunnelStats, seed *R) *R {
	// Replays borrow only immutable problem state. Reduce in candidate order so
	// completion order cannot change ties, postfilters or observer diagnostics.
	results := make([]struct {
		value     R
		evaluated bool
		worker    int
	}, len(candidates))
	// Long transitions dominate replay cost. Dispatch them first, retaining
	// candidate rank for all result storage, observations and reduction.
	type job struct {
		rank int
		pre  float64
	}
	jobs := make([]job, len(candidates))
	for i, candidate := range candidates {
		jobs[i].rank = i
		if p.Table != nil {
			for n, j := range candidate.Choice {
				jobs[i].pre = max(jobs[i].pre, p.Table.TPre[p.Table.ByPlan[n][j]])
			}
		}
	}
	slices.SortStableFunc(jobs, func(a, b job) int { return cmp.Compare(b.pre, a.pre) })
	workers := min(max(p.Workers, 1), len(candidates))
	var next atomic.Int64
	evaluate := func(worker int) {
		for {
			index := int(next.Add(1)) - 1
			if index >= len(jobs) {
				return
			}
			i := jobs[index].rank
			if p.Context != nil && p.Context.Err() != nil {
				return
			}
			if exact.Bounded(candidates[i].Choice) {
				results[i].value = exact.Evaluate(slices.Clone(candidates[i].Choice))
				results[i].evaluated = true
				results[i].worker = worker
			}
		}
	}
	if workers == 1 {
		evaluate(0)
	} else {
		var wg sync.WaitGroup
		for worker := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				evaluate(worker)
			}()
		}
		wg.Wait()
	}
	var rawBest, filteredBest, selected *R
	var rawFiltered R
	var rawGap, filteredGap float64
	var rawStart, filteredStart string
	anySurvivor := false
	reduce := func(res *R, candidate protocol.Candidate, slot int) {
		stats.Exact++
		exact.Observe(*res, slot)
		if !exact.Admits(*res) {
			return
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
	if p.Context != nil && p.Context.Err() != nil {
		return nil
	}
	if seed != nil {
		// Keep participates first with the same admission and postfilter rules.
		// Reuse its exact seed result rather than replaying it for selection.
		reduce(seed, protocol.Candidate{Joules: exact.Point(*seed).Joules, Start: "keep"}, 0)
	}
	for i, candidate := range candidates {
		if p.Context != nil && p.Context.Err() != nil {
			return nil
		}
		if results[i].evaluated {
			reduce(&results[i].value, candidate, results[i].worker)
		}
	}
	if p.Context != nil && p.Context.Err() != nil {
		return nil
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
	if p.Context != nil && p.Context.Err() != nil {
		return nil
	}
	choice := make([]int, len(p.Plans))
	best := funnelEvaluate(exact, choice, stats)
	for n, plans := range p.Plans {
		for j, plan := range plans {
			if p.Context != nil && p.Context.Err() != nil {
				return nil
			}
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
