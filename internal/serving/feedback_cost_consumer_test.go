package serving

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

// This file also runs unchanged on source baseline 4f2d97ca. The comparison
// runner compares decoded numerical consumer evidence, never encoded bytes.
func TestReplayCostConsumerEquivalence(t *testing.T) {
	emit := func(name string, value any) {
		data, err := json.Marshal(map[string]any{"case": name, "evidence": value})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("CONTROL_REPLAY_EVIDENCE %s", data)
	}
	for _, name := range []string{"finite-minimum", "frozen-contexts", "failure-heavy"} {
		settings, snapshot, keep := feedbackFixture()
		settings.MinimumDemand = map[string]float64{snapshot.Workloads[0].UID: 5000}
		if name == "frozen-contexts" {
			frozen := *snapshot.Workloads[0]
			frozen.Generation++
			post, sender, native := 7.0, 11.0, 20e6
			elapsed, life := int64(20e6), time.Millisecond
			for _, phase := range []string{phasePending, phaseAssigned, phaseQueued, phaseDeferred, phaseRunning, phaseReturning} {
				request := SchedulerRequest{ID: phase, Definition: &frozen, Phase: phase, Instance: "i1", BatchID: "actual", CompletionMarginMS: &post, NativeSenderElapsedMS: &sender, NativeElapsedNanos: &native}
				if phase == phasePending {
					request.Instance = ""
				}
				if phase == phaseRunning {
					request.ExchangeElapsedNanos, request.PrecedingIdleNanos, request.RemainingLifetime = &elapsed, new(int64), &life
				}
				if phase == phaseReturning {
					request.NativeOutcome = firstfloor.OutcomeBatchError
				}
				snapshot.Outstanding = append(snapshot.Outstanding, request)
			}
		}
		if name == "failure-heavy" {
			settings.MinimumDemand[snapshot.Workloads[0].UID] = 256000
			snapshot.Workloads[0].SLO.TargetLatency = 10 * time.Millisecond
			snapshot.Instances[0].MaxBatch = 1
			snapshot.Workloads[0].Admission.Policy = AdmissionPolicyReject
		}
		one := keep
		one.Keep = false
		one.Instances = slices.Clone(keep.Instances)
		one.Instances[0].MaxBatch = 1
		one.Actions = []Action{{ID: "route-one", Kind: schedulerActionRoute}}
		candidates := []SchedulerCandidate{keep, one}
		want := make([]FeedbackCandidateResult, len(candidates))
		for index, candidate := range candidates {
			var err error
			want[index], err = EvaluateFeedbackCandidate(context.Background(), settings, snapshot, candidate, 1)
			if err != nil || len(want[index].InvalidReasons) != 0 || len(want[index].Workloads) != 1 {
				t.Fatalf("%s candidate %d: %+v %v", name, index, want[index], err)
			}
			emit(fmt.Sprintf("%s/%d", name, index), want[index])
		}
		var workers sync.WaitGroup
		for _, index := range []int{1, 0, 1, 0} {
			workers.Add(1)
			go func(index int) {
				defer workers.Done()
				got, err := EvaluateFeedbackCandidate(context.Background(), settings, snapshot, candidates[index], 1)
				if err != nil || !reflect.DeepEqual(got, want[index]) {
					t.Errorf("%s candidate permutation/concurrency changed %d: got=%+v want=%+v err=%v", name, index, got, want[index], err)
				}
			}(index)
		}
		workers.Wait()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := EvaluateFeedbackCandidate(ctx, settings, snapshot, keep, 1); err != context.Canceled {
			t.Fatalf("canceled consumer: %v", err)
		}
	}
	deadline := time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, strategy := range []string{"v1", "v2"} {
		settings, snapshot, _ := capacityE1Fixture()
		settings.PlanSearchStrategy, settings.PlanSearchWorkers = strategy, 1
		planner := capacityPlannerForTest(t, settings, snapshot)
		chosen, err := planner.search(deadline)
		if err != nil || chosen.exclusion != "" || !planner.bounded(chosen.choice) {
			t.Fatalf("%s search: %+v %v", strategy, chosen, err)
		}
		emit("choice/"+strategy, map[string]any{"choice": chosen.choice, "key": chosen.key, "route": chosen.route, "joules": chosen.joules, "keepJoules": chosen.keepJoules, "feasible": chosen.feasible, "feedback": chosen.feedback, "slo": chosen.slo, "conditional": chosen.conditional, "assumptions": chosen.assumptions, "sources": chosen.sources})
	}
	settings, snapshot, candidate := feedbackFixture()
	zero := 0.0
	for index := range 256 {
		snapshot.Outstanding = append(snapshot.Outstanding, SchedulerRequest{ID: fmt.Sprintf("q%07d", index), Definition: snapshot.Workloads[0], Phase: phaseQueued, Instance: "i1", CompletionMarginMS: &zero, NativeSenderElapsedMS: &zero, NativeElapsedNanos: &zero})
	}
	state := schedulerReplayState{settings: settings, snap: snapshot, c: candidate, out: schedulerReplayResult{definitions: map[string]*Definition{}, outcomes: map[string]map[string]int{}, samples: map[string][]float64{}}}
	state.initializeInstances()
	state.initializeOutstanding()
	state.finishFeedback()
	emit("fixed-final-failure-reduction", map[string]any{"feedback": state.out.feedback, "slo": feedbackReplayEstimates(snapshot, state.out, true), "conditional": state.out.conditional})
}

// Benchmark the actual final feedback reducer at a fixed queue boundary. Event
// processing is intentionally outside this measurement; expiry queues are mutable.
func BenchmarkFeedbackCostFinalReduction(b *testing.B) {
	for _, count := range []int{2000, 8000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			settings, snapshot, candidate := feedbackFixture()
			snapshot.Arrivals = nil
			zero := 0.0
			for index := range count {
				snapshot.Outstanding = append(snapshot.Outstanding, SchedulerRequest{ID: fmt.Sprintf("q%07d", index), Definition: snapshot.Workloads[0], Phase: phaseQueued, Instance: "i1", CompletionMarginMS: &zero, NativeSenderElapsedMS: &zero, NativeElapsedNanos: &zero})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				state := schedulerReplayState{settings: settings, snap: snapshot, c: candidate, out: schedulerReplayResult{definitions: map[string]*Definition{}, outcomes: map[string]map[string]int{}, samples: map[string][]float64{}}}
				state.initializeInstances()
				state.initializeOutstanding()
				state.finishFeedback()
				if len(state.out.feedback) != 1 || state.out.feedback[0].Unfinished != count || state.out.feedback[0].PotentialExcess == nil || state.out.conditional.Queries != 2*count {
					b.Fatalf("fixed final reducer lost failure or conditional evidence: %+v", state.out)
				}
			}
		})
	}
}
