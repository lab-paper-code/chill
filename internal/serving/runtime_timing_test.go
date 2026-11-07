package serving

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func runtimeTimingStage(t *testing.T, timing RequestTimings, stage ServerStage) time.Duration {
	t.Helper()
	elapsed, ok := timing.Elapsed(stage)
	if !ok {
		t.Fatalf("missing reached stage %v", stage)
	}
	return elapsed
}

func runtimeTimingMissing(t *testing.T, timing RequestTimings, stages ...ServerStage) {
	t.Helper()
	for _, stage := range stages {
		if elapsed, ok := timing.Elapsed(stage); ok {
			t.Fatalf("unreached stage %v recorded at %v", stage, elapsed)
		}
	}
}

// The existing diagnostic owner sequence advances immediately before Mutex.Lock.
// Holding Runtime.mu until it advances creates a real lock barrier without sleeps.
func runtimeTimingLockBarrier(t *testing.T, r *Runtime, start func()) time.Time {
	t.Helper()
	d := r.mu.Recorder
	r.mu.Lock()
	before := d.sequence.Load()
	start()
	deadline := time.Now().Add(time.Second)
	for d.sequence.Load() == before {
		if time.Now().After(deadline) {
			r.mu.Unlock()
			t.Fatal("request did not reach held runtime lock")
		}
		runtime.Gosched()
	}
	releasedAt := time.Now()
	r.mu.Unlock()
	return releasedAt
}

func TestRuntimeTimingAttributesReceiveAndAcceptLockWait(t *testing.T) {
	r, _, _ := routingRuntime(t, map[string]int32{"w1": 1})
	d := &Diagnostics{events: make([]diagnosticEvent, 256)}
	d.Start()
	r.mu.Recorder, r.config.Diagnostics = d, d
	defer d.Stop()
	name := r.workloads["w1"].definition.Name
	type received struct {
		receipt Receipt
		err     error
	}
	result := make(chan received, 1)
	origin := time.Now()
	releasedAt := runtimeTimingLockBarrier(t, r, func() {
		go func() {
			receipt, err := r.Receive(name, origin)
			result <- received{receipt, err}
		}()
	})
	current := <-result
	if current.err != nil {
		t.Fatal(current.err)
	}
	receipt := current.receipt
	attempt := runtimeTimingStage(t, receipt.ServerTiming, ServerReceiveLockAttempt)
	acquired := runtimeTimingStage(t, receipt.ServerTiming, ServerReceiveLockAcquired)
	if attempt > releasedAt.Sub(origin) || acquired < releasedAt.Sub(origin) {
		t.Fatalf("receive wait escaped lock interval: attempt=%v acquired=%v released=%v", attempt, acquired, releasedAt.Sub(origin))
	}
	runtimeTimingMissing(t, receipt.ServerTiming, ServerReceiveDone, ServerAcceptCheckLockAttempt)
	receipt.ServerTiming.Mark(ServerReceiveDone, origin)
	type accepted struct {
		call *Call
		err  error
	}
	admitted := make(chan accepted, 1)
	releasedAt = runtimeTimingLockBarrier(t, r, func() {
		go func() {
			call, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
			admitted <- accepted{call, err}
		}()
	})
	acceptedCall := <-admitted
	if acceptedCall.err != nil {
		t.Fatal(acceptedCall.err)
	}
	attempt = runtimeTimingStage(t, receipt.ServerTiming, ServerAcceptCheckLockAttempt)
	acquired = runtimeTimingStage(t, receipt.ServerTiming, ServerAcceptCheckLockAcquired)
	validated := runtimeTimingStage(t, receipt.ServerTiming, ServerInputValidateStart)
	if attempt > releasedAt.Sub(origin) || acquired < releasedAt.Sub(origin) || validated < acquired {
		t.Fatalf("accept wait leaked into validation: attempt=%v acquired=%v validation=%v released=%v", attempt, acquired, validated, releasedAt.Sub(origin))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := acceptedCall.call.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Response(receipt, outcomeCompleted, ""); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeTimingKeepsReachedStagesOnRejection(t *testing.T) {
	for _, rejection := range []string{"expired", "changed", "input", "stopped"} {
		t.Run(rejection, func(t *testing.T) {
			r, _, _ := routingRuntime(t, map[string]int32{"w1": 1})
			receipt, err := r.Receive(r.workloads["w1"].definition.Name, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			input := &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}}
			switch rejection {
			case "expired":
				receipt.Deadline = receipt.ReceivedAt
			case "changed":
				receipt.DefinitionID = "other-definition"
			case "input":
				input = &InferenceInput{}
			case "stopped":
				if err := r.StopAdmission(receipt.UID, "TimingStopped"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.Accept(&receipt, input); err == nil {
				t.Fatal("rejected request was accepted")
			}
			runtimeTimingStage(t, receipt.ServerTiming, ServerAcceptCheckDone)
			if rejection == "expired" || rejection == "changed" {
				runtimeTimingMissing(t, receipt.ServerTiming, ServerInputValidateStart, ServerInputValidateDone, ServerValidatedEventDone, ServerAcceptLockAttempt)
			} else {
				runtimeTimingStage(t, receipt.ServerTiming, ServerInputValidateDone)
				if rejection == "input" {
					runtimeTimingMissing(t, receipt.ServerTiming, ServerValidatedEventDone, ServerAcceptLockAttempt)
				} else {
					runtimeTimingStage(t, receipt.ServerTiming, ServerValidatedEventDone)
					runtimeTimingStage(t, receipt.ServerTiming, ServerAcceptLockAcquired)
				}
			}
			runtimeTimingMissing(t, receipt.ServerTiming, ServerAcceptDone, ServerExecuteStart, ServerReplyReceived)
			if err := r.Response(receipt, "rejected", rejection); err != nil {
				t.Fatal(err)
			}
			log := r.config.ObservationLog.(*runtimeTestLog)
			if err := log.Drain(context.Background()); err != nil {
				t.Fatal(err)
			}
			rows := readRequestRows(t, log.config.Directory)
			if len(rows) != 1 || rows[0].Kind != RequestRowFinal || rows[0].ServerTiming != receipt.ServerTiming {
				t.Fatalf("rejection lost reached timing: %+v", rows)
			}
		})
	}
}

func TestRuntimeTimingResultLockReplyAndCanceledFinal(t *testing.T) {
	for _, outcome := range []string{"success", "domain-error", "backend-error", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
			d := &Diagnostics{events: make([]diagnosticEvent, 256)}
			d.Start()
			r.mu.Recorder, r.config.Diagnostics = d, d
			defer d.Stop()
			started, finish := make(chan struct{}), make(chan struct{})
			r.instances[instance.ID].Execute = func(_ context.Context, dispatch Dispatch) (BackendResult, error) {
				result := BackendResult{Finished: true}
				result.ServerTiming.Mark(ServerWorkerHTTPStart, dispatch.ReceivedAt)
				close(started)
				<-finish
				result.ServerTiming.Mark(ServerWorkerHTTPDone, dispatch.ReceivedAt)
				switch outcome {
				case "domain-error":
					return result, failure("PreprocessFailed", "input rejected by native preprocessor")
				case "backend-error":
					return result, errors.New("transport failed")
				default:
					return result, nil
				}
			}
			receipt, err := r.Receive(r.workloads["w1"].definition.Name, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			call, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
			if err != nil {
				t.Fatal(err)
			}
			receipt.ServerTiming.Mark(ServerAcceptDone, receipt.ReceivedAt)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("backend did not start")
			}
			var responseFinishedBefore time.Time
			if outcome == "canceled" {
				canceled, stop := context.WithCancel(ctx)
				stop()
				result, err := call.Wait(canceled)
				if !hasRuntimeError(err, "RequestCanceled") {
					t.Fatalf("cancel outcome changed: %v", err)
				}
				runtimeTimingMissing(t, result.ServerTiming, ServerReplyReceived, ServerWorkerHTTPDone)
				if err := r.Response(receipt, "canceled", "RequestCanceled"); err != nil {
					t.Fatal(err)
				}
				responseFinishedBefore = time.Now()
			}
			releasedAt := runtimeTimingLockBarrier(t, r, func() { close(finish) })
			result, err := call.Wait(ctx)
			responseOutcome, responseCode := outcomeCompleted, ""
			switch outcome {
			case "domain-error":
				if !hasRuntimeError(err, "PreprocessFailed") {
					t.Fatalf("domain error changed: %v", err)
				}
				responseOutcome, responseCode = "error", "PreprocessFailed"
			case "backend-error":
				if !hasRuntimeError(err, "BackendUnavailable") {
					t.Fatalf("backend error classification changed: %v", err)
				}
				responseOutcome, responseCode = "error", "BackendUnavailable"
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			attempt := runtimeTimingStage(t, result.ServerTiming, ServerResultLockAttempt)
			acquired := runtimeTimingStage(t, result.ServerTiming, ServerResultLockAcquired)
			httpDone := runtimeTimingStage(t, result.ServerTiming, ServerWorkerHTTPDone)
			received := runtimeTimingStage(t, result.ServerTiming, ServerReplyReceived)
			if httpDone > attempt || attempt > releasedAt.Sub(receipt.ReceivedAt) || acquired < releasedAt.Sub(receipt.ReceivedAt) || received < acquired {
				t.Fatalf("result lock wait attributed to backend or reply: http=%v attempt=%v acquired=%v received=%v released=%v", httpDone, attempt, acquired, received, releasedAt.Sub(receipt.ReceivedAt))
			}
			runtimeTimingMissing(t, receipt.ServerTiming, ServerExecuteStart, ServerWorkerHTTPStart, ServerReplyReceived)
			if outcome != "canceled" {
				receipt.ServerTiming.Merge(result.ServerTiming)
				if err := r.Response(receipt, responseOutcome, responseCode); err != nil {
					t.Fatal(err)
				}
			}
			r.JoinExecutions()
			log := r.config.ObservationLog.(*runtimeTestLog)
			if err := log.Drain(ctx); err != nil {
				t.Fatal(err)
			}
			rows := readRequestRows(t, log.config.Directory)
			if len(rows) != 2 || rows[0].Kind != RequestRowAssigned || rows[1].Kind != RequestRowFinal {
				t.Fatalf("assignment/final boundary changed: %+v", rows)
			}
			assigned, final := rows[0], rows[1]
			runtimeTimingStage(t, assigned.ServerTiming, ServerExecuteStart)
			runtimeTimingMissing(t, assigned.ServerTiming, ServerAssignedEventDone, ServerWorkerHTTPStart, ServerResultLockAttempt, ServerReplyReceived)
			runtimeTimingStage(t, final.ServerTiming, ServerAssignedEventDone)
			runtimeTimingStage(t, final.ServerTiming, ServerResultLockAcquired)
			if outcome == "canceled" {
				if final.Outcome != "canceled" || final.Error != "RequestCanceled" {
					t.Fatalf("late completion replaced canceled final: %+v", final)
				}
				if final.ElapsedNanos == nil || *final.ElapsedNanos > responseFinishedBefore.Sub(receipt.ReceivedAt).Nanoseconds() || *final.ElapsedNanos > int64(attempt) {
					t.Fatalf("late backend settle shifted response elapsed: %+v", final)
				}
				runtimeTimingMissing(t, final.ServerTiming, ServerReplyReceived)
			} else {
				if final.Outcome != responseOutcome || final.Error != responseCode {
					t.Fatalf("reply error changed final outcome: %+v", final)
				}
				runtimeTimingStage(t, final.ServerTiming, ServerReplySent)
				runtimeTimingStage(t, final.ServerTiming, ServerReplyReceived)
			}
		})
	}
}

func TestRuntimeTimingQueuedDeadlineDoesNotInventExecution(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	r.instances[instance.ID].outstanding = r.instances[instance.ID].Capacity
	receipt, err := r.Receive(r.workloads["w1"].definition.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	call.receipt.Deadline = receipt.ReceivedAt
	r.dispatchLocked()
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := call.Wait(ctx)
	if !hasRuntimeError(err, "RequestTimeout") {
		t.Fatalf("queued deadline changed: %v", err)
	}
	runtimeTimingStage(t, result.ServerTiming, ServerReplyReceived)
	runtimeTimingMissing(t, result.ServerTiming, ServerExecuteStart, ServerAssignedEventDone, ServerWorkerHTTPStart, ServerResultLockAttempt)
	receipt.ServerTiming.Merge(result.ServerTiming)
	if err := r.Response(receipt, "timeout", "RequestTimeout"); err != nil {
		t.Fatal(err)
	}
	log := r.config.ObservationLog.(*runtimeTestLog)
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	rows := readRequestRows(t, log.config.Directory)
	if len(rows) != 1 || rows[0].Kind != RequestRowFinal {
		t.Fatalf("queued rejection created assignment: %+v", rows)
	}
	runtimeTimingStage(t, rows[0].ServerTiming, ServerReplySent)
	runtimeTimingMissing(t, rows[0].ServerTiming, ServerExecuteStart, ServerWorkerHTTPStart)
}

func TestRuntimeTimingRestoresOnlyAssignedStagesAndKeepsFinalClosed(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	r.instances[instance.ID].Execute = func(context.Context, Dispatch) (BackendResult, error) {
		return BackendResult{}, failure("BackendUnavailable", "physical completion is unknown")
	}
	receipt, err := r.Receive(r.workloads["w1"].definition.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := call.Wait(ctx); !hasRuntimeError(err, "BackendUnavailable") {
		t.Fatalf("unfinished exchange changed: %v", err)
	}
	r.JoinExecutions()
	log := r.config.ObservationLog.(*runtimeTestLog)
	if err := log.Close(ctx); err != nil {
		t.Fatal(err)
	}
	rows := readRequestRows(t, log.config.Directory)
	if len(rows) != 1 || rows[0].Kind != RequestRowAssigned {
		t.Fatalf("unfinished request lost assigned boundary: %+v", rows)
	}
	assigned := rows[0]
	reopened, err := newRuntimeTestLog(log.config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close(context.Background()) })
	restored, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 4, ObservationLog: reopened})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.JoinExecutions)
	pending := restored.calls[receipt.ID]
	if pending == nil || pending.receipt.ServerTiming != assigned.ServerTiming || !pending.responseRecorded || pending.backendDone {
		t.Fatalf("restore changed assigned timing or ownership: %+v", pending)
	}
	runtimeTimingStage(t, pending.receipt.ServerTiming, ServerExecuteStart)
	runtimeTimingMissing(t, pending.receipt.ServerTiming, ServerAssignedEventDone, ServerWorkerHTTPDone, ServerResultLockAttempt, ServerReplySent, ServerReplyReceived)
	if err := restored.ConfirmInstanceStopped(instance.ID); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	rows = readRequestRows(t, log.config.Directory)
	if len(rows) != 2 || rows[1].Kind != RequestRowFinal || rows[1].ServerTiming != assigned.ServerTiming || rows[1].Outcome != outcomeIncomplete {
		t.Fatalf("restart final fabricated or lost timing: %+v", rows)
	}
	final := rows[1]
	late := BackendProgress{
		RequestID: receipt.ID, WorkloadUID: receipt.UID, Definition: call.identity,
		Phase: phaseFinished, At: time.Now().UTC(), ExecutionComplete: true, Outcome: outcomeCompleted,
	}
	if err := restored.ObserveBackend(instance.ID, late); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	rows = readRequestRows(t, log.config.Directory)
	if len(rows) != 3 || !reflect.DeepEqual(rows[1], final) || rows[2].Kind != RequestRowNative {
		t.Fatalf("late native evidence reopened final: %+v", rows)
	}
}

func TestRuntimeTimingDoesNotCreateResponseBeforeRequestID(t *testing.T) {
	r, _, _ := routingRuntime(t, map[string]int32{"w1": 1})
	if err := r.Response(Receipt{}, "error", "ReceiveFailed"); err != nil {
		t.Fatal(err)
	}
	log := r.config.ObservationLog.(*runtimeTestLog)
	if err := log.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rows := readRequestRows(t, log.config.Directory); len(rows) != 0 {
		t.Fatalf("response without RequestID created server row: %+v", rows)
	}
	if err := log.Err(); err != nil {
		t.Fatalf("response without RequestID latched observation failure: %v", err)
	}
}
