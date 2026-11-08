package serving

import (
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
	v2 "github.com/lab-paper-code/chill/internal/scheduler/v2"
)

func TestCapacityPolishStrictImprovementAndTermination(t *testing.T) {
	p := e5Planner(map[int]float64{0: 20, 1: 15}, evaluationNodes(), e5Start, nil)
	problem := p.scalarProblem(p.planTable(), time.Time{})
	candidates, stats := v2.Candidates(problem, problem.Table, 500000, 16, 4, time.Time{})
	if len(candidates) == 0 || stats.Deadline || stats.Exhausted {
		t.Fatalf("polish failed to terminate: candidates=%d stats=%+v", len(candidates), stats)
	}
	polishSteps := 0
	strictImprovement := false
	var previous protocol.Step
	for i, step := range stats.SearchSteps {
		if step.Phase == protocol.Polish {
			polishSteps++
			strictImprovement = strictImprovement || !previous.Feasible && step.Feasible ||
				previous.Feasible && step.Feasible && step.Joules-step.KeepJoules < previous.Joules-previous.KeepJoules
			if step.K == 0 || step.Evaluated == 0 || previous.Start != step.Start ||
				previous.Feasible && (!step.Feasible || step.Joules-step.KeepJoules > previous.Joules-previous.KeepJoules) {
				t.Fatalf("polish failed to make a strict improvement: %+v after %+v", step, previous)
			}
		}
		if i+1 == len(stats.SearchSteps) || stats.SearchSteps[i+1].Start != step.Start {
			if !slices.ContainsFunc(candidates, func(candidate protocol.Candidate) bool {
				return candidate.Feasible == step.Feasible && candidate.Joules == step.Joules
			}) {
				t.Fatalf("terminated endpoint was not retained: %+v", step)
			}
		}
		previous = step
	}
	if polishSteps == 0 || !strictImprovement {
		t.Fatal("polish failed to accept an improvement")
	}
	again, repeated := v2.Candidates(problem, problem.Table, 500000, 16, 4, time.Time{})
	if !slices.EqualFunc(candidates, again, func(a, b protocol.Candidate) bool {
		return slices.Equal(a.Choice, b.Choice) && a.Joules == b.Joules && a.Feasible == b.Feasible && a.Start == b.Start
	}) || stats.SurrogateEvaluated != repeated.SurrogateEvaluated || len(stats.SearchSteps) != len(repeated.SearchSteps) {
		t.Fatal("repeated polish changed its deterministic endpoint or termination")
	}
	t.Logf("steps=%d evaluations=%d J=%.9f", polishSteps, stats.SurrogateEvaluated, candidates[0].Joules)
}

func TestCapacityPolishEndpointMatchesExact(t *testing.T) {
	p := capacityTwinSwap(100, 0)
	// Direct Candidates callers seed the common-window baseline like Engine.Search.
	adapter := newCapacityExact(p)
	adapter.SeedKeep(adapter.Evaluate(make([]int, len(p.plans))))
	problem := p.scalarProblem(p.planTable(), time.Time{})
	candidates, stats := v2.Candidates(problem, problem.Table, 0, 1, 4, time.Time{})
	exact := e5Brute(p)
	if len(candidates) == 0 || exact == nil || !slices.Equal(candidates[0].Choice, exact.choice) ||
		candidates[0].Joules != exact.joules || candidates[0].Feasible != exact.feasible ||
		stats.Deadline || stats.Exhausted {
		t.Fatalf("polish endpoint differs from exhaustive optimum: candidates=%+v exact=%+v stats=%+v", candidates, exact, stats)
	}
	if len(stats.SearchSteps) != 2 || stats.SearchSteps[1].Phase != protocol.Polish ||
		stats.SearchSteps[1].K != 1 || stats.SearchSteps[1].Joules != candidates[0].Joules {
		t.Fatalf("missing optimal polish endpoint: %+v", stats.SearchSteps)
	}
}
