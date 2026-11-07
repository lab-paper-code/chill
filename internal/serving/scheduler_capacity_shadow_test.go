package serving

import (
	"reflect"
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
	started := time.Now()
	got, err := shadow.search(deadline)
	if err != nil {
		t.Fatal(err)
	}
	if got.key != want.key || got.joules != want.joules {
		t.Fatalf("shadow changed decision: %s %g want %s %g", got.key, got.joules, want.key, want.joules)
	}
	record, s := shadow.searched, shadow.searched.Shadow
	if record.Strategy != schedulerPlanSearchShadow || record.Key != want.key || record.Joules != want.joules || record.Neighborhood != v1.searched.Neighborhood || record.Evaluated != v1.searched.Evaluated || record.BudgetCut != v1.searched.BudgetCut {
		t.Fatalf("v1 record changed: %+v want %+v", record, v1.searched)
	}
	if s == nil || s.Key != comparison.key || s.Joules != comparison.joules || s.Evaluated != v2.searched.SurrogateEvaluated || s.ExactEvaluated != v2.searched.ExactEvaluated || s.BudgetCut != v2.searched.BudgetCut || s.TerminationReason != v2.searched.TerminationReason || s.Fallback != v2.searched.Fallback || s.Duration <= 0 {
		t.Fatalf("shadow record: %+v want %s %g %+v", s, comparison.key, comparison.joules, v2.searched)
	}
	// UTC timestamps omit the monotonic clock used by Duration.
	wallDuration := record.FinishedAt.Sub(record.StartedAt)
	if record.StartedAt.Sub(started) < s.Duration || record.Duration <= 0 || record.Duration < wallDuration-time.Millisecond || record.Duration > wallDuration+time.Millisecond {
		t.Fatalf("v2 must finish before v1 starts and v1 must time itself: %+v shadow=%+v", record, s)
	}
	if shadow.settings.PlanSearchStrategy != schedulerPlanSearchShadow {
		t.Fatal("strategy not restored")
	}
}

func TestCapacityShadowEvaluationBudgetKeepsV1Record(t *testing.T) {
	planner := func(strategy string) *capacityPlanner {
		p := e5Planner(e5Mixes[0], evaluationScaled(1, 0), e5Start, nil)
		p.settings.PlanSearchStrategy = strategy
		p.settings.PlanSearchEvaluations = 1
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
	if got.key != want.key || got.joules != want.joules || shadow.searched.Shadow == nil || shadow.searched.Shadow.BudgetCut || shadow.searched.Shadow.TerminationReason != schedulerEvaluationBudget {
		t.Fatalf("v2 budget must not change v1: got=%+v want=%+v record=%+v", got, want, shadow.searched)
	}
	normalize := func(record SchedulerPlanSearch) SchedulerPlanSearch {
		record.Strategy, record.Shadow = schedulerPlanSearchV1, nil
		record.StartedAt, record.FinishedAt, record.Duration = time.Time{}, time.Time{}, 0
		record.Steps = append([]SchedulerPlanSearchStep(nil), record.Steps...)
		for i := range record.Steps {
			record.Steps[i].Duration = 0
		}
		return record
	}
	if a, b := normalize(shadow.searched), normalize(v1.searched); !reflect.DeepEqual(a, b) {
		t.Fatalf("v1 record changed after v2 budget cut: got=%+v want=%+v", a, b)
	}
	if shadow.keepWatts != v1.keepWatts {
		t.Fatalf("v2 changed keep baseline: got=%g want=%g", shadow.keepWatts, v1.keepWatts)
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

func TestCapacityShadowWallDeadlineRecorded(t *testing.T) {
	p := e5Planner(e5Mixes[0], evaluationScaled(1, 0), e5Start, nil)
	p.settings.PlanSearchStrategy = schedulerPlanSearchShadow
	if _, err := p.search(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if p.searched.Shadow == nil || !p.searched.Shadow.BudgetCut || p.searched.Shadow.TerminationReason != schedulerDeadline || !p.searched.BudgetCut {
		t.Fatalf("shared wall deadline must cut both searches: %+v shadow=%+v", p.searched, p.searched.Shadow)
	}
}
