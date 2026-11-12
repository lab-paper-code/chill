package serving

import (
	"context"
	"testing"
	"time"
)

func TestSchedulerFixedBatchWaitsForPartialBatch(t *testing.T) {
	settings, snapshot := capacityFixture()
	settings.ObservationWindow, settings.ComparisonPeriod = 102*time.Millisecond, 102*time.Millisecond
	settings.DefaultDispatchOccupancyNanos = 0
	snapshot.DispatchOccupancy = nil
	profile := *snapshot.Instances[0].Profile
	profile.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 2}, []float64{10e6})}
	snapshot.Instances[0].Profile = &profile
	snapshot.Instances[0].MaxBatch, snapshot.Instances[0].FixedBatchSize = 2, 2
	snapshot.Workloads[0].SLO.Percentile = 95
	start := snapshot.ObservationEnd.Add(-settings.ObservationWindow)
	for j, offset := range []time.Duration{0, time.Millisecond, 100 * time.Millisecond, 101 * time.Millisecond} {
		snapshot.Arrivals = append(snapshot.Arrivals, SchedulerArrival{ID: string(rune('a' + j)), At: start.Add(offset),
			Definition: snapshot.Workloads[0], Accepted: true, Compute: true, WorkloadUID: snapshot.Workloads[0].UID})
	}
	candidate := SchedulerCandidate{ID: "keep", Keep: true, Instances: snapshot.Instances,
		Routing: map[string]map[string]int32{snapshot.Workloads[0].UID: {snapshot.Instances[0].ID: 1}}}
	snapshot.Routing = candidate.Routing
	estimate, err := EvaluateFeedbackCandidate(context.Background(), settings, snapshot, candidate, 3)
	if err != nil || len(estimate.InvalidReasons) != 0 || len(estimate.Workloads) != 1 {
		t.Fatalf("fixed-batch replay failed: %+v %v", estimate, err)
	}
	score := estimate.Workloads[0]
	if score.Population != 4 || score.OnTime != 4 || score.Unfinished != 0 ||
		score.SuccessQuantileNanos == nil || *score.SuccessQuantileNanos != 11e6 {
		t.Fatalf("partial batch must await its next actual arrival and drain: %+v", score)
	}
}
