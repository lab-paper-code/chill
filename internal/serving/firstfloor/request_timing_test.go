package firstfloor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
)

const timingScenarioCancel = "cancel"

func TestWorkerHTTPTimingAttributesBlockedExchange(t *testing.T) {
	for _, scenario := range []string{"success", "decode", "failure", timingScenarioCancel} {
		t.Run(scenario, func(t *testing.T) {
			definition := &serving.Definition{UID: "uid", Generation: 1, Model: serving.Model{UID: "model", Generation: 1}}
			entered, release := make(chan time.Time, 1), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				entered <- time.Now()
				<-release
				if scenario == timingScenarioCancel {
					return
				}
				if scenario == "decode" {
					_, _ = w.Write([]byte(`{`))
					return
				}
				if scenario == "failure" {
					w.WriteHeader(http.StatusServiceUnavailable)
					_ = json.NewEncoder(w).Encode(native.ErrorResponse{Error: "worker unavailable", ExecutionComplete: true})
					return
				}
				p := native.Progress{RequestID: "request", WorkloadUID: definition.UID, Definition: definition.Identity(), Phase: "finished", At: time.Now().UTC(), BatchID: 1, BatchSize: 1, ProfileID: "profile", ExecutionComplete: true}
				_ = json.NewEncoder(w).Encode(native.Response{ID: "request", ModelName: "model", ExecutionComplete: true, Outputs: []native.Field{{Name: "answer", Data: [][]byte{[]byte("yes")}}}, Progress: p})
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			origin := time.Now()
			type completion struct {
				result serving.BackendResult
				err    error
			}
			done := make(chan completion, 1)
			go func() {
				result, err := (&Client{HTTPClient: server.Client()}).Execute(ctx, Spec{Endpoint: server.URL, ModelName: "model", Profile: &serving.Profile{ID: "profile"}}, serving.Dispatch{RequestID: "request", Definition: definition, Input: &serving.InferenceInput{}, ReceivedAt: origin})
				done <- completion{result, err}
			}()
			arrived := <-entered
			select {
			case <-done:
				t.Fatal("exchange returned before worker response")
			default:
			}
			released := time.Now()
			if scenario == timingScenarioCancel {
				cancel()
			}
			close(release)
			completed := <-done
			start, hasStart := completed.result.ServerTiming.Elapsed(serving.ServerWorkerHTTPStart)
			end, hasEnd := completed.result.ServerTiming.Elapsed(serving.ServerWorkerHTTPDone)
			if !hasStart || !hasEnd || start > arrived.Sub(origin) || end < released.Sub(origin) || end-start != completed.result.ClientSpan {
				t.Fatalf("worker wait escaped HTTP span: start=%s end=%s span=%s presence=%v/%v", start, end, completed.result.ClientSpan, hasStart, hasEnd)
			}
			for _, stage := range []serving.ServerStage{serving.ServerWorkerHTTPGetConn, serving.ServerWorkerHTTPGotConn, serving.ServerWorkerHTTPConnectStart, serving.ServerWorkerHTTPConnectDone} {
				at, present := completed.result.ServerTiming.Elapsed(stage)
				if !present || at < start || at > end {
					t.Fatalf("reached HTTP trace stage %d missing or outside exchange: %s, %v", stage, at, present)
				}
			}
			if scenario != timingScenarioCancel {
				at, present := completed.result.ServerTiming.Elapsed(serving.ServerWorkerHTTPFirstResponseByte)
				if !present || at < released.Sub(origin) || at > end {
					t.Fatalf("first response byte escaped response wait: %s, %v", at, present)
				}
			}
			if (completed.err == nil) != (scenario == "success") {
				t.Fatalf("scenario %s error=%v", scenario, completed.err)
			}
			if scenario == timingScenarioCancel && completed.result.Finished {
				t.Fatal("canceled transport became execution completion")
			}
		})
	}
}

func TestWorkerTransportFailureRetainsHTTPBoundary(t *testing.T) {
	origin := time.Now()
	result, err := (&Client{HTTPClient: &http.Client{Transport: failingTransport{}}}).Execute(context.Background(), Spec{Endpoint: "http://native", ModelName: "model", Profile: &serving.Profile{ID: "profile"}}, serving.Dispatch{RequestID: "request", Definition: &serving.Definition{UID: "uid"}, Input: &serving.InferenceInput{}, ReceivedAt: origin})
	start, hasStart := result.ServerTiming.Elapsed(serving.ServerWorkerHTTPStart)
	end, hasEnd := result.ServerTiming.Elapsed(serving.ServerWorkerHTTPDone)
	if err == nil || result.Finished || !hasStart || !hasEnd || end-start != result.ClientSpan {
		t.Fatalf("transport failure lost reached boundaries: result=%+v err=%v", result, err)
	}
}

type traceTransport func(*http.Request) (*http.Response, error)

func (f traceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWorkerHTTPConcurrentAndLateTraceHooks(t *testing.T) {
	var captured *httptrace.ClientTrace
	transport := traceTransport(func(r *http.Request) (*http.Response, error) {
		captured = httptrace.ContextClientTrace(r.Context())
		if captured == nil {
			t.Fatal("native request has no HTTP trace")
		}
		var hooks sync.WaitGroup
		for range 32 {
			hooks.Add(1)
			go func() {
				defer hooks.Done()
				captured.GetConn("native:80")
				captured.ConnectStart("tcp", "native:80")
				captured.ConnectDone("tcp", "native:80", errors.New("dial failed"))
				captured.GotConn(httptrace.GotConnInfo{})
				captured.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("write failed")})
				captured.GotFirstResponseByte()
			}()
		}
		hooks.Wait()
		return nil, errors.New("transport failed")
	})
	result, err := (&Client{HTTPClient: &http.Client{Transport: transport}}).Execute(context.Background(),
		Spec{Endpoint: "http://native", ModelName: "model", Profile: &serving.Profile{ID: "profile"}},
		serving.Dispatch{RequestID: "request", Definition: &serving.Definition{UID: "uid"}, Input: &serving.InferenceInput{}, ReceivedAt: time.Now()})
	if err == nil || result.Finished {
		t.Fatalf("failed exchange changed execution proof: %+v, %v", result, err)
	}
	before := result.ServerTiming
	captured.GetConn("late:80")
	captured.GotFirstResponseByte()
	if result.ServerTiming != before {
		t.Fatal("late trace hook mutated returned timing snapshot")
	}
	start, _ := before.Elapsed(serving.ServerWorkerHTTPStart)
	end, _ := before.Elapsed(serving.ServerWorkerHTTPDone)
	for _, stage := range []serving.ServerStage{serving.ServerWorkerHTTPGetConn, serving.ServerWorkerHTTPGotConn, serving.ServerWorkerHTTPConnectStart, serving.ServerWorkerHTTPConnectDone, serving.ServerWorkerHTTPWroteRequest, serving.ServerWorkerHTTPFirstResponseByte} {
		at, present := before.Elapsed(stage)
		if !present || at < start || at > end {
			t.Fatalf("concurrent hook %d lost: %s, %v", stage, at, present)
		}
	}
}

func TestWorkerHTTPReusedConnectionHasNoInventedConnect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(native.ErrorResponse{Error: "unavailable", ExecutionComplete: true})
	}))
	defer server.Close()
	client := &Client{HTTPClient: server.Client()}
	for attempt := range 2 {
		result, err := client.Execute(context.Background(), Spec{Endpoint: server.URL, ModelName: "model", Profile: &serving.Profile{ID: "profile"}},
			serving.Dispatch{RequestID: "request", Definition: &serving.Definition{UID: "uid"}, Input: &serving.InferenceInput{}, ReceivedAt: time.Now()})
		if err == nil {
			t.Fatal("native failure unexpectedly succeeded")
		}
		for _, stage := range []serving.ServerStage{serving.ServerWorkerHTTPGetConn, serving.ServerWorkerHTTPGotConn, serving.ServerWorkerHTTPWroteRequest, serving.ServerWorkerHTTPFirstResponseByte} {
			if _, present := result.ServerTiming.Elapsed(stage); !present {
				t.Fatalf("attempt %d missing trace stage %d", attempt, stage)
			}
		}
		for _, stage := range []serving.ServerStage{serving.ServerWorkerHTTPConnectStart, serving.ServerWorkerHTTPConnectDone} {
			if _, present := result.ServerTiming.Elapsed(stage); present != (attempt == 0) {
				t.Fatalf("attempt %d connect presence=%v", attempt, present)
			}
		}
	}
}

func TestNativeFailureRetainsTimingWithoutCompletionProof(t *testing.T) {
	now := time.Now().UTC()
	write := int64(17)
	timing := &native.HTTPRequestTiming{HandlerEnteredAt: now, ResponseWriteStartNanos: &write}
	body, err := json.Marshal(native.ErrorResponse{Error: "bad proof", Code: serving.CodeDeadlineInfeasible, NativeHTTP: timing})
	if err != nil {
		t.Fatal(err)
	}
	result, err := nativeFailure(serving.BackendResult{Body: body, StatusCode: http.StatusServiceUnavailable},
		Spec{Profile: &serving.Profile{ID: "profile"}},
		serving.Dispatch{RequestID: "request", Definition: &serving.Definition{UID: "uid", Admission: serving.Admission{Policy: serving.AdmissionPolicyReject}}})
	if err == nil || result.Finished || result.Progress == nil || result.Progress.ExecutionComplete || result.Progress.NativeHTTP == nil || *result.Progress.NativeHTTP.ResponseWriteStartNanos != write {
		t.Fatalf("failure timing changed proof or disappeared: %+v, %v", result, err)
	}
}

func TestProgressCarriesNativeHTTPAndFirstDeferredAt(t *testing.T) {
	at := time.Now().UTC()
	decode := int64(0)
	for _, phase := range []string{"deferred", "running", "finished"} {
		p := native.Progress{RequestID: "request", WorkloadUID: "uid", Definition: "definition", Phase: phase, At: at, ExecutionComplete: phase == "finished",
			NativeHTTP: &native.HTTPRequestTiming{HandlerEnteredAt: at, DecodeStartNanos: &decode}, NativeDeferredAt: &at}
		body, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		converted, err := ParseProgress(append([]byte("CHILL_PROGRESS "), body...))
		if err != nil || converted.NativeHTTP == nil || converted.NativeHTTP.DecodeStartNanos == nil || *converted.NativeHTTP.DecodeStartNanos != 0 || converted.NativeDeferredAt == nil || !converted.NativeDeferredAt.Equal(at) {
			t.Fatalf("%s progress lost native boundaries: %+v, %v", phase, converted, err)
		}
	}
}

func TestWorkerHTTPDoneWaitsForFullResponseBody(t *testing.T) {
	bodyStarted, release := make(chan time.Time, 1), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"unavailable",`))
		w.(http.Flusher).Flush()
		bodyStarted <- time.Now()
		<-release
		_, _ = w.Write([]byte(`"executionComplete":true}`))
	}))
	defer server.Close()
	origin := time.Now()
	done := make(chan serving.BackendResult, 1)
	go func() {
		result, _ := (&Client{HTTPClient: server.Client()}).Execute(context.Background(),
			Spec{Endpoint: server.URL, ModelName: "model", Profile: &serving.Profile{ID: "profile"}},
			serving.Dispatch{RequestID: "request", Definition: &serving.Definition{UID: "uid"}, Input: &serving.InferenceInput{}, ReceivedAt: origin})
		done <- result
	}()
	<-bodyStarted
	select {
	case <-done:
		close(release)
		t.Fatal("HTTP exchange ended at headers instead of full body")
	default:
	}
	released := time.Now()
	close(release)
	result := <-done
	first, hasFirst := result.ServerTiming.Elapsed(serving.ServerWorkerHTTPFirstResponseByte)
	end, hasEnd := result.ServerTiming.Elapsed(serving.ServerWorkerHTTPDone)
	start, hasStart := result.ServerTiming.Elapsed(serving.ServerWorkerHTTPStart)
	if !hasFirst || !hasStart || !hasEnd || first > end || end < released.Sub(origin) || end-start != result.ClientSpan {
		t.Fatalf("full-read boundary lost: first=%s start=%s end=%s span=%s", first, start, end, result.ClientSpan)
	}
}
