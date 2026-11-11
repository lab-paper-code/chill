package serving

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestUnreadyDrainingInstanceUpdatesRuntimeImageFacts(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	r.mu.Lock()
	held := r.instances[instance.ID]
	held.outstanding = 1
	latest := held.ReadyInstance
	r.mu.Unlock()
	latest.State.Ready = false
	latest.State.Stopped = true
	latest.State.Reason = "native probe failed"
	latest.State.ActualImageID = "containerd://observed-runtime"
	latest.State.DesiredRuntimeImage = "registry/native@sha256:new-declaration"
	latest.State.ReplacementReason = "RunningOlderRuntimeImage"
	if err := r.InstallInstances([]ReadyInstance{latest}); err != nil {
		t.Fatal(err)
	}
	actual := r.Instances()[0]
	if actual.Ready || !actual.Stopped || actual.ActualImageID != latest.State.ActualImageID ||
		actual.DesiredRuntimeImage != latest.State.DesiredRuntimeImage || actual.ReplacementReason != latest.State.ReplacementReason {
		t.Fatalf("draining executor lost current image facts after probe failure: %+v", actual)
	}
}

func TestShutdownCannotReopenAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observationLog, err := newRuntimeTestLog(RuntimeTestObservationConfig{Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observationLog.Close(context.Background()) })
	runtime, err := NewRuntime(ctx, RuntimeConfig{ObservationLog: observationLog})
	if err != nil {
		t.Fatal(err)
	}
	definition := &Definition{Name: "workload", UID: "uid", Generation: 1, Model: Model{UID: "model", Generation: 1}, SLO: SLO{Percentile: 99, TargetLatency: time.Second}, Admission: Admission{Policy: AdmissionPolicyReject, MaxQueuedRequests: 1024}}
	if err = runtime.Publish(definition); err != nil {
		t.Fatal(err)
	}
	if err = runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{runtime.OpenAdmission(definition.UID), runtime.Publish(definition), runtime.PublishClosed(definition, "reconcile")} {
		var domain *Error
		if !errors.As(err, &domain) || domain.Code != "ShuttingDown" {
			t.Fatalf("shutdown gate reopened: %v", err)
		}
	}
	if state, err := runtime.State(definition.Name); err != nil || state.Admission || state.Reason != "ShuttingDown" {
		t.Fatalf("shutdown state changed: %+v %v", state, err)
	}
}

func TestRuntimePublishRejectsInvalidDefinitionAdmission(t *testing.T) {
	for _, closed := range []bool{false, true} {
		publish := "open"
		if closed {
			publish = "closed"
		}
		t.Run(publish, func(t *testing.T) {
			r, _, _ := routingRuntime(t, map[string]int32{"w1": 1})
			original := r.workloads["w1"].definition
			for _, admission := range []Admission{
				{}, {Policy: "Unknown", MaxQueuedRequests: 1},
				{Policy: AdmissionPolicyReject}, {Policy: AdmissionPolicyDelay, MaxQueuedRequests: -1},
			} {
				d := *original
				d.Name, d.UID, d.Admission = "invalid-workload", "invalid-uid", admission
				var err error
				if closed {
					err = r.PublishClosed(&d, "RestartDrain")
				} else {
					err = r.Publish(&d)
				}
				var domain *Error
				if !errors.As(err, &domain) || domain.Code != DefinitionAdmissionInvalid ||
					!strings.Contains(domain.Message, d.Name) || !strings.Contains(domain.Message, d.Identity()) {
					t.Fatalf("invalid admission %+v publication error: %v", admission, err)
				}
				definitions, _ := r.config.ObservationLog.RuntimeFacts()
				if definitions[d.Identity()] != nil || r.RetainedDefinition(d.Identity()) != nil {
					t.Fatal("invalid publication persisted a definition")
				}
				if _, err := r.State(d.Name); !hasRuntimeError(err, "WorkloadNotFound") {
					t.Fatalf("invalid definition registered: %v", err)
				}
			}
			if state, err := r.State(original.Name); err != nil || state.Applied != original {
				t.Fatalf("invalid publication changed existing registration: %+v %v", state, err)
			}
		})
	}
}

type admissionRuntimeFacts struct {
	RuntimeObservation
	definition *Definition
	pending    []RequestRow
}

func (o admissionRuntimeFacts) RuntimeFacts() (map[string]*Definition, []RequestRow) {
	return map[string]*Definition{o.definition.Identity(): o.definition}, o.pending
}

func TestRuntimeRestoreRejectsInvalidPendingDefinitionAdmission(t *testing.T) {
	r, _, _ := routingRuntime(t, map[string]int32{"w1": 1})
	d := *r.workloads["w1"].definition
	d.Admission = Admission{}
	now := time.Now()
	log := admissionRuntimeFacts{RuntimeObservation: r.config.ObservationLog, definition: &d,
		pending: []RequestRow{{RequestID: "unfinished", DefinitionID: d.Identity(), Node: &NodeReference{UID: "node"}, DispatchedAt: &now}}}
	restored, err := NewRuntime(context.Background(), RuntimeConfig{ObservationLog: log})
	var domain *Error
	if restored != nil || !errors.As(err, &domain) || domain.Code != DefinitionAdmissionInvalid || !strings.Contains(domain.Message, d.Identity()) {
		t.Fatalf("invalid pending definition restored: runtime=%v error=%v", restored, err)
	}
}

func TestAssignmentAdmissionDoesNotHoldRuntimeOrRequestState(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	log := r.config.ObservationLog.(*runtimeTestLog)
	_, snapshot := schedulerFixture()
	definition := snapshot.Workloads[0]
	executed := make(chan Dispatch, 1)
	r.mu.Lock()
	r.instances[instance.ID].Execute = func(_ context.Context, dispatch Dispatch) (BackendResult, error) {
		executed <- dispatch
		return BackendResult{Finished: true}, nil
	}
	r.mu.Unlock()
	log.mu.Lock()
	locked := true
	defer func() {
		if locked {
			log.mu.Unlock()
		}
	}()
	type accepted struct {
		receipt Receipt
		call    *Call
		err     error
	}
	acceptedCall := make(chan accepted, 1)
	go func() {
		receipt, err := r.Receive(definition.Name, time.Now())
		if err != nil {
			acceptedCall <- accepted{err: err}
			return
		}
		call, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
		acceptedCall <- accepted{receipt: receipt, call: call, err: err}
	}()
	var current accepted
	select {
	case current = <-acceptedCall:
		if current.err != nil {
			t.Fatal(current.err)
		}
	case <-time.After(time.Second):
		t.Fatal("observation writer blocked unrelated request-state transitions")
	}
	owned := make(chan error, 1)
	go func() {
		detached, err := r.DetachInstance(instance.ID)
		if detached {
			err = errors.New("pending assignment admission freed physical ownership")
		}
		owned <- err
	}()
	select {
	case err := <-owned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("assignment persistence held Runtime.mu")
	}
	select {
	case <-executed:
		t.Fatal("native execution preceded assignment admission")
	default:
	}
	unblockedAt := time.Now()
	log.mu.Unlock()
	locked = false
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := current.call.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	dispatch := <-executed
	if dispatch.Elapsed < unblockedAt.Sub(current.receipt.ReceivedAt) || dispatch.ReceivedAt != current.receipt.ReceivedAt {
		t.Fatalf("persistence wait extended the original lifetime: elapsed=%v received=%v", dispatch.Elapsed, dispatch.ReceivedAt)
	}
	if err := r.Response(current.receipt, outcomeCompleted, ""); err != nil {
		t.Fatal(err)
	}
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	rows := readRequestRows(t, log.config.Directory)
	if len(rows) != 2 || rows[0].Kind != RequestRowAssigned || rows[1].Kind != RequestRowFinal {
		t.Fatalf("assignment/final durability changed: %+v", rows)
	}
}

func TestFinalAdmissionDoesNotHoldRuntimeOrRequestState(t *testing.T) {
	r, _, _ := routingRuntime(t, map[string]int32{"w1": 1})
	log := r.config.ObservationLog.(*runtimeTestLog)
	_, snapshot := schedulerFixture()
	definition := snapshot.Workloads[0]
	receipt, err := r.Receive(definition.Name, time.Now())
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
	closed := make(chan error, 1)
	go func() { closed <- r.Response(receipt, outcomeCompleted, "") }()
	select {
	case <-r.Changed():
	case <-ctx.Done():
		t.Fatal("final persistence held Runtime.mu before its state transition")
	}
	received := make(chan error, 1)
	go func() {
		_, err := r.Receive(definition.Name, time.Now())
		received <- err
	}()
	select {
	case err := <-received:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("final persistence blocked unrelated request memory")
	}
	select {
	case err := <-closed:
		t.Fatalf("response returned before final append: %v", err)
	default:
	}
	log.mu.Unlock()
	locked = false
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	rows := readRequestRows(t, log.config.Directory)
	if len(rows) != 2 || rows[1].Kind != RequestRowFinal {
		t.Fatalf("final append lost after releasing Runtime.mu: %+v", rows)
	}
}

func TestCanceledPendingAssignmentNeverExecutes(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	log := r.config.ObservationLog.(*runtimeTestLog)
	_, snapshot := schedulerFixture()
	definition := snapshot.Workloads[0]
	executed := make(chan struct{}, 1)
	r.mu.Lock()
	r.instances[instance.ID].Execute = func(context.Context, Dispatch) (BackendResult, error) {
		executed <- struct{}{}
		return BackendResult{Finished: true}, nil
	}
	r.mu.Unlock()
	receipt, err := r.Receive(definition.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	log.mu.Lock()
	locked := true
	defer func() {
		if locked {
			log.mu.Unlock()
		}
	}()
	call, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := call.Wait(canceled); err == nil {
		t.Fatal("canceled caller remained live")
	}
	if err := r.Response(receipt, "canceled", "RequestCanceled"); err != nil {
		t.Fatal(err)
	}
	log.mu.Unlock()
	locked = false
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	_, err = call.Wait(ctx)
	var domain *Error
	if !errors.As(err, &domain) || domain.Code != "RequestCanceled" {
		t.Fatalf("pending assignment cancellation changed: %v", err)
	}
	if err := r.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executed:
		t.Fatal("canceled persistence wait reached native execution")
	default:
	}
}
