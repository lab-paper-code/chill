package serving

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// Finite evaluation limits and a fixed distant deadline keep both algorithm
// consumers deterministic. The same file runs on semantic baseline 02b521a0.
func TestNumericalReplayBothSearchConsumers(t *testing.T) {
	deadline := time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, strategy := range []string{"v1", "v2"} {
		t.Run(strategy, func(t *testing.T) {
			settings, snapshot, _ := capacityE1Fixture()
			settings.PlanSearchStrategy, settings.PlanSearchWorkers = strategy, 1
			p := capacityPlannerForTest(t, settings, snapshot)
			keep := p.evaluateCurrentKeep(p.periodSeconds)
			slow := p.evaluate(capacityChoiceForTest(t, p, map[string]string{"lut": "profile@lut-fixed/b1/w1"}), 20.2)
			fast := p.evaluate(capacityChoiceForTest(t, p, map[string]string{"genesis3": "profile@genesis3-fixed/b1/w1"}), 10.7)
			if slow.joules >= fast.joules || !fast.better(&slow) {
				t.Fatalf("exact outcome ordering changed: slow=%+v fast=%+v", slow, fast)
			}
			chosen, err := p.search(deadline)
			if err != nil || chosen.exclusion != "" || !p.bounded(chosen.choice) || len(chosen.feedback) == 0 {
				t.Fatalf("search numerical consumer: chosen=%+v err=%v", chosen, err)
			}
			for _, result := range []capacityResult{keep, slow, fast, chosen} {
				direct, err := EvaluateFeedbackCandidate(context.Background(), settings, p.snapshot, p.candidate(result), 0)
				if err != nil || len(direct.InvalidReasons) != 0 || !reflect.DeepEqual(result.feedback, direct.Workloads) || !reflect.DeepEqual(result.slo, direct.SLO) || !reflect.DeepEqual(result.conditional, direct.ConditionalService) {
					t.Fatalf("candidate %s route=%g diverged from exact replay: result=%+v direct=%+v err=%v", result.key, result.route, result, direct, err)
				}
			}
			t.Logf("key=%s route=%g joules=%g keepJoules=%g feasible=%v final=%v feedback=%+v", chosen.key, chosen.route, chosen.joules, chosen.keepJoules, chosen.feasible, chosen.final, chosen.feedback)
		})
	}
}

func BenchmarkNumericalPlannerConsumers(b *testing.B) {
	deadline := time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, strategy := range []string{"v1", "v2"} {
		b.Run(strategy, func(b *testing.B) {
			settings, snapshot, _ := capacityE1Fixture()
			settings.PlanSearchStrategy, settings.PlanSearchWorkers = strategy, 1
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				p := capacityPlannerForTest(b, settings, snapshot)
				if result, err := p.search(deadline); err != nil || result.exclusion != "" {
					b.Fatalf("search failed: result=%+v err=%v", result, err)
				}
			}
		})
	}
}
