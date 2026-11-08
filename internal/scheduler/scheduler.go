// Package scheduler selects the two official stateless scheduler engines.
package scheduler

import (
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
	"github.com/lab-paper-code/chill/internal/scheduler/v1"
	"github.com/lab-paper-code/chill/internal/scheduler/v2"
)

// Select receives the strategy already checked by the application's settings boundary.
func Select[R any](strategy string) protocol.Engine[R] {
	switch strategy {
	case protocol.V1:
		return v1.Engine[R]{}
	case protocol.V2:
		return second[R]{}
	}
	return nil
}

type second[R any] struct{}

func (second[R]) Search(p protocol.Problem, exact protocol.Exact[R]) (protocol.Outcome[R], error) {
	started := time.Now()
	result, err := (v2.Engine[R]{}).Search(p, exact)
	if err != nil || result.Found {
		return result, err
	}
	core := result.Diagnostics
	result, err = (v1.Engine[R]{}).Search(p, exact)
	d := &result.Diagnostics
	d.Strategy, d.Fallback = protocol.V2, protocol.V1
	d.StartedAt, d.FinishedAt = core.StartedAt, time.Now().UTC()
	d.Duration = time.Since(started)
	d.Steps, d.SurrogateEvaluated, d.TrailingEvaluated = core.Steps, core.SurrogateEvaluated, core.TrailingEvaluated
	d.BudgetCut, d.TerminationReason = core.BudgetCut, core.TerminationReason
	if p.PostFilter {
		result.Value = exact.PostFilter(result.Value)
	}
	return result, err
}
