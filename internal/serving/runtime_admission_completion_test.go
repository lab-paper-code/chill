package serving

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func TestRuntimeAdmissionPreIPCCompletionDispatchesBeforeRefresh(t *testing.T) {
	testRuntimeAdmissionPreIPCCompletion(t, "jetsonx", 16, 12742, 0.098432)
}

// control379 PLC 20:04:35: lattepanda profile's name contains "fixed", but
// its actual ACK is dynamic maxBatchSize=1, not a fixed-batch exception.
func TestRuntimeAdmissionPLCPreIPCCompletionDispatchesBeforeRefresh(t *testing.T) {
	testRuntimeAdmissionPreIPCCompletion(t, "lattepanda", 1, 16197, 1.059477)
}

func testRuntimeAdmissionPreIPCCompletion(t *testing.T, node string, maxBatch int, batchID uint64, serviceElapsedMS float64) {
	t.Helper()
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
	finish := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	})
	r.mu.Lock()
	i := r.instances[wanted.ID]
	i.Capacity = 2
	i.State.Node.Name = node
	i.State.Native.Metadata.Policy.FixedBatchSize = 0
	i.State.Native.Metadata.Policy.MaxBatchSize = maxBatch
	i.Execute = func(context.Context, Dispatch) (BackendResult, error) {
		<-finish
		return BackendResult{Finished: true, Progress: &BackendProgress{Phase: phaseFinished, BatchID: batchID, BatchSize: 1, BackendMS: 0.000001, ExecutionComplete: true}}, nil
	}
	r.mu.Unlock()
	first, receipt := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
	r.mu.Lock()
	observedAt := time.Now()
	original := *i.State.Native
	original.Requests = []firstfloor.RequestState{{
		Context: firstfloor.RequestContext{RequestID: receipt.ID, WorkloadUID: receipt.UID, Definition: first.identity, AdmissionPolicy: "Reject", SLO: firstfloor.SLO{Percentile: 95, TargetMS: 1000}},
		Phase:   phaseRunning, BatchID: batchID, BatchSize: 1, ServiceElapsedMS: serviceElapsedMS,
	}}
	i.State.Native, i.State.ObservedAt = &original, observedAt
	r.mu.Unlock()
	second, _ := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
	r.mu.Lock()
	if second.assignment != nil || second.admissionState != AdmissionUnknown {
		t.Fatal("pre-IPC running must remain unknown without completion proof")
	}
	r.mu.Unlock()
	close(finish)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := first.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Wait(ctx); err != nil {
		t.Fatalf("completed pre-IPC observation blocked dispatch before next refresh: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if second.assignment == nil {
		t.Fatal("queued request did not dispatch")
	}
	if len(i.State.Native.Requests) != 0 {
		t.Fatal("physical completion remained in admission state")
	}
	if !i.State.ObservedAt.Equal(observedAt) {
		t.Fatal("gateway completion forged a fresh physical observation")
	}
	if len(original.Requests) != 1 {
		t.Fatal("gateway mutated the immutable observer snapshot")
	}
	if !reflect.DeepEqual(i.State.Native.Metadata.Policy, original.Metadata.Policy) {
		t.Fatal("completion replaced the actual ACK policy")
	}
}

func TestRuntimeAdmissionCompletionRequiresPhysicalIdentityAndProof(t *testing.T) {
	for _, name := range []string{"timeout", "transport failure", "wrong executor", "wrong node", "physical completion", "no execution"} {
		t.Run(name, func(t *testing.T) {
			r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
			r.mu.Lock()
			defer r.mu.Unlock()
			i := r.instances[wanted.ID]
			q := admissionTestRequest("physical")
			q.Context.WorkloadUID, q.Context.Definition = "w1", r.workloads["w1"].identity
			q.Phase, q.BatchID, q.BatchSize = phaseRunning, 12742, 1
			if name == "no execution" {
				q.Phase, q.BatchID, q.BatchSize = phaseQueued, 0, 0
			}
			i.State.Native.Requests = []firstfloor.RequestState{q}
			c := &Call{receipt: Receipt{ID: q.Context.RequestID, UID: q.Context.WorkloadUID}, identity: q.Context.Definition, instance: i,
				assignment: &Assignment{Instance: i.State.ID, Node: i.State.Node, DispatchedAt: time.Now().Add(-10 * time.Millisecond)}, backendDone: true,
				progress: &BackendProgress{Phase: phaseFinished, BatchID: q.BatchID, BatchSize: q.BatchSize, ExecutionComplete: true}}
			if q.BatchID != 0 {
				c.progress.BackendMS = 0.000001
			}
			switch name {
			case "timeout", "transport failure":
				c.backendDone = false
				c.transportDone = true
			case "wrong executor":
				c.assignment.Instance = "another-container"
			case "wrong node":
				c.assignment.Node.UID = "another-node"
			}
			i.outstanding = 1
			r.releaseLocked(c)
			wantRemoved := name == "physical completion" || name == "no execution"
			if (len(i.State.Native.Requests) == 0) != wantRemoved {
				t.Fatalf("completion removal=%v, want %v", len(i.State.Native.Requests) == 0, wantRemoved)
			}
			if !c.transportDone && i.outstanding != 1 {
				t.Fatal("native proof released open HTTP transport occupancy")
			}
			if name == "no execution" && i.State.Native.LastServiceEndAgeNanos != nil {
				t.Fatal("no-execution proof invented a service completion")
			}
		})
	}
}

func TestRuntimeAdmissionCompletionPreservesRunningMembersAndRejectsStaleProbe(t *testing.T) {
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.instances[wanted.ID]
	observed := *i.State.Native
	a, b := admissionTestRequest("a"), admissionTestRequest("b")
	for _, q := range []*firstfloor.RequestState{&a, &b} {
		q.Context.WorkloadUID, q.Context.Definition = "w1", r.workloads["w1"].identity
		q.Phase, q.BatchID, q.BatchSize = phaseRunning, 12742, 2
	}
	observed.Requests = []firstfloor.RequestState{a, b}
	i.State.Native = &observed
	i.outstanding = 2
	for j, q := range []firstfloor.RequestState{a, b} {
		c := &Call{receipt: Receipt{ID: q.Context.RequestID, UID: q.Context.WorkloadUID}, identity: q.Context.Definition, instance: i,
			assignment: &Assignment{Instance: i.State.ID, Node: i.State.Node, DispatchedAt: time.Now().Add(-10 * time.Millisecond)}, backendDone: true,
			progress: &BackendProgress{Phase: phaseFinished, BatchID: q.BatchID, BatchSize: q.BatchSize, BackendMS: 0.000001, ExecutionComplete: true}}
		r.releaseLocked(c)
		if len(i.State.Native.Requests) != 1-j {
			t.Fatal("completed batch member remained in admission state")
		}
		if j == 0 && i.State.Native.LastServiceEndAgeNanos != nil {
			t.Fatal("partial batch completion made the executor idle")
		}
	}
	if i.State.Native.LastServiceEndAgeNanos == nil || *i.State.Native.LastServiceEndAgeNanos != 0 {
		t.Fatal("fully completed running batch did not reset the decision idle anchor")
	}
	i.State.Native = &observed
	i.State.NativeSpanAt = time.Time{}
	i.State.LastServiceEndAgeUpperNanos = nil
	i.State.AdmissionIdleUnknown = false
	i.installAdmissionSnapshot()
	if len(i.State.Native.Requests) != 0 {
		t.Fatal("in-flight stale probe resurrected completed requests")
	}
	fresh := observed
	fresh.Requests = nil
	i.State.Native, i.State.ObservedAt = &fresh, time.Now()
	i.installAdmissionSnapshot()
	if len(i.admissionCompleted) != 0 {
		t.Fatal("later probe retained unnecessary request tombstones")
	}
}

func TestRuntimeAdmissionGatewaySettlementDoesNotInventNativeReturn(t *testing.T) {
	r, _, _ := routingRuntime(t, map[string]int32{"w1": 1})
	d := r.workloads["w1"].definition
	c := &Call{receipt: Receipt{ID: "gateway-only", UID: d.UID, ReceivedAt: time.Now()}, definition: d, identity: d.Identity(), backendDone: true, transportDone: true}
	r.mu.Lock()
	r.calls[c.receipt.ID] = c
	r.mu.Unlock()
	snapshot := r.SchedulerInputs(time.Now)
	if len(snapshot.Outstanding) != 1 || snapshot.Outstanding[0].Phase != phasePending || snapshot.Outstanding[0].Instance != "" {
		t.Fatal("gateway-only settlement invented native return occupancy")
	}
}
