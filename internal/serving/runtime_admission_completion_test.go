package serving

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

// A reply removes its request from the gateway's copy of the executor state
// before the next probe, without touching the probe itself.
func TestRuntimeAdmissionCompletionReconcilesBeforeRefresh(t *testing.T) {
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
	i.Execute = func(context.Context, Dispatch) (BackendResult, error) {
		<-finish
		return BackendResult{Finished: true, Progress: executedTestProgress(12742, 1, 0, time.Microsecond)}, nil
	}
	r.mu.Unlock()
	first, receipt := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
	r.mu.Lock()
	observedAt := time.Now()
	original := *i.State.Native
	original.Requests = []firstfloor.RequestState{{
		Context: firstfloor.RequestContext{RequestID: receipt.ID, WorkloadUID: receipt.UID, Definition: first.identity, AdmissionPolicy: "Reject", SLO: firstfloor.SLO{Percentile: 95, TargetMS: 1000}},
		Phase:   phaseRunning, BatchID: 12742, BatchSize: 1, ServiceElapsedMS: 0.098432,
	}}
	i.State.Native, i.State.ObservedAt = &original, observedAt
	r.mu.Unlock()
	close(finish)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := first.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(i.State.Native.Requests) != 0 {
		t.Fatal("physical completion remained in the executor state")
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
	const noExecution = "no execution"
	for _, name := range []string{"timeout", "transport failure", "wrong executor", "wrong node", "physical completion", noExecution} {
		t.Run(name, func(t *testing.T) {
			r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
			r.mu.Lock()
			defer r.mu.Unlock()
			i := r.instances[wanted.ID]
			q := admissionTestRequest("physical")
			q.Context.WorkloadUID, q.Context.Definition = "w1", r.workloads["w1"].identity
			q.Phase, q.BatchID, q.BatchSize = phaseRunning, 12742, 1
			if name == noExecution {
				q.Phase, q.BatchID, q.BatchSize = phaseQueued, 0, 0
			}
			i.State.Native.Requests = []firstfloor.RequestState{q}
			// The warmed actor's last service end is an hour old; only a proven
			// execution may move it.
			warm := int64(time.Hour)
			i.State.Native.LastServiceEndAgeNanos = &warm
			c := &Call{receipt: Receipt{ID: q.Context.RequestID, UID: q.Context.WorkloadUID}, identity: q.Context.Definition, instance: i,
				assignment: &Assignment{Instance: i.State.ID, Node: i.State.Node, DispatchedAt: time.Now().Add(-10 * time.Millisecond)}, backendDone: true,
				progress: &BackendProgress{Phase: phaseFinished, BatchID: q.BatchID, BatchSize: q.BatchSize, ExecutionComplete: true}}
			if q.BatchID != 0 {
				c.progress = executedTestProgress(q.BatchID, q.BatchSize, 0, time.Microsecond)
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
			wantRemoved := name == "physical completion" || name == noExecution
			if (len(i.State.Native.Requests) == 0) != wantRemoved {
				t.Fatalf("completion removal=%v, want %v", len(i.State.Native.Requests) == 0, wantRemoved)
			}
			if !c.transportDone && i.outstanding != 1 {
				t.Fatal("native proof released open HTTP transport occupancy")
			}
			if age := i.State.Native.LastServiceEndAgeNanos; name == noExecution && (age == nil || *age < int64(time.Hour)) {
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
	warm := int64(time.Hour) // the warmed actor's last service end
	observed.LastServiceEndAgeNanos = &warm
	i.State.Native = &observed
	i.outstanding = 2
	for j, q := range []firstfloor.RequestState{a, b} {
		c := &Call{receipt: Receipt{ID: q.Context.RequestID, UID: q.Context.WorkloadUID}, identity: q.Context.Definition, instance: i,
			assignment: &Assignment{Instance: i.State.ID, Node: i.State.Node, DispatchedAt: time.Now().Add(-10 * time.Millisecond)}, backendDone: true,
			progress: executedTestProgress(q.BatchID, q.BatchSize, 0, time.Microsecond)}
		r.releaseLocked(c)
		if len(i.State.Native.Requests) != 1-j {
			t.Fatal("completed batch member remained in admission state")
		}
		if age := i.State.Native.LastServiceEndAgeNanos; j == 0 && (age == nil || *age < int64(time.Hour)) {
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
