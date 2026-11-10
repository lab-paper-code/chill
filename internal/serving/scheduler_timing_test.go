package serving

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSchedulerEvaluationTimingStages(t *testing.T) {
	for _, strategy := range []string{"v1", "v2"} {
		t.Run(strategy, func(t *testing.T) {
			settings, snapshot := schedulerFixture()
			settings.PlanSearchStrategy = strategy
			snapshot.ObservedSince = snapshot.ObservationEnd.Add(-time.Hour)
			started := time.Now()
			decision := capacityDecideForTest(t, settings, snapshot)
			elapsed := time.Since(started)
			timing := decision.EvaluationTiming
			if timing.Preparation <= 0 || timing.Search <= 0 || timing.Payback <= 0 || timing.Rates <= 0 || timing.Projection <= 0 || timing.Preparation+timing.Search+timing.Payback+timing.Rates+timing.Projection > elapsed {
				t.Fatalf("evaluation stages missing or overlapping: %+v; elapsed=%v", timing, elapsed)
			}
			if decision.Capacity == nil || decision.Capacity.Search == nil {
				t.Fatal("fixture did not search")
			}
			search := decision.Capacity.Search
			if search.ExactElapsed <= 0 || search.ExactElapsed+search.SurrogateElapsed > search.Duration || search.Duration > timing.Search || (search.SurrogateElapsed > 0) != (strategy == "v2") {
				t.Fatalf("search timers lost backend scope: %+v; evaluation=%+v", search, timing)
			}
		})
	}
	settings, snapshot := schedulerFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	decision, err := EvaluateScheduler(ctx, settings, snapshot)
	if !errors.Is(err, context.Canceled) || decision.EvaluationTiming.Preparation <= 0 || decision.EvaluationTiming.Search != 0 {
		t.Fatalf("canceled evaluation lost early-return timing: %+v %v", decision.EvaluationTiming, err)
	}
	settings.ControlInterval = 0
	decision, err = EvaluateScheduler(context.Background(), settings, snapshot)
	if err != nil || decision.EvaluationTiming.Preparation <= 0 || decision.EvaluationTiming.Search != 0 {
		t.Fatalf("invalid-settings Keep path lost preparation timing: %+v %v", decision.EvaluationTiming, err)
	}
}
