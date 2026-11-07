package observation

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// This own-Write regression intentionally uses only B APIs so it can be overlaid
// on ad4e22af for FAIL-before. Maintenance Sync alone already passed on B.
func TestRuntimeDispatchDoesNotWaitForOwnObservationWrite(t *testing.T) {
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
	// Finish all earlier writes before installing a seam read by the worker.
	if err := log.Flush(ctx); err != nil {
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
	if err := runtime.InstallInstances([]serving.ReadyInstance{{
		State:   serving.InstanceState{ID: instance.ID, Node: instance.Node, Profile: instance.Profile.ID, Ready: true},
		Profile: instance.Profile, Routing: map[string]int32{definition.UID: 1}, Capacity: 1,
		Execute: func(context.Context, serving.Dispatch) (serving.BackendResult, error) {
			executing <- struct{}{}
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
		t.Fatal("assignment did not reach its own Write")
	}
	select {
	case <-executing:
	case <-time.After(time.Second):
		t.Fatal("native Execute waited for its own observation Write")
	}
	submitted := make(chan error, 1)
	var secondReceipt serving.Receipt
	var secondCall *serving.Call
	go func() {
		at := time.Now().UTC()
		received, err := runtime.Receive(definition.Name, at)
		if err != nil {
			submitted <- err
			return
		}
		accepted, err := runtime.Accept(&received, &serving.InferenceInput{Inputs: []serving.RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
		if err != nil {
			submitted <- err
			return
		}
		secondReceipt, secondCall = received, accepted
		for _, submit := range []func() error{
			func() error {
				return log.RequestEvent(serving.RequestEvent{RequestID: "behind-write", WorkloadUID: definition.UID, Kind: "assigned", At: at, ReceivedAt: at, Assignment: &serving.Assignment{Instance: instance.ID, DispatchedAt: at}})
			},
			func() error { return log.RequestClosed("behind-write", serving.RequestTimings{}) },
			func() error { return log.ManagementEvent(serving.ManagementEvent{WorkloadUID: definition.UID, At: at}) },
			func() error { return log.SchedulerRecord(SchedulerObservation{At: at, Snapshot: &snapshot}) },
			func() error { return log.ResourcesRecord(ResourceObservation{Kind: "server", At: &at}) },
			func() error {
				return log.ClusterRecord(ClusterObservation{Kind: "issue", At: at, Event: "raised", Source: "test", Reason: "blocked-write"})
			},
			func() error {
				return log.DefinitionRecord(&serving.Definition{Name: "other", UID: "other", Generation: 1})
			},
		} {
			if err := submit(); err != nil {
				submitted <- err
				return
			}
		}
		submitted <- nil
	}()
	select {
	case err := <-submitted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("observation producer waited for blocked own Write")
	}
	resume()
	complete()
	if _, err := call.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Response(receipt, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := secondCall.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Response(secondReceipt, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, row := range readRequestRows(t, log.config.Directory) {
		if row.RequestID == "behind-write" {
			kinds = append(kinds, row.Kind)
		}
	}
	if len(kinds) != 2 || kinds[0] != serving.RequestRowAssigned || kinds[1] != serving.RequestRowFinal {
		t.Fatalf("queued assignment/final order changed: %v", kinds)
	}
}
