package firstfloor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
