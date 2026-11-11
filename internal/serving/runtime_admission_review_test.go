package serving

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

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
		progress: &BackendProgress{Phase: phaseFinished, BatchID: 12742, BatchSize: 1, BackendMS: 1, ExecutionComplete: true}}
	return r, i, observed, c
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

func TestAdmissionCompletionUsesMaximumQuantileOverPossibleGap(t *testing.T) {
	r, i, _, c := admissionReviewCompletion(t, phaseRunning)
	zero, thirty := int64(0), int64(30*time.Millisecond)
	profile := *i.Profile
	profile.Batches = []BatchProfile{schedulerTestPairedBatch(BatchProfile{Batch: 1}, []firstfloor.ServiceSample{
		{TrialID: "a", ServiceDurationNanos: int64(20 * time.Millisecond)},
		{TrialID: "a", PrecedingIdleNanos: &zero, ServiceDurationNanos: int64(20 * time.Millisecond)},
		{TrialID: "a", PrecedingIdleNanos: &thirty, ServiceDurationNanos: int64(50 * time.Millisecond)},
	})}
	i.Profile = &profile
	i.admissionBatches = profile.NativeBundle().Batches
	i.State.Native.Metadata.Policy.MaxBatchSize = 1
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completeAdmissionLocked(c)
	now := time.Now()
	r.beginAdmissionLocked(now)
	defer r.endAdmissionLocked()
	definition := *r.workloads["w1"].definition
	definition.SLO.TargetLatency = 35 * time.Millisecond
	candidate := &Call{receipt: Receipt{ID: "candidate", UID: definition.UID, ReceivedAt: now, Deadline: now.Add(time.Second)}, definition: &definition, identity: c.identity, completionMarginSet: true}
	estimate := r.estimateLocked(i, candidate, now)
	if !estimate.Known || estimate.Completion != 50*time.Millisecond {
		t.Fatalf("completion gap uncertainty admitted with an optimistic conditional quantile: %+v", estimate)
	}
	if admissionCandidateAvailable(estimate, false, 35*time.Millisecond) {
		t.Fatal("uncertain completion admitted an infeasible 35ms candidate")
	}
}

func TestAdmissionCompletedExchangeRequiresIPCExecutionEvidence(t *testing.T) {
	for _, ipcStarted := range []bool{false, true} {
		r, i, _, c := admissionReviewCompletion(t, phaseRunning)
		c.progress = nil
		c.receipt.ReceivedAt = c.assignment.DispatchedAt.Add(-20 * time.Millisecond)
		c.receipt.ServerTiming.Record(ServerWorkerHTTPStart, 20*time.Millisecond)
		if ipcStarted {
			elapsed := int64(0)
			i.State.Native.Requests[0].ExchangeElapsedNanos = &elapsed
		}
		r.mu.Lock()
		r.completeAdmissionLocked(c)
		if len(i.State.Native.Requests) != 0 || i.State.AdmissionIdleUnknown == ipcStarted {
			t.Fatalf("IPC started=%v: completed exchange lost its execution-proof distinction", ipcStarted)
		}
		r.mu.Unlock()
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
