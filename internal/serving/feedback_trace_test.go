package serving

import (
	"context"
	"testing"
	"time"
)

func TestFeedbackTraceUsesFiniteActualArrivalsAndFixedBatch(t *testing.T) {
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
	trace, err := ReplayFeedbackTrace(context.Background(), settings, snapshot, candidate, 3)
	if err != nil || len(trace.InvalidReasons) != 0 {
		t.Fatalf("trace failed: %+v %v", trace, err)
	}
	if len(trace.BatchServiceNanos) != 2 || trace.BatchServiceNanos[0] != 10e6 || trace.BatchServiceNanos[1] != 10e6 {
		t.Fatalf("fixed physical batches not retained: %+v", trace.BatchServiceNanos)
	}
	latency := trace.LatencyNanos[snapshot.Workloads[0].Identity()]
	if len(latency) != 4 || latency[0] != 10e6 || latency[3] != 11e6 {
		t.Fatalf("actual gaps or finite drain changed: %v", latency)
	}
	estimate, err := EvaluateFeedbackCandidate(context.Background(), settings, snapshot, candidate, 3)
	if err != nil || len(estimate.Workloads) != 1 || estimate.Workloads[0].SuccessQuantileNanos == nil || *estimate.Workloads[0].SuccessQuantileNanos != latency[3] {
		t.Fatalf("diagnostic changed admission replay: %+v %v", estimate, err)
	}
}
