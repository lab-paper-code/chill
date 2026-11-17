package profilederivation

import (
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func TestSaturationDispatchUsesActualBackloggedBatchService(t *testing.T) {
	start := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	samples := []NativeServiceSample{
		{BatchID: 1, BatchSize: 2, RecordIDs: []string{"a", "b"}, ServiceSample: firstfloor.ServiceSample{TrialID: "pod", ServiceDurationNanos: 3e6},
			StartedAt: start, FinishedAt: start.Add(3 * time.Millisecond)},
		{BatchID: 2, BatchSize: 2, RecordIDs: []string{"c", "d"}, ServiceSample: firstfloor.ServiceSample{TrialID: "pod", ServiceDurationNanos: 9e6},
			StartedAt: start.Add(5 * time.Millisecond), FinishedAt: start.Add(14 * time.Millisecond), LatestEnqueuedAt: start.Add(2 * time.Millisecond)},
	}
	occupancy := SaturationDispatchOccupancy(samples, 2)
	if len(occupancy) != 1 || occupancy[0] != 1e6 {
		t.Fatalf("d=%v, want (5ms-3ms)/2 = 1ms per input", occupancy)
	}
	if got := float64(samples[0].ServiceDurationNanos) + 2*occupancy[0]; got != 5e6 {
		t.Fatalf("cycle=S+B*d=%g, want 5ms", got)
	}
	samples[1].LatestEnqueuedAt = start.Add(4 * time.Millisecond)
	if occupancy := SaturationDispatchOccupancy(samples, 2); len(occupancy) != 0 {
		t.Fatalf("non-backlogged pair entered d: %+v", occupancy)
	}
}
