package observation

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// Same-root replacement is distinct from the existing crash-image copy test:
// release the original owner inode, reacquire it, and reopen the retained bytes.
func TestSameVolumeReplacementRestoresPhysicalOwnership(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	owner, err := AcquireObservationOwnership(directory, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	log, _ := clockedObservationLog(t, ObservationLogConfig{Directory: directory}, time.Now().UTC())
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
	var executions atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	ready := serving.ReadyInstance{
		State:   serving.InstanceState{ID: instance.ID, Node: instance.Node, Profile: instance.Profile.ID, Ready: true},
		Profile: instance.Profile, Routing: map[string]int32{definition.UID: 1}, Capacity: 1,
		Execute: func(ctx context.Context, dispatch serving.Dispatch) (serving.BackendResult, error) {
			executions.Add(1)
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return serving.BackendResult{Finished: true}, nil
		},
	}
	if err := runtime.InstallInstances([]serving.ReadyInstance{ready}); err != nil {
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
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("native execution did not begin")
	}
	if err := log.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	replacementOwner, err := AcquireObservationOwnership(directory, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacementOwner.Close() }()
	replacement, err := NewObservationLog(log.config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacement.Close(ctx) }()
	restored, err := newObservedRuntime(t, ctx, serving.RuntimeConfig{QueueLimit: 1, ObservationLog: replacement})
	if err != nil {
		t.Fatal(err)
	}
	state, err := restored.State(definition.Name)
	if err != nil || state.Reason != restoredOwnershipReason || state.Assigned != 1 {
		t.Fatalf("same-volume replacement lost ownership: %+v %v", state, err)
	}
	if detached, err := restored.DetachInstance(instance.ID); err != nil || detached {
		t.Fatalf("same-volume replacement freed physical instance: %v %v", detached, err)
	}
	restored.SetProfiles([]serving.Profile{*instance.Profile})
	ready.Execute = func(context.Context, serving.Dispatch) (serving.BackendResult, error) {
		executions.Add(1)
		return serving.BackendResult{Finished: true}, nil
	}
	if err := restored.InstallInstances([]serving.ReadyInstance{ready}); err != nil {
		t.Fatal(err)
	}
	next, err := restored.Receive(definition.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restored.Accept(next, &serving.InferenceInput{Inputs: []serving.RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}}); err == nil {
		t.Fatal("replacement admitted during RestartDrain")
	}
	if executions.Load() != 1 {
		t.Fatal("replacement reexecuted retained assignment")
	}
	resume()
	// The old process's recorder is deliberately closed, so its late result is
	// not an ownership authority for the replacement. Only physical proof is.
	_, _ = call.Wait(ctx)
	if err := restored.ConfirmInstanceStopped(instance.ID); err != nil {
		t.Fatal(err)
	}
	if !restored.Drained(definition.UID) {
		t.Fatal("physical stop proof did not release restored assignment")
	}
}
