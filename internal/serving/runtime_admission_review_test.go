package serving

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func admissionTestRequest(id string) firstfloor.RequestState {
	return firstfloor.RequestState{Context: firstfloor.RequestContext{RequestID: id, WorkloadUID: "w", Definition: "d", AdmissionPolicy: "Reject", SLO: firstfloor.SLO{Percentile: 95, TargetMS: 1000}}, Phase: phaseQueued}
}

func admissionReviewCompletion(t *testing.T, phase string) (*Runtime, *runtimeInstance, firstfloor.State, *Call) {
	t.Helper()
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
	i := r.instances[wanted.ID]
	observed := *i.State.Native
	q := admissionTestRequest("physical")
	q.Context.WorkloadUID, q.Context.Definition = "w1", r.workloads["w1"].identity
	q.Phase, q.BatchID, q.BatchSize = phase, 12742, 1
	if phase == phaseQueued {
		q.BatchID, q.BatchSize = 0, 0
	}
	observed.Requests = []firstfloor.RequestState{q}
	i.State.Native, i.State.ObservedAt = &observed, time.Now().Add(-100*time.Millisecond)
	i.Capacity, i.outstanding = 2, 1
	c := &Call{receipt: Receipt{ID: q.Context.RequestID, UID: q.Context.WorkloadUID}, identity: q.Context.Definition, instance: i,
		assignment: &Assignment{Instance: i.State.ID, Node: i.State.Node, DispatchedAt: time.Now().Add(-130 * time.Millisecond)}, backendDone: true,
		progress: executedTestProgress(12742, 1, 0, time.Millisecond)}
	return r, i, observed, c
}

// executedTestProgress is a finished native progress with the executor's own
// timing from handler entry: immediate enqueue, then wait and service.
func executedTestProgress(batchID uint64, batchSize int, wait, service time.Duration) *BackendProgress {
	enqueue, waited := int64(0), int64(wait)
	return &BackendProgress{Phase: phaseFinished, BatchID: batchID, BatchSize: batchSize, BackendMS: 1, ExecutionComplete: true, ExecutorWaitNanos: &waited,
		NativeHTTP: &firstfloor.HTTPRequestTiming{EnqueueNanos: &enqueue}, ServiceSample: &firstfloor.ServiceSample{ServiceDurationNanos: int64(service)}}
}

func TestAdmissionExportsLocalSpanAnchorWithoutChangingProbeProvenance(t *testing.T) {
	r, i, _, c := admissionReviewCompletion(t, phaseRunning)
	r.mu.Lock()
	r.completeAdmissionLocked(c)
	anchor := i.nativeAdmissionAt()
	observed := i.State.ObservedAt
	r.mu.Unlock()
	states := r.Instances()
	data, err := json.Marshal(states[0])
	if err != nil {
		t.Fatal(err)
	}
	var exported struct {
		ObservedAt   time.Time `json:"observedAt"`
		NativeSpanAt time.Time `json:"nativeSpanAt"`
	}
	if err := json.Unmarshal(data, &exported); err != nil {
		t.Fatal(err)
	}
	if !exported.NativeSpanAt.Equal(anchor) || !exported.ObservedAt.Equal(observed) {
		t.Fatalf("export lost local anchor or changed probe provenance: %+v, anchor=%s observed=%s", exported, anchor, observed)
	}
}

func TestAdmissionRepublishedProbeKeepsFirstCompletionReceipt(t *testing.T) {
	r, i, observed, c := admissionReviewCompletion(t, phaseRunning)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completeAdmissionLocked(c)
	first := i.nativeAdmissionAt()
	i.State.Native = &observed
	i.State.NativeSpanAt = time.Time{}
	i.State.LastServiceEndAgeUpperNanos = nil
	i.State.AdmissionIdleUnknown = false
	i.applyAdmissionCompletions(first.Add(10 * time.Millisecond))
	if i.State.Native.LastServiceEndAgeNanos == nil || *i.State.Native.LastServiceEndAgeNanos != int64(10*time.Millisecond) {
		t.Fatalf("republished old snapshot reset the service-end anchor: %+v", i.State.Native.LastServiceEndAgeNanos)
	}
}

func TestAdmissionQueuedObservationUsesConfirmedExecutedBatchEnd(t *testing.T) {
	r, i, _, c := admissionReviewCompletion(t, phaseQueued)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completeAdmissionLocked(c)
	if i.State.Native.LastServiceEndAgeNanos == nil {
		t.Fatal("queued observation retained startup after confirmed batch execution completed")
	}
}

// A request that waited in the native queue has its HTTP start far before its
// service end. The executor's own timing places the end after the HTTP start;
// a completion whose timing is missing or ends after its receipt stays unknown,
// even when a probe shows its exchange running.
func TestAdmissionCompletionPlacesServiceEndFromExecutorTiming(t *testing.T) {
	for _, name := range []string{"placed", "no enqueue", "no wait", "no service", "no progress", "end after receipt"} {
		t.Run(name, func(t *testing.T) {
			r, i, _, c := admissionReviewCompletion(t, phaseRunning)
			c.receipt.ReceivedAt = c.assignment.DispatchedAt.Add(-5 * time.Millisecond)
			c.receipt.ServerTiming.Record(ServerWorkerHTTPStart, 10*time.Millisecond)
			httpStart := c.assignment.DispatchedAt.Add(5 * time.Millisecond)
			executor := 105 * time.Millisecond
			c.progress = executedTestProgress(12742, 1, 90*time.Millisecond, 15*time.Millisecond)
			switch name {
			case "no enqueue":
				c.progress.NativeHTTP.EnqueueNanos = nil
			case "no wait":
				c.progress.ExecutorWaitNanos = nil
			case "no service":
				c.progress.ServiceSample = nil
			case "no progress":
				c.progress = nil
				elapsed := int64(0)
				i.State.Native.Requests[0].ExchangeElapsedNanos = &elapsed
			case "end after receipt":
				c.progress = executedTestProgress(12742, 1, 200*time.Millisecond, 15*time.Millisecond)
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			r.completeAdmissionLocked(c)
			fact := i.admissionCompleted[admissionRequestKey{c.receipt.ID, c.receipt.UID, c.identity}]
			if name != "placed" {
				if fact.executed || !i.State.AdmissionIdleUnknown {
					t.Fatal("a completion without a placed service end became an executed fact")
				}
				return
			}
			lower, upper := i.State.Native.LastServiceEndAgeNanos, i.State.LastServiceEndAgeUpperNanos
			if i.State.AdmissionIdleUnknown || lower == nil || upper == nil {
				t.Fatal("executed completion did not bound the service end")
			}
			if want := fact.receivedAt.Sub(httpStart.Add(executor)); time.Duration(*upper-*lower) != want {
				t.Fatalf("service-end interval %v, want %v after HTTP start plus executor time", time.Duration(*upper-*lower), want)
			}
		})
	}
}

func TestAdmissionCompletionUsesIPCSampleWithZeroBackendSpan(t *testing.T) {
	r, i, _, c := admissionReviewCompletion(t, phaseRunning)
	c.progress.BackendMS = 0
	c.progress.ServiceMS = 1
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completeAdmissionLocked(c)
	if i.State.AdmissionIdleUnknown || len(i.State.Native.Requests) != 0 || i.State.Native.LastServiceEndAgeNanos == nil {
		t.Fatal("measured IPC completion with zero backend time stalled admission")
	}
}

func TestAdmissionLaterExecutionProofKeepsFirstReceipt(t *testing.T) {
	r, i, _, c := admissionReviewCompletion(t, phaseRunning)
	proof := c.progress
	c.progress = nil
	c.receipt.ReceivedAt = c.assignment.DispatchedAt.Add(-20 * time.Millisecond)
	c.receipt.ServerTiming.Record(ServerWorkerHTTPStart, 20*time.Millisecond)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completeAdmissionLocked(c)
	key := admissionRequestKey{c.receipt.ID, c.receipt.UID, c.identity}
	first := i.admissionCompleted[key].receivedAt
	if !i.State.AdmissionIdleUnknown {
		t.Fatal("an unclassified completed exchange invented a service end")
	}
	c.progress = proof
	r.completeAdmissionLocked(c)
	if i.State.AdmissionIdleUnknown || i.State.LastServiceEndAgeUpperNanos == nil || !i.admissionCompleted[key].receivedAt.Equal(first) {
		t.Fatal("later execution proof failed to refine the original completion fact")
	}
}

func TestAdmissionAbsentProofDoesNotInventUnknownService(t *testing.T) {
	r, i, _, c := admissionReviewCompletion(t, phaseQueued)
	c.progress, c.backendDone, c.transportDone = nil, false, true
	c.definition = r.workloads["w1"].definition
	c.receipt.Name, c.receipt.ReceivedAt = c.definition.Name, c.assignment.DispatchedAt.Add(-20*time.Millisecond)
	c.receipt.ServerTiming.Record(ServerWorkerHTTPStart, 20*time.Millisecond)
	c.exchangeFinishedAt = time.Now().Add(-10 * time.Millisecond)
	c.assignment.Profile = i.Profile.ID
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[c.receipt.ID] = c
	i.State.Native.Requests = nil
	i.State.NativeVerified, i.State.ObservedAt = true, time.Now()
	r.finishAbsentLocked(i.State)
	if !c.backendDone || i.State.AdmissionIdleUnknown {
		t.Fatal("verified native absence lost its existing no-execution proof")
	}
}

func TestSchedulerCaptureCompletionCannotSplitPhysicalAndRequestViews(t *testing.T) {
	r, _, _, c := admissionReviewCompletion(t, phaseRunning)
	c.definition = r.workloads["w1"].definition
	c.receipt.Name, c.receipt.ReceivedAt = c.definition.Name, c.assignment.DispatchedAt.Add(-20*time.Millisecond)
	c.backendDone = false
	r.mu.Lock()
	r.calls[c.receipt.ID] = c
	r.mu.Unlock()
	// In the old two-read capture, the clock runs after Instances unlocked:
	// completion then updates the request view but not the captured native view.
	inputs, physical := r.SchedulerInputsAndInstances(func() time.Time {
		c.backendDone = true
		r.completeAdmissionLocked(c)
		return time.Now()
	})
	if len(inputs.Outstanding) != 1 || inputs.Outstanding[0].Phase != phaseReturning {
		t.Fatal("capture lost the newly confirmed completion")
	}
	if len(physical) != 1 || len(physical[0].Native.Requests) != 0 || physical[0].NativeSpanAt.IsZero() ||
		physical[0].NativeSpanAt.After(inputs.At) {
		t.Fatal("capture mixed a completed outstanding request with pre-completion native state")
	}
}
