package observation

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
)

const restoredOwnershipReason = "RestartDrain"

// Requests still synchronously append their own assigned/final rows. Neither
// operation may inherit a maintenance wait, including sync of the same inode.
func TestRequestsDoNotWaitForMaintenanceIO(t *testing.T) {
	for _, maintenance := range []string{"sync", "remove"} {
		t.Run(maintenance, func(t *testing.T) {
			log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
			seedObservationMinutes(t, log, clock, observationTestHour.Add(time.Second))
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			resume := sync.OnceFunc(func() { close(release) })
			t.Cleanup(resume)
			if maintenance == "sync" {
				log.syncFile = func(file *os.File) error {
					once.Do(func() { close(entered); <-release })
					return file.Sync()
				}
			} else {
				clock.set(observationTestHour.Add(3 * time.Hour))
				if err := log.Flush(context.Background()); err != nil {
					t.Fatal(err)
				}
				writer, _ := observationTestWriters.Load(log)
				worker, err := observationTestWorker(log, writer.(evidencestore.StreamingImmutableWriter))
				if err != nil {
					t.Fatal(err)
				}
				if err := worker.Scan(context.Background()); err != nil {
					t.Fatal(err)
				}
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
					err = log.RequestClosed("during-maintenance")
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
			rows := readRequestRows(t, log.config.Directory)
			var kinds []string
			for _, row := range rows {
				if row.RequestID == "during-maintenance" {
					kinds = append(kinds, row.Kind)
				}
			}
			if len(kinds) != 2 || kinds[0] != serving.RequestRowAssigned || kinds[1] != serving.RequestRowFinal {
				t.Fatalf("synchronous request row order changed: %v", kinds)
			}
			resume()
			awaitObservationLogOperation(t, done)
		})
	}
}

// A Flush owns duplicate descriptors, not pointers that rotation can invalidate.
// Close waits for the durability barrier, then rejects any later assignment.
func TestFlushPinsFilesAcrossRotationAndClose(t *testing.T) {
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	assignObservationRequest(t, log, "first", observationTestHour)
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
	if err := log.RequestClosed("first"); err != nil {
		t.Fatal(err)
	}
	if err := log.RequestClosed("second"); err != nil {
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

// Take the crash image from inside the actual native Execute boundary, before
// any Flush/Close. NewRuntime must reconstruct its physical slot and route fence.
func TestAssignmentPrecedesExecuteAndRestartOwnership(t *testing.T) {
	ctx := context.Background()
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, time.Now().UTC())
	runtime, err := newObservedRuntime(t, ctx, serving.RuntimeConfig{QueueLimit: 1, ObservationLog: log})
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot := schedulerFixture()
	definition, instance := snapshot.Workloads[0], snapshot.Instances[0]
	runtime.SetProfiles([]serving.Profile{*instance.Profile})
	if err := runtime.Publish(definition); err != nil {
		t.Fatal(err)
	}
	executing := make(chan serving.Dispatch, 1)
	release := make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	if err := runtime.InstallInstances([]serving.ReadyInstance{{
		State:   serving.InstanceState{ID: instance.ID, Node: instance.Node, Profile: instance.Profile.ID, Ready: true},
		Profile: instance.Profile, Routing: map[string]int32{definition.UID: 1}, Capacity: 1,
		Execute: func(ctx context.Context, dispatch serving.Dispatch) (serving.BackendResult, error) {
			executing <- dispatch
			select {
			case <-release:
			case <-ctx.Done():
			}
			return serving.BackendResult{Finished: true}, nil
		},
	}}); err != nil {
		t.Fatal(err)
	}
	receipt, err := runtime.Receive(definition.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call, err := runtime.Accept(receipt, &serving.InferenceInput{Inputs: []serving.RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case dispatch := <-executing:
		if dispatch.RequestID != receipt.ID {
			t.Fatal("wrong native request")
		}
	case <-time.After(time.Second):
		t.Fatal("native execution did not begin")
	}
	rows := readRequestRows(t, log.config.Directory)
	if len(rows) != 1 || rows[0].Kind != serving.RequestRowAssigned || rows[0].RequestID != receipt.ID {
		t.Fatalf("native execution preceded assignment append: %+v", rows)
	}
	crash := t.TempDir()
	if err := filepath.WalkDir(log.config.Directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(log.config.Directory, path)
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
	config := log.config
	config.Directory = crash
	restarted, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close(context.Background()) })
	restored, err := newObservedRuntime(t, ctx, serving.RuntimeConfig{QueueLimit: 1, ObservationLog: restarted})
	if err != nil {
		t.Fatal(err)
	}
	state, err := restored.State(definition.Name)
	if err != nil || state.Reason != restoredOwnershipReason || state.Assigned != 1 {
		t.Fatalf("restart failed to restore physical ownership: %+v %v", state, err)
	}
	if detached, err := restored.DetachInstance(instance.ID); err != nil || detached {
		t.Fatalf("restart freed a potentially executing physical slot: %v %v", detached, err)
	}
	resume()
	if _, err := call.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Response(receipt, "completed", ""); err != nil {
		t.Fatal(err)
	}
}

func TestFinalAppendDoesNotLoseConcurrentRequestFold(t *testing.T) {
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	assignObservationRequest(t, log, "folding", observationTestHour)
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
	go func() { closed <- log.RequestClosed("folding") }()
	<-entered
	if err := log.RequestEvent(serving.RequestEvent{RequestID: "folding", Kind: "response_finished",
		At: observationTestHour.Add(time.Second), Outcome: "canceled", ElapsedNanos: int64(time.Second)}); err != nil {
		t.Fatal(err)
	}
	resume()
	awaitObservationLogOperation(t, closed)
	if err := log.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := log.Query(context.Background(), "uid", observationTestHour, observationTestHour.Add(time.Minute), nil, nil, 0, "")
	if err != nil || result.Total != 1 || result.Outcomes["canceled"] != 1 {
		t.Fatalf("concurrent fold lost or counted twice: %+v %v", result, err)
	}
}
