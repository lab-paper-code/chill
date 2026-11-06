package serving

import (
	"testing"
	"time"
)

func TestCapacityShadowMatchesIndependentSearches(t *testing.T) {
	planner := func(strategy string) *capacityPlanner {
		p := e5Planner(e5Mixes[0], evaluationScaled(1, 0), e5Start, nil)
		p.settings.PlanSearchStrategy = strategy
		return p
	}
	v1, v2, shadow := planner(schedulerPlanSearchV1), planner(schedulerPlanSearchV2), planner(schedulerPlanSearchShadow)
	deadline := time.Now().Add(time.Hour)
	want, err := v1.search(deadline)
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := v2.search(deadline)
	if err != nil {
		t.Fatal(err)
	}
	got, err := shadow.search(deadline)
	if err != nil {
		t.Fatal(err)
	}
	if got.key != want.key || got.joules != want.joules {
		t.Fatalf("shadow changed decision: %s %g want %s %g", got.key, got.joules, want.key, want.joules)
	}
	record, s := shadow.searched, shadow.searched.Shadow
	if record.Strategy != schedulerPlanSearchShadow || record.Key != want.key || record.Neighborhood != v1.searched.Neighborhood || record.Evaluated != v1.searched.Evaluated || record.BudgetCut != v1.searched.BudgetCut {
		t.Fatalf("v1 record changed: %+v want %+v", record, v1.searched)
	}
	if s == nil || s.Key != comparison.key || s.Joules != comparison.joules || s.Evaluated != v2.searched.SurrogateEvaluated || s.ExactEvaluated != v2.searched.ExactEvaluated || s.Fallback != v2.searched.Fallback || s.Duration <= 0 {
		t.Fatalf("shadow record: %+v want %s %g %+v", s, comparison.key, comparison.joules, v2.searched)
	}
	if shadow.settings.PlanSearchStrategy != schedulerPlanSearchShadow {
		t.Fatal("strategy not restored")
	}
}

func TestCapacityShadowFallbackKeepsV1(t *testing.T) {
	planner := func(strategy string) *capacityPlanner {
		p := e5Planner(e5Mixes[0], evaluationScaled(1, 0), e5Start, nil)
		p.settings.PlanSearchStrategy = strategy
		// Reject every funnel and exactK1 choice at the existing global bound.
		for _, plans := range p.plans {
			for _, plan := range plans {
				plan.others = p.settings.MaxInstances + 1
			}
		}
		return p
	}
	v1, shadow := planner(schedulerPlanSearchV1), planner(schedulerPlanSearchShadow)
	deadline := time.Now().Add(time.Hour)
	want, err := v1.search(deadline)
	if err != nil {
		t.Fatal(err)
	}
	got, err := shadow.search(deadline)
	if err != nil {
		t.Fatal(err)
	}
	s := shadow.searched.Shadow
	if got.key != want.key || got.joules != want.joules || shadow.searched.Evaluated != v1.searched.Evaluated || s == nil || s.Fallback != schedulerPlanSearchV1 || s.Key != "" || s.ExactEvaluated != 0 {
		t.Fatalf("fallback changed v1: %s %g want %s %g record=%+v shadow=%+v", got.key, got.joules, want.key, want.joules, shadow.searched, s)
	}
}

func TestCapacityShadowDecision(t *testing.T) {
	s, snap, rows, _ := capacityE1Fixture()
	s.PlanSearchStrategy = schedulerPlanSearchShadow
	d := capacityDecideForTest(t, s, snap, rows)
	if d.Capacity == nil || d.Capacity.Search == nil || d.Capacity.Search.Strategy != schedulerPlanSearchShadow || d.Capacity.Search.Shadow == nil || d.Capacity.Search.Shadow.Key == "" {
		t.Fatalf("missing shadow decision: %+v", d.Capacity)
	}
}
