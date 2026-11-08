package serving

import (
	"math"
	"testing"
	"time"
)

func requestPathRow() RequestRow {
	// Worker absolute time intentionally differs from server receipt by a day.
	worker := time.Unix(86400, 0)
	queued, running, finished := worker, worker.Add(3*time.Millisecond), worker.Add(8*time.Millisecond)
	elapsed, accepted, dispatched := int64(30*time.Millisecond), int64(2*time.Millisecond), int64(7*time.Millisecond)
	native := 10.0
	row := RequestRow{
		Kind: RequestRowFinal, Outcome: outcomeCompleted, NativeOutcome: outcomeCompleted,
		ExecutionComplete: true, NativeExecutionComplete: true,
		ReceivedAt: time.Unix(0, 0), ElapsedNanos: &elapsed, AcceptedNanos: &accepted, DispatchedNanos: &dispatched,
		NativeQueuedAt: &queued, NativeRunningAt: &running, NativeFinishedAt: &finished,
		NativeElapsedMS: &native, QueueMS: 5,
	}
	row.ServerTiming.Record(ServerSenderFreeze, 12*time.Millisecond)
	return row
}

func TestRequestPathSampleUsesDirectWorkerSpans(t *testing.T) {
	row := requestPathRow()
	got, reason := RequestPathSample(&row)
	want := RequestPath{TotalNanos: int64(17 * time.Millisecond), PostNanos: int64(8 * time.Millisecond), IngressNanos: int64(2 * time.Millisecond)}
	if reason != "" || got != want {
		t.Fatalf("path=%+v reason=%q, want %+v", got, reason, want)
	}
	// More assignment persistence raises E and F equally: post cost stays fixed.
	*row.ElapsedNanos += int64(20 * time.Millisecond)
	row.ServerTiming = RequestTimings{}
	row.ServerTiming.Record(ServerSenderFreeze, 32*time.Millisecond)
	got, reason = RequestPathSample(&row)
	if reason != "" || got.PostNanos != want.PostNanos || got.TotalNanos != want.TotalNanos+int64(20*time.Millisecond) {
		t.Fatalf("assignment persistence leaked into post path: %+v %q", got, reason)
	}
	row.NativeQueuedAt, row.NativeRunningAt = nil, nil
	row.QueuedNanos, row.RunningNanos = nil, nil
	row.finished = &BackendProgress{BatchID: 1, At: *row.NativeFinishedAt, NativeElapsedMS: *row.NativeElapsedMS, QueueMS: row.QueueMS}
	row.FillNativeStages()
	if row.QueuedNanos == nil || row.RunningNanos == nil {
		t.Fatal("test did not reconstruct native offsets")
	}
	if _, reason = RequestPathSample(&row); reason != requestPathMissingNativeTiming {
		t.Fatalf("reconstructed offsets admitted as native evidence: %q", reason)
	}
}

func TestRequestPathSamplePreservesZeroAndExcludesUnusable(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*RequestRow)
		reason string
	}{
		{"missing-server", func(r *RequestRow) { r.ElapsedNanos = nil }, "missing_server_timing"},
		{"missing-freeze", func(r *RequestRow) { r.ServerTiming = RequestTimings{} }, "missing_server_timing"},
		{"missing-native", func(r *RequestRow) { r.NativeElapsedMS = nil }, "missing_native_timing"},
		{"nonfinite", func(r *RequestRow) { r.QueueMS = math.NaN() }, "nonfinite"},
		{"infinite", func(r *RequestRow) { *r.NativeElapsedMS = math.Inf(1) }, "nonfinite"},
		{"range", func(r *RequestRow) { *r.NativeElapsedMS = math.MaxFloat64 }, "out_of_range"},
		{"inverted-worker", func(r *RequestRow) { r.NativeRunningAt = r.NativeFinishedAt; r.NativeFinishedAt = r.NativeQueuedAt }, "inverted"},
		{"negative-ingress", func(r *RequestRow) { r.QueueMS = 1 }, "inverted"},
		{"negative-post", func(r *RequestRow) { *r.ElapsedNanos = int64(15 * time.Millisecond) }, "inverted"},
		{"native-failure", func(r *RequestRow) { r.NativeOutcome = "batch_error" }, "native_failed"},
		{"native-error", func(r *RequestRow) { r.NativeError = "runner failed" }, "native_failed"},
		{"native-incomplete", func(r *RequestRow) { r.NativeExecutionComplete = false }, "incomplete"},
		{"response-failed", func(r *RequestRow) { r.Outcome = runtimeTimingCanceled }, "failed"},
		{"response-incomplete", func(r *RequestRow) { r.ExecutionComplete = false }, "incomplete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := requestPathRow()
			test.change(&row)
			if got, reason := RequestPathSample(&row); reason != test.reason || got != (RequestPath{}) {
				t.Fatalf("path=%+v reason=%q, want %q", got, reason, test.reason)
			}
		})
	}
	row := requestPathRow()
	*row.ElapsedNanos, *row.AcceptedNanos, *row.DispatchedNanos, *row.NativeElapsedMS, row.QueueMS = 0, 0, 0, 0, 0
	row.NativeRunningAt, row.NativeFinishedAt = row.NativeQueuedAt, row.NativeQueuedAt
	row.ServerTiming = RequestTimings{}
	row.ServerTiming.Record(ServerSenderFreeze, 0)
	if got, reason := RequestPathSample(&row); reason != "" || got != (RequestPath{}) {
		t.Fatalf("observed zero rejected: %+v %q", got, reason)
	}
}
