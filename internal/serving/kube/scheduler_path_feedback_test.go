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
		kind    string
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
	if len(second.PathFeedback) != 1 {
		t.Fatalf("Tick omitted workload feedback: %+v", second.PathFeedback)
	}
	feedback := second.PathFeedback[0]
	if feedback.WorkloadUID != workload.UID || feedback.Percentile != workload.SLO.Percentile ||
		feedback.TotalNanos != float64(50*time.Millisecond) || feedback.PostNanos != float64(30*time.Millisecond) ||
		feedback.SampleCount != 1 || feedback.Source != "empirical" ||
		!reflect.DeepEqual(published.PathFeedback, second.PathFeedback) {
		t.Fatalf("Tick omitted joined reserve sample: %+v", second.PathFeedback)
	}
	if first.PathFeedback[0].Source != "configured" || first.PathFeedback[0].PostNanos != float64(7*time.Millisecond) {
		t.Fatal("later Tick changed prior decision input")
	}
	if second.Settings.CompletionMarginMS != 7 || c.Settings().CompletionMarginMS != 7 {
		t.Fatal("observed feedback mutated configured settings")
	}
}

func TestSchedulerCapacityProjectsOnlyCurrentCommittedRateTests(t *testing.T) {
	c := &SchedulerController{reservationState: &reservationState{}}
	if got := c.Capacity(); len(got) != 0 {
		t.Fatalf("uncommitted rate evidence: %+v", got)
	}
	test := serving.SchedulerFeedbackRate{InstanceID: "selected-instance",
		Input:            serving.CapacityInput{WorkloadUID: "workload", DispatchOccupancy: &serving.DispatchOccupancy{PerRequest: time.Millisecond}},
		FeedbackCapacity: serving.FeedbackCapacity{KappaHat: 2, Tests: []serving.FeedbackRateTest{{Requests: 3}}},
	}
	c.state.Decision.Capacity = &serving.SchedulerCapacity{Workloads: []serving.SchedulerCapacityWorkload{{
		UID: "workload", Tests: []serving.SchedulerFeedbackRate{test},
		KeepTests: []serving.SchedulerFeedbackRate{{InstanceID: "keep-only"}},
	}}}
	got := c.Capacity()
	if !reflect.DeepEqual(got, []serving.SchedulerFeedbackRate{test}) {
		t.Fatalf("query mixed keep-only or stale grid evidence into current tests: %+v", got)
	}
	got[0].InstanceID = "changed"
	got[0].Tests[0].Requests = 99
	got[0].Input.DispatchOccupancy.PerRequest = time.Second
	retained := c.state.Decision.Capacity.Workloads[0].Tests[0]
	if retained.Tests[0].Requests != 3 || retained.Input.DispatchOccupancy.PerRequest != time.Millisecond {
		t.Fatalf("query caller mutated nested committed evidence: %+v", retained)
	}
	if next := c.Capacity(); !reflect.DeepEqual(next, []serving.SchedulerFeedbackRate{test}) {
		t.Fatalf("query caller mutated committed rate evidence: %+v", next)
	}
	c.state.Decision.Capacity = nil
	if got := c.Capacity(); len(got) != 0 {
		t.Fatalf("query retained evidence absent from the latest decision: %+v", got)
	}
}
