package observation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

type stalledWindowContext struct {
	context.Context
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *stalledWindowContext) Err() error {
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.Context.Err()
}

func TestColdWindowScanDoesNotBlockWriter(t *testing.T) {
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	at := observationTestHour
	ctx := context.Background()
	if err := log.submitRecord(observationRequestStream, serving.RequestRow{Kind: serving.RequestRowFinal, RequestID: "first", ReceivedAt: at}, nil, at); err != nil {
		t.Fatal(err)
	}
	read, err := log.prepare(ctx, map[string][2]time.Time{observationRequestStream: {at, at.Add(time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	defer read.close()
	log.retainWindow(at, at.Add(time.Minute), false, read.now)
	read.cacheWindow = true
	stalled := &stalledWindowContext{Context: ctx, entered: make(chan struct{}), release: make(chan struct{})}
	read.ctx = stalled
	scanned := make(chan error, 1)
	go func() {
		scanned <- log.scanWindowRows(read, observationRequestStream, func(windowRecord) error { return nil })
	}()
	resume := sync.OnceFunc(func() { close(stalled.release) })
	defer func() {
		resume()
		if err := <-scanned; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-stalled.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("cold scan did not enter its reader")
	}
	if err := log.submitRecord(observationRequestStream, serving.RequestRow{Kind: serving.RequestRowFinal, RequestID: "second", ReceivedAt: at}, nil, at); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := log.Drain(deadline); err != nil {
		t.Fatalf("cold decode blocked FIFO writer/barrier: %v", err)
	}
}

func TestOccupancyLimitStopsBeforeColdCachePopulation(t *testing.T) {
	for _, corruptTail := range []bool{false, true} {
		t.Run(fmt.Sprint(corruptTail), func(t *testing.T) {
			log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
			var data bytes.Buffer
			encoder := json.NewEncoder(&data)
			for i := 1; i <= 100; i++ {
				if err := encoder.Encode(occupancyScanRow(observationTestHour, uint64(i))); err != nil {
					t.Fatal(err)
				}
			}
			if corruptTail {
				data.WriteString("not JSON\n")
			}
			name := log.segment(observationRequestStream, observationTestHour)
			if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(name, data.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if err := log.indexSegments([]time.Time{observationTestHour}); err != nil {
				t.Fatal(err)
			}
			visited := 0
			_, err := log.ScanOccupancyRowsBounded(context.Background(), "", "", observationTestHour, observationTestHour.Add(time.Second), 1, func(serving.RequestRow) { visited++ })
			var failure *serving.Error
			if !errors.As(err, &failure) || failure.Code != "OccupancySelectionLimit" || visited != 0 {
				t.Fatalf("selection did not stop at the limit: %v visited=%d", err, visited)
			}
			log.windowMu.Lock()
			cached := 0
			for _, segment := range log.windowSegments {
				cached += len(segment.records)
			}
			log.windowMu.Unlock()
			if cached != 0 {
				t.Fatalf("refused occupancy scan populated %d cached records", cached)
			}
		})
	}
}

func TestHistoricalWindowDoesNotExtendRollingCache(t *testing.T) {
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	at := observationTestHour
	if err := log.submitRecord(observationRequestStream, serving.RequestRow{Kind: serving.RequestRowFinal, RequestID: "old", ReceivedAt: at}, nil, at); err != nil {
		t.Fatal(err)
	}
	if _, err := log.LoadWindow(context.Background(), at.Add(-time.Minute), at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	clock.set(at.Add(time.Hour))
	got, err := log.LoadWindow(context.Background(), at, at.Add(time.Second))
	if err != nil || len(got.Arrivals) != 1 {
		t.Fatalf("historical evidence changed: %+v %v", got, err)
	}
	log.windowMu.Lock()
	defer log.windowMu.Unlock()
	if log.windowSpan > time.Minute+time.Second {
		t.Fatalf("one-second historical query expanded rolling retention: %s", log.windowSpan)
	}
	for _, segment := range log.windowSegments {
		if segment.at.Before(clock.now().Add(-time.Minute - time.Second).Truncate(time.Minute)) {
			t.Fatalf("historical segment entered rolling cache: %s", segment.at)
		}
	}
}

func TestColdCapturedPrefixDoesNotSkipConcurrentAppend(t *testing.T) {
	archive := newReadableObservationArchive(t)
	log, _ := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	from, to := observationTestHour.Add(-time.Hour), observationTestHour.Add(time.Minute)
	appendRow := func(id string) {
		t.Helper()
		if err := log.submitRecord(observationRequestStream, serving.RequestRow{Kind: serving.RequestRowFinal, RequestID: id, ReceivedAt: observationTestHour, WorkloadUID: "uid"}, nil, observationTestHour); err != nil {
			t.Fatal(err)
		}
		if err := log.Drain(ctx); err != nil {
			t.Fatal(err)
		}
	}
	appendRow("a")
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	defer resume()
	archive.beforeList = sync.OnceFunc(func() { close(entered); <-release })
	type outcome struct {
		window LoadWindow
		err    error
	}
	done := make(chan outcome, 1)
	go func() { window, err := log.LoadWindow(ctx, from, to); done <- outcome{window, err} }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	appendRow("b")
	resume()
	select {
	case first := <-done:
		if first.err != nil || len(first.window.Arrivals) != 1 {
			t.Fatalf("frozen prefix: %+v %v", first.window, first.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	appendRow("c")
	got, err := log.LoadWindow(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := log.Query(ctx, "uid", from, to, nil, nil, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Arrivals) != disk.Total {
		t.Fatalf("cached=%v disk_rows=%d; committed request lost", got.Arrivals, disk.Total)
	}
}

func TestWindowCacheRetainsLargeWindowAndPrunesBySpan(t *testing.T) {
	for _, writer := range []bool{false, true} {
		t.Run(fmt.Sprint(writer), func(t *testing.T) {
			log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
			ctx := context.Background()
			from, to := observationTestHour.Add(-time.Minute), observationTestHour.Add(time.Second)
			const rows = 32*1024 + 1
			if writer {
				if _, err := log.LoadWindow(ctx, from, to); err != nil {
					t.Fatal(err)
				}
			}
			var data bytes.Buffer
			encoder := json.NewEncoder(&data)
			for id := range rows {
				row := serving.RequestRow{Kind: serving.RequestRowFinal, RequestID: fmt.Sprint(id), WorkloadUID: "uid", ReceivedAt: observationTestHour}
				if writer {
					if err := log.submitRecord(observationRequestStream, row, nil, observationTestHour); err != nil {
						t.Fatal(err)
					}
				} else if err := encoder.Encode(row); err != nil {
					t.Fatal(err)
				}
			}
			if !writer {
				name := log.segment(observationRequestStream, observationTestHour)
				if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, data.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
				if err := log.indexSegments([]time.Time{observationTestHour}); err != nil {
					t.Fatal(err)
				}
			}
			timing := &SchedulerControlObservation{}
			got, err := log.LoadWindowTimed(ctx, from, to, timing)
			if err != nil {
				t.Fatal(err)
			}
			if timing.WindowBudgetFallbacks != 0 {
				t.Fatalf("unexpected budget fallback: %+v", timing)
			}
			disk, err := log.Query(ctx, "uid", from, to, nil, nil, 0, "", "")
			if err != nil {
				t.Fatal(err)
			}
			want := make([]serving.SchedulerArrival, 0, len(disk.Requests))
			for _, row := range disk.Requests {
				want = append(want, requestArrival(&row, nil))
			}
			slices.SortFunc(want, func(a, b serving.SchedulerArrival) int { return strings.Compare(a.ID, b.ID) })
			if !reflect.DeepEqual(got.Arrivals, want) || len(want) != rows {
				t.Fatalf("cache changed exact arrivals: got=%d want=%d", len(got.Arrivals), len(want))
			}
			log.windowMu.Lock()
			retained := log.windowRecords
			actual := 0
			for _, segment := range log.windowSegments {
				actual += len(segment.records)
			}
			log.windowMu.Unlock()
			if retained != rows || retained != actual {
				t.Fatalf("cache record accounting: retained=%d actual=%d", retained, actual)
			}
			warmTiming := &SchedulerControlObservation{}
			warm, err := log.LoadWindowTimed(ctx, from, to, warmTiming)
			if err != nil || !reflect.DeepEqual(warm, got) {
				t.Fatalf("warm cache differs from cold/disk window: %v", err)
			}
			if warmTiming.WindowDecode != 0 || warmTiming.WindowBudgetFallbacks != 0 {
				t.Fatalf("large warm window decoded disk rows: %+v", warmTiming)
			}
			clock.set(observationTestHour.Add(3 * time.Minute))
			if _, err := log.LoadWindow(ctx, clock.now().Add(-time.Minute), clock.now()); err != nil {
				t.Fatal(err)
			}
			log.windowMu.Lock()
			defer log.windowMu.Unlock()
			if log.windowRecords != 0 || len(log.windowSegments) != 0 {
				t.Fatalf("rows older than rolling span retained: rows=%d segments=%d", log.windowRecords, len(log.windowSegments))
			}
		})
	}
}
