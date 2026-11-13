package observation

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

const (
	timedOut              = "timeout"
	firstReason           = "first"
	clientGzipContentType = "application/gzip"
)

func TestClientStreamUsesCapturedPrefixAndArchiveRetention(t *testing.T) {
	ctx := context.Background()
	archive := newReadableObservationArchive(t)
	log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
	from, to := observationTestHour, observationTestHour.Add(time.Minute)
	clock.set(from.Add(time.Second))
	first := serving.ClientObservation{ObservationID: firstReason, WorkloadName: "workload", ElapsedNanos: 80e6, Outcome: timedOut}
	if err := log.ClientObservation(ctx, first); err != nil {
		t.Fatal(err)
	}
	read, err := log.prepare(ctx, map[string][2]time.Time{
		observationRequestStream: {from, {}}, observationClientStream: {from, to},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer read.close()
	if err := log.ClientObservation(ctx, serving.ClientObservation{ObservationID: "later", WorkloadName: "workload", Outcome: timedOut}); err != nil {
		t.Fatal(err)
	}
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	samples, clients, err := log.clientWindow(read, nil, nil, from, to)
	if err != nil || len(samples) != 0 || len(clients) != 1 || clients[0].ObservationID != firstReason {
		t.Fatalf("client capture leaked later FIFO admission: %+v %+v %v", samples, clients, err)
	}
	// Archive and delete through the same ordinary hourly retention path.
	clock.set(from.Add(3 * time.Hour))
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := log.ClientObservationPage(ctx, from, to, 10, 0)
	if err != nil || len(page.Observations) != 2 || archive.opens == 0 {
		t.Fatalf("clients not queryable after local retention: %+v %v opens=%d", page, err, archive.opens)
	}
	if page.Observations[0].RequestID != "" || page.Observations[0].WorkloadUID != "" {
		t.Fatalf("archive invented timeout identity: %+v", page.Observations[0])
	}
	raw, err := log.OpenRaw(ctx, observationHour(from), observationClientStream)
	if err != nil {
		t.Fatal(err)
	}
	if raw.ContentType != clientGzipContentType {
		t.Fatalf("archive clients did not use common raw stream: %+v", raw)
	}
	if err := raw.Reader.Close(); err != nil {
		t.Fatal(err)
	}
	window, err := log.LoadWindow(ctx, from, to)
	if err != nil || len(window.ClientObservations) != 0 || len(window.PathSamples) != 0 {
		t.Fatalf("archive resurrected expired online evidence: %+v %v", window, err)
	}
}

func TestClientStreamNeverJoinsAReusedWorkloadName(t *testing.T) {
	ctx := context.Background()
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	from, to := observationTestHour, observationTestHour.Add(time.Minute)
	clock.set(from.Add(time.Second))
	if err := log.ClientObservation(ctx, serving.ClientObservation{
		ObservationID: "unverified-uid", RequestID: "not-yet-seen", WorkloadName: "workload", WorkloadUID: "old-uid", Outcome: timedOut,
	}); err != nil {
		t.Fatal(err)
	}
	definition := &serving.Definition{Name: "workload", UID: "new-uid", Generation: 1}
	if err := log.DefinitionRecord(definition); err != nil {
		t.Fatal(err)
	}
	if err := log.RequestEvent(serving.RequestEvent{
		RequestID: "not-yet-seen", WorkloadUID: definition.UID, Definition: definition.Identity(), Kind: "received", At: from,
	}); err != nil {
		t.Fatal(err)
	}
	window, err := log.LoadWindow(ctx, from, to)
	if err != nil || len(window.ClientObservations) != 1 || window.ClientObservations[0].WorkloadUID != "old-uid" || len(window.PathSamples) != 0 {
		t.Fatalf("same name falsely joined different UID: %+v %v", window, err)
	}
	// Raw query remains evidence of the supplied assertion, not a rewritten UID.
	clock.set(from.Add(2 * time.Minute))
	raw, err := log.OpenRaw(ctx, observationHour(from), observationClientStream)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Reader.Close() }()
	var report serving.ClientObservation
	decoder := json.NewDecoder(raw.Reader)
	if err := decoder.Decode(&report); err != nil || report.WorkloadUID != "old-uid" {
		t.Fatalf("raw identity assertion changed: %+v %v", report, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("unexpected raw client row: %v", err)
	}
}

func TestClientWindowAddsNativeEvidenceWithoutRewritingReports(t *testing.T) {
	ctx := context.Background()
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	from, to := observationTestHour, observationTestHour.Add(time.Minute)
	clock.set(from.Add(time.Second))
	definition := &serving.Definition{Name: "workload", UID: "uid", Generation: 1}
	if err := log.DefinitionRecord(definition); err != nil {
		t.Fatal(err)
	}
	accepted, exchange, elapsed := int64(8e6), int64(95e6), int64(101e6)
	var timing serving.RequestTimings
	timing.Record(serving.ServerAssigned, 28*time.Millisecond)
	timing.Record(serving.ServerSenderElapsedFrozen, 30*time.Millisecond)
	for _, id := range []string{"full-reply", timedOut} {
		row := serving.RequestRow{
			Kind: serving.RequestRowFinal, RequestID: id, WorkloadUID: definition.UID, DefinitionID: definition.Identity(),
			ReceivedAt: from, Instance: "instance", AcceptedNanos: &accepted, ExchangeNanos: &exchange,
			ElapsedNanos: &elapsed, ServerTiming: timing, ExecutionComplete: id == "full-reply", Outcome: outcomeCompleted,
		}
		outcome := outcomeCompleted
		code := 200
		if id == timedOut {
			row.Outcome, row.Error = timedOut, "RequestTimeout"
			outcome = timedOut
		}
		if err := log.submitRecord(observationRequestStream, row, nil, clock.now()); err != nil {
			t.Fatal(err)
		}
		if err := log.ClientObservation(ctx, serving.ClientObservation{
			ObservationID: id, RequestID: id, WorkloadName: definition.Name,
			ElapsedNanos: 120e6, Outcome: outcome, StatusCode: &code,
		}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := log.LoadWindow(ctx, from, to)
	if err != nil || len(before.PathSamples) != 0 || len(before.ClientObservations) != 2 {
		t.Fatalf("missing terminal native evidence invented a sample: %+v %v", before, err)
	}
	for _, id := range []string{"full-reply", timedOut} {
		if err := log.RecordNativeSupplement(serving.RequestEvent{
			RequestID: id, WorkloadUID: definition.UID, NativeInstance: "instance", At: clock.now(),
			NativeProgress: &serving.BackendProgress{Phase: "finished", At: from.Add(90 * time.Millisecond), NativeElapsedMS: 60, ExecutionComplete: true, Outcome: "completed"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	after, err := log.LoadWindow(ctx, from, to)
	if err != nil || len(after.PathSamples) != 1 || after.PathSamples[0].RequestID != "full-reply" ||
		after.PathSamples[0].TotalNanos != 40e6 || after.PathSamples[0].PostNanos != 30e6 ||
		len(after.ClientObservations) != 2 || after.ClientObservations[1].Outcome != timedOut {
		t.Fatalf("native evidence failed to join, or timeout became finite: %+v %v", after, err)
	}
	requests, err := log.Query(ctx, definition.UID, from, to, nil, nil, 0, "", "")
	if err != nil || len(requests.Requests) != 2 {
		t.Fatalf("request query: %+v %v", requests, err)
	}
	for _, row := range requests.Requests {
		if row.NativeElapsedMS != nil || row.NativeExecutionComplete {
			t.Fatalf("path join rewrote immutable HTTP evidence: %+v", row)
		}
	}
}

func TestClientFeedbackAdmissionDoesNotReadRequestHistory(t *testing.T) {
	ctx := context.Background()
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	var once sync.Once
	log.writeFile = func(file *os.File, data []byte) (int, error) {
		once.Do(func() { close(entered); <-release })
		return file.Write(data)
	}
	if err := log.SchedulerRecord(SchedulerObservation{At: clock.now()}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("disk worker did not enter the blocked write")
	}
	admitted := make(chan error, 1)
	go func() {
		admitted <- log.ClientObservation(ctx, serving.ClientObservation{
			ObservationID: "without-history-read", RequestID: "opaque-request", WorkloadName: "workload", Outcome: timedOut,
		})
	}()
	select {
	case err := <-admitted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("feedback admission waited for a request-history read barrier")
	}
	resume()
}

func TestClientWindowBoundsRequestReadsAndDecodesOnlySelectedFirstReports(t *testing.T) {
	ctx := context.Background()
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	old := observationTestHour.Add(time.Second)
	clock.set(old)
	// These unrelated old payloads have readable identities but deliberately
	// undecodable duration bodies. A current tick must not hydrate those bodies.
	if err := log.submitRecord(observationRequestStream, map[string]any{
		"kind": "final", "requestID": "old-request", "receivedAt": old, "nativeElapsedMS": "unrelated payload",
	}, nil, old); err != nil {
		t.Fatal(err)
	}
	if err := log.submitRecord(observationClientStream, map[string]any{
		"observationID": "unrelated-history", "workloadName": "workload", "reportedAt": old,
		"elapsedNanos": "unrelated payload", "outcome": timedOut,
	}, nil, old); err != nil {
		t.Fatal(err)
	}
	if err := log.ClientObservation(ctx, serving.ClientObservation{
		ObservationID: "first-outside-current-window", WorkloadName: "workload", Outcome: timedOut,
	}); err != nil {
		t.Fatal(err)
	}
	from := observationTestHour.Add(2 * time.Minute)
	to := from.Add(time.Minute)
	clock.set(from)
	if err := log.ClientObservation(ctx, serving.ClientObservation{
		ObservationID: "first-outside-current-window", WorkloadName: "workload", Outcome: timedOut,
	}); err != nil {
		t.Fatal(err)
	}
	window, err := log.LoadWindow(ctx, from, to)
	if err != nil || len(window.PathSamples) != 0 || len(window.ClientObservations) != 0 || len(window.Arrivals) != 0 {
		t.Fatalf("current window hydrated unrelated history or resurrected a duplicate: %+v %v", window, err)
	}
}

func TestClientFirstReportEligibilitySurvivesRestartAndExpires(t *testing.T) {
	ctx := context.Background()
	log, clock := clockedObservationLog(t, ObservationLogConfig{Retention: time.Minute}, observationTestHour)
	appendClient := func(id, request string) {
		t.Helper()
		if err := log.ClientObservation(ctx, serving.ClientObservation{
			ObservationID: id, RequestID: request, WorkloadName: "workload", Outcome: timedOut,
		}); err != nil {
			t.Fatal(err)
		}
	}
	reopen := func() {
		t.Helper()
		if err := log.Close(ctx); err != nil {
			t.Fatal(err)
		}
		reopened, err := NewObservationLog(log.config)
		if err != nil {
			t.Fatal(err)
		}
		log = reopened
		t.Cleanup(func() {
			if err := reopened.Close(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	clock.set(observationTestHour.Add(time.Second))
	appendClient("original", "request")
	clock.set(observationTestHour.Add(10 * time.Second))
	appendClient("original", "request-alias")
	clock.set(observationTestHour.Add(11 * time.Second))
	appendClient("observation-alias", "request")
	clock.set(observationTestHour.Add(20 * time.Second))
	appendClient("original", "")
	clock.set(observationTestHour.Add(30 * time.Second))
	reopen()
	appendClient("original", "request")
	window, err := log.LoadWindow(ctx, clock.now(), clock.now().Add(time.Second))
	if err != nil || len(window.ClientObservations) != 0 || len(window.PathSamples) != 0 {
		t.Fatalf("restart counted a duplicate as a new observation: %+v %v", window, err)
	}
	// Old duplicate reports must not refresh the original identities at restore.
	clock.set(observationTestHour.Add(62 * time.Second))
	reopen()
	appendClient("original", "request")
	window, err = log.LoadWindow(ctx, clock.now(), clock.now().Add(time.Second))
	if err != nil || len(window.ClientObservations) != 1 || len(window.PathSamples) != 0 ||
		window.ClientObservations[0].WorkloadUID != "" || len(log.clientSeen) != 4 {
		t.Fatalf("expiry, alias restoration or unknown join changed: %+v %v identities=%d", window, err, len(log.clientSeen))
	}
	raw, err := log.ClientObservationPage(ctx, observationTestHour, clock.now().Add(time.Second), 100, 0)
	if err != nil || len(raw.Observations) != 6 {
		t.Fatalf("restart/expiry changed immutable raw reports: %+v %v", raw, err)
	}
	clock.set(observationTestHour.Add(125 * time.Second))
	appendClient("fresh", "")
	if len(log.clientSeen) != 1 || len(log.clientExpiry) != 1 || log.clientExpiryHead != 0 {
		t.Fatalf("expired identity storage retained: ids=%d queue=%d head=%d", len(log.clientSeen), len(log.clientExpiry), log.clientExpiryHead)
	}
}

func TestClientIdentityExpiryCompactsBackingStorage(t *testing.T) {
	log := &ObservationLog{}
	at := observationTestHour
	for index := range 64 {
		log.rememberClientIdentityLocked(clientObservationIdentity{workload: "workload", id: string(rune('a' + index))}, at)
	}
	log.rememberClientIdentityLocked(clientObservationIdentity{workload: "workload", id: "retained"}, at.Add(30*time.Second))
	log.pruneClientIdentitiesLocked(at.Add(time.Second))
	if len(log.clientSeen) != 1 || len(log.clientExpiry) != 1 || cap(log.clientExpiry) > 4 || log.clientExpiryHead != 0 {
		t.Fatalf("expiry left a large dead backing array: ids=%d queue=%d cap=%d head=%d", len(log.clientSeen), len(log.clientExpiry), cap(log.clientExpiry), log.clientExpiryHead)
	}
}
