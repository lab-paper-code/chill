package observation

import (
	"context"
	"errors"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

func TestScanSessionRowsSelectsReceiptHoursWithoutWideningCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log, clock := clockedObservationLog(t, ObservationLogConfig{OccupancyTail: 5 * time.Minute}, observationTestHour)
	runtime, err := newObservedRuntime(t, ctx, serving.RuntimeConfig{ObservationLog: log})
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "session-uid/2/"
	start := observationTestHour.Add(59*time.Minute + 59*time.Second)
	end := start.Add(2 * time.Second)
	// Keep an existing scheduler cache policy: the session must neither widen it
	// nor populate its decoded records, even across a receipt-hour boundary.
	log.retainWindow(start.Add(-time.Minute), start, false, start)
	for _, entry := range []struct {
		id      string
		uid     string
		receipt time.Time
		skew    time.Duration
	}{
		{"behind", prefix + "saturation", start, -30 * time.Second},
		{"ahead", prefix + "sparse", end, 30 * time.Second},
		{"unrelated", "other-session/2/sparse", start, 0},
		{"other-batch", "session-uid/4/sparse", start, 0},
	} {
		clock.set(entry.receipt)
		progress := serving.BackendProgress{RequestID: entry.id, WorkloadUID: entry.uid,
			Phase: "running", At: entry.receipt.Add(entry.skew), BatchID: 1, BatchSize: 2}
		if err := runtime.ObserveBackend("measurement-pod/container", progress); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	log.windowMu.Lock()
	windowSpan, occupancySpan, windowNow := log.windowSpan, log.occupancySpan, log.windowNow
	cached := log.windowRecords
	log.windowMu.Unlock()
	seen := map[string]serving.RequestRow{}
	if err := log.ScanSessionRows(ctx, prefix, start, end, func(row serving.RequestRow) error {
		seen[row.RequestID] = row
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen["behind"].NativeRunningAt == nil || seen["ahead"].NativeRunningAt == nil {
		t.Fatalf("session selection lost shifted native rows or included unrelated workloads: %+v", seen)
	}
	if !seen["behind"].NativeRunningAt.Equal(start.Add(-30*time.Second)) ||
		!seen["ahead"].NativeRunningAt.Equal(end.Add(30*time.Second)) {
		t.Fatalf("session scan changed native timestamps: %+v", seen)
	}
	log.windowMu.Lock()
	defer log.windowMu.Unlock()
	if log.windowSpan != windowSpan || log.occupancySpan != occupancySpan ||
		!log.windowNow.Equal(windowNow) || log.windowRecords != cached {
		t.Fatalf("session scan changed writer cache: window=%v occupancy=%v now=%v records=%d",
			log.windowSpan, log.occupancySpan, log.windowNow, log.windowRecords)
	}
}

func TestScanSessionRowsPropagatesYieldError(t *testing.T) {
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	row := serving.RequestRow{Kind: serving.RequestRowNative, RequestID: "request", WorkloadUID: "session/1/sparse"}
	if err := log.submitRecord(observationRequestStream, row, nil, observationTestHour); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("stop session scan")
	err := log.ScanSessionRows(context.Background(), "session/", observationTestHour, observationTestHour.Add(time.Second),
		func(serving.RequestRow) error { return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("yield error = %v, want %v", err, stop)
	}
}
