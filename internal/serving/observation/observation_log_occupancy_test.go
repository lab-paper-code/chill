package observation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	serving "github.com/lab-paper-code/chill/internal/serving"
)

func TestScanOccupancyRowsKeepsErrorsOpenAndLateNative(t *testing.T) {
	l, clock := clockedObservationLog(t, ObservationLogConfig{OccupancyTail: 3 * time.Minute}, observationTestHour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime, err := newObservedRuntime(t, ctx, serving.RuntimeConfig{ObservationLog: l})
	if err != nil {
		t.Fatal(err)
	}
	at := observationTestHour.Add(time.Second)
	send := func(id string, e serving.RequestEvent) {
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
		send(id, serving.RequestEvent{Kind: "received", ReceivedAt: at})
		send(id, serving.RequestEvent{Kind: "assigned", Assignment: &serving.Assignment{Node: serving.NodeReference{UID: node}, Instance: "pod/" + node, DispatchedAt: at}})
		if id == "open" {
			continue
		}
		outcome := "completed"
		if id == timedOut {
			outcome = timedOut
		}
		send(id, serving.RequestEvent{Kind: "response_finished", Outcome: outcome})
		if err := l.RequestClosed(id, serving.RequestTimings{}); err != nil {
			t.Fatal(err)
		}
	}
	// This evidence arrives in a later minute, after the timeout final row.
	clock.set(at.Add(2 * time.Minute))
	for i, phase := range []string{"queued", "running", "finished"} {
		nativeAt := at.Add(time.Duration(i+1) * 10 * time.Millisecond)
		p := &serving.BackendProgress{RequestID: timedOut, WorkloadUID: "uid", Phase: phase, At: nativeAt, BatchID: 7, BatchSize: 1, ExecutionComplete: phase == "finished", QueueMS: 999}
		if err := runtime.ObserveBackend("pod/node-uid", *p); err != nil {
			t.Fatal(err)
		}
	}
	kinds := map[string]int{}
	var lateRunning *time.Time
	gap, err := l.ScanOccupancyRows(context.Background(), "uid", "node-uid", at, at.Add(time.Second), func(r serving.RequestRow) {
		kinds[r.Kind]++
		if r.Kind == serving.RequestRowNative && r.NativeRunningAt != nil {
			lateRunning = r.NativeRunningAt
		}
	})
	if err != nil || gap != nil || kinds[serving.RequestRowFinal] != 2 || kinds[serving.RequestRowAssigned] != 3 || kinds[serving.RequestRowOpen] != 1 || kinds[serving.RequestRowNative] != 3 || lateRunning == nil || !lateRunning.Equal(at.Add(20*time.Millisecond)) {
		t.Fatalf("scanner lost physical/unknown evidence: kinds=%v running=%v gap=%v err=%v", kinds, lateRunning, gap, err)
	}
	if len(l.open) != 1 {
		t.Fatalf("late evidence reopened HTTP request: %d", len(l.open))
	}
	result, err := l.Query(context.Background(), "uid", at, at.Add(time.Second), nil, nil, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range result.Requests {
		if r.Kind == serving.RequestRowNative {
			t.Fatal("native supplement replaced HTTP row")
		}
	}
}

// Load sent straight to a measurement instance has no registered Workload or
// gateway call. Its native progress is still recorded and read back by the
// request context's Workload identifier and the native running time.
func TestNativeRowsWithoutWorkloadReadBackByIdentifier(t *testing.T) {
	l, clock := clockedObservationLog(t, ObservationLogConfig{OccupancyTail: time.Minute}, observationTestHour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime, err := newObservedRuntime(t, ctx, serving.RuntimeConfig{ObservationLog: l})
	if err != nil {
		t.Fatal(err)
	}
	const uid = "chill-system/session/session-uid/2/saturation"
	at := observationTestHour.Add(time.Second)
	clock.set(at)
	idle := int64(5e6)
	for i, phase := range []string{"queued", "running", "finished"} {
		p := serving.BackendProgress{RequestID: "load-1", WorkloadUID: uid, Definition: "session-load", Phase: phase,
			At: at.Add(time.Duration(i) * 10 * time.Millisecond), BatchID: 9, BatchSize: 2, ExecutionComplete: i == 2}
		if p.ExecutionComplete {
			p.ServiceSample = &native.ServiceSample{ServiceDurationNanos: 7e6, PrecedingIdleNanos: &idle, TrialID: uid}
		}
		if err := runtime.ObserveBackend("pod-uid/containerd://running", p); err != nil {
			t.Fatal(err)
		}
	}
	var running, finished *serving.RequestRow
	if _, err := l.ScanOccupancyRows(ctx, uid, "", at, at.Add(time.Second), func(r serving.RequestRow) {
		if r.Kind == serving.RequestRowNative && r.NativeRunningAt != nil {
			running = &r
		}
		if r.Kind == serving.RequestRowNative && r.NativeFinishedAt != nil {
			finished = &r
		}
	}); err != nil {
		t.Fatal(err)
	}
	if running == nil || finished == nil || finished.BatchID != 9 || finished.BatchSize != 2 || finished.ServiceDurationNanos != 7e6 ||
		finished.PrecedingIdleNanos == nil || *finished.PrecedingIdleNanos != idle || finished.NativeTrialID != uid ||
		!running.NativeRunningAt.Equal(at.Add(10*time.Millisecond)) {
		t.Fatalf("native rows not read back: running %+v finished %+v", running, finished)
	}
}

func TestDirectNativeProvenanceNeverUsesQueueMS(t *testing.T) {
	at := observationTestHour
	r := serving.RequestRow{ReceivedAt: at}
	p := serving.BackendProgress{Phase: "running", At: at.Add(20 * time.Millisecond), BatchID: 1, BatchSize: 1, QueueMS: 900}
	r.Fold(&serving.RequestEvent{Kind: "backend_running", At: at.Add(30 * time.Millisecond), NativeProgress: &p})
	if r.NativeRunningAt == nil || !r.NativeRunningAt.Equal(p.At) || r.RunningNanos == nil || *r.RunningNanos != int64(30*time.Millisecond) {
		t.Fatalf("mixed native and legacy provenance: %+v", r)
	}
}

func TestNativeSupplementsPreserveRestartDrainAssignment(t *testing.T) {
	const requestID = "restart-native-request"
	const instance = "native-restart/physical"
	ctx := context.Background()
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	config := log.config
	d := &serving.Definition{Name: "restart-workload", UID: "restart-workload-uid", Generation: 1,
		Model: serving.Model{UID: "restart-model-uid", Generation: 1}, SLO: serving.SLO{Percentile: 99, TargetLatency: time.Second}, Admission: serving.Admission{Policy: serving.AdmissionPolicyReject, MaxQueuedRequests: 1024}}
	if err := log.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	at := observationTestHour.Add(time.Second)
	assignment := &serving.Assignment{Instance: instance, Node: serving.NodeReference{Name: "restart-node", UID: "restart-node-uid"}, DispatchedAt: at}
	for _, phase := range []string{"received", "assigned"} {
		event := serving.RequestEvent{RequestID: requestID, WorkloadUID: d.UID, Definition: d.Identity(), Kind: phase, At: at, ReceivedAt: at}
		if phase == "assigned" {
			event.Assignment = assignment
		}
		if err := log.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	for _, phase := range []string{"queued", "running"} {
		progress := &serving.BackendProgress{RequestID: requestID, WorkloadUID: d.UID, Definition: d.Identity(), Phase: phase, At: at, BatchID: 7, BatchSize: 1}
		if err := log.RecordNativeSupplement(serving.RequestEvent{RequestID: requestID, WorkloadUID: d.UID, Definition: d.Identity(),
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
	runtime, err := newObservedRuntime(t, ctx, serving.RuntimeConfig{ObservationLog: reopened})
	if err != nil {
		t.Fatal(err)
	}
	state, stateErr := runtime.State(d.Name)
	instances := runtime.Instances()
	if stateErr != nil || state.Reason != "RestartDrain" || state.Assigned != 1 ||
		len(instances) != 1 || instances[0].ID != instance || runtime.Drained(d.UID) {
		t.Fatalf("native supplements lost restart ownership: state=%+v instances=%+v err=%v", state, instances, stateErr)
	}
	if detached, err := runtime.DetachInstance(instance); err != nil || detached {
		t.Fatalf("native phases without completion released ownership: detached=%v err=%v", detached, err)
	}
	nativeRows := 0
	_, err = reopened.ScanOccupancyRows(context.Background(), d.UID, assignment.Node.UID, at.Add(-time.Second), at.Add(time.Second), func(row serving.RequestRow) {
		if row.Kind == serving.RequestRowNative {
			nativeRows++
		}
	})
	if err != nil || nativeRows != 2 {
		t.Fatalf("restart lost raw physical supplements: native=%d err=%v", nativeRows, err)
	}
	result, err := reopened.Query(context.Background(), d.UID, at.Add(-time.Second), at.Add(time.Second), nil, nil, 0, "", "")
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
			gap, err := log.ScanOccupancyRows(context.Background(), "w", "node-uid", from.In(location), now.In(location), func(row serving.RequestRow) {
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
	first := occupancyScanRow(from, 1)
	first.NativeRunningAt = nil
	if err := encoder.Encode(first); err != nil {
		t.Fatal(err)
	}
	for i := range 10000 {
		outside := occupancyScanRow(from.Add(-time.Hour), uint64(i+2))
		if err := encoder.Encode(outside); err != nil {
			t.Fatal(err)
		}
	}
	supplement := occupancyScanRow(from, 1)
	supplement.Kind, supplement.ReceivedAt = serving.RequestRowNative, time.Time{}
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
	estimator := serving.NewOccupancyEstimator(map[string]*serving.Profile{p.ID: p}, policies, from, from.Add(time.Second))
	visited := 0
	_, err := log.ScanOccupancyRowsBounded(context.Background(), "", "", from, from.Add(time.Second), 1, func(row serving.RequestRow) {
		visited++
		estimator.Add(row)
	})
	if err != nil {
		t.Fatal(err)
	}
	if visited != 3 {
		t.Fatalf("outside rows reached the estimator: visited=%d", visited)
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
		if err := encoder.Encode(occupancyScanRow(from, id)); err != nil {
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
	_, err := log.ScanOccupancyRowsBounded(context.Background(), "", "", from, from.Add(time.Second), 1, func(serving.RequestRow) {
		visited++
	})
	if err == nil || visited != 0 {
		t.Fatalf("partial request population published: err=%v visited=%d", err, visited)
	}
}

func TestNativeBatchJoinMissingFinishedNeverPublishesOccupancy(t *testing.T) {
	for _, tc := range []struct {
		name          string
		lastQueued    time.Duration
		missingFinish bool
		batches       int
		completed     int
		pairs         int
	}{
		{name: "complete backlogged", lastQueued: 9 * time.Millisecond, batches: 2, completed: 4, pairs: 1},
		{name: "complete late member", lastQueued: 11 * time.Millisecond, batches: 2, completed: 4},
		{name: "missing late member finish", lastQueued: 11 * time.Millisecond, missingFinish: true, batches: 1, completed: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
			runtime, err := newObservedRuntime(t, ctx, serving.RuntimeConfig{ObservationLog: log})
			if err != nil {
				t.Fatal(err)
			}
			const uid = "session/2/saturation"
			at := observationTestHour.Add(time.Second)
			clock.set(at)
			for batch := uint64(1); batch <= 2; batch++ {
				for member := range 2 {
					start := time.Duration(batch-1) * 12 * time.Millisecond
					queued := time.Duration(0)
					if batch == 2 {
						queued = 9 * time.Millisecond
						if member == 1 {
							queued = tc.lastQueued
						}
					}
					for phase, offset := range []time.Duration{queued, start, start + 10*time.Millisecond} {
						if tc.missingFinish && batch == 2 && member == 1 && phase == 2 {
							continue
						}
						progress := serving.BackendProgress{
							RequestID: fmt.Sprintf("%d/%d", batch, member), WorkloadUID: uid,
							Phase: []string{"queued", "running", "finished"}[phase], At: at.Add(offset),
							BatchID: batch, BatchSize: 2, ExecutionComplete: phase == 2,
						}
						if phase == 2 {
							progress.ServiceSample = &native.ServiceSample{ServiceDurationNanos: int64(10 * time.Millisecond), TrialID: uid}
						}
						if err := runtime.ObserveBackend("measurement-pod/container", progress); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if err := log.Drain(ctx); err != nil {
				t.Fatal(err)
			}
			join := serving.NewNativeBatchJoin()
			if err := log.ScanSessionRows(ctx, "session/2/", at, at.Add(time.Second), func(row serving.RequestRow) error {
				join.Add(row)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			rows, err := join.Batches()
			if err != nil || len(rows) != tc.batches || join.CompletedInputs() != tc.completed {
				t.Fatalf("rows=%+v completed=%d err=%v", rows, join.CompletedInputs(), err)
			}
			samples := make([]profilederivation.NativeServiceSample, len(rows))
			for i, row := range rows {
				samples[i] = profilederivation.NativeServiceSample{
					BatchID: row.BatchID, BatchSize: row.BatchSize, RecordIDs: make([]string, row.BatchSize),
					ServiceSample: native.ServiceSample{ServiceDurationNanos: row.ServiceDurationNanos, TrialID: uid},
					StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, LatestEnqueuedAt: row.LatestEnqueuedAt,
				}
			}
			occupancy := profilederivation.SaturationDispatchOccupancy(samples, 2)
			if len(occupancy) != tc.pairs || len(occupancy) == 1 && occupancy[0] != float64(time.Millisecond) {
				t.Fatalf("occupancy=%v want %d pairs", occupancy, tc.pairs)
			}
		})
	}
}
