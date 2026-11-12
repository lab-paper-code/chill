package serving

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

// These assertions use row JSON so the same regression exercises the old writer
// without referring to the newly added evidence types.
func admissionFinalJSON(t *testing.T, r *Runtime, id string) map[string]any {
	t.Helper()
	log := r.config.ObservationLog.(*runtimeTestLog)
	if err := log.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, row := range readRequestRows(t, log.config.Directory) {
		if row.RequestID == id && row.Kind == RequestRowFinal {
			return requestRowJSON(t, row)
		}
	}
	t.Fatalf("missing final row for %s", id)
	return nil
}

func TestAdmissionRecordPredictionSurvivesExecutorRejection(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	i := r.instances[instance.ID]
	i.Capacity = 4
	i.State.Native.At = time.Now().UTC()
	i.State.Native.Metadata.Policy.DecisionID = "ack-policy"
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	firstID := ""
	i.Execute = func(ctx context.Context, d Dispatch) (BackendResult, error) {
		if d.RequestID == firstID {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return BackendResult{Finished: true}, nil
		}
		return BackendResult{Finished: true}, &Error{Code: CodeDeadlineInfeasible, Stage: AdmissionStageExecutor, Policy: AdmissionPolicyReject}
	}
	// Hold one real unobserved assignment while admitting the second request.
	receipt, err := r.Receive(r.workloads["w1"].definition.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	firstID = receipt.ID
	receipt.Deadline = time.Now().Add(time.Second)
	if _, err = r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}}); err != nil {
		t.Fatal(err)
	}
	c, second := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
	_, err = c.Wait(context.Background())
	var rejection *Error
	if !errors.As(err, &rejection) || rejection.Stage != AdmissionStageExecutor {
		t.Fatalf("expected executor rejection, got %v", err)
	}
	if err = r.Response(second, "rejected", CodeDeadlineInfeasible); err != nil {
		t.Fatal(err)
	}
	row := admissionFinalJSON(t, r, second.ID)
	prediction, ok := row["admissionPrediction"].(map[string]any)
	if !ok || prediction["predictedCompletionNanos"] == nil || prediction["predictedCompletionNanos"].(float64) <= 0 || prediction["remainingSLONanos"] == nil {
		t.Fatalf("admitted prediction missing from rejected final: %+v", row)
	}
	input := prediction["input"].(map[string]any)
	policy := input["ackPolicy"].(map[string]any)
	if input["instance"] != instance.ID || input["profileID"] != i.Profile.ID || input["observedAt"] == nil || input["nativeAt"] == nil || input["snapshotAgeNanos"] == nil || input["unobservedAssignments"] != float64(1) || policy["decisionID"] != "ack-policy" {
		t.Fatalf("prediction lost exact admission input: %+v", input)
	}
}

func TestAdmissionRecordEveryProducibleWaitReason(t *testing.T) {
	for _, reason := range []string{"PreIPCRunning", "ProfileMismatch", "MissingPolicyOrBatch", "ResidualUnsupported", "MissingObservedAt", "NoCapacity", "AdmissionStopped"} {
		t.Run(reason, func(t *testing.T) {
			r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
			i := r.instances[instance.ID]
			i.State.Native.At = time.Now().UTC()
			i.State.Native.Metadata.Policy.DecisionID = "observed-ack"
			w := r.workloads["w1"]
			q := admissionCallState(&Call{receipt: Receipt{ID: "running", UID: "w1", ReceivedAt: time.Now()}, definition: w.definition, identity: w.identity}, time.Now())
			q.Phase, q.BatchID, q.BatchSize = "running", 12, 1
			switch reason {
			case "PreIPCRunning":
				i.State.Native.Requests = []firstfloor.RequestState{q}
			case "ProfileMismatch":
				i.State.Native.Metadata.Policy.ProfileID = "other-profile"
			case "MissingPolicyOrBatch":
				i.State.Native = nil
			case "ResidualUnsupported":
				elapsed := int64(time.Hour)
				q.ExchangeElapsedNanos = &elapsed
				i.State.Native.Requests = []firstfloor.RequestState{q}
			case "MissingObservedAt":
				i.State.ObservedAt = time.Time{}
			case "NoCapacity":
				i.Capacity = 0
			case "AdmissionStopped":
				i.State.Stopped = true
			}
			c, receipt := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
			if c.assignment != nil {
				t.Fatal("recording changed unknown/capacity behavior")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, _ = c.Wait(ctx)
			if err := r.Response(receipt, "timeout", "RequestTimeout"); err != nil {
				t.Fatal(err)
			}
			row := admissionFinalJSON(t, r, receipt.ID)
			waits, ok := row["admissionWaits"].([]any)
			if !ok || len(waits) == 0 {
				t.Fatalf("wait subreason missing: %+v", row)
			}
			wait := waits[0].(map[string]any)
			input := wait["input"].(map[string]any)
			if wait["reason"] != reason || wait["at"] == nil || wait["elapsedNanos"] == nil || input["instance"] != instance.ID || input["capacity"] == nil || input["outstanding"] == nil {
				t.Fatalf("wrong wait provenance: %+v", wait)
			}
			if reason == "PreIPCRunning" {
				running := input["running"].([]any)[0].(map[string]any)
				if running["batchID"] != float64(12) || running["batchSize"] != float64(1) || running["exchangeElapsedNanos"] != nil || input["observedAt"] == nil {
					t.Fatalf("pre-IPC evidence lost: %+v", input)
				}
			}
		})
	}
}

func TestAdmissionRecordWorkloadStoppedBeforeAcceptance(t *testing.T) {
	r, _, _ := routingRuntime(t, map[string]int32{"w1": 1})
	if err := r.StopAdmission("w1", "retirement"); err != nil {
		t.Fatal(err)
	}
	receipt, err := r.Receive(r.workloads["w1"].definition.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err == nil {
		t.Fatal("stopped admission accepted")
	}
	if err = r.Response(receipt, "error", "retirement"); err != nil {
		t.Fatal(err)
	}
	row := admissionFinalJSON(t, r, receipt.ID)
	waits, ok := row["admissionWaits"].([]any)
	if !ok || len(waits) != 1 || waits[0].(map[string]any)["reason"] != "AdmissionStopped" || waits[0].(map[string]any)["detail"] != "retirement" {
		t.Fatalf("stopped decision missing: %+v", row)
	}
}

func TestAdmissionRecordDeferredTransition(t *testing.T) {
	r, _, _ := routingRuntime(t, map[string]int32{"w1": 1})
	d := *r.workloads["w1"].definition
	d.Generation++
	d.Admission.Policy = AdmissionPolicyDelay
	if err := r.Publish(&d); err != nil {
		t.Fatal(err)
	}
	c, receipt := admissionRuntimeCall(t, r, "w1", time.Now().Add(-time.Second), time.Now().Add(time.Second))
	if _, err := c.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Response(receipt, "completed", ""); err != nil {
		t.Fatal(err)
	}
	row := admissionFinalJSON(t, r, receipt.ID)
	if row["deferredNanos"] == nil || row["deferredNanos"].(float64) <= 0 {
		t.Fatalf("normal→deferred timestamp missing: %+v", row)
	}
	found := false
	for _, value := range row["admissionWaits"].([]any) {
		wait := value.(map[string]any)
		found = found || wait["state"] == "deferred" && wait["elapsedNanos"] == row["deferredNanos"] && wait["input"] != nil
	}
	if !found {
		t.Fatalf("deferred transition reason missing: %+v", row)
	}
}

func TestAdmissionRecordNativeTimingSurvivesLateProgress(t *testing.T) {
	at := time.Now().UTC()
	finished, write := int64(8), int64(10)
	row := RequestRow{ReceivedAt: at}
	row.FoldNative(&BackendProgress{Phase: "finished", NativeElapsedMS: 1,
		NativeHTTP: &firstfloor.HTTPRequestTiming{HandlerEnteredAt: at, FinishedNanos: &finished, ResponseWriteStartNanos: &write}})
	row.FoldNative(&BackendProgress{Phase: "finished", NativeElapsedMS: 1,
		NativeHTTP: &firstfloor.HTTPRequestTiming{HandlerEnteredAt: at, FinishedNanos: &finished}})
	if row.NativeHTTP.ResponseWriteStartNanos == nil || *row.NativeHTTP.ResponseWriteStartNanos != write {
		t.Fatal("late progress erased response boundary")
	}
	row.Fold(&RequestEvent{NativeHTTP: &firstfloor.HTTPRequestTiming{HandlerEnteredAt: at}})
	if row.NativeElapsedMS == nil || *row.NativeElapsedMS != 1 {
		t.Fatal("timing-only failure invented native execution metrics")
	}
}

func TestAdmissionRecordTransportAndNativeDeferredOnFinalRow(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	r.instances[instance.ID].Execute = func(_ context.Context, d Dispatch) (BackendResult, error) {
		var timing RequestTimings
		for stage, offset := range map[ServerStage]time.Duration{
			ServerWorkerHTTPStart: 10, ServerWorkerHTTPGetConn: 12, ServerWorkerHTTPGotConn: 24,
			ServerWorkerHTTPConnectStart: 14, ServerWorkerHTTPConnectDone: 20,
			ServerWorkerHTTPWroteRequest: 30, ServerWorkerHTTPFirstResponseByte: 80, ServerWorkerHTTPDone: 100,
		} {
			timing.Record(stage, offset)
		}
		received, decodeStart, decoded, enqueued, finished, write := int64(2), int64(3), int64(5), int64(6), int64(42), int64(46)
		deferred := d.ReceivedAt.Add(time.Microsecond).UTC()
		return BackendResult{Finished: true, ClientSpan: 90, ServerTiming: timing,
			Progress: &BackendProgress{Phase: "finished", NativeElapsedMS: .000040, NativeDeferredAt: &deferred,
				NativeHTTP: &firstfloor.HTTPRequestTiming{HandlerEnteredAt: d.ReceivedAt.UTC(), ReceivedNanos: &received,
					DecodeStartNanos: &decodeStart, DecodeDoneNanos: &decoded, EnqueueNanos: &enqueued,
					FinishedNanos: &finished, ResponseWriteStartNanos: &write}}}, nil
	}
	c, receipt := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
	if _, err := c.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Response(receipt, "completed", ""); err != nil {
		t.Fatal(err)
	}
	row := admissionFinalJSON(t, r, receipt.ID)
	native := row["nativeHTTP"].(map[string]any)
	for _, key := range []string{"handlerEnteredAt", "receivedNanos", "decodeStartNanos", "decodeDoneNanos", "enqueueNanos", "finishedNanos", "responseWriteStartNanos"} {
		if native[key] == nil {
			t.Fatalf("native transport boundary %s missing: %+v", key, row)
		}
	}
	if row["nativeDeferredAt"] == nil {
		t.Fatal("native normal→deferred timestamp missing")
	}
	gateway := map[string]float64{}
	for _, value := range row["serverTiming"].([]any) {
		point := value.(map[string]any)
		gateway[point["stage"].(string)] = point["elapsedNanos"].(float64)
	}
	var sum float64
	stages := []string{"workerHTTPStart", "workerHTTPGetConn", "workerHTTPGotConn", "workerHTTPWroteRequest", "workerHTTPFirstResponseByte", "workerHTTPDone"}
	for j := 1; j < len(stages); j++ {
		if _, ok := gateway[stages[j]]; !ok {
			t.Fatalf("gateway transport boundary %s missing", stages[j])
		}
		sum += gateway[stages[j]] - gateway[stages[j-1]]
	}
	if sum != row["clientSpanNanos"] {
		t.Fatalf("exclusive transport segments %g do not sum to exchange %v", sum, row["clientSpanNanos"])
	}
}

func TestAdmissionTimingOnlyFailureAllowsVerifiedAbsentDrain(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	i := r.instances[instance.ID]
	write := int64(17)
	i.Execute = func(_ context.Context, d Dispatch) (BackendResult, error) {
		return BackendResult{NativeHTTP: &firstfloor.HTTPRequestTiming{
				HandlerEnteredAt: d.ReceivedAt.UTC(), ResponseWriteStartNanos: &write}},
			errors.New("unproved native error")
	}
	c, receipt := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
	if _, err := c.Wait(context.Background()); err == nil {
		t.Fatal("native error disappeared")
	}
	if err := r.Response(receipt, "error", "BackendUnavailable"); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	if c.progress != nil || c.backendDone || i.outstanding != 1 {
		r.mu.Unlock()
		t.Fatal("timing diagnostics became physical progress or completion proof")
	}
	i.State.Native.Requests = nil
	i.State.NativeVerified, i.State.ObservedAt = true, time.Now()
	r.finishAbsentLocked(i.State)
	if err := r.unlock(); err != nil {
		t.Fatal(err)
	}
	if !c.backendDone || i.outstanding != 0 || r.calls[receipt.ID] != nil {
		t.Fatal("verified absence did not release outstanding occupancy")
	}
	row := admissionFinalJSON(t, r, receipt.ID)
	if row["nativeHTTP"] == nil || row["nativeElapsedMS"] != nil {
		t.Fatalf("diagnostic-only final lost timing or invented native execution: %+v", row)
	}
}
