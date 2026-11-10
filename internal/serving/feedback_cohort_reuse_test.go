package serving

import (
	"context"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

func feedbackCohortFixture() (SchedulerSettings, SchedulerSnapshot, []SchedulerCandidate) {
	settings, snapshot, keep := feedbackFixture()
	settings.MinimumDemand = map[string]float64{snapshot.Workloads[0].UID: 5000}
	// Equal-time accepted requests precede the repeated arrivals and supplements.
	// Frozen SLO/native/POST context must stay outside the shared arrival cohort.
	frozen := *snapshot.Workloads[0]
	frozen.Generation++
	post, sender, native := 7.0, 11.0, 20e6
	elapsed, lifetime := int64(20e6), time.Millisecond
	snapshot.Outstanding = []SchedulerRequest{
		{ID: "running", Definition: &frozen, Phase: phaseRunning, Instance: "i1", BatchID: "actual", CompletionMarginMS: &post, NativeSenderElapsedMS: &sender, NativeElapsedNanos: &native, ExchangeElapsedNanos: &elapsed, RemainingLifetime: &lifetime},
		{ID: "queued", Definition: &frozen, Phase: phaseQueued, Instance: "i1", QueueOrder: 1, CompletionMarginMS: &post, NativeSenderElapsedMS: &sender, NativeElapsedNanos: &native},
		{ID: "pending", Definition: &frozen, Phase: phasePending, CompletionMarginMS: &post},
	}
	one := keep
	one.Keep = false
	one.Instances = slices.Clone(keep.Instances)
	one.Instances[0].MaxBatch = 1
	one.Actions = []Action{{ID: "route-one", Kind: schedulerActionRoute}}
	return settings, snapshot, []SchedulerCandidate{keep, one}
}

func TestFeedbackArrivalCohortIsolatesCandidates(t *testing.T) {
	settings, snapshot, candidates := feedbackCohortFixture()
	cohort, err := newFeedbackArrivalCohort(context.Background(), settings, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]FeedbackCandidateResult, len(candidates))
	for index, candidate := range candidates {
		want[index], err = EvaluateFeedbackCandidate(context.Background(), settings, snapshot, candidate, 1)
		if err != nil || len(want[index].InvalidReasons) != 0 || want[index].Workloads[0].Population != 8 || want[index].Workloads[0].MinimumDemandArrivals != 3 {
			t.Fatalf("direct cohort: %+v %v", want[index], err)
		}
	}
	before := make([]schedulerReplayRequest, len(cohort.requests))
	for index, request := range cohort.requests {
		before[index] = *request
	}
	var workers sync.WaitGroup
	for _, index := range []int{1, 0, 0, 1} {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			got, err := evaluateFeedbackCandidate(context.Background(), settings, snapshot, candidates[index], 1, cohort)
			if err != nil || !reflect.DeepEqual(got, want[index]) {
				t.Errorf("candidate %d changed with shared cohort: got=%+v want=%+v err=%v", index, got, want[index], err)
			}
		}(index)
	}
	workers.Wait()
	for index, request := range cohort.requests {
		if !reflect.DeepEqual(*request, before[index]) {
			t.Fatalf("candidate mutated shared request %d", index)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := evaluateFeedbackCandidate(ctx, settings, snapshot, candidates[0], 1, cohort); err != context.Canceled {
		t.Fatalf("canceled shared-cohort replay: %v", err)
	}
	if _, err := newFeedbackArrivalCohort(ctx, settings, snapshot); err != context.Canceled {
		t.Fatalf("canceled cohort construction: %v", err)
	}
	got, err := evaluateFeedbackCandidate(context.Background(), settings, snapshot, candidates[0], 1, cohort)
	if err != nil || !reflect.DeepEqual(got, want[0]) {
		t.Fatalf("canceled candidate poisoned cohort: %+v %v", got, err)
	}
}

func TestFeedbackArrivalCohortPreservesSameTimeOrder(t *testing.T) {
	settings, snapshot, _ := feedbackCohortFixture()
	cohort, err := newFeedbackArrivalCohort(context.Background(), settings, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	fresh := schedulerReplayState{settings: settings, snap: snapshot, horizon: float64(settings.ComparisonPeriod), out: schedulerReplayResult{definitions: map[string]*Definition{}, outcomes: map[string]map[string]int{}}}
	fresh.initializeInstances()
	fresh.initializeOutstanding()
	shared := fresh
	shared.requests = slices.Clone(fresh.requests)
	shared.out.definitions = map[string]*Definition{}
	if !fresh.initializeArrivals(context.Background()) || !shared.initializeArrivalCohort(context.Background(), cohort) {
		t.Fatal("arrival initialization failed")
	}
	for index, request := range fresh.requests {
		if !reflect.DeepEqual(*request, *shared.requests[index]) {
			t.Fatalf("same-time order/context changed at %d: fresh=%+v shared=%+v", index, request, shared.requests[index])
		}
		if !request.observed && request == shared.requests[index] {
			t.Fatalf("mutable arrival alias at %d", index)
		}
	}
}
