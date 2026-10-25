package serving

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestShutdownCannotReopenAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observationLog.Close(context.Background()) })
	runtime, err := NewRuntime(ctx, RuntimeConfig{QueueLimit: 1, ObservationLog: observationLog})
	if err != nil {
		t.Fatal(err)
	}
	definition := &Definition{Name: "workload", UID: "uid", Generation: 1, Model: Model{UID: "model", Generation: 1}, SLO: SLO{Percentile: 99, TargetLatency: time.Second}}
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

func TestAssignmentPersistenceDoesNotHoldRuntimeOrRequestState(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	log := r.config.ObservationLog
	_, snapshot := schedulerFixture()
	definition := snapshot.Workloads[0]
	executed := make(chan Dispatch, 1)
	r.mu.Lock()
	r.instances[instance.ID].Execute = func(_ context.Context, dispatch Dispatch) (BackendResult, error) {
		rows := readRequestRows(t, log.config.Directory)
		if len(rows) != 1 || rows[0].Kind != requestRowAssigned || rows[0].RequestID != dispatch.RequestID {
			t.Errorf("native execution lacks its durable assignment: %+v", rows)
		}
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
		call, err := r.Accept(receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
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
	owned := make(chan int, 1)
	go func() {
		r.mu.Lock()
		count := r.instances[instance.ID].outstanding
		r.mu.Unlock()
		owned <- count
	}()
	select {
	case count := <-owned:
		if count != 1 {
			t.Fatalf("pending durable assignment lost physical ownership: %d", count)
		}
	case <-time.After(time.Second):
		t.Fatal("assignment persistence held Runtime.mu")
	}
	select {
	case <-executed:
		t.Fatal("native execution preceded durable assignment")
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
	rows := readRequestRows(t, log.config.Directory)
	if len(rows) != 2 || rows[0].Kind != requestRowAssigned || rows[1].Kind != requestRowFinal {
		t.Fatalf("assignment/final durability changed: %+v", rows)
	}
}

func TestFinalPersistenceDoesNotHoldRuntimeOrRequestState(t *testing.T) {
	r, _, _ := routingRuntime(t, map[string]int32{"w1": 1})
	log := r.config.ObservationLog
	_, snapshot := schedulerFixture()
	definition := snapshot.Workloads[0]
	receipt, err := r.Receive(definition.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call, err := r.Accept(receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
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
	rows := readRequestRows(t, log.config.Directory)
	if len(rows) != 2 || rows[1].Kind != requestRowFinal {
		t.Fatalf("final append lost after releasing Runtime.mu: %+v", rows)
	}
}

func TestCanceledPendingAssignmentNeverExecutes(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	log := r.config.ObservationLog
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
	call, err := r.Accept(receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
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
