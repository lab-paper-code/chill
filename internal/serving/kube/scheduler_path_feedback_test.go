package kube

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// Seed exact local timing evidence; Tick must join the client report through the
// real observation window instead of taking a test-supplied feedback value.
func recordSchedulerPathSample(t *testing.T, c *SchedulerController, workload *serving.Definition) {
	t.Helper()
	received := time.Now().Add(-200 * time.Millisecond)
	var timing serving.RequestTimings
	timing.Record(serving.ServerAssigned, 30*time.Millisecond)
	timing.Record(serving.ServerSenderElapsedFrozen, 40*time.Millisecond)
	for _, stage := range []struct {
		kind string
		elapsed time.Duration
	}{{"received", 0}, {"accepted", 10 * time.Millisecond}, {"assigned", 30 * time.Millisecond}, {"backend_exchange_finished", 110 * time.Millisecond}, {"response_finished", 120 * time.Millisecond}} {
		event := serving.RequestEvent{RequestID: "path-sample", WorkloadName: workload.Name, WorkloadUID: workload.UID,
			Definition: workload.Identity(), Kind: stage.kind, At: received.Add(stage.elapsed), ReceivedAt: received,
			ElapsedNanos: int64(stage.elapsed), ServerTiming: timing, Outcome: "completed", ExecutionComplete: true}
		if stage.kind == "backend_exchange_finished" {
			event.NativeProgress = &serving.BackendProgress{RequestID: event.RequestID, WorkloadUID: workload.UID,
				Definition: workload.Identity(), Phase: "finished", At: received.Add(100 * time.Millisecond),
				NativeElapsedMS: 60, BatchID: 1, BatchSize: 1, ExecutionComplete: true, Outcome: "completed"}
		}
		if err := c.observationLog.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.observationLog.RequestClosed("path-sample", timing); err != nil {
		t.Fatal(err)
	}
	if err := c.observationLog.ClientObservation(context.Background(), serving.ClientObservation{
		ObservationID: "path-report", RequestID: "path-sample", WorkloadName: workload.Name,
		WorkloadUID: workload.UID, ElapsedNanos: int64(130 * time.Millisecond), Outcome: "completed",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTickCapturesAndPublishesSameWindowPathFeedback(t *testing.T) {
	c, _ := tickReadySubsetController(t, nil)
	c.config.Settings.AutonomousWorkloads = nil
	c.config.Settings.CompletionMarginMS = 7
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var published serving.SchedulerDecision
	c.config.OnDecision = func(decision serving.SchedulerDecision, snapshot serving.SchedulerSnapshot) error {
		if !reflect.DeepEqual(decision.PathFeedback, snapshot.PathFeedback) ||
			!reflect.DeepEqual(c.State().Decision.PathFeedback, decision.PathFeedback) {
			t.Fatal("callback saw different captured, decided or committed feedback")
		}
		published = decision
		return nil
	}
	first, err := c.Tick(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.PathFeedback) != 1 || first.PathFeedback[0].Source != "configured" || first.PathFeedback[0].PostNanos != float64(7*time.Millisecond) {
		t.Fatalf("missing configured initial model: %+v", first.PathFeedback)
	}
	workload := c.runtime.SchedulerInputs(time.Now).Workloads[0]
	recordSchedulerPathSample(t, c, workload)
	second, err := c.Tick(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := serving.PathFeedback{WorkloadUID: workload.UID, Percentile: workload.SLO.Percentile,
		TotalNanos: float64(50 * time.Millisecond), PostNanos: float64(30 * time.Millisecond), SampleCount: 1, Source: "empirical"}
	if !reflect.DeepEqual(second.PathFeedback, []serving.PathFeedback{want}) || !reflect.DeepEqual(published.PathFeedback, second.PathFeedback) {
		t.Fatalf("Tick omitted joined reserve sample: %+v", second.PathFeedback)
	}
	if first.PathFeedback[0].Source != "configured" || first.PathFeedback[0].PostNanos != float64(7*time.Millisecond) {
		t.Fatal("later Tick changed prior decision input")
	}
	if second.Settings.CompletionMarginMS != 7 || c.Settings().CompletionMarginMS != 7 {
		t.Fatal("observed feedback mutated configured settings")
	}
}
