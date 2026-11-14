package serving

import (
	"strconv"
	"testing"
	"time"
	"unsafe"
)

func BenchmarkNativeBatchJoinMemberMemory(b *testing.B) {
	const requests = 15_000 * 16
	queued := time.Unix(1000, 0)
	running, finished := queued.Add(time.Millisecond), queued.Add(2*time.Millisecond)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		join := NewNativeBatchJoin()
		for i := range requests {
			join.Add(RequestRow{Kind: RequestRowNative, RequestID: strconv.Itoa(i), BatchID: uint64(i/16 + 1),
				BatchSize: 16, NativeQueuedAt: &queued, NativeRunningAt: &running, NativeFinishedAt: &finished,
				ServiceDurationNanos: time.Millisecond.Nanoseconds()})
		}
		if got := join.CompletedInputs(); got != requests {
			b.Fatalf("complete inputs %d of %d", got, requests)
		}
	}
	b.ReportMetric(float64(unsafe.Sizeof(occupancyRequest{})), "request-struct-B")
	// The lower bound excludes request ID backing bytes and map bucket overhead.
	minimum := unsafe.Sizeof(occupancyRequest{}) + unsafe.Sizeof("") + unsafe.Sizeof((*occupancyRequest)(nil))
	b.ReportMetric(float64(minimum), "min-retained-B/request")
}
