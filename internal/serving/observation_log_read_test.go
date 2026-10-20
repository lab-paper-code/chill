package serving

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func appendAssignedRequest(t *testing.T, observationLog *ObservationLog, id string, at time.Time) {
	t.Helper()
	assignment := &Assignment{Node: NodeReference{Name: "node", UID: "node-uid"}, Instance: "i1", DispatchedAt: at}
	for _, kind := range []string{phaseReceived, phaseAssigned} {
		if err := observationLog.RequestEvent(RequestEvent{RequestID: id, WorkloadUID: "w", Kind: kind, At: at, ReceivedAt: at, Assignment: assignment}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestObservationReadAllowsIngressAndSchedulerAppendWhilePaused(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	at := observationTestHour.Add(time.Second)
	clock.set(at)
	appendAssignedRequest(t, observationLog, "before", at)
	if err := observationLog.SchedulerRecord(SchedulerObservation{At: at}); err != nil {
		t.Fatal(err)
	}
	read, err := observationLog.capture(map[string][2]time.Time{observationRequestStream: {}, observationSchedulerStream: {}})
	if err != nil {
		t.Fatal(err)
	}
	defer read.close()
	paused, resume := make(chan struct{}), make(chan struct{})
	type readResult struct {
		requests, scheduler int
		err                 error
	}
	readDone := make(chan readResult, 1)
	go func() {
		result := readResult{}
		result.err = observationLog.scanStream(read, observationRequestStream, func([]byte) error {
			result.requests++
			close(paused)
			<-resume
			return nil
		})
		if result.err == nil {
			result.err = observationLog.scanStream(read, observationSchedulerStream, func([]byte) error {
				result.scheduler++
				return nil
			})
		}
		readDone <- result
	}()
	select {
	case <-paused:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not reach its captured record")
	}
	appendDone := make(chan error, 1)
	go func() {
		later := at.Add(time.Nanosecond)
		appendAssignedRequest(t, observationLog, "after", later)
		appendDone <- observationLog.SchedulerRecord(SchedulerObservation{At: later})
	}()
	awaitObservationLogOperation(t, appendDone)
	close(resume)
	result := <-readDone
	if result.err != nil || result.requests != 1 || result.scheduler != 1 {
		t.Fatalf("reader included appends after its capture: %+v", result)
	}
	load, err := observationLog.LoadWindow(at.Add(-time.Second), at.Add(time.Second))
	if err != nil || len(load.Arrivals) != 2 || !observationLog.Health().Healthy {
		t.Fatalf("next consumer lost completed appends: %+v %v", load, err)
	}
	page, _, _, err := observationLog.SchedulerObservationPage(at.Add(-time.Second), at.Add(time.Second), 10, 0)
	if err != nil || len(page) != 2 {
		t.Fatalf("next scheduler consumer lost an appended record: %+v %v", page, err)
	}
}

func TestObservationReadReusesLargeLineBufferAndPreservesSelectedPayloads(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	at := observationTestHour.Add(time.Second)
	clock.set(at)
	payload := json.RawMessage(`{"retained":"` + strings.Repeat("x", 512<<10) + `"}`)
	event := SchedulerObservation{At: at, Action: &SchedulerActionObservation{ID: "large-source", Expected: payload}}
	const records = 9
	allocations := func(expected int) float64 {
		t.Helper()
		read, err := observationLog.capture(map[string][2]time.Time{observationSchedulerStream: {}})
		if err != nil {
			t.Fatal(err)
		}
		defer read.close()
		count := 0
		var scanErr error
		allocated := testing.AllocsPerRun(1, func() {
			count = 0
			for _, file := range read.files[observationSchedulerStream] {
				if _, err := file.file.Seek(0, 0); err != nil {
					t.Fatal(err)
				}
			}
			scanErr = observationLog.scanStream(read, observationSchedulerStream, func(raw []byte) error {
				count++
				if len(raw) <= 512<<10 {
					t.Fatal("large raw record was truncated")
				}
				return nil
			})
		})
		if scanErr != nil || count != expected {
			t.Fatalf("captured prefix changed: count=%d expected=%d error=%v", count, expected, scanErr)
		}
		return allocated
	}
	for index := range records {
		event.At = at.Add(time.Duration(index) * time.Nanosecond)
		if err := observationLog.SchedulerRecord(event); err != nil {
			t.Fatal(err)
		}
	}
	many := allocations(records)
	// The same scan over one record allocates the same buffers.
	single, singleClock := clockedObservationLog(t, ObservationLogConfig{}, at)
	singleClock.set(at)
	event.At = at
	if err := single.SchedulerRecord(event); err != nil {
		t.Fatal(err)
	}
	observationLog = single
	one := allocations(1)
	if many > one {
		t.Fatalf("line allocations grew with record count: one=%g many=%g", one, many)
	}
	page, next, _, err := observationLog.SchedulerObservationPage(at.Add(-time.Nanosecond), at.Add(time.Second), records, 0)
	if err != nil || len(page) != 1 || next != -1 || !bytes.Equal(page[0].Action.Expected, payload) {
		t.Fatalf("large selected page lost its payload: count=%d next=%d error=%v", len(page), next, err)
	}
}

func TestObservationReadIgnoresUnterminatedLastLine(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	at := observationTestHour.Add(time.Second)
	clock.set(at)
	if err := observationLog.SchedulerRecord(SchedulerObservation{At: at}); err != nil {
		t.Fatal(err)
	}
	unterminated, err := json.Marshal(schedulerLine{At: at.Add(time.Nanosecond)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = observationLog.files[observationSchedulerStream].Write(unterminated); err != nil {
		t.Fatal(err)
	}
	page, _, _, err := observationLog.SchedulerObservationPage(at.Add(-time.Second), at.Add(time.Second), 10, 0)
	if err != nil || len(page) != 1 || !observationLog.Health().Healthy {
		t.Fatalf("line still being written was read: %+v %v %+v", page, err, observationLog.Health())
	}
}

func TestObservationReadReusesGrownBufferAcrossFiles(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	payload := json.RawMessage(`{"retained":"` + strings.Repeat("x", 512<<10) + `"}`)
	const files = 5
	for index := range files {
		at := observationTestHour.Add(time.Duration(index) * time.Minute)
		clock.set(at)
		if err := observationLog.SchedulerRecord(SchedulerObservation{At: at, Action: &SchedulerActionObservation{ID: "large-source", Expected: payload}}); err != nil {
			t.Fatal(err)
		}
	}
	allocations := func(to time.Time, expected int) float64 {
		t.Helper()
		read, err := observationLog.capture(map[string][2]time.Time{observationSchedulerStream: {observationTestHour, to}})
		if err != nil {
			t.Fatal(err)
		}
		defer read.close()
		count := 0
		var scanErr error
		allocated := testing.AllocsPerRun(1, func() {
			count = 0
			for _, file := range read.files[observationSchedulerStream] {
				if _, err := file.file.Seek(0, 0); err != nil {
					t.Fatal(err)
				}
			}
			scanErr = observationLog.scanStream(read, observationSchedulerStream, func([]byte) error {
				count++
				return nil
			})
		})
		if scanErr != nil || count != expected {
			t.Fatalf("cross-file scan changed: count=%d expected=%d error=%v", count, expected, scanErr)
		}
		return allocated
	}
	one := allocations(observationTestHour, 1)
	many := allocations(observationTestHour.Add(time.Hour), files)
	// Per-file limit readers remain; the large line buffer grows once.
	if many > one+2*(files-1) {
		t.Fatalf("large buffer growth repeated per file: one=%g many=%g", one, many)
	}
}
