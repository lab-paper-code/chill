package serving

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanOccupancyRowsKeepsErrorsOpenAndLateNative(t *testing.T) {
	const timedOut = "timeout"
	l, clock := clockedObservationLog(t, ObservationLogConfig{OccupancyTail: 3 * time.Minute}, observationTestHour)
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
	gap, err := l.ScanOccupancyRows(context.Background(), "uid", "node-uid", at, at.Add(time.Second), func(r RequestRow) {
		kinds[r.Kind]++
		if r.Kind == RequestRowNative && r.NativeRunningAt != nil {
			lateRunning = r.NativeRunningAt
		}
	})
	if err != nil || gap != nil || kinds[requestRowFinal] != 2 || kinds[requestRowAssigned] != 3 || kinds[requestRowOpen] != 1 || kinds[RequestRowNative] != 3 || lateRunning == nil || !lateRunning.Equal(at.Add(20*time.Millisecond)) {
		t.Fatalf("scanner lost physical/unknown evidence: kinds=%v running=%v gap=%v err=%v", kinds, lateRunning, gap, err)
	}
	if len(l.open) != 1 {
		t.Fatalf("late evidence reopened HTTP request: %d", len(l.open))
	}
	result, err := l.Query(context.Background(), "uid", at, at.Add(time.Second), nil, nil, 0, "")
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
	_, err = reopened.ScanOccupancyRows(context.Background(), d.UID, assignment.Node.UID, at.Add(-time.Second), at.Add(time.Second), func(row RequestRow) {
		if row.Kind == RequestRowNative {
			nativeRows++
		}
	})
	if err != nil || nativeRows != 2 {
		t.Fatalf("restart lost raw physical supplements: native=%d err=%v", nativeRows, err)
	}
	result, err := reopened.Query(context.Background(), d.UID, at.Add(-time.Second), at.Add(time.Second), nil, nil, 0, "")
	if err != nil || len(result.Requests) != 1 {
		t.Fatalf("restart duplicated or lost HTTP assignment: %+v err=%v", result.Requests, err)
	}
	row := result.Requests[0]
	if row.NativeQueuedAt != nil || row.NativeRunningAt != nil || row.ExecutionComplete {
		t.Fatalf("supplement fabricated completed HTTP evidence: %+v", row)
	}
}

func TestScanOccupancyRowsAcceptsLocalNowBounds(t *testing.T) {
	now := observationTestHour.Add(2 * time.Minute).In(time.Local)
	from := now.Add(-time.Minute)
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, from.UTC())
	at := from.Add(time.Second).UTC()
	clock.set(at)
	appendAssignedRequest(t, log, "local-now", at)
	clock.set(now.UTC())
	for _, location := range []*time.Location{time.UTC, time.Local} {
		t.Run(location.String(), func(t *testing.T) {
			rows := 0
			gap, err := log.ScanOccupancyRows(context.Background(), "w", "node-uid", from.In(location), now.In(location), func(row RequestRow) {
				rows++
				if row.RequestID != "local-now" {
					t.Fatalf("unexpected occupancy row: %+v", row)
				}
			})
			if err != nil || gap != nil || rows != 3 {
				t.Fatalf("local now lost occupancy coverage: rows=%d gap=%v err=%v", rows, gap, err)
			}
		})
	}
}

func TestScanOccupancyRowsBoundsRetainedPopulationAndTail(t *testing.T) {
	p, policies, from := directFixture(1)
	log, clock := clockedObservationLog(t, ObservationLogConfig{OccupancyTail: 2 * time.Second}, from)
	clock.set(from.Add(48 * time.Hour))
	var rows bytes.Buffer
	encoder := json.NewEncoder(&rows)
	first := directRow(from, 1, 1, 0, 0, 20)
	first.NativeRunningAt = nil
	if err := encoder.Encode(first); err != nil {
		t.Fatal(err)
	}
	for i := range 10000 {
		outside := directRow(from.Add(-time.Hour), uint64(i+2), 1, 0, 0, 20)
		if err := encoder.Encode(outside); err != nil {
			t.Fatal(err)
		}
	}
	supplement := directRow(from, 1, 1, 0, 0, 20)
	supplement.Kind, supplement.ReceivedAt = RequestRowNative, time.Time{}
	if err := encoder.Encode(supplement); err != nil {
		t.Fatal(err)
	}
	name := log.segment(observationRequestStream, from)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, rows.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	// An unrelated later hour is deliberately unreadable JSON. The one-second
	// query must neither visit nor decode it, even though the log is two days old.
	future := log.segment(observationRequestStream, from.Add(24*time.Hour))
	if err := os.MkdirAll(filepath.Dir(future), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(future, []byte("not JSON\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// The configured two-second tail must also exclude a later minute.
	late := log.segment(observationRequestStream, from.Add(2*time.Minute))
	if err := os.WriteFile(late, []byte("not JSON\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := log.indexSegments([]time.Time{from.UTC().Truncate(time.Hour), from.Add(24 * time.Hour).UTC().Truncate(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	estimator := NewOccupancyEstimator(map[string]*Profile{p.ID: p}, policies, from, from.Add(time.Second))
	visited := 0
	_, err := log.ScanOccupancyRowsBounded(context.Background(), "", "", from, from.Add(time.Second), 1, func(row RequestRow) {
		visited++
		estimator.Add(row)
	})
	if err != nil {
		t.Fatal(err)
	}
	if visited != 3 || len(estimator.requests) != 1 {
		t.Fatalf("outside rows retained: visited=%d requests=%d", visited, len(estimator.requests))
	}
	result := estimator.Results()
	if len(result) != 1 || result[0].RawRows != 2 || result[0].FinalRows != 1 ||
		result[0].CompleteFinalRows != 0 || result[0].CompleteRequests != 1 ||
		result[0].EnrichedCompleteRawRows != 2 {
		t.Fatalf("joined denominators=%+v", result)
	}
}

func TestScanOccupancyRowsCapRejectsBeforePublishing(t *testing.T) {
	_, _, from := directFixture(1)
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, from)
	var rows bytes.Buffer
	encoder := json.NewEncoder(&rows)
	for id := uint64(1); id <= 2; id++ {
		if err := encoder.Encode(directRow(from, id, 1, 0, 0, 20)); err != nil {
			t.Fatal(err)
		}
	}
	name := log.segment(observationRequestStream, from)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, rows.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := log.indexSegments([]time.Time{from.UTC().Truncate(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	visited := 0
	_, err := log.ScanOccupancyRowsBounded(context.Background(), "", "", from, from.Add(time.Second), 1, func(RequestRow) {
		visited++
	})
	if err == nil || visited != 0 {
		t.Fatalf("partial request population published: err=%v visited=%d", err, visited)
	}
}
