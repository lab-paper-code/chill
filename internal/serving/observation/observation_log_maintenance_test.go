package observation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
)

// Maintenance Sync and retention must not delay admission. Unlike the own-Write
// regression, the maintenance-Sync case also passed with synchronous B recording.
func TestRequestsDoNotWaitForMaintenanceIO(t *testing.T) {
	for _, maintenance := range []string{observationSyncFailure, "remove"} {
		t.Run(maintenance, func(t *testing.T) {
			log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
			seedObservationMinutes(t, log, clock, observationTestHour.Add(time.Second))
			if err := log.Drain(context.Background()); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			resume := sync.OnceFunc(func() { close(release) })
			t.Cleanup(resume)
			if maintenance == observationSyncFailure {
				log.syncFile = func(file *os.File) error {
					once.Do(func() { close(entered); <-release })
					return file.Sync()
				}
			} else {
				clock.set(observationTestHour.Add(3 * time.Hour))
				log.removeAll = func(path string) error {
					once.Do(func() { close(entered); <-release })
					return os.RemoveAll(path)
				}
			}
			done := make(chan error, 1)
			go func() { done <- log.Flush(context.Background()) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("maintenance did not reach injected disk operation")
			}
			request := make(chan error, 1)
			go func() {
				at := clock.now()
				err := log.RequestEvent(serving.RequestEvent{RequestID: "during-maintenance", WorkloadUID: "uid", Kind: "assigned", At: at, ReceivedAt: at,
					Assignment: &serving.Assignment{Instance: "physical", DispatchedAt: at}})
				if err == nil {
					err = log.RequestClosed("during-maintenance", serving.RequestTimings{})
				}
				request <- err
			}()
			select {
			case err := <-request:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				resume()
				<-done
				<-request
				t.Fatal("RequestEvent/RequestClosed waited for maintenance disk I/O")
			}
			resume()
			awaitObservationLogOperation(t, done)
			if err := log.Drain(context.Background()); err != nil {
				t.Fatal(err)
			}
			rows := readRequestRows(t, log.config.Directory)
			var kinds []string
			for _, row := range rows {
				if row.RequestID == "during-maintenance" {
					kinds = append(kinds, row.Kind)
				}
			}
			if len(kinds) != 2 || kinds[0] != serving.RequestRowAssigned || kinds[1] != serving.RequestRowFinal {
				t.Fatalf("queued request row order changed: %v", kinds)
			}
		})
	}
}

// Worker serialization keeps rotated files inside the durability barrier.
// Close waits for that barrier, then rejects any later assignment.
func TestFlushPinsFilesAcrossRotationAndClose(t *testing.T) {
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	assignObservationRequest(t, log, "first", observationTestHour)
	if err := log.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	var once sync.Once
	log.syncFile = func(file *os.File) error {
		once.Do(func() { close(entered); <-release })
		return file.Sync()
	}
	flushed := make(chan error, 1)
	go func() { flushed <- log.Flush(context.Background()) }()
	<-entered
	clock.set(observationTestHour.Add(time.Hour))
	assignObservationRequest(t, log, "second", clock.now())
	if err := log.RequestClosed("first", serving.RequestTimings{}); err != nil {
		t.Fatal(err)
	}
	if err := log.RequestClosed("second", serving.RequestTimings{}); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- log.Close(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("Close passed a pending durability barrier: %v", err)
	default:
	}
	resume()
	awaitObservationLogOperation(t, flushed)
	awaitObservationLogOperation(t, closed)
	if err := log.RequestEvent(serving.RequestEvent{RequestID: "after-close", Kind: "assigned", At: clock.now()}); err == nil {
		t.Fatal("assignment accepted after Close")
	}
	reopened, err := NewObservationLog(log.config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close(context.Background()) })
	if _, pending := reopened.RuntimeFacts(); len(pending) != 0 {
		t.Fatalf("Close lost final rows: %+v", pending)
	}
	rows := readRequestRows(t, log.config.Directory)
	if len(rows) != 4 {
		t.Fatalf("rollover lost rows: %+v", rows)
	}
	for _, hour := range []time.Time{observationTestHour, observationTestHour.Add(time.Hour)} {
		if _, err := os.Stat(filepath.Join(log.root, observationHour(hour))); err != nil {
			t.Fatal(err)
		}
	}
}

// A memory-only assignment can execute, but cannot be invented on restart.
// Copy the active owner's bytes while its own assignment Write is blocked.
func TestBlockedAssignmentCrashRequiresFreshExecutorProof(t *testing.T) {
	ctx := context.Background()
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, time.Now().UTC())
	runtime, err := newObservedRuntime(t, ctx, serving.RuntimeConfig{ObservationLog: log})
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot := schedulerFixture()
	definition, instance := snapshot.Workloads[0], snapshot.Instances[0]
	runtime.SetProfiles([]serving.Profile{*instance.Profile})
	if err := runtime.Publish(definition); err != nil {
		t.Fatal(err)
	}
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	var once sync.Once
	log.writeFile = func(file *os.File, data []byte) (int, error) {
		if bytes.Contains(data, []byte(`"kind":"assigned"`)) {
			once.Do(func() { close(entered); <-release })
		}
		return file.Write(data)
	}
	executing, finish := make(chan struct{}, 1), make(chan struct{})
	complete := sync.OnceFunc(func() { close(finish) })
	t.Cleanup(complete)
	candidate := serving.ReadyInstance{
		State:   serving.InstanceState{ID: instance.ID, PodUID: "pod", Node: instance.Node, Profile: instance.Profile.ID, Ready: true, ObservedAt: time.Now(), Native: fixtureAdmissionNative(instance.Profile)},
		Profile: instance.Profile, Routing: map[string]int32{definition.UID: 1}, Capacity: 1,
		Execute: func(context.Context, serving.Dispatch) (serving.BackendResult, error) {
			executing <- struct{}{}
			<-finish
			return serving.BackendResult{Finished: true}, nil
		},
	}
	if err := runtime.InstallInstances([]serving.ReadyInstance{candidate}); err != nil {
		t.Fatal(err)
	}
	receipt, err := runtime.Receive(definition.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call, err := runtime.Accept(&receipt, &serving.InferenceInput{Inputs: []serving.RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("assignment did not reach Write")
	}
	select {
	case <-executing:
	case <-time.After(time.Second):
		t.Fatal("native execution waited for assignment Write")
	}
	config := log.config
	config.Directory = copyObservationCrashImage(t, log.config.Directory)
	restarted, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close(ctx) })
	if !restarted.Recovery().Unknown || !restarted.Health().Healthy {
		t.Fatal("unclean tail must be explicit unknown, not a current recorder failure")
	}
	if rows := readRequestRows(t, config.Directory); len(rows) != 0 {
		t.Fatalf("unwritten assignment fabricated on restart: %+v", rows)
	}
	restored, err := newObservedRuntime(t, ctx, serving.RuntimeConfig{ObservationLog: restarted})
	if err != nil {
		t.Fatal(err)
	}
	restored.SetProfiles([]serving.Profile{*instance.Profile})
	if err := restored.Publish(definition); err != nil {
		t.Fatal(err)
	}
	if err := restored.InstallInstances([]serving.ReadyInstance{candidate}); err != nil {
		t.Fatal(err)
	}
	state, err := restored.State(definition.Name)
	if err != nil || state.Assigned != 0 {
		t.Fatalf("missing assignment became a restored Call: %+v %v", state, err)
	}
	if restored.Drained(definition.UID) {
		t.Fatal("empty restored calls proved unknown executor drained")
	}
	if detached, err := restored.DetachInstance(instance.ID); err != nil || detached {
		t.Fatalf("unknown executor detached: %v %v", detached, err)
	}
	select {
	case <-executing:
		t.Fatal("restart replayed a missing assignment")
	default:
	}
	assertFreshExecutorRecovery(t, restored, restarted, candidate, definition, executing, log.Started())
	resume()
	complete()
	if _, err := call.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Response(receipt, outcomeCompleted, ""); err != nil {
		t.Fatal(err)
	}
}

func copyObservationCrashImage(t *testing.T, directory string) string {
	t.Helper()
	crash := t.TempDir()
	if err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(crash, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, 0600)
	}); err != nil {
		t.Fatal(err)
	}
	return crash
}

func TestFinalAppendDoesNotLoseConcurrentRequestFold(t *testing.T) {
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	assignObservationRequest(t, log, "folding", observationTestHour)
	if err := log.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	var once sync.Once
	log.writeFile = func(file *os.File, data []byte) (int, error) {
		if bytes.Contains(data, []byte(`"kind":"final"`)) {
			once.Do(func() { close(entered); <-release })
		}
		return file.Write(data)
	}
	closed := make(chan error, 1)
	go func() { closed <- log.RequestClosed("folding", serving.RequestTimings{}) }()
	<-entered
	awaitObservationLogOperation(t, closed)
	if err := log.RequestEvent(serving.RequestEvent{RequestID: "folding", Kind: "response_finished",
		At: observationTestHour.Add(time.Second), Outcome: "canceled", ElapsedNanos: int64(time.Second)}); err != nil {
		t.Fatal(err)
	}
	resume()
	if _, err := log.writeBarrier(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	rows := readRequestRows(t, log.config.Directory)
	if len(rows) != 2 || rows[0].Kind != serving.RequestRowAssigned || rows[1].Kind != serving.RequestRowFinal || rows[1].Outcome != "" {
		t.Fatalf("first final did not preserve its admitted version: %+v", rows)
	}
	visible, err := log.Query(context.Background(), "uid", observationTestHour, observationTestHour.Add(time.Minute), nil, nil, 0, "", "")
	if err != nil || visible.Total != 1 || visible.Outcomes["canceled"] != 0 {
		t.Fatalf("stored final no longer masks the newer open fold: %+v %v", visible, err)
	}
	if err := log.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := log.Query(context.Background(), "uid", observationTestHour, observationTestHour.Add(time.Minute), nil, nil, 0, "", "")
	if err != nil || result.Total != 1 || result.Outcomes["canceled"] != 1 {
		t.Fatalf("concurrent fold lost or counted twice: %+v %v", result, err)
	}
	rows = readRequestRows(t, log.config.Directory)
	if len(rows) != 3 || rows[2].Kind != serving.RequestRowFinal || rows[2].Outcome != "canceled" {
		t.Fatalf("later finalization lost the retained newer version: %+v", rows)
	}
}

// Explicit Flush retries the retained FIFO head, but never reopens admission.
func TestWorkerFailureRetainsFIFOAndRuntimeFailure(t *testing.T) {
	for _, stream := range []string{observationDefinitionCatalog, observationCatalogStream, serving.RequestRowAssigned, serving.RequestRowFinal, serving.RequestRowNative, observationManagementStream, observationSchedulerStream, observationResourcesStream, observationClusterStream, observationSyncFailure, observationRotationFailure} {
		t.Run(stream, func(t *testing.T) {
			ctx := context.Background()
			log, clock := clockedObservationLog(t, ObservationLogConfig{}, time.Now().UTC())
			runtime, err := newObservedRuntime(t, ctx, serving.RuntimeConfig{ObservationLog: log})
			if err != nil {
				t.Fatal(err)
			}
			_, snapshot := schedulerFixture()
			definition := snapshot.Workloads[0]
			if err := runtime.Publish(definition); err != nil {
				t.Fatal(err)
			}
			receipt, err := runtime.Receive(definition.Name, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if stream == serving.RequestRowFinal || stream == observationRotationFailure {
				assignObservationRequest(t, log, "head", time.Now().UTC())
			}
			if err := log.Drain(ctx); err != nil {
				t.Fatal(err)
			}
			cause := errors.New("injected observation write failure")
			failing, entered, resume := injectObservationWorkerFailure(t, log, stream, cause)
			at := time.Now().UTC()
			if err := submitObservationFailureHead(log, clock, stream, definition, at); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("worker did not reach failing head")
			}
			assignObservationRequest(t, log, "tail", at)
			if err := log.RequestClosed("tail", serving.RequestTimings{}); err != nil {
				t.Fatal(err)
			}
			resume()
			select {
			case <-log.Failure():
			case <-time.After(time.Second):
				t.Fatal("worker failure did not become visible")
			}
			assertObservationFailureConsumers(t, log, runtime, definition.Name, receipt, cause, at)
			failing.Store(false)
			if err := log.Flush(ctx); err != nil {
				t.Fatalf("explicit retry lost the retained head: %v", err)
			}
			assertRecoveredObservationFIFO(t, log, stream)
			if _, err := runtime.Receive(definition.Name, time.Now()); rawErrorCode(err) != serving.ObservationUnavailableCode {
				t.Fatalf("storage retry reopened sticky-failed admission: %v", err)
			}
			if err := log.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if marker := readObservationRunMarker(t, log.config.Directory); !marker.Clean {
				t.Fatal("successful retained-prefix close was not log-clean")
			}
		})
	}
}

func readObservationRunMarker(t *testing.T, directory string) observationRunState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, "async-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var marker observationRunState
	if err := json.Unmarshal(data, &marker); err != nil {
		t.Fatal(err)
	}
	return marker
}

func TestQueuedClocksRolloverAndMixedClose(t *testing.T) {
	ctx := context.Background()
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	_, snapshot := schedulerFixture()
	definition, profile := snapshot.Workloads[0], snapshot.Instances[0].Profile
	if err := log.DefinitionRecord(definition); err != nil {
		t.Fatal(err)
	}
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	var once sync.Once
	log.writeFile = func(file *os.File, data []byte) (int, error) {
		if bytes.Contains(data, []byte(`"kind":"assigned"`)) {
			once.Do(func() { close(entered); <-release })
		}
		return file.Write(data)
	}
	received := observationTestHour.Add(time.Second)
	dispatched := received.Add(3 * time.Second)
	clock.set(observationTestHour.Add(time.Hour - time.Second))
	for _, event := range []serving.RequestEvent{
		{RequestID: "clocked", WorkloadUID: definition.UID, Definition: definition.Identity(), Kind: "received", At: received, ReceivedAt: received, LifetimeNanos: int64(time.Minute)},
		{RequestID: "clocked", Kind: "validated", At: received.Add(time.Second), ElapsedNanos: int64(time.Second)},
		{RequestID: "clocked", Kind: "accepted", At: received.Add(2 * time.Second), ElapsedNanos: int64(2 * time.Second)},
		{RequestID: "clocked", Kind: "assigned", At: dispatched, ElapsedNanos: int64(3 * time.Second),
			Assignment: &serving.Assignment{Instance: "physical", Node: snapshot.Nodes[0].Node, Profile: profile.ID, ProfileBody: profile, DispatchedAt: dispatched}},
	} {
		if err := log.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("assignment did not reach own Write")
	}
	queued, running, finished := received.Add(4*time.Second), received.Add(5*time.Second), received.Add(6*time.Second)
	clock.set(observationTestHour.Add(time.Hour + time.Second))
	for _, event := range []serving.RequestEvent{
		{RequestID: "clocked", Kind: "backend_queued", At: queued, ElapsedNanos: int64(4 * time.Second), NativeProgress: &serving.BackendProgress{Phase: "queued", At: queued}},
		{RequestID: "clocked", Kind: "backend_running", At: running, ElapsedNanos: int64(5 * time.Second), NativeProgress: &serving.BackendProgress{Phase: "running", At: running}},
		{RequestID: "clocked", Kind: "backend_finished", At: finished, ElapsedNanos: int64(6 * time.Second), ExecutionComplete: true,
			NativeProgress: &serving.BackendProgress{Phase: "finished", At: finished, ExecutionComplete: true, Outcome: outcomeCompleted, BatchID: 1, BatchSize: 1,
				QueueMS: 1, ServiceMS: 2, BackendMS: 3, PreprocessMS: 4, PostprocessMS: 5, FirstTokenMS: 6, NativeElapsedMS: 7, Units: 1}},
		{RequestID: "clocked", Kind: "backend_exchange_finished", At: finished.Add(time.Second), ElapsedNanos: int64(7 * time.Second)},
		{RequestID: "clocked", Kind: "response_finished", At: finished.Add(2 * time.Second), ElapsedNanos: int64(8 * time.Second), ClientSpanNanos: int64(time.Second), Outcome: outcomeCompleted},
	} {
		if err := log.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.RequestClosed("clocked", serving.RequestTimings{}); err != nil {
		t.Fatal(err)
	}
	for _, submit := range []func() error{
		func() error {
			return log.RecordNativeSupplement(serving.RequestEvent{RequestID: "orphan", At: finished, NativeInstance: "physical", NativeProgress: &serving.BackendProgress{Phase: "finished", At: finished, ExecutionComplete: true}})
		},
		func() error {
			return log.ManagementEvent(serving.ManagementEvent{WorkloadUID: definition.UID, At: received})
		},
		func() error { return log.SchedulerRecord(SchedulerObservation{At: received, Snapshot: &snapshot}) },
		func() error {
			return log.ResourcesRecord(ResourceObservation{Kind: "node", ReceivedAt: &received, Samples: []ResourceSample{{SampledAt: queued}}})
		},
		func() error {
			return log.ClusterRecord(ClusterObservation{Kind: "issue", At: received, Source: "test", Event: "raised", Reason: "clock"})
		},
	} {
		if err := submit(); err != nil {
			t.Fatal(err)
		}
	}
	// Only append selection is clamped; the backward scientific event survives.
	clock.set(observationTestHour)
	if err := log.ManagementEvent(serving.ManagementEvent{WorkloadUID: definition.UID, At: observationTestHour}); err != nil {
		t.Fatal(err)
	}
	clock.set(observationTestHour.Add(3 * time.Hour))
	assertMixedQueueClose(t, log, resume)
	assertQueuedRequestClocks(t, log)
	assertCapturedHourCatalogs(t, log, definition, profile)
	assertCapturedRequestHours(t, log)
	assertCapturedStreamClocks(t, log)
	assertCleanMixedReopen(t, log)
}

func assertMixedQueueClose(t *testing.T, log *ObservationLog, resume func()) {
	t.Helper()
	ctx := context.Background()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	flushed := make(chan error, 1)
	go func() { flushed <- log.Flush(canceled) }()
	select {
	case err := <-flushed:
		t.Fatalf("archive cancellation bypassed the blocked local drain: %v", err)
	case <-time.After(time.Second):
	}
	closed := make(chan error, 1)
	go func() { closed <- log.Close(canceled) }()
	select {
	case err := <-closed:
		t.Fatalf("Close discarded the blocked mixed prefix: %v", err)
	default:
	}
	if marker := readObservationRunMarker(t, log.config.Directory); marker.Clean {
		t.Fatal("memory-only tail marked log-clean")
	}
	resume()
	select {
	case err := <-flushed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("drained Flush lost archive cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled Flush did not finish after local storage recovered")
	}
	awaitObservationLogOperation(t, closed)
	if marker := readObservationRunMarker(t, log.config.Directory); !marker.Clean {
		t.Fatal("joined mixed-prefix Close did not commit clean evidence")
	}
}

func assertQueuedRequestClocks(t *testing.T, log *ObservationLog) {
	t.Helper()
	received := observationTestHour.Add(time.Second)
	dispatched := received.Add(3 * time.Second)
	rows := readRequestRows(t, log.config.Directory)
	if len(rows) != 3 || rows[0].Kind != serving.RequestRowAssigned || rows[1].Kind != serving.RequestRowFinal || rows[2].Kind != serving.RequestRowNative || rows[2].RequestID != "orphan" {
		t.Fatalf("mixed FIFO lost row order/native separation: %+v", rows)
	}
	assigned, final := rows[0], rows[1]
	if !assigned.ReceivedAt.Equal(received) || assigned.DispatchedAt == nil || !assigned.DispatchedAt.Equal(dispatched) ||
		!final.ReceivedAt.Equal(received) || final.LifetimeNanos != int64(time.Minute) || final.ClientSpanNanos != int64(time.Second) ||
		final.Outcome != outcomeCompleted || !final.ExecutionComplete {
		t.Fatalf("worker clock replaced request receipt/dispatch/outcome: %+v %+v", assigned, final)
	}
	for _, stage := range []struct {
		got  *int64
		want time.Duration
	}{
		{final.ValidatedNanos, time.Second}, {final.AcceptedNanos, 2 * time.Second}, {final.DispatchedNanos, 3 * time.Second},
		{final.QueuedNanos, 4 * time.Second}, {final.RunningNanos, 5 * time.Second}, {final.ExecutedNanos, 6 * time.Second},
		{final.ExchangeNanos, 7 * time.Second}, {final.ElapsedNanos, 8 * time.Second},
	} {
		if stage.got == nil || *stage.got != int64(stage.want) {
			t.Fatalf("admitted stage clock changed: %+v", final)
		}
	}
	assertQueuedNativeClocks(t, final, received)
}

func assertQueuedNativeClocks(t *testing.T, final serving.RequestRow, received time.Time) {
	t.Helper()
	queued, running, finished := received.Add(4*time.Second), received.Add(5*time.Second), received.Add(6*time.Second)
	if final.NativeQueuedAt == nil || !final.NativeQueuedAt.Equal(queued) || final.NativeRunningAt == nil || !final.NativeRunningAt.Equal(running) ||
		final.NativeFinishedAt == nil || !final.NativeFinishedAt.Equal(finished) || !final.NativeExecutionComplete ||
		final.BatchID != 1 || final.BatchSize != 1 || final.QueueMS != 1 || final.ServiceMS != 2 || final.BackendMS != 3 ||
		final.PreprocessMS != 4 || final.PostprocessMS != 5 || final.FirstTokenMS != 6 || final.NativeElapsedMS == nil || *final.NativeElapsedMS != 7 || final.Units != 1 {
		t.Fatalf("native measured values changed across queue delay: %+v", final)
	}
}

func assertCapturedHourCatalogs(t *testing.T, log *ObservationLog, definition *serving.Definition, profile *serving.Profile) {
	t.Helper()
	// The old assignment stays in its captured minute. Every later stream uses
	// the next hour, not the eventual worker clock or backward local clock.
	oldHour, nextHour := observationHour(observationTestHour), observationHour(observationTestHour.Add(time.Hour))
	for _, hour := range []string{oldHour, nextHour} {
		catalogs, err := filepath.Glob(filepath.Join(log.root, hour, observationCatalogStream+".*.jsonl"))
		if err != nil || len(catalogs) == 0 {
			t.Fatalf("missing captured-hour catalog %s: %v", hour, err)
		}
		found := map[string]bool{}
		for _, name := range catalogs {
			if err := scanObservationFile(name, func(data []byte) error {
				var row CatalogRow
				if err := json.Unmarshal(data, &row); err != nil {
					return err
				}
				if row.Kind == observationDefinitionCatalog && row.Key == definition.Identity() {
					var body serving.Definition
					if err := json.Unmarshal(row.Body, &body); err != nil {
						return err
					}
					found[observationDefinitionCatalog] = body.UID == definition.UID
				}
				if row.Kind == observationProfileCatalog && row.Key == profile.ID {
					var body serving.Profile
					if err := json.Unmarshal(row.Body, &body); err != nil {
						return err
					}
					found[observationProfileCatalog] = body.ID == profile.ID
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		if !found[observationDefinitionCatalog] || !found[observationProfileCatalog] {
			t.Fatalf("captured-hour row references lost catalog bodies in %s: %v", hour, found)
		}
	}
}

func assertCapturedRequestHours(t *testing.T, log *ObservationLog) {
	t.Helper()
	oldHour, nextHour := observationHour(observationTestHour), observationHour(observationTestHour.Add(time.Hour))
	for _, part := range []struct{ hour, kind string }{{oldHour, serving.RequestRowAssigned}, {nextHour, serving.RequestRowFinal}} {
		names, err := filepath.Glob(filepath.Join(log.root, part.hour, observationRequestStream+".*.jsonl"))
		if err != nil || len(names) != 1 {
			t.Fatalf("captured request append hour %s missing: %v %v", part.hour, names, err)
		}
		found := false
		if err := scanObservationFile(names[0], func(data []byte) error {
			var row serving.RequestRow
			if err := json.Unmarshal(data, &row); err != nil {
				return err
			}
			if row.RequestID == "clocked" && row.Kind == part.kind {
				found = true
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("request %s moved out of its admitted hour %s", part.kind, part.hour)
		}
	}
}

func assertCapturedStreamClocks(t *testing.T, log *ObservationLog) {
	t.Helper()
	nextHour := observationHour(observationTestHour.Add(time.Hour))
	received := observationTestHour.Add(time.Second)
	queued := received.Add(4 * time.Second)
	for _, stream := range []string{observationManagementStream, observationSchedulerStream, observationResourcesStream, observationClusterStream} {
		names, err := filepath.Glob(filepath.Join(log.root, "*", stream+".*.jsonl"))
		if err != nil || len(names) != 1 || filepath.Base(filepath.Dir(names[0])) != nextHour {
			t.Fatalf("stream %s reopened old/worker hour: %v %v", stream, names, err)
		}
		if err := scanObservationFile(names[0], func(data []byte) error {
			if stream == observationResourcesStream {
				var row ResourceObservation
				if err := json.Unmarshal(data, &row); err != nil {
					return err
				}
				if row.ReceivedAt == nil || !row.ReceivedAt.Equal(received) || len(row.Samples) != 1 || !row.Samples[0].SampledAt.Equal(queued) {
					t.Fatalf("resource sample clock changed: %+v", row)
				}
			} else {
				var row struct {
					At time.Time `json:"at"`
				}
				if err := json.Unmarshal(data, &row); err != nil {
					return err
				}
				if !row.At.Equal(received) && !(stream == observationManagementStream && row.At.Equal(observationTestHour)) {
					t.Fatalf("stream %s event clock changed: %s", stream, row.At)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func assertCleanMixedReopen(t *testing.T, log *ObservationLog) {
	t.Helper()
	ctx := context.Background()
	reopened, err := NewObservationLog(log.config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close(ctx) })
	if reopened.Recovery().Unknown {
		t.Fatal("successful mixed-prefix handoff became crash unknown")
	}
	if _, pending := reopened.RuntimeFacts(); len(pending) != 0 {
		t.Fatalf("mixed Close omitted a final: %+v", pending)
	}
}

func TestAsyncWriteFailureDoesNotFinishExecutingRequest(t *testing.T) {
	ctx := context.Background()
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, time.Now().UTC())
	runtime, err := newObservedRuntime(t, ctx, serving.RuntimeConfig{ObservationLog: log})
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot := schedulerFixture()
	definition, instance := snapshot.Workloads[0], snapshot.Instances[0]
	runtime.SetProfiles([]serving.Profile{*instance.Profile})
	if err := runtime.Publish(definition); err != nil {
		t.Fatal(err)
	}
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("assignment storage failed after dispatch admission")
	entered, release := make(chan struct{}), make(chan struct{})
	executing, finish := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	complete := sync.OnceFunc(func() { close(finish) })
	var failing atomic.Bool
	failing.Store(true)
	t.Cleanup(func() { failing.Store(false); resume(); complete(); _ = log.Flush(ctx) })
	log.writeFile = func(file *os.File, data []byte) (int, error) {
		if bytes.Contains(data, []byte(`"kind":"assigned"`)) && failing.Load() {
			close(entered)
			<-release
			return 0, cause
		}
		return file.Write(data)
	}
	if err := runtime.InstallInstances([]serving.ReadyInstance{{
		State:   serving.InstanceState{ID: instance.ID, Node: instance.Node, Profile: instance.Profile.ID, Ready: true, ObservedAt: time.Now(), Native: fixtureAdmissionNative(instance.Profile)},
		Profile: instance.Profile, Routing: map[string]int32{definition.UID: 1}, Capacity: 1,
		Execute: func(context.Context, serving.Dispatch) (serving.BackendResult, error) {
			close(executing)
			<-finish
			return serving.BackendResult{Finished: true}, nil
		},
	}}); err != nil {
		t.Fatal(err)
	}
	receipt, err := runtime.Receive(definition.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call, err := runtime.Accept(&receipt, &serving.InferenceInput{Inputs: []serving.RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("assignment did not reach Write")
	}
	select {
	case <-executing:
	case <-time.After(time.Second):
		t.Fatal("native exchange did not start during own Write")
	}
	if err := runtime.Response(receipt, "canceled", "RequestCanceled"); err != nil {
		t.Fatal(err)
	}
	resume()
	select {
	case <-log.Failure():
	case <-time.After(time.Second):
		t.Fatal("worker failure not published")
	}
	state, err := runtime.State(definition.Name)
	if err != nil || state.Assigned != 1 || state.Reason != serving.ObservationUnavailableCode {
		t.Fatalf("disk failure released running physical ownership: %+v %v", state, err)
	}
	if detached, err := runtime.DetachInstance(instance.ID); detached {
		t.Fatalf("disk failure detached running physical ownership: %v", err)
	}
	complete()
	if result, err := call.Wait(ctx); !result.Finished || rawErrorCode(err) != serving.ObservationUnavailableCode {
		t.Fatalf("actual completion lost its observation failure: %+v %v", result, err)
	}
	if err := log.RequestClosed(receipt.ID, serving.RequestTimings{}); !errors.Is(err, cause) {
		t.Fatalf("failed final admission did not retain the storage failure: %v", err)
	}
	failing.Store(false)
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	rows := readRequestRows(t, log.config.Directory)
	if len(rows) != 2 || rows[0].Kind != serving.RequestRowAssigned || rows[1].Kind != serving.RequestRowFinal ||
		rows[1].RequestID != receipt.ID || rows[1].Outcome != "canceled" {
		t.Fatalf("recovered physical completion lost assignment/final: %+v", rows)
	}
}

func assertFreshExecutorRecovery(t *testing.T, restored *serving.Runtime, restarted *ObservationLog, candidate serving.ReadyInstance, definition *serving.Definition, executing <-chan struct{}, started time.Time) {
	t.Helper()
	ctx := context.Background()
	// An empty but unverified state must not release the exact executor.
	candidate.State.Native = &native.State{Ready: true}
	candidate.State.ObservedAt = time.Now()
	if err := restored.InstallInstances([]serving.ReadyInstance{candidate}); err != nil {
		t.Fatal(err)
	}
	if restored.Drained(definition.UID) {
		t.Fatal("unverified native empty cleared unknown occupancy")
	}
	candidate.State.NativeVerified = true
	candidate.State.ObservedAt = restarted.Recovery().Started.Add(-time.Second)
	if err := restored.InstallInstances([]serving.ReadyInstance{candidate}); err != nil {
		t.Fatal(err)
	}
	if restored.Drained(definition.UID) {
		t.Fatal("pre-boot verified native empty cleared unknown occupancy")
	}
	candidate.State.ObservedAt = time.Now()
	if err := restored.InstallInstances([]serving.ReadyInstance{candidate}); err != nil {
		t.Fatal(err)
	}
	if !restored.Drained(definition.UID) {
		t.Fatal("fresh verified exact executor proof did not release occupancy")
	}
	if err := restarted.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if rows := readRequestRows(t, restarted.config.Directory); len(rows) != 0 {
		t.Fatalf("native drain proof invented missing HTTP evidence: %+v", rows)
	}
	select {
	case <-executing:
		t.Fatal("physical recovery replayed a missing assignment")
	default:
	}
	index, err := restarted.RawHour(ctx, observationHour(started))
	if err != nil || len(index.Gaps) == 0 {
		t.Fatalf("physical release erased historical unknown: %+v %v", index, err)
	}
}

const (
	observationSyncFailure     = "sync"
	observationRotationFailure = "rotation"
)

func injectObservationWorkerFailure(t *testing.T, log *ObservationLog, stream string, cause error) (*atomic.Bool, <-chan struct{}, func()) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	var failing atomic.Bool
	failing.Store(true)
	t.Cleanup(func() { failing.Store(false); resume(); _ = log.Flush(context.Background()) })
	var once sync.Once
	log.writeFile = func(file *os.File, data []byte) (int, error) {
		target := strings.HasPrefix(filepath.Base(file.Name()), stream+".") ||
			stream == observationDefinitionCatalog && filepath.Base(file.Name()) == observationDefinitionsFile ||
			(stream == serving.RequestRowAssigned || stream == serving.RequestRowFinal || stream == serving.RequestRowNative) && bytes.Contains(data, []byte(`"kind":"`+stream+`"`))
		if target && failing.Load() {
			once.Do(func() { close(entered); <-release })
			n, err := file.Write(data[:len(data)/2])
			return n, errors.Join(cause, err)
		}
		return file.Write(data)
	}
	if stream == observationSyncFailure || stream == observationRotationFailure {
		log.syncFile = func(file *os.File) error {
			if failing.Load() {
				once.Do(func() { close(entered); <-release })
				return cause
			}
			return file.Sync()
		}
	}
	return &failing, entered, resume
}

func submitObservationFailureHead(log *ObservationLog, clock *observationClock, stream string, definition *serving.Definition, at time.Time) error {
	switch stream {
	case observationSyncFailure:
		go func() { _ = log.Drain(context.Background()) }()
		return nil
	case observationRotationFailure:
		clock.set(clock.now().Add(time.Minute))
		return log.RequestEvent(serving.RequestEvent{RequestID: "rotated", WorkloadUID: definition.UID, Kind: serving.RequestRowAssigned, At: at, ReceivedAt: at,
			Assignment: &serving.Assignment{Instance: "physical", DispatchedAt: at}})
	case observationDefinitionCatalog:
		return log.DefinitionRecord(&serving.Definition{Name: schedulerHistoryOther, UID: schedulerHistoryOther, Generation: 1})
	case observationCatalogStream, serving.RequestRowAssigned:
		return log.RequestEvent(serving.RequestEvent{RequestID: "head", WorkloadUID: definition.UID, Definition: definition.Identity(), Kind: serving.RequestRowAssigned, At: at, ReceivedAt: at,
			Assignment: &serving.Assignment{Instance: "physical", DispatchedAt: at}})
	case serving.RequestRowFinal:
		return log.RequestClosed("head", serving.RequestTimings{})
	case serving.RequestRowNative:
		return log.RecordNativeSupplement(serving.RequestEvent{RequestID: "orphan", NativeInstance: "physical", At: at, NativeProgress: &serving.BackendProgress{At: at, Phase: "finished"}})
	case observationManagementStream:
		return log.ManagementEvent(serving.ManagementEvent{WorkloadUID: definition.UID, At: at})
	case observationSchedulerStream:
		return log.SchedulerRecord(SchedulerObservation{At: at})
	case observationResourcesStream:
		return log.ResourcesRecord(ResourceObservation{Kind: "server", At: &at})
	case observationClusterStream:
		return log.ClusterRecord(ClusterObservation{Kind: "issue", Event: "raised", At: at, Source: "test", Reason: "write-failure"})
	default:
		return errors.New("unsupported observation failure stream")
	}
}

func assertObservationFailureConsumers(t *testing.T, log *ObservationLog, runtime *serving.Runtime, workload string, receipt serving.Receipt, cause error, at time.Time) {
	t.Helper()
	if !errors.Is(log.Err(), cause) || log.Health().Healthy {
		t.Fatalf("worker failure not latched: %v %+v", log.Err(), log.Health())
	}
	state, err := runtime.State(workload)
	if err != nil || state.Reason != serving.ObservationUnavailableCode {
		t.Fatalf("Runtime did not observe failure without another event: %+v %v", state, err)
	}
	if _, err := runtime.Receive(workload, time.Now()); rawErrorCode(err) != serving.ObservationUnavailableCode {
		t.Fatalf("Receive changed observation failure family: %v", err)
	}
	if _, err := runtime.Accept(&receipt, &serving.InferenceInput{}); rawErrorCode(err) != serving.ObservationUnavailableCode {
		t.Fatalf("Accept changed observation failure family: %v", err)
	}
	if err := log.RequestEvent(serving.RequestEvent{RequestID: "post-failure", Kind: "received", At: at}); !errors.Is(err, cause) {
		t.Fatalf("fold-only submission ignored sticky failure: %v", err)
	}
	for _, row := range readRequestRows(t, log.config.Directory) {
		if row.RequestID == "tail" {
			t.Fatalf("later row passed failed FIFO head: %+v", row)
		}
	}
	if err := log.Flush(context.Background()); !errors.Is(err, cause) {
		t.Fatalf("unrecovered Flush reported success: %v", err)
	}
	if marker := readObservationRunMarker(t, log.config.Directory); marker.Clean {
		t.Fatal("failed prefix was marked log-clean")
	}
}

func assertRecoveredObservationFIFO(t *testing.T, log *ObservationLog, stream string) {
	t.Helper()
	var kinds []string
	for _, row := range readRequestRows(t, log.config.Directory) {
		if row.RequestID == "tail" {
			kinds = append(kinds, row.Kind)
		}
	}
	if len(kinds) != 2 || kinds[0] != serving.RequestRowAssigned || kinds[1] != serving.RequestRowFinal {
		t.Fatalf("retry lost/duplicated/reordered tail: %v", kinds)
	}
	switch stream {
	case observationCatalogStream, serving.RequestRowAssigned, serving.RequestRowFinal, serving.RequestRowNative, observationRotationFailure:
		assertRecoveredRequestHead(t, log, stream)
	case observationSyncFailure:
		// The successful retry and clean Close assert the retained Sync barrier.
	default:
		assertRecoveredStreamHead(t, log, stream)
	}
}

func assertRecoveredRequestHead(t *testing.T, log *ObservationLog, stream string) {
	t.Helper()
	wantID, wantKind := "head", serving.RequestRowAssigned
	switch stream {
	case serving.RequestRowFinal:
		wantKind = serving.RequestRowFinal
	case serving.RequestRowNative:
		wantID, wantKind = "orphan", serving.RequestRowNative
	case observationRotationFailure:
		wantID = "rotated"
	}
	surviving := 0
	for _, row := range readRequestRows(t, log.config.Directory) {
		if row.RequestID == wantID && row.Kind == wantKind {
			surviving++
		}
	}
	if surviving != 1 {
		t.Fatalf("retry retained %d copies of failed head %s/%s", surviving, wantID, wantKind)
	}
}

func assertRecoveredStreamHead(t *testing.T, log *ObservationLog, stream string) {
	t.Helper()
	pattern := filepath.Join(log.root, "*", stream+".*.jsonl")
	if stream == observationDefinitionCatalog {
		pattern = filepath.Join(log.root, observationDefinitionsFile)
	}
	names, err := filepath.Glob(pattern)
	if err != nil || len(names) == 0 {
		t.Fatalf("retry omitted failed %s stream: %v %v", stream, names, err)
	}
	surviving := 0
	for _, name := range names {
		if err := scanObservationFile(name, func(data []byte) error {
			if stream == observationDefinitionCatalog {
				var row DefinitionObservation
				if err := json.Unmarshal(data, &row); err != nil {
					return err
				}
				if row.Definition == nil || row.Definition.UID != schedulerHistoryOther {
					return nil
				}
			}
			surviving++
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if surviving != 1 {
		t.Fatalf("retry retained %d failed %s rows", surviving, stream)
	}
}
