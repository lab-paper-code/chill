package serving

import (
	"context"
	"testing"
	"time"
)

func TestScanOccupancyRowsKeepsErrorsOpenAndLateNative(t *testing.T) {
	const timedOut = "timeout"
	l, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime, err := NewRuntime(ctx, RuntimeConfig{QueueLimit: 1, ObservationLog: l})
	if err != nil {
		t.Fatal(err)
	}
	at := observationTestHour.Add(time.Second)
	send := func(id string, e RequestEvent) {
		e.RequestID = id
		e.WorkloadUID = "uid"
		if e.At.IsZero() {
			e.At = at
		}
		if err := l.RequestEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"completed", timedOut, "open", "other-node"} {
		node := "node-uid"
		if id == "other-node" {
			node = "elsewhere"
		}
		send(id, RequestEvent{Kind: phaseReceived, ReceivedAt: at})
		send(id, RequestEvent{Kind: phaseAssigned, Assignment: &Assignment{Node: NodeReference{UID: node}, Instance: "pod/" + node, DispatchedAt: at}})
		if id == "open" {
			continue
		}
		outcome := "completed"
		if id == timedOut {
			outcome = timedOut
		}
		send(id, RequestEvent{Kind: eventResponseFinished, Outcome: outcome})
		if err := l.RequestClosed(id); err != nil {
			t.Fatal(err)
		}
	}
	// This evidence arrives in a later minute, after the timeout final row.
	clock.set(at.Add(2 * time.Minute))
	for i, phase := range []string{phaseQueued, phaseRunning, phaseFinished} {
		nativeAt := at.Add(time.Duration(i+1) * 10 * time.Millisecond)
		p := &BackendProgress{RequestID: timedOut, WorkloadUID: "uid", Phase: phase, At: nativeAt, BatchID: 7, BatchSize: 1, ExecutionComplete: phase == phaseFinished, QueueMS: 999}
		if err := runtime.ObserveBackend("pod/node-uid", *p); err != nil {
			t.Fatal(err)
		}
	}
	kinds := map[string]int{}
	var lateRunning *time.Time
	gap, err := l.ScanOccupancyRows("uid", "node-uid", at, at.Add(time.Second), func(r RequestRow) {
		kinds[r.Kind]++
		if r.Kind == RequestRowNative && r.NativeRunningAt != nil {
			lateRunning = r.NativeRunningAt
		}
	})
	if err != nil || gap != nil || kinds[requestRowFinal] != 2 || kinds[requestRowAssigned] != 1 || kinds[RequestRowNative] != 3 || lateRunning == nil || !lateRunning.Equal(at.Add(20*time.Millisecond)) {
		t.Fatalf("scanner lost physical/unknown evidence: kinds=%v running=%v gap=%v err=%v", kinds, lateRunning, gap, err)
	}
	if len(l.open) != 1 {
		t.Fatalf("late evidence reopened HTTP request: %d", len(l.open))
	}
	result, err := l.Query("uid", at, at.Add(time.Second), nil, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range result.Requests {
		if r.Kind == RequestRowNative {
			t.Fatal("native supplement replaced HTTP row")
		}
	}
}

func TestDirectNativeProvenanceNeverUsesQueueMS(t *testing.T) {
	at := observationTestHour
	r := RequestRow{ReceivedAt: at}
	p := BackendProgress{Phase: phaseRunning, At: at.Add(20 * time.Millisecond), BatchID: 1, BatchSize: 1, QueueMS: 900}
	r.fold(&RequestEvent{Kind: "backend_running", At: at.Add(30 * time.Millisecond), NativeProgress: &p})
	if r.NativeRunningAt == nil || !r.NativeRunningAt.Equal(p.At) || r.RunningNanos == nil || *r.RunningNanos != int64(30*time.Millisecond) {
		t.Fatalf("mixed native and legacy provenance: %+v", r)
	}
}

func TestNativeSupplementsPreserveRestartDrainAssignment(t *testing.T) {
	const requestID = "restart-native-request"
	const instance = "native-restart/physical"
	const restartDrainReason = "RestartDrain"
	ctx := context.Background()
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	config := log.config
	d := &Definition{Name: "restart-workload", UID: "restart-workload-uid", Generation: 1,
		Model: Model{UID: "restart-model-uid", Generation: 1}, SLO: SLO{Percentile: 99, TargetLatency: time.Second}}
	if err := log.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	at := observationTestHour.Add(time.Second)
	assignment := &Assignment{Instance: instance, Node: NodeReference{Name: "restart-node", UID: "restart-node-uid"}, DispatchedAt: at}
	for _, phase := range []string{phaseReceived, phaseAssigned} {
		event := RequestEvent{RequestID: requestID, WorkloadUID: d.UID, Definition: d.Identity(), Kind: phase, At: at, ReceivedAt: at}
		if phase == phaseAssigned {
			event.Assignment = assignment
		}
		if err := log.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	for _, phase := range []string{phaseQueued, phaseRunning} {
		progress := &BackendProgress{RequestID: requestID, WorkloadUID: d.UID, Definition: d.Identity(), Phase: phase, At: at, BatchID: 7, BatchSize: 1}
		if err := log.RecordNativeSupplement(RequestEvent{RequestID: requestID, WorkloadUID: d.UID, Definition: d.Identity(),
			At: at, NativeInstance: instance, NativeProgress: progress}); err != nil {
			t.Fatal(err)
		}
	}
	// Crash without a final HTTP row or native completion proof.
	if err := log.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close(ctx) }()
	runtime, err := NewRuntime(ctx, RuntimeConfig{QueueLimit: 1, ObservationLog: reopened})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.cancel()
	call, workload := runtime.calls[requestID], runtime.workloads[d.UID]
	if call == nil || workload == nil || workload.reason != restartDrainReason || call.assignment.Instance != instance || runtime.Drained(d.UID) {
		t.Fatalf("native supplements lost restart ownership: call=%+v workload=%+v", call, workload)
	}
	if detached, err := runtime.DetachInstance(instance); err != nil || detached {
		t.Fatalf("native phases without completion released ownership: detached=%v err=%v", detached, err)
	}
	nativeRows := 0
	_, err = reopened.ScanOccupancyRows(d.UID, assignment.Node.UID, at.Add(-time.Second), at.Add(time.Second), func(row RequestRow) {
		if row.Kind == RequestRowNative {
			nativeRows++
		}
	})
	if err != nil || nativeRows != 2 {
		t.Fatalf("restart lost raw physical supplements: native=%d err=%v", nativeRows, err)
	}
	result, err := reopened.Query(d.UID, at.Add(-time.Second), at.Add(time.Second), nil, nil, 0, "")
	if err != nil || len(result.Requests) != 1 {
		t.Fatalf("restart duplicated or lost HTTP assignment: %+v err=%v", result.Requests, err)
	}
	row := result.Requests[0]
	if row.NativeQueuedAt != nil || row.NativeRunningAt != nil || row.ExecutionComplete {
		t.Fatalf("supplement fabricated completed HTTP evidence: %+v", row)
	}
}
