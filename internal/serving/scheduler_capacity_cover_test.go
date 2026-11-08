package serving

import (
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
	v2 "github.com/lab-paper-code/chill/internal/scheduler/v2"
)

func TestCapacityCoverSatisfiedAndOverload(t *testing.T) {
	for _, test := range []struct {
		name     string
		rates    map[int]float64
		feasible bool
	}{
		{"recover", map[int]float64{0: 110, 1: 15}, true},
		{"overload", map[int]float64{0: 1000000}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := e5Planner(test.rates, evaluationNodes(), e5Start, nil)
			problem := p.scalarProblem(p.planTable(), time.Time{})
			candidates, stats := v2.Candidates(problem, problem.Table, 500000, 16, 4, time.Time{})
			if len(candidates) == 0 || candidates[0].Feasible != test.feasible {
				t.Fatalf("want feasible=%v: candidates=%+v", test.feasible, candidates)
			}
			if stats.Deadline || stats.Exhausted {
				t.Fatalf("cover exhausted budget: %+v", stats)
			}
			coverSteps := 0
			var previous protocol.Step
			for _, step := range stats.SearchSteps {
				if step.Start == capacitySearchStartKeep && step.K == 0 && step.Feasible {
					t.Fatal("fixture must start insufficient")
				}
				if step.Phase == protocol.Cover {
					coverSteps++
					if step.K != 1 || step.Evaluated == 0 || previous.Start != step.Start || previous.Feasible {
						t.Fatalf("cover must accept one move only while insufficient: %+v after %+v", step, previous)
					}
				}
				previous = step
			}
			if coverSteps == 0 {
				t.Fatal("missing accepted cover moves")
			}
			t.Logf("feasible=%v evaluations=%d J=%.9f", candidates[0].Feasible, stats.SurrogateEvaluated, candidates[0].Joules)
		})
	}
}
