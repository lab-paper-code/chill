package observation

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

func appendAssignedRequest(t *testing.T, observationLog *ObservationLog, id string, at time.Time) {
	t.Helper()
	assignment := &serving.Assignment{Node: serving.NodeReference{Name: "node", UID: "node-uid"}, Instance: "i1", DispatchedAt: at}
	for _, kind := range []string{"received", "assigned"} {
		if err := observationLog.RequestEvent(serving.RequestEvent{RequestID: id, WorkloadUID: "w", Kind: kind, At: at, ReceivedAt: at, Assignment: assignment}); err != nil {
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
	read, err := observationLog.capture(context.Background(), map[string][2]time.Time{observationRequestStream: {}, observationSchedulerStream: {}})
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
	load, err := observationLog.LoadWindow(context.Background(), at.Add(-time.Second), at.Add(time.Second))
	if err != nil || len(load.Arrivals) != 2 || !observationLog.Health().Healthy {
		t.Fatalf("next consumer lost completed appends: %+v %v", load, err)
	}
	page, _, _, _, err := observationLog.SchedulerObservationPage(context.Background(), at.Add(-time.Second), at.Add(time.Second), 10, 0, SchedulerObservationSelection{})
	if err != nil || len(page) != 2 {
		t.Fatalf("next scheduler consumer lost an appended record: %+v %v", page, err)
	}
}

func TestObservationCaptureOpensOutsideWriterLock(t *testing.T) {
	recorder := serving.NewDiagnostics()
	log, clock := clockedObservationLog(t, ObservationLogConfig{Diagnostics: recorder}, observationTestHour)
	at := observationTestHour.Add(time.Second)
	clock.set(at)
	if err := log.SchedulerRecord(SchedulerObservation{At: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := log.writeBarrier(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	recorder.Start()
	read, err := log.capture(context.Background(), map[string][2]time.Time{observationSchedulerStream: {}})
	if err != nil {
		t.Fatal(err)
	}
	read.close()
	recorder.Stop()
	var output bytes.Buffer
	metadata, err := recorder.WriteEvents(&output)
	if err != nil {
		t.Fatal(err)
	}
	raw := output.Bytes()
	if metadata.EventBytes != 64 || len(raw)%metadata.EventBytes != 0 {
		t.Fatalf("invalid diagnostic event framing: bytes=%d eventBytes=%d", len(raw), metadata.EventBytes)
	}
	held, opens, captures := false, 0, 0
	for offset := 0; offset < len(raw); offset += metadata.EventBytes {
		data := raw[offset : offset+metadata.EventBytes]
		event := struct {
			kind  serving.DiagnosticKind
			lock  byte
			phase serving.DiagnosticPhase
			owner uint64
		}{
			kind: serving.DiagnosticKind(binary.LittleEndian.Uint16(data[48:50])),
			lock: data[50], phase: serving.DiagnosticPhase(data[51]),
			owner: binary.LittleEndian.Uint64(data[8:16]),
		}
		if event.kind == serving.DiagnosticMutex && event.lock == 2 {
			if event.phase == serving.DiagnosticAcquired {
				held = true
			}
			if event.phase == serving.DiagnosticReleased {
				held = false
			}
		}
		if event.kind == serving.DiagnosticCapture && event.phase == serving.DiagnosticBegin {
			captures++
		}
		if event.kind == serving.DiagnosticFileOpen && event.phase == serving.DiagnosticBegin {
			opens++
			if held || event.lock != 0 || event.owner != 0 {
				t.Fatal("capture opened a file under the writer lock")
			}
		}
		if event.kind == serving.DiagnosticFileStat {
			t.Fatal("capture statted a file instead of using its complete append offset")
		}
	}
	if held || opens != 1 || captures != 1 || metadata.DroppedContention != 0 {
		t.Fatalf("capture diagnostic edges missing: held=%v opens=%d captures=%d dropped=%d", held, opens, captures, metadata.DroppedContention)
	}
}

func TestObservationCaptureFreezesPathsAndOffsetsBeforeOpening(t *testing.T) {
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	at := observationTestHour.Add(time.Second)
	clock.set(at)
	if err := log.SchedulerRecord(SchedulerObservation{At: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := log.writeBarrier(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	// Pause exactly after the in-memory snapshot, before any read handle opens.
	read, segments := log.captureSnapshot(map[string][2]time.Time{observationSchedulerStream: {}}, nil)
	defer read.close()
	for _, next := range []time.Time{at.Add(time.Minute), at} {
		clock.set(next)
		if err := log.SchedulerRecord(SchedulerObservation{At: next}); err != nil {
			t.Fatal(err)
		}
	}
	if len(segments[observationSchedulerStream]) != 1 {
		t.Fatalf("snapshot did not retain exactly its original path: %+v", segments)
	}
	for stream, files := range segments {
		for _, segment := range files {
			if err := log.openReadFile(read, stream, segment); err != nil {
				t.Fatal(err)
			}
		}
	}
	rows := 0
	if err := log.scanStream(read, observationSchedulerStream, func([]byte) error {
		rows++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("opening after rotation/reopen included post-snapshot appends: %d", rows)
	}
}

// scanLargeLines requires every captured scheduler line to arrive in the one
// buffer the first line grew. Buffer identity shows reuse; AllocsPerRun would
// also count allocations the runtime makes on other goroutines.
func scanLargeLines(t *testing.T, observationLog *ObservationLog, bounds [2]time.Time) int {
	t.Helper()
	read, err := observationLog.capture(context.Background(), map[string][2]time.Time{observationSchedulerStream: bounds})
	if err != nil {
		t.Fatal(err)
	}
	defer read.close()
	var buffer *byte
	count := 0
	err = observationLog.scanStream(read, observationSchedulerStream, func(raw []byte) error {
		count++
		if len(raw) <= 512<<10 {
			t.Fatal("large raw record was truncated")
		}
		if buffer == nil {
			buffer = &raw[0]
		}
		if &raw[0] != buffer {
			t.Fatalf("line %d grew a new buffer", count)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestObservationReadReusesLargeLineBufferAndPreservesSelectedPayloads(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	at := observationTestHour.Add(time.Second)
	clock.set(at)
	// Equal-length lines with distinct payloads: a selected payload that
	// borrowed the reused buffer would read a later line.
	payloads := make([]json.RawMessage, 2)
	for index := range payloads {
		payloads[index] = json.RawMessage(`{"retained":"` + strings.Repeat(string(rune('a'+index)), 512<<10) + `"}`)
		event := SchedulerObservation{At: at.Add(time.Duration(index+1) * time.Nanosecond), Action: &SchedulerActionObservation{ID: "large-source", Expected: payloads[index]}}
		if err := observationLog.SchedulerRecord(event); err != nil {
			t.Fatal(err)
		}
	}
	if count := scanLargeLines(t, observationLog, [2]time.Time{}); count != len(payloads) {
		t.Fatalf("captured prefix changed: count=%d expected=%d", count, len(payloads))
	}
	page, next, _, _, err := observationLog.SchedulerObservationPage(context.Background(), at, at.Add(time.Second), len(payloads), 0, SchedulerObservationSelection{})
	if err != nil || len(page) != len(payloads) || next != -1 {
		t.Fatalf("large selected page lost records: count=%d next=%d error=%v", len(page), next, err)
	}
	for index, record := range page {
		if !bytes.Equal(record.Action.Expected, payloads[index]) {
			t.Fatalf("selected record %d lost its payload", index)
		}
	}
}

func TestObservationReadIgnoresUnterminatedLastLine(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	at := observationTestHour.Add(time.Second)
	clock.set(at)
	if err := observationLog.SchedulerRecord(SchedulerObservation{At: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := observationLog.writeBarrier(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	unterminated, err := json.Marshal(schedulerLine{At: at.Add(time.Nanosecond)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = observationLog.files[observationSchedulerStream].Write(unterminated); err != nil {
		t.Fatal(err)
	}
	page, _, _, _, err := observationLog.SchedulerObservationPage(context.Background(), at.Add(-time.Second), at.Add(time.Second), 10, 0, SchedulerObservationSelection{})
	if err != nil || len(page) != 1 || !observationLog.Health().Healthy {
		t.Fatalf("line still being written was read: %+v %v %+v", page, err, observationLog.Health())
	}
}

func TestObservationReadReusesGrownBufferAcrossFiles(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	payload := json.RawMessage(`{"retained":"` + strings.Repeat("x", 512<<10) + `"}`)
	const files = 2
	for index := range files {
		at := observationTestHour.Add(time.Duration(index) * time.Minute)
		clock.set(at)
		if err := observationLog.SchedulerRecord(SchedulerObservation{At: at, Action: &SchedulerActionObservation{ID: "large-source", Expected: payload}}); err != nil {
			t.Fatal(err)
		}
	}
	if count := scanLargeLines(t, observationLog, [2]time.Time{observationTestHour, observationTestHour.Add(time.Hour)}); count != files {
		t.Fatalf("cross-file scan changed: count=%d expected=%d", count, files)
	}
}

const localSource = "local"

func TestObservationReadCoverageIgnoresBoundLocations(t *testing.T) {
	ctx := context.Background()
	archive := newReadableObservationArchive(t)
	log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
	from, to := observationTestHour.Add(time.Second), observationTestHour.Add(time.Minute)
	clock.set(from)
	appendAssignedRequest(t, log, "location", from)
	if err := log.RequestClosed("location", serving.RequestTimings{}); err != nil {
		t.Fatal(err)
	}
	clock.set(to)
	locations := []*time.Location{time.UTC, time.Local, time.FixedZone("KST", 9*60*60), time.FixedZone("half-hour", -3*60*60-30*60)}
	for _, source := range []string{localSource, observationArchiveSource} {
		t.Run(source, func(t *testing.T) {
			if source == localSource {
				archive.listError = errors.New("local bounds must not list the archive")
			} else {
				archive.listError = nil
				clock.set(observationTestHour.Add(3 * time.Hour))
				if err := log.Flush(ctx); err != nil {
					t.Fatal(err)
				}
			}
			var want serving.ObservationCoverage
			for index, location := range locations {
				t.Run(location.String(), func(t *testing.T) {
					bounds := map[string][2]time.Time{observationRequestStream: {from.In(location), to.In(location)}}
					read, err := log.prepare(ctx, bounds)
					if err != nil {
						t.Fatal(err)
					}
					coverage := read.coverage(bounds)
					read.close()
					if len(coverage.Sources) != 1 || coverage.Sources[0].Source != source || len(coverage.Gaps) != 0 || coverage.GapUntil(from.In(location)) != nil {
						t.Fatalf("location changed coverage: %+v", coverage)
					}
					if index == 0 {
						want = coverage
					} else if !reflect.DeepEqual(coverage, want) {
						t.Fatalf("same instants produced different coverage: got=%+v want=%+v", coverage, want)
					}
				})
			}
			if source == localSource && archive.lists != 0 {
				t.Fatalf("local bounds listed archive %d times", archive.lists)
			}
		})
	}
}

func TestObservationPublicReadsIgnoreBoundLocations(t *testing.T) {
	ctx := context.Background()
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	from, to := observationTestHour.Add(time.Second), observationTestHour.Add(time.Minute)
	clock.set(from)
	appendAssignedRequest(t, log, "location", from)
	if err := log.ManagementEvent(serving.ManagementEvent{WorkloadUID: "w", At: from}); err != nil {
		t.Fatal(err)
	}
	if err := log.SchedulerRecord(SchedulerObservation{At: from}); err != nil {
		t.Fatal(err)
	}
	clock.set(to)
	for _, location := range []*time.Location{time.UTC, time.Local, time.FixedZone("KST", 9*60*60), time.FixedZone("half-hour", -3*60*60-30*60)} {
		t.Run(location.String(), func(t *testing.T) {
			start, end := from.In(location), to.In(location)
			query, err := log.Query(ctx, "w", start, end, nil, nil, 10, "", "")
			if err != nil || query.Total != 1 || len(query.Management) != 1 || query.GapUntil != nil || len(query.Coverage.Gaps) != 0 {
				t.Fatalf("request/management lookup changed with bound location: %+v %v", query, err)
			}
			load, err := log.LoadWindow(ctx, start, end)
			if err != nil || len(load.Arrivals) != 1 || load.GapUntil != nil || len(load.Coverage.Gaps) != 0 {
				t.Fatalf("load lookup changed with bound location: %+v %v", load, err)
			}
			events, next, gap, coverage, err := log.SchedulerObservationPage(ctx, start, end, 10, 0, SchedulerObservationSelection{})
			if err != nil || len(events) != 1 || next != -1 || gap != nil || len(coverage.Gaps) != 0 {
				t.Fatalf("scheduler lookup changed with bound location: events=%+v next=%d gap=%v coverage=%+v err=%v", events, next, gap, coverage, err)
			}
		})
	}
}
