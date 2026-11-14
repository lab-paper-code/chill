package serving

import (
	"math"
	"slices"
	"testing"
	"time"

	v2 "github.com/lab-paper-code/chill/internal/scheduler/v2"
)

// The former kappa dual was not a certificate for the frozen fluid objective.
// Search must still retain deterministic starts and physically fixed nodes.
func TestCapacityFrozenStartsDeterministic(t *testing.T) {
	for _, priced := range []bool{false, true} {
		p := e5Planner(map[int]float64{0: 60, 1: 15, 2: 1.2}, evaluationScaled(2, 0), e5Start)
		p.settings.PlanPricedStart = priced
		problem := p.scalarProblem(p.planTable(), time.Time{})
		a, sa := v2.Candidates(problem, problem.Table, 10000, 16, 4, time.Time{})
		b, sb := v2.Candidates(problem, problem.Table, 10000, 16, 4, time.Time{})
		if len(a) == 0 || len(a) != len(b) || sa.SurrogateEvaluated != sb.SurrogateEvaluated {
			t.Fatalf("priced=%t unstable starts", priced)
		}
		for i := range a {
			if !slices.Equal(a[i].Choice, b[i].Choice) || a[i].Joules != b[i].Joules || a[i].Feasible != b[i].Feasible || a[i].Start != b[i].Start {
				t.Fatalf("priced=%t endpoint=%d changed", priced, i)
			}
		}
	}
}

func TestCapacityFrozenStartsFixedMultiWorkloadKeep(t *testing.T) {
	current := make(map[string]bool, len(e5Start)+1)
	for id, present := range e5Start {
		current[id] = present
	}
	current["rasp5/plc/q1"] = true
	p := e5Planner(map[int]float64{0: 60, 1: 15}, evaluationScaled(2, 0), current)
	p.compared[0], p.settings.PlanPricedStart = false, true
	problem := p.scalarProblem(p.planTable(), time.Time{})
	keep := problem.Table.ByPlan[0][0]
	if problem.Table.RoutingRate[keep][0] <= 0 || problem.Table.RoutingRate[keep][1] <= 0 || problem.Table.RoutingRateServiceNanos[keep][0] <= 0 || problem.Table.RoutingRateServiceNanos[keep][1] <= 0 {
		t.Fatal("fixture must retain both workloads' frozen terms")
	}
	candidates, _ := v2.Candidates(problem, problem.Table, 10000, 16, 4, time.Time{})
	if len(candidates) == 0 {
		t.Fatal("missing endpoint")
	}
	for _, candidate := range candidates {
		if candidate.Choice[0] != 0 {
			t.Fatalf("uncompared node changed: %+v", candidate)
		}
	}
}

func TestCapacityFrozenStartsZeroCostAndNoDemand(t *testing.T) {
	p := capacityDeltaTiny([]float64{0, 0}, 0)
	for _, plans := range p.plans {
		for _, plan := range plans {
			plan.items[0][0].model, plan.retained[0][0].model = schedulerTestModel(0), schedulerTestModel(0)
		}
	}
	p.settings.PlanPricedStart = true
	problem := p.scalarProblem(p.planTable(), time.Time{})
	candidates, stats := v2.Candidates(problem, problem.Table, 100, 4, 4, time.Time{})
	if len(candidates) == 0 || stats.Deadline {
		t.Fatalf("empty demand lost keep: %+v", stats)
	}
	for _, candidate := range candidates {
		if !candidate.Feasible || math.IsNaN(candidate.Joules) || math.IsInf(candidate.Joules, 0) {
			t.Fatalf("invalid zero-demand endpoint: %+v", candidate)
		}
	}
}
