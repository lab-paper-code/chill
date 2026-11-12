package firstfloor

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNativeHTTPHandlerTimingAndDeferredPersistence(t *testing.T) {
	s, _ := startTestActor(t, modeServing, "0", withDeadline(DeadlineDrop))
	h := NewHandler(s)
	for _, tc := range []struct {
		id, input string
		target    float64
		status    int
	}{{"deferred", "sample", 1, http.StatusOK}, {"failure", "raise", 1000, http.StatusInternalServerError}} {
		t.Run(tc.id, func(t *testing.T) {
			before := time.Now()
			reply := postJSON(h, "/v2/models/synthetic/infer", Request{ID: tc.id,
				Context: RequestContext{RequestID: tc.id, WorkloadUID: "uid", Definition: "uid/1", AdmissionPolicy: AdmissionDelay,
					ReceivedAt: before.Add(time.Hour).UTC(), SenderElapsedMS: 10, SLO: SLO{Percentile: 95, TargetMS: tc.target}}, Inputs: rawInputs(tc.input)})
			after := time.Now()
			if reply.Code != tc.status {
				t.Fatalf("status %d: %s", reply.Code, reply.Body.String())
			}
			var p Progress
			if tc.status == http.StatusOK {
				var response Response
				if err := json.Unmarshal(reply.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				p = response.Progress
			} else {
				var failure ErrorResponse
				if err := json.Unmarshal(reply.Body.Bytes(), &failure); err != nil || failure.Progress == nil {
					t.Fatalf("missing failure progress: %s, %v", reply.Body.String(), err)
				}
				p = *failure.Progress
			}
			assertNativeHTTPBoundaries(t, p, before, after)
			if tc.id == "deferred" {
				if p.NativeDeferredAt == nil || p.NativeDeferredAt.Before(before) || p.NativeDeferredAt.After(p.At) {
					t.Fatalf("normal to deferred timestamp lost at completion: %+v", p)
				}
			} else if p.NativeDeferredAt != nil {
				t.Fatal("feasible failure invented a deferred transition")
			}
		})
	}
}

func assertNativeHTTPBoundaries(t *testing.T, p Progress, before, after time.Time) {
	t.Helper()
	timing := p.NativeHTTP
	if timing == nil || timing.HandlerEnteredAt.Before(before) || timing.HandlerEnteredAt.After(after) || timing.HandlerEnteredAt.Location() != time.UTC {
		t.Fatalf("missing native handler UTC anchor: %+v", timing)
	}
	previous := int64(0)
	var total int64
	for _, boundary := range []*int64{timing.ReceivedNanos, timing.DecodeStartNanos, timing.DecodeDoneNanos, timing.EnqueueNanos, timing.FinishedNanos, timing.ResponseWriteStartNanos} {
		if boundary == nil || *boundary < previous {
			t.Fatalf("native local boundaries absent or out of order: %+v", timing)
		}
		total += *boundary - previous
		previous = *boundary
	}
	nativeElapsed := *timing.FinishedNanos - *timing.ReceivedNanos
	if total != *timing.ResponseWriteStartNanos || p.NativeElapsedMS != milliseconds(time.Duration(nativeElapsed)) {
		t.Fatalf("native local pieces changed elapsed semantics: %+v", p)
	}
	// Decode and enqueue are nested inside NativeElapsedMS. Add only the
	// handler prefix and finished-to-write suffix to that existing native span.
	prefix, suffix := *timing.ReceivedNanos, *timing.ResponseWriteStartNanos-*timing.FinishedNanos
	if prefix+nativeElapsed+suffix != *timing.ResponseWriteStartNanos {
		t.Fatal("native elapsed decomposition overlaps handler-local pieces")
	}
	if p.ExecutorWaitNanos == nil || *p.ExecutorWaitNanos > *timing.FinishedNanos-*timing.EnqueueNanos {
		t.Fatalf("executor wait escaped native enqueue to finish: %+v", p)
	}
}

func TestNativeHTTPDecodeFailureRetainsReachedBoundaries(t *testing.T) {
	s, _ := startTestActor(t, modeServing, "0", nil)
	before := time.Now()
	reply := httptest.NewRecorder()
	NewHandler(s).ServeHTTP(reply, httptest.NewRequest(http.MethodPost, "/v2/models/synthetic/infer", bytes.NewBufferString(`{"id":`)))
	var failure ErrorResponse
	if err := json.Unmarshal(reply.Body.Bytes(), &failure); err != nil || reply.Code != http.StatusBadRequest || failure.Progress != nil {
		t.Fatalf("decode failure changed proof: %d %s, %v", reply.Code, reply.Body.String(), err)
	}
	timing := failure.NativeHTTP
	if timing == nil || timing.HandlerEnteredAt.Before(before) || timing.DecodeStartNanos == nil || timing.DecodeDoneNanos == nil || timing.ResponseWriteStartNanos == nil ||
		*timing.DecodeStartNanos > *timing.DecodeDoneNanos || *timing.DecodeDoneNanos > *timing.ResponseWriteStartNanos || timing.EnqueueNanos != nil || timing.FinishedNanos != nil {
		t.Fatalf("decode failure lost reached boundaries or invented enqueue: %+v", timing)
	}
}

func TestNativeHTTPProgressSnapshotsKeepFirstDeferredAt(t *testing.T) {
	entered := time.Now()
	decodeStart, decodeDone := int64(1), int64(2)
	r := &request{received: entered.Add(time.Nanosecond), handlerEntered: entered, enqueued: entered.Add(3 * time.Nanosecond), deferredAt: entered.Add(4 * time.Nanosecond),
		httpTiming: &HTTPRequestTiming{HandlerEnteredAt: entered.UTC(), DecodeStartNanos: &decodeStart, DecodeDoneNanos: &decodeDone}}
	a := schedulerActor{cfg: &Config{Policy: testPolicy()}}
	deferred := a.progress(r, phaseDeferred, entered.Add(5*time.Nanosecond), runnerOutput{}, false)
	if deferred.NativeDeferredAt == nil || deferred.NativeHTTP == nil {
		t.Fatalf("deferred progress lacks native boundary snapshot: %+v", deferred)
	}
	for _, phase := range []string{phaseRunning, phaseFinished} {
		p := a.progress(r, phase, entered.Add(10*time.Nanosecond), runnerOutput{}, phase == phaseFinished)
		if p.NativeDeferredAt == nil || !p.NativeDeferredAt.Equal(*deferred.NativeDeferredAt) || p.NativeHTTP == nil || p.NativeHTTP.EnqueueNanos == nil || *p.NativeHTTP.EnqueueNanos != 3 {
			t.Fatalf("%s lost first deferred or enqueue boundary: %+v", phase, p)
		}
		if phase == phaseFinished && (p.NativeHTTP.FinishedNanos == nil || *p.NativeHTTP.FinishedNanos != 10 || p.NativeElapsedMS != milliseconds(9*time.Nanosecond)) {
			t.Fatalf("finished boundary replaced native elapsed origin: %+v", p)
		}
	}
	if deferred.NativeHTTP.FinishedNanos != nil || deferred.NativeHTTP.ResponseWriteStartNanos != nil {
		t.Fatal("later progress mutated earlier snapshot")
	}
}
