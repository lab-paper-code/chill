package serving

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func TestAsyncWriterFailureRetainsPhysicalOwner(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	log := r.config.ObservationLog.(*runtimeTestLog)
	started, finishExchange := make(chan struct{}), make(chan struct{})
	r.mu.Lock()
	r.instances[instance.ID].Execute = func(ctx context.Context, _ Dispatch) (BackendResult, error) {
		close(started)
		select {
		case <-finishExchange:
		case <-ctx.Done():
		}
		return BackendResult{}, failure("BackendUnavailable", "physical completion is unknown")
	}
	r.mu.Unlock()
	blockRuntimeObservationWrites(t, log)
	_, snapshot := schedulerFixture()
	d := snapshot.Workloads[0]
	receipt, err := r.Receive(d.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("assignment admission waited for its failed disk Write")
	}
	select {
	case <-log.Failure():
	case <-ctx.Done():
		t.Fatal("writer failure was not published")
	}
	state, err := r.State(d.Name)
	if err != nil || state.Admission || state.Reason != ObservationUnavailableCode {
		t.Fatalf("writer failure was invisible without another request: %+v %v", state, err)
	}
	if _, err := r.Receive(d.Name, time.Now()); !hasRuntimeError(err, ObservationUnavailableCode) {
		t.Fatalf("post-failure receipt did not use existing unavailable family: %v", err)
	}
	if detached, err := r.DetachInstance(instance.ID); err != nil || detached {
		t.Fatalf("writer failure freed live physical execution: %v %v", detached, err)
	}
	close(finishExchange)
	if _, err := call.Wait(ctx); !hasRuntimeError(err, ObservationUnavailableCode) {
		t.Fatalf("failed recording exchange did not report unavailable: %v", err)
	}
	if detached, err := r.DetachInstance(instance.ID); err != nil || detached {
		t.Fatalf("transport return without physical proof freed ownership: %v %v", detached, err)
	}
	r.JoinExecutions()
}

func TestOrphanSupplementFailureFencesNewAdmission(t *testing.T) {
	r, _, _ := routingRuntime(t, map[string]int32{"w1": 1})
	_, snapshot := schedulerFixture()
	d := snapshot.Workloads[0]
	// A rejected direct supplement has no HTTP call to carry its error latch.
	if err := r.ObserveBackend("orphan-executor", BackendProgress{RequestID: "orphan"}); err == nil {
		t.Fatal("invalid orphan supplement unexpectedly succeeded")
	}
	if _, err := r.Receive(d.Name, time.Now()); !hasRuntimeError(err, ObservationUnavailableCode) {
		t.Fatalf("orphan supplement error did not fence new work: %v", err)
	}
}

type runtimeAsyncObservation struct {
	RuntimeObservation
	boot    ObservationRecovery
	mu      sync.Mutex
	failed  error
	failure chan struct{}
	entered chan struct{}
	release <-chan struct{}
}

func (l *runtimeAsyncObservation) Recovery() ObservationRecovery { return l.boot }
func (l *runtimeAsyncObservation) Failure() <-chan struct{}      { return l.failure }
func (l *runtimeAsyncObservation) Err() error {
	l.mu.Lock()
	err := l.failed
	l.mu.Unlock()
	if err != nil {
		return err
	}
	return l.RuntimeObservation.Err()
}
func (l *runtimeAsyncObservation) fail(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed == nil {
		l.failed = err
		close(l.failure)
	}
}
func (l *runtimeAsyncObservation) Drain(ctx context.Context) error {
	l.mu.Lock()
	entered, release := l.entered, l.release
	l.mu.Unlock()
	if release != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return l.RuntimeObservation.Drain(ctx)
}

func runtimeAsyncFixture(t *testing.T, unknown bool) (*Runtime, ReadyInstance, *Definition, *runtimeAsyncObservation, *runtimeTestLog) {
	t.Helper()
	base, err := newRuntimeTestLog(RuntimeTestObservationConfig{Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close(context.Background()) })
	log := &runtimeAsyncObservation{RuntimeObservation: base, boot: ObservationRecovery{Unknown: unknown, Started: time.Now()}, failure: make(chan struct{})}
	r, err := NewRuntime(context.Background(), RuntimeConfig{ObservationLog: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.JoinExecutions)
	_, snapshot := schedulerFixture()
	d, actual := snapshot.Workloads[0], snapshot.Instances[0]
	r.SetProfiles([]Profile{*actual.Profile})
	if err := r.PublishClosed(d, "RestartDrain"); err != nil {
		t.Fatal(err)
	}
	instance := ReadyInstance{State: InstanceState{ID: actual.ID, PodUID: "pod-a", Node: actual.Node, Profile: actual.Profile.ID, Ready: true, NativeVerified: true, ObservedAt: time.Now(), Native: &firstfloor.State{Ready: true, LastServiceEndAgeNanos: new(int64), Metadata: firstfloor.Metadata{Policy: firstfloor.Policy{ProfileID: actual.Profile.ID, MaxBatchSize: actual.MaxBatch}}}}, Profile: actual.Profile, OwnerWorkloadUID: d.UID, Routing: map[string]int32{d.UID: 1}, Capacity: 1,
		Execute: func(context.Context, Dispatch) (BackendResult, error) { return BackendResult{Finished: true}, nil }}
	return r, instance, d, log, base
}

func TestObservationFailureNotifiesWithoutAnotherEvent(t *testing.T) {
	r, _, d, log, _ := runtimeAsyncFixture(t, false)
	if err := r.OpenAdmission(d.UID); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	for len(r.changed) > 0 {
		<-r.changed
	}
	log.fail(errors.New("writer failed"))
	// The listener cannot acquire Runtime.mu yet; the synchronous guard must
	// already reject publication during this scheduling gap.
	if err := r.validatePublicationLocked(d); !hasRuntimeError(err, ObservationUnavailableCode) {
		r.mu.Unlock()
		t.Fatalf("failure latch waited for notification scheduling: %v", err)
	}
	r.mu.Unlock()
	select {
	case <-r.Changed():
	case <-time.After(time.Second):
		t.Fatal("writer failure did not wake runtime consumers")
	}
	if _, err := r.Receive(d.Name, time.Now()); !hasRuntimeError(err, ObservationUnavailableCode) {
		t.Fatalf("failure notification did not fence receipt: %v", err)
	}
}

func TestRecoveryIsExecutorLocalAndRequiresFreshExactEmptyProof(t *testing.T) {
	r, a, d, log, _ := runtimeAsyncFixture(t, true)
	dispatched := make(chan string, 1)
	a.Execute = func(context.Context, Dispatch) (BackendResult, error) {
		dispatched <- a.State.ID
		return BackendResult{Finished: true}, nil
	}
	b := a
	b.State.ID, b.State.PodUID = "executor-b", "pod-b"
	b.State.Native = &firstfloor.State{Ready: true, Requests: []firstfloor.RequestState{{Context: firstfloor.RequestContext{RequestID: "lost-b"}, Phase: phaseRunning}}}
	b.Execute = func(context.Context, Dispatch) (BackendResult, error) {
		dispatched <- b.State.ID
		return BackendResult{Finished: true}, nil
	}
	c := a
	c.State.ID, c.State.PodUID = "executor-c", "pod-c"
	c.State.Ready, c.State.NativeVerified = false, false
	c.Execute = func(context.Context, Dispatch) (BackendResult, error) {
		dispatched <- c.State.ID
		return BackendResult{Finished: true}, nil
	}
	pending := ReadyInstance{State: InstanceState{PodUID: "pending-pod", Node: a.State.Node}, OwnerWorkloadUID: d.UID}
	install := func() {
		t.Helper()
		if err := r.InstallInstances([]ReadyInstance{a, b, c, pending}); err != nil {
			t.Fatal(err)
		}
	}
	install()
	if r.InstanceRecoveryUnknown(a.State.ID) || !r.InstanceRecoveryUnknown(b.State.ID) || !r.InstanceRecoveryUnknown(c.State.ID) || !r.InstanceRecoveryUnknown(pending.State.PodUID) {
		t.Fatal("one empty executor incorrectly released busy, unreadable or pending peers")
	}
	if reopened, err := r.ResumeAfterRestart(context.Background(), d.UID); err != nil || !reopened {
		t.Fatalf("unrelated unknown peers blocked healthy serving: %v %v", reopened, err)
	}
	assertHealthyServingWithFencedPeer(t, r, d, a.State.ID, b.State.ID, dispatched)
	if err := r.ObserveBackend(b.State.ID, BackendProgress{RequestID: "lost-b", WorkloadUID: d.UID, Definition: d.Identity(), Phase: phaseFinished, At: time.Now(), ExecutionComplete: true}); err != nil {
		t.Fatal(err)
	}
	if !r.InstanceRecoveryUnknown(b.State.ID) {
		t.Fatal("one orphan request completion certified the whole actor empty")
	}
	c.State.NativeVerified = true
	c.State.ObservedAt = log.boot.Started.Add(-time.Nanosecond)
	install()
	if !r.InstanceRecoveryUnknown(c.State.ID) {
		t.Fatal("pre-boot empty state released recovery")
	}
	c.State.NativeVerified = false
	c.State.ObservedAt = time.Now()
	install()
	if !r.InstanceRecoveryUnknown(c.State.ID) {
		t.Fatal("failed read reused preserved empty Native")
	}
	b.State.Native = &firstfloor.State{Ready: true}
	b.State.ObservedAt = time.Now()
	install()
	if r.InstanceRecoveryUnknown(b.State.ID) || !r.InstanceRecoveryUnknown(c.State.ID) {
		t.Fatal("fresh empty proof did not release only its exact executor")
	}
	replacement := b
	replacement.State.ID = "replacement-container-b"
	replacement.State.NativeVerified = false
	if err := r.InstallInstances([]ReadyInstance{a, b, c, pending, replacement}); err != nil {
		t.Fatal(err)
	}
	if !r.InstanceRecoveryUnknown(replacement.State.ID) {
		t.Fatal("a late replacement inherited its predecessor's CLEAR")
	}
	if err := r.ConfirmInstanceStopped(c.State.ID); err != nil {
		t.Fatal(err)
	}
	if r.InstanceRecoveryUnknown(c.State.ID) || !r.InstanceRecoveryUnknown(pending.State.PodUID) {
		t.Fatal("exact termination cleared another physical identity")
	}
	if err := r.Forget(d.UID); !hasRuntimeError(err, "DrainIncomplete") {
		t.Fatalf("unknown physical occupancy was forgotten: %v", err)
	}
	if detached, err := r.DetachNode(a.State.Node.UID); err != nil || detached {
		t.Fatalf("healthy sibling justified reuse of an unknown occupied node: %v %v", detached, err)
	}
}

func assertHealthyServingWithFencedPeer(t *testing.T, r *Runtime, d *Definition, healthy, unknown string, dispatched <-chan string) {
	t.Helper()
	state, err := r.State(d.Name)
	if err != nil || !state.Admission || state.Reason != reasonReady {
		t.Fatalf("historical uncertainty poisoned healthy admission: %+v %v", state, err)
	}
	receipt, err := r.Receive(d.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := call.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if id := <-dispatched; id != healthy {
		t.Fatalf("request reached an unknown executor: %s", id)
	}
	if err := r.Response(receipt, outcomeCompleted, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.StopAdmission(d.UID, "Stopping"); err != nil {
		t.Fatal(err)
	}
	if done, err := r.Drain(ctx, d.UID); err != nil || done {
		t.Fatalf("empty known-call map proved unknown registration drained: %v %v", done, err)
	}
	if err := r.Detach(d.UID); !hasRuntimeError(err, "DrainIncomplete") {
		t.Fatalf("registration detached while an executor remained unknown: %v", err)
	}
	if detached, err := r.DetachInstance(unknown); err != nil || detached {
		t.Fatalf("busy unknown executor was detached: %v %v", detached, err)
	}
}

func TestTerminationBeforeExactDiscoveryRetainsRecoveryFence(t *testing.T) {
	r, instance, _, _, _ := runtimeAsyncFixture(t, true)
	instance.State.PodUID = ""
	instance.State.Ready, instance.State.NativeVerified = false, false
	if err := r.InstallInstances([]ReadyInstance{instance}); err != nil {
		t.Fatal(err)
	}
	if err := r.ConfirmInstanceStopped(instance.State.ID); err != nil {
		t.Fatal(err)
	}
	if !r.InstanceRecoveryUnknown(instance.State.ID) {
		t.Fatal("sparse restored identity supplied a per-executor CLEAR")
	}
	instance.State.PodUID = "discovered-pod"
	instance.State.Terminated = true
	if err := r.InstallInstances([]ReadyInstance{instance}); err != nil {
		t.Fatal(err)
	}
	if r.InstanceRecoveryUnknown(instance.State.ID) {
		t.Fatal("later exact terminated discovery did not release its fence")
	}
}

func TestExactTerminalPodDrainsAfterNodeDisappears(t *testing.T) {
	for _, proof := range []string{"published", "confirmed"} {
		t.Run(proof, func(t *testing.T) {
			r, instance, d, _, _ := runtimeAsyncFixture(t, true)
			instance.State.Node.UID = ""
			instance.State.Ready = false
			if err := r.InstallInstances([]ReadyInstance{instance}); err != nil {
				t.Fatal(err)
			}
			if !r.InstanceRecoveryUnknown(instance.State.ID) {
				t.Fatal("running native-empty proof ignored missing Node identity")
			}
			instance.State.Native, instance.State.NativeVerified = nil, false
			if proof == "published" {
				instance.State.Terminated = true
				if err := r.InstallInstances([]ReadyInstance{instance}); err != nil {
					t.Fatal(err)
				}
			} else if err := r.ConfirmInstanceStopped(instance.State.ID); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if done, err := r.Drain(ctx, d.UID); err != nil || !done {
				t.Fatalf("exact terminal Pod still blocked registration drain: %v %v", done, err)
			}
			if err := r.RetirePod(instance.State.PodUID); err != nil {
				t.Fatalf("deleted terminal Pod retained occupancy after Node disappearance: %v", err)
			}
			for _, retained := range r.Instances() {
				if retained.PodUID == instance.State.PodUID {
					t.Fatal("retired terminal Pod remained in physical inventory")
				}
			}
		})
	}
}

func TestLifecycleWaitsForDurablePrefixOutsideRuntimeLock(t *testing.T) {
	for _, operation := range []string{"restart", "shutdown", "detach", "forget", "instance", "node"} {
		t.Run(operation, func(t *testing.T) {
			r, instance, d, log, base := runtimeAsyncFixture(t, false)
			if err := r.InstallInstances([]ReadyInstance{instance}); err != nil {
				t.Fatal(err)
			}
			if err := r.OpenAdmission(d.UID); err != nil {
				t.Fatal(err)
			}
			receipt, err := r.Receive(d.Name, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			call, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := call.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			if err := r.Response(receipt, outcomeCompleted, ""); err != nil {
				t.Fatal(err)
			}
			if err := r.StopAdmission(d.UID, "RestartDrain"); err != nil {
				t.Fatal(err)
			}
			if operation == "forget" {
				if err := r.Detach(d.UID); err != nil {
					t.Fatal(err)
				}
			}
			entered, release := make(chan struct{}, 1), make(chan struct{})
			var releaseOnce sync.Once
			releaseWaiter := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseWaiter()
			log.mu.Lock()
			log.entered, log.release = entered, release
			log.mu.Unlock()
			completed := make(chan error, 1)
			go func() {
				completed <- runRuntimeLifecycle(ctx, r, d.UID, instance.State, operation)
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("lifecycle did not cross the recorder prefix")
			}
			select {
			case err := <-completed:
				t.Fatalf("lifecycle acknowledged before durability: %v", err)
			default:
			}
			// A concurrent consumer must acquire Runtime.mu while storage waits.
			view := make(chan WorkloadState, 1)
			go func() { state, _ := r.State(d.Name); view <- state }()
			select {
			case state := <-view:
				if state.Admission {
					t.Fatal("lifecycle reopened before its final prefix")
				}
			case <-ctx.Done():
				t.Fatal("durability wait held Runtime.mu")
			}
			log.mu.Lock()
			log.release = nil
			log.mu.Unlock()
			// The waiter owns this release independently of later producers.
			releaseWaiter()
			if err := <-completed; err != nil {
				t.Fatal(err)
			}
			rows := readRequestRows(t, base.config.Directory)
			found := false
			for _, row := range rows {
				found = found || row.RequestID == receipt.ID && row.Kind == RequestRowFinal
			}
			if !found {
				t.Fatal("lifecycle succeeded without its durable HTTP final")
			}
		})
	}
}

func runRuntimeLifecycle(ctx context.Context, r *Runtime, uid string, instance InstanceState, operation string) error {
	switch operation {
	case "restart":
		done, err := r.ResumeAfterRestart(ctx, uid)
		if err == nil && !done {
			return errors.New("restart remained closed")
		}
		return err
	case "shutdown":
		return r.Shutdown(ctx)
	case "detach":
		return r.Detach(uid)
	case "forget":
		return r.Forget(uid)
	case "instance":
		done, err := r.DetachInstance(instance.ID)
		if err == nil && !done {
			return errors.New("instance remained attached")
		}
		return err
	case "node":
		done, err := r.DetachNode(instance.Node.UID)
		if err == nil && !done {
			return errors.New("node remained attached")
		}
		return err
	default:
		return errors.New("unknown runtime lifecycle scenario")
	}
}

func TestRestartPrefixDoesNotWaitForUnrelatedFinalization(t *testing.T) {
	r, _, _ := routingRuntime(t, map[string]int32{"w1": 1, "w2": 1})
	log := r.config.ObservationLog.(*runtimeTestLog)
	receipt, err := r.Receive("disabled", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := call.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.StopAdmission("w1", "RestartDrain"); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	for len(r.changed) > 0 {
		<-r.changed
	}
	r.mu.Unlock()
	log.mu.Lock()
	locked := true
	defer func() {
		if locked {
			log.mu.Unlock()
		}
	}()
	finalized := make(chan error, 1)
	go func() { finalized <- r.Response(receipt, outcomeCompleted, "") }()
	select {
	case <-r.Changed():
	case <-ctx.Done():
		t.Fatal("unrelated workload did not reach final admission")
	}
	reopened := make(chan error, 1)
	go func() {
		done, err := r.ResumeAfterRestart(ctx, "w1")
		if err == nil && !done {
			err = errors.New("unrelated finalization blocked restart")
		}
		reopened <- err
	}()
	select {
	case err := <-reopened:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("restart required unrelated producers to become empty")
	}
	log.mu.Unlock()
	locked = false
	if err := <-finalized; err != nil {
		t.Fatal(err)
	}
}

func hasRuntimeError(err error, code string) bool {
	var domain *Error
	return errors.As(err, &domain) && domain.Code == code
}

// A never-started Pod closes only the scientific gap; it may still start, so
// the executor fence must hold until termination or a verified empty probe.
func TestRecoveryNeverStartedPodKeepsExecutorFence(t *testing.T) {
	r, _, _, log, _ := runtimeAsyncFixture(t, true)
	pending := ReadyInstance{State: InstanceState{PodUID: "pending", NeverStarted: true, ObservedAt: time.Now().UTC()}}
	if err := r.InstallInstances([]ReadyInstance{pending}); err != nil {
		t.Fatal(err)
	}
	if !r.InstanceRecoveryUnknown(pending.State.PodUID) {
		t.Fatal("never-started observation released the executor fence")
	}
	if err := log.Err(); err != nil {
		t.Fatal(err)
	}
	state := r.Instances()[0]
	if state.ID != "" || state.Terminated || state.Stopped || state.Ready {
		t.Fatalf("never-started recovery changed physical lifecycle: %+v", state)
	}
}
