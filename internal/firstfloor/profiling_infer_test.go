package firstfloor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Decode the wire contract independently of native's added response fields.
type profilingHTTPProgress struct {
	Progress
	BatchRequestIDs []string   `json:"batchRequestIDs"`
	BatchStartedAt  *time.Time `json:"batchStartedAt"`
	BatchFinishedAt *time.Time `json:"batchFinishedAt"`
}

type profilingHTTPResponse struct {
	Response
	Progress profilingHTTPProgress `json:"progress"`
}

// postFixedInference sends independent requests through the normal HTTP handler.
func postFixedInference(h http.Handler, values ...string) []*httptest.ResponseRecorder {
	replies := make([]*httptest.ResponseRecorder, len(values))
	done := make(chan int, len(values))
	for i, value := range values {
		go func(i int, value string) {
			id := fmt.Sprintf("input-%d", i)
			replies[i] = postJSON(h, "/v2/models/synthetic/infer", Request{
				ID: id, Context: RequestContext{RequestID: id, WorkloadUID: "physical-trial", Definition: "fixed/1", AdmissionPolicy: AdmissionReject,
					ReceivedAt: time.Now().UTC(), SLO: SLO{Percentile: 95, TargetMS: 1}}, Inputs: rawInputs(value)})
			done <- i
		}(i, value)
	}
	for range values {
		<-done
	}
	return replies
}

func TestProfilingInferWithoutBundleUsesFixedBatchHTTP(t *testing.T) {
	s, cfg := startTestActor(t, modeProfiling, "4", func(c *Config) {
		c.Policy = Policy{FixedBatchSize: 2}
		c.ProfilePath = filepath.Join(t.TempDir(), "missing-bundle.json")
		path := filepath.Join(t.TempDir(), "config.json")
		data, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		loaded, err := ReadConfig(path)
		if err != nil {
			t.Fatalf("profiling must start without a profile bundle: %v", err)
		}
		*c = *loaded
	})
	if cfg.Policy.Batches != nil {
		t.Fatal("profiling loaded service distributions")
	}
	server := httptest.NewServer(NewHandler(s))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	type reply struct {
		response profilingHTTPResponse
		err      error
		status   int
	}
	send := func(id, value string) <-chan reply {
		done := make(chan reply, 1)
		body, err := json.Marshal(Request{ID: id, Context: RequestContext{RequestID: id, WorkloadUID: "physical-trial", Definition: "fixed/1", AdmissionPolicy: AdmissionReject,
			ReceivedAt: time.Now().UTC(), SLO: SLO{Percentile: 95, TargetMS: 1}}, Inputs: rawInputs(value)})
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			response, err := client.Post(server.URL+"/v2/models/synthetic/infer", "application/json", bytes.NewReader(body))
			if err != nil {
				done <- reply{err: err}
				return
			}
			var result profilingHTTPResponse
			err = json.NewDecoder(response.Body).Decode(&result)
			if closeErr := response.Body.Close(); err == nil {
				err = closeErr
			}
			done <- reply{result, err, response.StatusCode}
		}()
		return done
	}
	var previous *ServiceSample
	var lastBatch uint64
	for batch := range 2 {
		ids := []string{fmt.Sprintf("batch-%d-a", batch), fmt.Sprintf("batch-%d-b", batch)}
		first := send(ids[0], "a")
		state := awaitActorState(t, s, func(state State) bool { return len(state.Requests) == 1 })
		if state.Executions != uint64(batch) || state.Requests[0].Phase != phaseQueued {
			t.Fatalf("B-1 must wait without executing: %+v", state)
		}
		second := send(ids[1], "b+c")
		var replies []profilingHTTPResponse
		for i, done := range []<-chan reply{first, second} {
			result := <-done
			if result.err != nil || result.status != http.StatusOK || !result.response.ExecutionComplete || result.response.ID != ids[i] {
				t.Fatalf("fixed profiling inference failed: %+v", result)
			}
			wantValue := []string{"a", "b+c"}[i]
			if !reflect.DeepEqual(result.response.Outputs, echoOutputs(wantValue)) || result.response.Progress.Units != i+1 {
				t.Fatalf("per-request output or units lost: %+v", result.response)
			}
			p := result.response.Progress
			if p.BatchID == 0 || p.BatchSize != 2 || p.ServiceSample == nil || p.ServiceSample.ServiceDurationNanos <= 0 || p.ServiceSample.TrialID != "physical-trial" {
				t.Fatalf("physical batch identity or service sample lost: %+v", p)
			}
			if !reflect.DeepEqual(p.BatchRequestIDs, ids) || p.BatchStartedAt == nil || p.BatchFinishedAt == nil || p.BatchStartedAt.Location() != time.UTC || p.BatchFinishedAt.Location() != time.UTC || !p.BatchFinishedAt.After(*p.BatchStartedAt) {
				t.Fatalf("batch membership or UTC exchange anchors lost: %+v", p)
			}
			serviceMS := milliseconds(time.Duration(p.ServiceSample.ServiceDurationNanos))
			if p.ServiceMS != serviceMS || p.PreprocessMS+p.BackendMS+p.PostprocessMS > serviceMS || p.NativeElapsedMS < serviceMS {
				t.Fatalf("batch stage spans must nest inside native request: %+v", p)
			}
			replies = append(replies, result.response)
		}
		if replies[0].Progress.BatchID != replies[1].Progress.BatchID || !reflect.DeepEqual(replies[0].Progress.ServiceSample, replies[1].Progress.ServiceSample) || replies[0].Progress.BatchID <= lastBatch {
			t.Fatalf("requests must share one new physical batch: %+v", replies)
		}
		sample := replies[0].Progress.ServiceSample
		if previous == nil && sample.PrecedingIdleNanos != nil || previous != nil && (sample.PrecedingIdleNanos == nil || *sample.PrecedingIdleNanos < 0) {
			t.Fatalf("preceding idle must use actual exchange anchors: %+v", sample)
		}
		previous, lastBatch = sample, replies[0].Progress.BatchID
	}
	state := awaitActorState(t, s, func(state State) bool { return state.CompletedRequests == 4 })
	if state.Executions != 2 || state.AcceptedRequests != 4 {
		t.Fatalf("counters must count inputs and physical batches separately: %+v", state)
	}
	response, err := client.Post(server.URL+"/v2/models/synthetic/profile", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted profile endpoint returned %d", response.StatusCode)
	}
}

func TestServingStillRequiresProfileBundle(t *testing.T) {
	cfg := Config{Model: "synthetic", Mode: modeServing, Inputs: []string{"input"}, Outputs: []string{"output"}, RunnerCommand: []string{"runner"}, RunnerTimeoutMS: 1000,
		MaxQueueRequests: 2, MaxQueueBytes: 32, MaxRequestBytes: 32, MaxBatchSize: 2, BatchSizes: []int{1, 2}, ProfilePath: filepath.Join(t.TempDir(), "missing.json"), Policy: Policy{FixedBatchSize: 2}}
	path := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(path); !os.IsNotExist(err) {
		t.Fatalf("serving must reject a missing profile bundle: %v", err)
	}
	if err := ValidateConfig(&cfg); err == nil {
		t.Fatal("serving accepted an unmeasured fixed batch")
	}
}

func TestProfilingInferPreservesPerRequestFailures(t *testing.T) {
	s, _ := startTestActor(t, modeProfiling, "0", nil)
	replies := postFixedInference(NewHandler(s), "a", "fail:bad jpeg")
	var success profilingHTTPResponse
	var failure struct {
		ErrorResponse
		Progress *profilingHTTPProgress `json:"progress"`
	}
	if replies[0].Code != http.StatusOK || json.Unmarshal(replies[0].Body.Bytes(), &success) != nil ||
		!reflect.DeepEqual(success.Outputs, echoOutputs("a")) {
		t.Fatalf("one rejected sample must not lose its peer's result: %d %s", replies[0].Code, replies[0].Body.String())
	}
	if replies[1].Code != http.StatusUnprocessableEntity || json.Unmarshal(replies[1].Body.Bytes(), &failure) != nil ||
		!failure.ExecutionComplete || failure.Progress == nil || failure.Progress.Outcome != OutcomePreprocessFailed {
		t.Fatalf("sample rejection must retain normal inference failure: %d %s", replies[1].Code, replies[1].Body.String())
	}
	if success.Progress.BatchID != failure.Progress.BatchID || !reflect.DeepEqual(success.Progress.BatchRequestIDs, failure.Progress.BatchRequestIDs) ||
		!reflect.DeepEqual(success.Progress.ServiceSample, failure.Progress.ServiceSample) {
		t.Fatalf("failed sample must retain physical batch membership and exchange: %+v %+v", success.Progress, failure.Progress)
	}
}
