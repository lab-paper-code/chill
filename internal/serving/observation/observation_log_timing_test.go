package observation

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

func TestRequestTimingSnapshotsAndLateSupplementStaySeparate(t *testing.T) {
	ctx := context.Background()
	at := observationTestHour
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, at)
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	var once sync.Once
	log.writeFile = func(file *os.File, data []byte) (int, error) {
		once.Do(func() { close(entered); <-release })
		return file.Write(data)
	}
	event := serving.RequestEvent{RequestID: "a", WorkloadUID: "uid", Kind: "assigned", At: at, ReceivedAt: at,
		Assignment: &serving.Assignment{Instance: "physical", DispatchedAt: at}}
	event.ServerTiming.Record(serving.ServerAuthStart, 0)
	if err := log.RequestEvent(event); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not reach storage")
	}
	read, _ := log.captureSnapshot(map[string][2]time.Time{observationRequestStream: {at, at.Add(time.Minute)}}, []time.Time{at})
	defer read.close()
	captured := read.open["a"]
	if captured == nil {
		t.Fatal("reader did not capture assigned request")
	}
	// Producer-local mutation and later folds must not alter the captured row
	// or the assigned bytes waiting at the actual writer boundary.
	event.ServerTiming.Record(serving.ServerAuthDone, 9)
	if _, present := captured.ServerTiming.Elapsed(serving.ServerAuthDone); present {
		t.Fatal("producer mutation changed reader snapshot")
	}
	event.Kind = "validated"
	if err := log.RequestEvent(event); err != nil {
		t.Fatal(err)
	}
	other := serving.RequestEvent{RequestID: "b", WorkloadUID: "uid", Kind: "assigned", At: at, ReceivedAt: at,
		Assignment: &serving.Assignment{Instance: "physical", DispatchedAt: at}}
	other.ServerTiming.Record(serving.ServerAuthStart, 7)
	if err := log.RequestEvent(other); err != nil {
		t.Fatal(err)
	}
	event.Kind, event.Outcome = "response_finished", "canceled"
	event.ServerTiming.Record(serving.ServerResponseWriteDone, 12)
	if err := log.RequestEvent(event); err != nil {
		t.Fatal(err)
	}
	if err := log.RequestClosed("a"); err != nil {
		t.Fatal(err)
	}
	resume()
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	rows := readRequestRows(t, log.config.Directory)
	if len(rows) != 3 || rows[0].Kind != serving.RequestRowAssigned || rows[2].Kind != serving.RequestRowFinal {
		t.Fatalf("assignment/final order changed: %+v", rows)
	}
	assertServerTiming(t, rows[0].ServerTiming, serving.ServerAuthStart, 0, true)
	assertServerTiming(t, rows[0].ServerTiming, serving.ServerAuthDone, 0, false)
	assertServerTiming(t, rows[1].ServerTiming, serving.ServerAuthStart, 7, true)
	assertServerTiming(t, rows[2].ServerTiming, serving.ServerAuthDone, 9, true)
	assertServerTiming(t, captured.ServerTiming, serving.ServerAuthDone, 0, false)
	before, err := log.Query(ctx, "uid", at, at.Add(time.Minute), nil, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	final := timingRow(t, before.Requests, "a")
	late := serving.RequestEvent{RequestID: "a", WorkloadUID: "uid", Kind: "backend_finished", At: at.Add(time.Second), NativeInstance: "physical",
		NativeProgress: &serving.BackendProgress{Phase: "finished", At: at.Add(time.Second), ExecutionComplete: true}}
	late.ServerTiming.Record(serving.ServerAuthStart, 999)
	late.ServerTiming.Record(serving.ServerWorkerHTTPDone, 30)
	if err := log.RequestEvent(late); err != nil {
		t.Fatal(err)
	}
	after, err := log.Query(ctx, "uid", at, at.Add(time.Minute), nil, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(final, timingRow(t, after.Requests, "a")) {
		t.Fatal("late native evidence changed closed HTTP final")
	}
	assertServerTiming(t, final.ServerTiming, serving.ServerWorkerHTTPDone, 0, false)
	rows = readRequestRows(t, log.config.Directory)
	if len(rows) != 4 || rows[3].Kind != serving.RequestRowNative {
		t.Fatalf("late evidence did not use native supplement: %+v", rows)
	}
	assertServerTiming(t, rows[3].ServerTiming, serving.ServerWorkerHTTPDone, 30, true)
	// Mutating a returned live query value must not mutate the retained row.
	for i := range after.Requests {
		if after.Requests[i].RequestID == "b" {
			after.Requests[i].ServerTiming.Record(serving.ServerReplyReceived, 55)
		}
	}
	again, err := log.Query(ctx, "uid", at, at.Add(time.Minute), nil, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	assertServerTiming(t, timingRow(t, again.Requests, "b").ServerTiming, serving.ServerReplyReceived, 0, false)
}

func TestRequestTimingRestoreArchiveQueryAndRawRoundTrip(t *testing.T) {
	ctx := context.Background()
	at := observationTestHour
	archive := rawTestArchive{newReadableObservationArchive(t)}
	log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, at)
	for i, id := range []string{"a", "b"} {
		event := serving.RequestEvent{RequestID: id, WorkloadUID: "uid", Kind: "assigned", At: at, ReceivedAt: at,
			Assignment: &serving.Assignment{Instance: "physical", DispatchedAt: at}}
		event.ServerTiming.Record(serving.ServerAuthStart, time.Duration(i))
		event.ServerTiming.Record(serving.ServerAuthDone, time.Duration(math.MaxInt64-int64(i)))
		if err := log.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(ctx); err != nil {
		t.Fatal(err)
	}
	clock.set(at.Add(time.Minute))
	restarted, clock := clockedObservationLog(t, log.config, clock.now())
	_, restored := restarted.RuntimeFacts()
	if len(restored) != 2 {
		t.Fatalf("restart lost assigned requests: %+v", restored)
	}
	for i := range restored {
		index := time.Duration(0)
		if restored[i].RequestID == "b" {
			index = 1
		}
		assertServerTiming(t, restored[i].ServerTiming, serving.ServerAuthStart, index, true)
		assertServerTiming(t, restored[i].ServerTiming, serving.ServerAuthDone, time.Duration(math.MaxInt64)-index, true)
		restored[i].ServerTiming.Record(serving.ServerResponseWriteStart, 50)
	}
	live, err := restarted.Query(ctx, "uid", at, at.Add(time.Second), nil, nil, 0, "")
	if err != nil || len(live.Requests) != 2 {
		t.Fatalf("restored query: %+v, %v", live, err)
	}
	for _, row := range live.Requests {
		assertServerTiming(t, row.ServerTiming, serving.ServerResponseWriteStart, 0, false)
		event := serving.RequestEvent{RequestID: row.RequestID, Kind: "response_finished", At: at.Add(time.Second), Outcome: "completed"}
		event.ServerTiming.Record(serving.ServerResponseFlushDone, 100)
		if err := restarted.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
		if err := restarted.RequestClosed(row.RequestID); err != nil {
			t.Fatal(err)
		}
	}
	want, err := restarted.Query(ctx, "uid", at, at.Add(time.Second), nil, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	clock.set(at.Add(2 * time.Minute))
	local := readTimingRaw(t, restarted, at, false)
	var rawRows []serving.RequestRow
	decoder := json.NewDecoder(bytes.NewReader(local))
	for decoder.More() {
		var row serving.RequestRow
		if err := decoder.Decode(&row); err != nil {
			t.Fatal(err)
		}
		rawRows = append(rawRows, row)
	}
	if len(rawRows) != 4 {
		t.Fatalf("raw export lost assignment/final rows: %+v", rawRows)
	}
	for _, row := range rawRows {
		assertServerTiming(t, row.ServerTiming, serving.ServerResponseFlushDone, 100, row.Kind == serving.RequestRowFinal)
	}
	clock.set(at.Add(3 * time.Hour))
	if err := restarted.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	// A fresh root forces archive decoding instead of retained in-memory rows.
	cold, _ := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, clock.now())
	got, err := cold.Query(ctx, "uid", at, at.Add(time.Second), nil, nil, 0, "")
	if err != nil || !reflect.DeepEqual(got.Requests, want.Requests) {
		t.Fatalf("archive query changed timing values: %+v, %v", got, err)
	}
	for _, row := range got.Requests {
		index := time.Duration(0)
		if row.RequestID == "b" {
			index = 1
		}
		assertServerTiming(t, row.ServerTiming, serving.ServerAuthStart, index, true)
	}
	if archived := readTimingRaw(t, cold, at, true); !bytes.Equal(local, archived) {
		t.Fatal("archive raw export rewrote request bytes")
	}
}

func assertServerTiming(t *testing.T, timings serving.RequestTimings, stage serving.ServerStage, want time.Duration, present bool) {
	t.Helper()
	elapsed, exists := timings.Elapsed(stage)
	if exists != present || exists && elapsed != want {
		t.Fatalf("stage %d = %d, %v; want %d, %v", stage, elapsed, exists, want, present)
	}
}

func timingRow(t *testing.T, rows []serving.RequestRow, id string) serving.RequestRow {
	t.Helper()
	for _, row := range rows {
		if row.RequestID == id {
			return row
		}
	}
	t.Fatalf("request %q not found", id)
	return serving.RequestRow{}
}

func readTimingRaw(t *testing.T, log *ObservationLog, at time.Time, compressed bool) []byte {
	t.Helper()
	raw, err := log.OpenRaw(context.Background(), observationHour(at), observationRequestStream)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Reader.Close() }()
	var reader io.Reader = raw.Reader
	if compressed {
		if raw.ContentType != "application/gzip" {
			t.Fatalf("archive did not use stored gzip: %q", raw.ContentType)
		}
		gzipReader, err := gzip.NewReader(raw.Reader)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = gzipReader.Close() }()
		reader = gzipReader
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
