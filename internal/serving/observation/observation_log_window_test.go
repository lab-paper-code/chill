package observation

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestLoadWindowReusesWrittenRows(t *testing.T) {
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour.Add(time.Minute))
	at := observationTestHour.Add(time.Minute)
	ctx := context.Background()
	for _, kind := range []string{"received", "response_finished"} {
		if err := log.RequestEvent(serving.RequestEvent{Kind: kind, RequestID: "request", At: at, Outcome: "completed"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.RequestClosed("request", serving.RequestTimings{}); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		timing := &SchedulerControlObservation{}
		window, err := log.LoadWindowTimed(ctx, at, at.Add(time.Second), timing)
		if err != nil || len(window.Arrivals) != i+1 {
			t.Fatalf("window: %+v %v", window, err)
		}
		if i == 1 && timing.WindowDecode != 0 {
			t.Fatalf("warm window decoded written records again: %s", timing.WindowDecode)
		}
		if i == 0 {
			for _, kind := range []string{"received", "response_finished"} {
				if err := log.RequestEvent(serving.RequestEvent{Kind: kind, RequestID: "second", At: at, Outcome: "completed"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := log.RequestClosed("second", serving.RequestTimings{}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestWindowMatchesLogAcrossLateSupplementAndRestart(t *testing.T) {
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	ctx := context.Background()
	from, to := observationTestHour, observationTestHour.Add(2*time.Minute)
	clock.set(from.Add(time.Second))
	definition := &serving.Definition{Name: "workload", UID: "uid", Generation: 1}
	if err := log.DefinitionRecord(definition); err != nil {
		t.Fatal(err)
	}
	var timing serving.RequestTimings
	timing.Record(serving.ServerAssigned, 28*time.Millisecond)
	timing.Record(serving.ServerSenderElapsedFrozen, 30*time.Millisecond)
	elapsed, accepted, exchange := int64(101e6), int64(8e6), int64(95e6)
	for _, id := range []string{"a", "b"} {
		row := serving.RequestRow{Kind: serving.RequestRowFinal, RequestID: id, WorkloadUID: definition.UID,
			DefinitionID: definition.Identity(), ReceivedAt: from, Instance: "instance", AcceptedNanos: &accepted,
			ElapsedNanos: &elapsed, ExchangeNanos: &exchange, ServerTiming: timing, ExecutionComplete: true, Outcome: outcomeCompleted}
		if err := log.submitRecord(observationRequestStream, row, nil, clock.now()); err != nil {
			t.Fatal(err)
		}
		code := 200
		report := serving.ClientObservation{ObservationID: id, RequestID: id, WorkloadName: definition.Name,
			ElapsedNanos: 120e6, Outcome: outcomeCompleted, StatusCode: &code}
		for range 2 {
			if err := log.ClientObservation(ctx, report); err != nil {
				t.Fatal(err)
			}
		}
	}
	check := func(log *ObservationLog, samples int) LoadWindow {
		t.Helper()
		var first LoadWindow
		for i := range 2 {
			got, err := log.LoadWindow(ctx, from, to)
			if err != nil || len(got.Arrivals) != 2 || len(got.PathSamples) != samples || len(got.ClientObservations) != 2 {
				t.Fatalf("window: %+v %v", got, err)
			}
			if i == 0 {
				first = got
			} else if !reflect.DeepEqual(first, got) {
				t.Fatalf("cold/warm windows differ:\n%+v\n%+v", first, got)
			}
			assertWindowRequestsMatchDisk(t, log, from, to)
		}
		return first
	}
	check(log, 0)
	clock.set(from.Add(time.Minute + time.Second))
	for _, id := range []string{"a", "b"} {
		if err := log.RecordNativeSupplement(serving.RequestEvent{RequestID: id, WorkloadUID: definition.UID,
			NativeInstance: "instance", At: clock.now(), NativeProgress: &serving.BackendProgress{Phase: "finished",
				At: from.Add(90 * time.Millisecond), NativeElapsedMS: 60, ExecutionComplete: true, Outcome: outcomeCompleted}}); err != nil {
			t.Fatal(err)
		}
	}
	mutated := check(log, 2)
	*mutated.ClientObservations[0].StatusCode = 500
	want := check(log, 2)
	if err := log.Close(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewObservationLog(log.config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restarted.Close(ctx) }()
	got := check(restarted, 2)
	// Restart coverage describes its own boot/gaps, not a fabricated continuous
	// process. Each capture's coverage is checked against the log independently.
	want.Coverage, got.Coverage = serving.ObservationCoverage{}, serving.ObservationCoverage{}
	want.GapUntil, got.GapUntil = nil, nil
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("restart changed window evidence:\n%+v\n%+v", want, got)
	}
}

func TestWindowConcurrentWriterAndOccupancy(t *testing.T) {
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	ctx := context.Background()
	from, to := observationTestHour, observationTestHour.Add(time.Minute)
	errors := make(chan error, 3)
	var workers sync.WaitGroup
	workers.Add(3)
	for worker := range 3 {
		go func() {
			defer workers.Done()
			for i := range 100 {
				var err error
				switch worker {
				case 0:
					err = log.submitRecord(observationRequestStream, serving.RequestRow{Kind: serving.RequestRowFinal,
						RequestID: fmt.Sprint(i), ReceivedAt: from, WorkloadUID: "uid"}, nil, from)
				case 1:
					_, err = log.LoadWindow(ctx, from, to)
				case 2:
					_, err = log.ScanOccupancyRows(ctx, "", "", from, to, func(serving.RequestRow) {})
				}
				if err != nil {
					errors <- err
					return
				}
			}
		}()
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	window, err := log.LoadWindow(ctx, from, to)
	if err != nil || len(window.Arrivals) != 100 {
		t.Fatalf("concurrent capture lost committed rows: %+v %v", window, err)
	}
	assertWindowRequestsMatchDisk(t, log, from, to)
}

func TestWindowPrunesAfterConsumersStop(t *testing.T) {
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	ctx := context.Background()
	for minute := range 8 {
		at := observationTestHour.Add(time.Duration(minute) * time.Minute)
		clock.set(at)
		if err := log.submitRecord(observationRequestStream, serving.RequestRow{Kind: serving.RequestRowFinal,
			RequestID: fmt.Sprint(minute), ReceivedAt: at}, nil, at); err != nil {
			t.Fatal(err)
		}
		if minute == 1 {
			if _, err := log.LoadWindow(ctx, observationTestHour, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	log.windowMu.Lock()
	retained := len(log.windowSegments)
	log.windowMu.Unlock()
	if retained > 2 {
		t.Fatalf("inactive consumers retained old segments: %d", retained)
	}
	// An explicit older read restores the exact evidence after eviction.
	got, err := log.LoadWindow(ctx, observationTestHour, clock.now().Add(time.Second))
	if err != nil || len(got.Arrivals) != 8 {
		t.Fatalf("eviction changed historical window contents: %+v %v", got, err)
	}
}

func assertWindowRequestsMatchDisk(t *testing.T, log *ObservationLog, from, to time.Time) {
	t.Helper()
	ranges := map[string][2]time.Time{observationRequestStream: {from, {}}, observationClientStream: {from, to}}
	read, err := log.prepare(context.Background(), ranges)
	if err != nil {
		t.Fatal(err)
	}
	defer read.close()
	diskNative, memoryNative := map[string]serving.RequestRow{}, map[string]serving.RequestRow{}
	disk, err := log.requestRowsAndNative(read, func(*requestEnvelope) bool { return true }, diskNative, from, to)
	if err != nil {
		t.Fatal(err)
	}
	memory, err := log.windowRequests(read, memoryNative, from, to)
	if err != nil {
		t.Fatal(err)
	}
	order := func(a, b *serving.RequestRow) int { return strings.Compare(a.RequestID, b.RequestID) }
	slices.SortFunc(disk, order)
	slices.SortFunc(memory, order)
	if !reflect.DeepEqual(disk, memory) || !reflect.DeepEqual(diskNative, memoryNative) {
		t.Fatalf("memory/log request join differs: disk=%+v memory=%+v native=%+v/%+v", disk, memory, diskNative, memoryNative)
	}
	window, err := log.LoadWindow(context.Background(), from, to)
	coverage := read.coverage(map[string][2]time.Time{observationRequestStream: {from, to}, observationClientStream: {from, to}})
	if err != nil || !reflect.DeepEqual(window.Coverage, coverage) {
		t.Fatalf("memory window changed log coverage: %+v %+v %v", window.Coverage, coverage, err)
	}
}

func BenchmarkLoadWindow(b *testing.B) {
	at := time.Now().UTC()
	log, err := NewObservationLog(ObservationLogConfig{Directory: b.TempDir(), Store: "evidence", Retention: time.Hour, Writer: &observationArchive{directory: b.TempDir()}, Now: func() time.Time { return at }})
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		if err := log.Close(context.Background()); err != nil {
			b.Error(err)
		}
	}()
	for i := range 3000 {
		id := fmt.Sprint(i)
		for _, kind := range []string{"received", "response_finished"} {
			if err := log.RequestEvent(serving.RequestEvent{Kind: kind, RequestID: id, WorkloadUID: "uid", At: at.Add(-time.Duration(i) * time.Millisecond), Outcome: "completed"}); err != nil {
				b.Fatal(err)
			}
		}
		if err := log.RequestClosed(id, serving.RequestTimings{}); err != nil {
			b.Fatal(err)
		}
	}
	ctx := context.Background()
	if _, err := log.LoadWindow(ctx, at.Add(-time.Minute), at.Add(time.Second)); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := log.LoadWindow(ctx, at.Add(-time.Minute), at.Add(time.Second)); err != nil {
			b.Fatal(err)
		}
	}
}
