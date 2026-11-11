package serving

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func admissionRuntimeCall(t *testing.T, r *Runtime, uid string, at, deadline time.Time) (*Call, Receipt) {
	t.Helper()
	receipt, err := r.Receive(r.workloads[uid].definition.Name, at)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Deadline = deadline
	c, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	return c, receipt
}

func TestRuntimeAdmissionChoosesFeasibleOverInfeasible(t *testing.T) {
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
	slow := r.instances[wanted.ID].ReadyInstance
	p := *slow.Profile
	p.ID = "slow-profile"
	p.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{float64(time.Second)})}
	slow.Profile = &p
	slow.State.ID = "slow"
	slow.State.Profile = p.ID
	slow.State.Native = runtimeTestNative(&p)
	slow.State.ObservedAt = time.Now()
	slow.Routing = map[string]int32{"w1": 1000}
	fast := r.instances[wanted.ID].ReadyInstance
	if err := r.InstallInstances([]ReadyInstance{slow, fast}); err != nil {
		t.Fatal(err)
	}
	c, receipt := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := c.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if c.assignment == nil || c.assignment.Instance != wanted.ID {
		t.Fatalf("SWRR selected infeasible high weight executor: %+v", c.assignment)
	}
	if err := r.Response(receipt, outcomeCompleted, ""); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeAdmissionRejectsAllInfeasible(t *testing.T) {
	r, _, _ := routingRuntime(t, map[string]int32{"w1": 1})
	c, receipt := admissionRuntimeCall(t, r, "w1", time.Now().Add(-time.Second), time.Now().Add(time.Second))
	_, err := c.Wait(context.Background())
	var rejection *Error
	if !errors.As(err, &rejection) || rejection.Code != CodeDeadlineInfeasible || rejection.Stage != AdmissionStageGateway || rejection.PredictedCompletionNanos == nil || rejection.RemainingSLONanos == nil || c.assignment != nil {
		t.Fatalf("infeasible request executed or lost decision: %v %+v", err, c.assignment)
	}
	if err := r.Response(receipt, "rejected", rejection.Code); err != nil {
		t.Fatal(err)
	}
	log := r.config.ObservationLog.(*runtimeTestLog)
	if err := log.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows := readRequestRows(t, log.config.Directory)
	last := rows[len(rows)-1]
	if last.Outcome != "rejected" || last.Policy != AdmissionPolicyReject || last.Rejection == nil || last.GatewayWaitNanos == nil || last.ExecutorWaitNanos != nil {
		t.Fatalf("gateway rejection row lost queue provenance: %+v", last)
	}
}

func TestRuntimeAdmissionUnknownWaitsWithoutDispatch(t *testing.T) {
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
	r.mu.Lock()
	r.instances[wanted.ID].State.Native = nil
	r.mu.Unlock()
	c, _ := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
	if c.assignment != nil || c.admissionState != AdmissionUnknown {
		t.Fatalf("unknown executor dispatched: %+v", c)
	}
	if state, err := r.State(c.receipt.Name); err != nil || state.Reason != AdmissionUnknown || state.Pending != 1 {
		t.Fatalf("unknown not visible in pending state: %+v %v", state, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Wait(ctx); err == nil {
		t.Fatal("cancel did not remove unknown queue entry")
	}
	if len(r.workloads["w1"].queue) != 0 || c.queueEndedAt.IsZero() {
		t.Fatal("unknown call retained after cancellation")
	}
}

func TestRuntimeAdmissionDelayYieldsToOtherWorkloadAndTimesOut(t *testing.T) {
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1, "w2": 1})
	d := *r.workloads["w1"].definition
	d.Generation++
	d.Admission.Policy = AdmissionPolicyDelay
	if err := r.Publish(&d); err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 1)
	r.mu.Lock()
	i := r.instances[wanted.ID]
	i.Capacity = 0
	i.Execute = func(ctx context.Context, dispatch Dispatch) (BackendResult, error) {
		started <- dispatch.Definition.UID
		<-ctx.Done()
		return BackendResult{Finished: true}, ctx.Err()
	}
	r.mu.Unlock()
	deadline := time.Now().Add(50 * time.Millisecond)
	delayed, receipt := admissionRuntimeCall(t, r, "w1", time.Now().Add(-time.Second), deadline)
	normal, _ := admissionRuntimeCall(t, r, "w2", time.Now(), time.Now().Add(time.Second))
	r.mu.Lock()
	i.Capacity = 1
	r.dispatchLocked()
	r.mu.Unlock()
	select {
	case uid := <-started:
		if uid != "w2" {
			t.Fatalf("delay jumped normal request on shared executor: %s", uid)
		}
	case <-time.After(time.Second):
		t.Fatal("normal request did not dispatch")
	}
	if !delayed.deferred || delayed.assignment != nil || normal.assignment == nil {
		t.Fatal("delay queue did not retain late request")
	}
	snapshot := r.SchedulerInputs(time.Now)
	found := false
	for _, q := range snapshot.Outstanding {
		if q.ID == delayed.receipt.ID {
			found = q.Phase == phaseDeferred && q.Instance == ""
		}
	}
	if !found {
		t.Fatal("snapshot lost gateway deferred phase")
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	_, err := delayed.Wait(ctx)
	var domain *Error
	if !errors.As(err, &domain) || domain.Code != "RequestTimeout" || len(r.workloads["w1"].queue) != 0 {
		t.Fatalf("delayed request ignored final lifetime: %v", err)
	}
	if err := r.Response(receipt, schedulerOutcomeTimeout, "RequestTimeout"); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeAdmissionDelayUsesEarliestCompletion(t *testing.T) {
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
	fast := r.instances[wanted.ID].ReadyInstance
	slow := fast
	p := *fast.Profile
	p.ID = "slow"
	p.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{float64(500 * time.Millisecond)})}
	slow.State.ID = "a-slow"
	slow.Profile = &p
	slow.State.Profile = p.ID
	slow.State.Native = runtimeTestNative(&p)
	slow.State.ObservedAt = time.Now()
	if err := r.InstallInstances([]ReadyInstance{slow, fast}); err != nil {
		t.Fatal(err)
	}
	w := r.workloads["w1"]
	now := time.Now()
	c := &Call{receipt: Receipt{ID: "late", UID: "w1", ReceivedAt: now.Add(-time.Second)}, definition: w.definition, identity: w.identity, deferred: true}
	r.mu.Lock()
	choice := r.chooseLocked(w.route, c, now)
	r.mu.Unlock()
	if choice.entry == nil || choice.entry.instance.State.ID != wanted.ID {
		t.Fatalf("delay did not choose earliest completion: %+v", choice)
	}
}

func TestRuntimeAdmissionQueueLimitCountsDelayPerDefinition(t *testing.T) {
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1, "w2": 1})
	d := *r.workloads["w1"].definition
	d.Generation++
	d.Admission.MaxQueuedRequests = 1
	if err := r.Publish(&d); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.instances[wanted.ID].Capacity = 0
	r.mu.Unlock()
	first, _ := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
	r.mu.Lock()
	first.deferred = true
	r.mu.Unlock()
	receipt, err := r.Receive(d.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	var domain *Error
	if !errors.As(err, &domain) || domain.Code != CodeQueueFull || domain.Stage != AdmissionStageGateway || domain.Policy != AdmissionPolicyReject {
		t.Fatalf("queue boundary lost per-definition rejection: %v", err)
	}
	other, _ := admissionRuntimeCall(t, r, "w2", time.Now(), time.Now().Add(time.Second))
	if other == nil || len(r.workloads["w1"].queue) != 1 || len(r.workloads["w2"].queue) != 1 {
		t.Fatal("one workload consumed another workload's queue budget")
	}
}

func TestRuntimeAdmissionRetirementPreservesDelayedRoute(t *testing.T) {
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
	r.mu.Lock()
	r.instances[wanted.ID].Capacity = 0
	r.mu.Unlock()
	c, _ := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
	r.mu.Lock()
	c.deferred = true
	targets := map[string]map[string]int32{"w1": {}}
	r.retainAcceptedRoutesLocked(targets, []string{"w1"})
	r.mu.Unlock()
	if targets["w1"][wanted.ID] <= 0 {
		t.Fatal("route retirement discarded accepted delayed work")
	}
	if stopped, err := r.StopSchedulerInstance(wanted.ID); err != nil || stopped {
		t.Fatalf("only delayed route stopped before drain: %v %v", stopped, err)
	}
}

func TestRuntimeAdmissionIndexesAssignmentsAndAcceptedArrivalRate(t *testing.T) {
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
	now := time.Now()
	i := r.instances[wanted.ID]
	i.State.ObservedAt = now
	i.State.Native.ArrivalAgesMS = map[string][]float64{"w1": {10, 30}}
	w := r.workloads["w1"]
	c := &Call{receipt: Receipt{ID: "unobserved", UID: "w1", ReceivedAt: now.Add(-20 * time.Millisecond), Deadline: now.Add(time.Second)}, definition: w.definition, identity: w.identity, instance: i, assignment: &Assignment{DispatchedAt: now.Add(-5 * time.Millisecond)}}
	r.calls[c.receipt.ID] = c
	r.mu.Lock()
	defer r.mu.Unlock()
	r.beginAdmissionLocked(now)
	if len(i.admissionCalls) != 1 || len(i.admissionAssignments) != 1 {
		t.Fatal("unobserved assignment index lost call")
	}
	if got := i.admissionArrivals["w1"].accepted(now).rate(); got != 200 {
		t.Fatalf("candidate native accepted interval rate=%v want 200", got)
	}
	if got := i.admissionAssignments[0].ArrivalRate; got != 200 {
		t.Fatalf("assignment accepted interval rate=%v want 200", got)
	}
	candidate := &Call{receipt: Receipt{ID: "candidate", UID: "w1", ReceivedAt: now, Deadline: now.Add(time.Second)}, definition: w.definition, identity: w.identity}
	if got := r.estimateLocked(i, candidate, now); !got.Known {
		t.Fatalf("indexed assignment projection unknown: %+v", got)
	}
	r.endAdmissionLocked()
	for _, q := range i.admissionAssignments[:cap(i.admissionAssignments)] {
		if q.Context.RequestID != "" {
			t.Fatal("assignment scratch retained strings")
		}
	}
	for _, c := range i.admissionCalls[:cap(i.admissionCalls)] {
		if c != nil {
			t.Fatal("assignment scratch retained call")
		}
	}
	for _, q := range r.admissionScratch.states[:cap(r.admissionScratch.states)] {
		if q.Context.RequestID != "" {
			t.Fatal("projection scratch retained strings")
		}
	}
	if len(i.admissionObserved) != 0 || len(i.admissionArrivals) != 0 {
		t.Fatal("dispatch identity/rate index retained request identities")
	}
}

func TestRuntimeAdmissionUnknownStopsWholeWorkloadPass(t *testing.T) {
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
	i := r.instances[wanted.ID]
	i.State.Native = nil
	w := r.workloads["w1"]
	now := time.Now()
	for j := range 1024 {
		c := &Call{receipt: Receipt{ID: fmt.Sprintf("queued-%d", j), UID: "w1", ReceivedAt: now, Deadline: now.Add(time.Second)}, definition: w.definition, identity: w.identity}
		w.queue = append(w.queue, c)
	}
	r.mu.Lock()
	r.dispatchLocked()
	r.mu.Unlock()
	if !w.admissionBlocked || w.queue[0].admissionState != AdmissionUnknown || w.queue[1].admissionState != "" || len(w.queue) != 1024 || cap(r.admissionScratch.states) != 0 {
		t.Fatalf("executor-level Unknown repeated projection across queue: blocked=%v first=%s second=%s queue=%d scratch=%d", w.admissionBlocked, w.queue[0].admissionState, w.queue[1].admissionState, len(w.queue), cap(r.admissionScratch.states))
	}
}

func TestRuntimeAdmissionRejectsLifetimeMissBeforeSLO(t *testing.T) {
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
	d := *r.workloads["w1"].definition
	d.Generation++
	d.SLO.TargetLatency = 2 * time.Second
	if err := r.Publish(&d); err != nil {
		t.Fatal(err)
	}
	i := r.instances[wanted.ID].ReadyInstance
	p := *i.Profile
	p.ID = "lifetime-profile"
	p.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{float64(time.Second)})}
	i.Profile = &p
	i.State.Profile = p.ID
	i.State.Native = runtimeTestNative(&p)
	i.State.ObservedAt = time.Now()
	if err := r.InstallInstances([]ReadyInstance{i}); err != nil {
		t.Fatal(err)
	}
	c, receipt := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(100*time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.Wait(ctx)
	var domain *Error
	if !errors.As(err, &domain) || domain.Code != CodeDeadlineInfeasible || domain.Stage != AdmissionStageGateway || c.assignment != nil {
		t.Fatalf("known lifetime-infeasible Reject waited or dispatched: %v", err)
	}
	if err := r.Response(receipt, schedulerOutcomeRejected, domain.Code); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeAdmissionExpiredQueueProofOmitsCompletionPrediction(t *testing.T) {
	now := time.Now()
	c := &Call{receipt: Receipt{ReceivedAt: now}, definition: &Definition{SLO: SLO{TargetLatency: time.Second}, Admission: Admission{Policy: AdmissionPolicyReject}}}
	err := admissionRejection(c, admissionChoice{infeasible: true}, now)
	if err.Code != CodeDeadlineInfeasible || err.PredictedCompletionNanos != nil || err.RemainingSLONanos == nil || *err.RemainingSLONanos != int64(time.Second) {
		t.Fatalf("expired-queue proof fabricated a completion prediction: %+v", err)
	}
}
