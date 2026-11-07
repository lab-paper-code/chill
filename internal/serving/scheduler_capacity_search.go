package serving

import (
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// search walks from two starts, keep and every Node emptied (its plan without
// instances, keep when it has none), so a change of many Nodes that each step
// from keep would refuse is still reached from the other side. From each
// start a step moves to the best admissible plan set that changes at most k
// Nodes, first with k = 2 until no step improves, then with k = 3 from there.
// The better end of the two starts is the result. At deadline the sweep
// stops; the best plan set found so far stays and p.searched records the cut.
func (p *capacityPlanner) search(deadline time.Time) (capacityResult, error) {
	if p.settings.PlanSearchStrategy == schedulerPlanSearchShadow {
		return p.searchShadow(deadline)
	}
	if p.settings.PlanSearchStrategy == schedulerPlanSearchV2 {
		return p.searchV2(deadline)
	}
	start := time.Now()
	p.searched.Strategy = schedulerPlanSearchV1
	p.searched.StartedAt = start.UTC()
	keep, empty := make([]int, len(p.plans)), make([]int, len(p.plans))
	for n, plans := range p.plans {
		empty[n] = max(0, slices.IndexFunc(plans, func(plan *capacityPlan) bool { return plan.record.Eligible && len(plan.instances) == 0 }))
	}
	starts := [][]int{keep}
	if !slices.Equal(empty, keep) {
		starts = append(starts, empty)
	}
	ends := make([]capacityResult, len(starts))
	startNames := [2]string{"keep", "empty"}
	for s, choice := range starts {
		stepStart := time.Now()
		ends[s] = p.evaluate(slices.Clone(choice), p.routeOf(choice))
		if s == 0 {
			// Seed the common-window baseline before any parallel neighbor search.
			p.keepWatts = ends[s].joules / ends[s].period
		}
		p.searched.Steps = append(p.searched.Steps, SchedulerPlanSearchStep{
			Start: startNames[s], Joules: ends[s].joules,
			ComparisonWindow: time.Duration(ends[s].period * 1e9), KeepJoules: ends[s].keepJoules,
			Feasible: ends[s].feasible, Final: ends[s].final, Duration: time.Since(stepStart),
		})
	}
	for k := 2; k <= 3 && !p.searched.BudgetCut; k++ {
		p.searched.Neighborhood = k
		for s := range starts {
			for !p.searched.BudgetCut {
				evaluated := p.searched.Evaluated
				stepStart := time.Now()
				step, err := p.sweep(ends[s].choice, k, deadline)
				if err != nil {
					return capacityResult{}, err
				}
				if step == nil || !step.better(&ends[s]) {
					p.searched.TrailingEvaluated += p.searched.Evaluated - evaluated
					break
				}
				record := SchedulerPlanSearchStep{
					Start: startNames[s], K: k, Joules: step.joules,
					ComparisonWindow: time.Duration(step.period * 1e9), KeepJoules: step.keepJoules,
					ChangedNodes: make([]NodeReference, 0, k),
					Feasible:     step.feasible, Final: step.final, Evaluated: p.searched.Evaluated - evaluated,
					Duration: time.Since(stepStart),
				}
				for n, j := range step.choice {
					if j != ends[s].choice[n] {
						record.ChangedNodes = append(record.ChangedNodes, p.snapshot.Nodes[n].Node)
					}
				}
				p.searched.Steps = append(p.searched.Steps, record)
				ends[s] = *step
			}
		}
	}
	best := ends[0]
	for _, end := range ends[1:] {
		if p.bounded(end.choice) && p.admits(&end) && end.better(&best) {
			best = end
		}
	}
	p.searched.TerminationReason = schedulerNoImprovement
	if p.searched.BudgetCut {
		p.searched.TerminationReason = schedulerBudgetCut
		p.searched.Steps[len(p.searched.Steps)-1].BudgetCut = true
	}
	finished := time.Now()
	p.searched.FinishedAt = finished.UTC()
	p.searched.Duration = finished.Sub(start)
	p.searched.Key, p.searched.Joules = best.key, best.joules
	return best, nil
}

// sweep evaluates the admissible plan sets that change 1 to k Nodes of choice
// and returns the best (nil when there is none). planSearchWorkers goroutines
// take the first changed Node plan in turn; better is a total order, so the
// result does not depend on their timing.
func (p *capacityPlanner) sweep(choice []int, k int, deadline time.Time) (*capacityResult, error) {
	type move struct{ n, j int }
	moves := []move{}
	for n, plans := range p.plans {
		for j, plan := range plans {
			if j != choice[n] && plan.record.Eligible {
				moves = append(moves, move{n, j})
			}
		}
	}
	workers := p.settings.PlanSearchWorkers
	bests, counts := make([]*capacityResult, workers), make([]int, workers)
	excluded := make([]map[string]int, workers)
	rejections := make([]map[string]SchedulerTransitionReserve, workers)
	for w := range excluded {
		excluded[w] = map[string]int{}
	}
	var next atomic.Int64
	var cut atomic.Bool
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := slices.Clone(choice)
			var walk func(i, left int)
			walk = func(i, left int) {
				m := moves[i]
				c[m.n] = m.j
				defer func() { c[m.n] = choice[m.n] }()
				if cut.Load() || time.Now().After(deadline) {
					cut.Store(true)
					return
				}
				if p.bounded(c) {
					counts[w]++
					res := p.evaluate(slices.Clone(c), p.routeOf(c))
					rejections[w] = reserveRejections(rejections[w], res.reserves, res.key)
					if res.exclusion != "" {
						excluded[w][res.exclusion]++
					}
					if p.admits(&res) && res.better(bests[w]) {
						bests[w] = &res
					}
				}
				for i2 := i + 1; left > 1 && i2 < len(moves); i2++ {
					if moves[i2].n != m.n {
						walk(i2, left-1)
					}
				}
			}
			for i := int(next.Add(1)) - 1; i < len(moves) && p.ctx.Err() == nil && !cut.Load(); i = int(next.Add(1)) - 1 {
				walk(i, k)
			}
		}()
	}
	wg.Wait()
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	if p.searched.Excluded == nil {
		p.searched.Excluded = map[string]int{}
	}
	for _, counts := range excluded {
		for reason, count := range counts {
			p.searched.Excluded[reason] += count
		}
	}
	for _, records := range rejections {
		for _, record := range records {
			p.searched.ReserveRejections = reserveRejections(p.searched.ReserveRejections, []SchedulerTransitionReserve{record}, "")
		}
	}
	var best *capacityResult
	for w, res := range bests {
		p.searched.Evaluated += counts[w]
		if res != nil && res.better(best) {
			best = res
		}
	}
	p.searched.BudgetCut = cut.Load()
	return best, nil
}

// admits applies the same SLO and horizon-gain judgment to retirement as to
// serving changes. Overload recovery may expand or replace, never just retire.
func (p *capacityPlanner) admits(res *capacityResult) bool {
	if res.exclusion != "" || res.unroutedPrepare || res.unroutes {
		return false
	}
	for n, j := range res.choice {
		if p.plans[n][j].retires {
			if !res.feasible {
				return false
			}
			// A feasible target repairing infeasible Keep is recovery, including
			// a replacement whose source and destination are on different Nodes.
			if !p.keepFeasible {
				return true
			}
			if res.keepJoules <= res.joules {
				return false
			}
			keep := capacityResult{choice: make([]int, len(p.plans))}
			return res.keepJoules-res.joules > p.gainBound(keep, *res)
		}
	}
	return true
}
