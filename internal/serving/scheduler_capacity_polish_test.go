package serving

import (
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
	v2 "github.com/lab-paper-code/chill/internal/scheduler/v2"
)

func TestCapacityPolishStrictImprovementAndTermination(t *testing.T) {
	p := e5Planner(map[int]float64{0: 20, 1: 15}, evaluationNodes(), e5Start)
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

func TestCapacityPolishEndpointMatchesFrozenEnergy(t *testing.T) {
	p := capacityDeltaTiny([]float64{10}, 1)
	p.keepWatts = 10
	keep := p.plans[0][0]
	keep.before = 10
	keep.items[0][0].model, keep.retained[0][0].model = schedulerTestModel(0), schedulerTestModel(0)
	move := *keep
	move.keep, move.record.Key, move.after = false, "lower-idle", 9
	p.plans[0] = append(p.plans[0], &move)
	problem := p.scalarProblem(p.planTable(), time.Time{})
	candidates, stats := v2.Candidates(problem, problem.Table, 0, 1, 4, time.Time{})
	if len(candidates) == 0 || !slices.Equal(candidates[0].Choice, []int{1}) ||
		candidates[0].Joules != 540 || !candidates[0].Feasible || stats.Deadline || stats.Exhausted {
		t.Fatalf("polish lost frozen energy optimum: candidates=%+v stats=%+v", candidates, stats)
	}
	if !slices.ContainsFunc(stats.SearchSteps, func(step protocol.Step) bool {
		return step.Phase == protocol.Polish && step.K == 1 && step.Joules == 540
	}) {
		t.Fatalf("missing optimal polish endpoint: %+v", stats.SearchSteps)
	}
}
