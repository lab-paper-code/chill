package serving

import (
	"math"
	"time"
)

// RequestPath separates server path costs from replay queueing and native work.
type RequestPath struct {
	TotalNanos   int64
	PostNanos    int64
	IngressNanos int64
}

// RequestPathSample uses server elapsed offsets and direct worker-clock spans only.
// A nonempty reason excludes this row from completed path samples, never serving.
func RequestPathSample(row *RequestRow) (RequestPath, string) {
	freeze, reason := requestPathTiming(row)
	if reason != "" {
		return RequestPath{}, reason
	}
	return requestPathArithmetic(row, freeze)
}

// requestPathTiming checks eligibility and the presence of direct clock evidence.
func requestPathTiming(row *RequestRow) (time.Duration, string) {
	if row == nil {
		return 0, "missing_row"
	}
	if row.Error != "" || (row.Outcome != "" && row.Outcome != outcomeCompleted && row.Outcome != outcomeIncomplete) {
		return 0, "failed"
	}
	if row.NativeError != "" || (row.NativeOutcome != "" && row.NativeOutcome != outcomeCompleted) {
		return 0, "native_failed"
	}
	if !row.ExecutionComplete || !row.NativeExecutionComplete || row.Outcome == outcomeIncomplete {
		return 0, "incomplete"
	}
	if row.Outcome == "" || row.NativeOutcome == "" {
		return 0, "missing_outcome"
	}
	freeze, frozen := row.ServerTiming.Elapsed(ServerSenderFreeze)
	if row.ElapsedNanos == nil || row.AcceptedNanos == nil || row.DispatchedNanos == nil || !frozen {
		return 0, "missing_server_timing"
	}
	// Reconstructed QueuedNanos/RunningNanos cannot establish worker ingress.
	if row.NativeQueuedAt == nil || row.NativeRunningAt == nil || row.NativeFinishedAt == nil || row.NativeElapsedMS == nil {
		return 0, requestPathMissingNativeTiming
	}
	if row.NativeQueuedAt.IsZero() || row.NativeRunningAt.IsZero() || row.NativeFinishedAt.IsZero() {
		return 0, requestPathMissingNativeTiming
	}
	return freeze, ""
}

const requestPathMissingNativeTiming = "missing_native_timing"

// requestPathArithmetic derives spans after timing evidence has been checked.
func requestPathArithmetic(row *RequestRow, freeze time.Duration) (RequestPath, string) {
	if math.IsNaN(row.QueueMS) || math.IsInf(row.QueueMS, 0) || math.IsNaN(*row.NativeElapsedMS) || math.IsInf(*row.NativeElapsedMS, 0) {
		return RequestPath{}, "nonfinite"
	}
	queue := math.Round(row.QueueMS * float64(time.Millisecond))
	native := math.Round(*row.NativeElapsedMS * float64(time.Millisecond))
	if queue >= 0x1p63 || native >= 0x1p63 {
		return RequestPath{}, "out_of_range"
	}
	elapsed, accepted, dispatched := *row.ElapsedNanos, *row.AcceptedNanos, *row.DispatchedNanos
	if accepted < 0 || dispatched < accepted || int64(freeze) < dispatched || elapsed < int64(freeze) || row.QueueMS < 0 || *row.NativeElapsedMS < row.QueueMS || row.NativeRunningAt.Before(*row.NativeQueuedAt) || row.NativeFinishedAt.Before(*row.NativeRunningAt) {
		return RequestPath{}, "inverted"
	}
	ingress := int64(queue) - row.NativeRunningAt.Sub(*row.NativeQueuedAt).Nanoseconds()
	post := elapsed - int64(freeze) - int64(native)
	total := elapsed - (dispatched - accepted) - int64(native)
	if ingress < 0 || post < 0 {
		return RequestPath{}, "inverted"
	}
	if ingress > math.MaxInt64-total {
		return RequestPath{}, "out_of_range"
	}
	return RequestPath{TotalNanos: total + ingress, PostNanos: post, IngressNanos: ingress}, ""
}
