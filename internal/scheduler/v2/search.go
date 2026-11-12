// Package v2 implements cover, polish, product combination and price-guided search.
package v2

import (
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

type Engine[R any] struct{}

func (Engine[R]) Search(p protocol.Problem, exact protocol.Exact[R]) (out protocol.Outcome[R], err error) {
	started := time.Now()
	surrogateElapsed := time.Duration(0)
	exactElapsed := time.Duration(0)
	defer func() {
		out.Diagnostics.SurrogateElapsed = surrogateElapsed
		out.Diagnostics.ExactElapsed = exactElapsed
	}()
	keep := exact.Evaluate(make([]int, len(p.Plans)))
	exact.SeedKeep(keep)
	exactElapsed = time.Since(started)
	surrogateStarted := time.Now()
	candidates, core := Candidates(p, p.Table, p.Evaluations, p.Finalists, p.Active, p.Deadline)
	surrogateElapsed = time.Since(surrogateStarted)
	if err := p.Context.Err(); err != nil {
		return protocol.Outcome[R]{}, err
	}
	stats := FunnelStats{}
	exactStarted := time.Now()
	best := Funnel(p, exact, candidates, &stats, &keep)
	if err := p.Context.Err(); err != nil {
		return protocol.Outcome[R]{}, err
	}
	fallback := ""
	if best == nil {
		fallback = "exactK1"
		best = ExactK1(p, exact, &stats)
		if err := p.Context.Err(); err != nil {
			return protocol.Outcome[R]{}, err
		}
		if best != nil {
			point := exact.Point(*best)
			stats.RawKey, stats.RawJoules = point.Key, point.Joules
		}
		if best != nil && p.PostFilter {
			filtered := exact.PostFilter(*best)
			best = &filtered
		}
	}
	exactElapsed += time.Since(exactStarted)
	if best == nil {
		d := protocol.Diagnostics{Strategy: protocol.V2, Fallback: protocol.V1, StartedAt: started.UTC()}
		recordCore(&d, core)
		return protocol.Outcome[R]{Diagnostics: d}, nil
	}
	pruned := make(map[string]int, len(p.Table.Pruned))
	for reason, count := range p.Table.Pruned {
		pruned[reason] += count
	}
	d := protocol.Diagnostics{Strategy: protocol.V2, Pruned: pruned, SurrogateEvaluated: core.SurrogateEvaluated,
		Key: stats.RawKey, Joules: stats.RawJoules, Finalists: len(candidates), ExactEvaluated: stats.Exact, Evaluated: stats.Exact,
		SurrogateGapJoules: stats.Gap, PostFilterReverts: stats.Reverts, Fallback: fallback,
		BudgetCut: core.Deadline, Duration: time.Since(started), StartedAt: started.UTC(), FinishedAt: time.Now().UTC(),
		DualBound: core.DualBound, Start: stats.Start}
	recordCore(&d, core)
	return protocol.Outcome[R]{Value: *best, Diagnostics: d, Found: true}, p.Context.Err()
}

func recordCore(d *protocol.Diagnostics, core CoreStats) {
	d.Steps = core.SearchSteps
	d.SurrogateEvaluated = core.SurrogateEvaluated
	d.TrailingEvaluated = core.SurrogateEvaluated
	for _, step := range core.SearchSteps {
		d.TrailingEvaluated -= step.Evaluated
	}
	d.BudgetCut = core.Deadline
	d.TerminationReason = protocol.NoImprovement
	if core.Deadline {
		d.TerminationReason = protocol.Deadline
		if len(d.Steps) > 0 {
			d.Steps[len(d.Steps)-1].BudgetCut = true
		}
	} else if core.Exhausted {
		d.TerminationReason = protocol.EvaluationBudget
	}
}
