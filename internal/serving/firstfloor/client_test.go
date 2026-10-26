package firstfloor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
)

func TestNativeCompletionRequiresContainedMonotonicSpans(t *testing.T) {
	definition := &serving.Definition{UID: "uid", Generation: 1, Model: serving.Model{UID: "model", Generation: 1}, SLO: serving.SLO{Percentile: 95, TargetLatency: time.Second}}
	for _, tc := range []struct {
		name             string
		backend, elapsed float64
		valid            bool
	}{
		{"backend inside service", 3, 8, true},
		{"backend exceeds service", 5, 8, false},
		{"service exceeds elapsed", 3, 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := native.Progress{RequestID: "request", WorkloadUID: "uid", Definition: definition.Identity(), Phase: "finished", At: time.Now().UTC(), BatchID: 7, BatchSize: 1, ProfileID: "profile", QueueMS: 2, ServiceMS: 4, BackendMS: tc.backend, NativeElapsedMS: tc.elapsed, ExecutionComplete: true}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if err := json.NewEncoder(w).Encode(native.Response{ModelName: "model", ID: "request", ExecutionComplete: true, Outputs: []native.Field{{Name: "answer", Data: [][]byte{[]byte("yes")}}}, Progress: p}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			result, err := (&Client{HTTPClient: server.Client()}).Execute(context.Background(), Spec{Endpoint: server.URL, ModelName: "model", Profile: &serving.Profile{ID: "profile"}}, serving.Dispatch{RequestID: "request", Definition: definition, ReceivedAt: time.Now(), Input: &serving.InferenceInput{}})
			if tc.valid {
				if err != nil || !result.Finished {
					t.Fatalf("valid nested spans lost completion: %+v %v", result, err)
				}
			} else if err == nil || result.Finished {
				t.Fatalf("impossible spans became physical proof: %+v %v", result, err)
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ParseProgress(append([]byte("CHILL_PROGRESS "), raw...))
			if (err == nil) != tc.valid {
				t.Fatalf("progress and response disagree on contained spans: %v", err)
			}
		})
	}
}
func TestNativeTransportFailureDoesNotCompletePhysicalExecution(t *testing.T) {
	client := &Client{HTTPClient: &http.Client{Transport: failingTransport{}}}
	result, err := client.Execute(context.Background(), Spec{Endpoint: "http://native", ModelName: "model", Profile: &serving.Profile{ID: "profile"}}, serving.Dispatch{RequestID: "request", Definition: &serving.Definition{UID: "uid"}, Input: &serving.InferenceInput{}})
	if err == nil || result.Finished {
		t.Fatal("HTTP transport failure became physical completion proof")
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection lost after write")
}
func TestProgressUsesDirectIdentityAndExplicitFinish(t *testing.T) {
	p := native.Progress{RequestID: "request", WorkloadUID: "uid", Definition: "uid/1/model/1", Phase: "finished", At: time.Now().UTC(), ExecutionComplete: true, BatchSize: 4}
	raw, _ := json.Marshal(p)
	observed, err := ParseProgress(append([]byte("2026-10-08T12:00:00Z CHILL_PROGRESS "), raw...))
	if err != nil || observed.RequestID != "request" || !observed.ExecutionComplete {
		t.Fatalf("direct native identity lost: %+v %v", observed, err)
	}
	p.ExecutionComplete = false
	raw, _ = json.Marshal(p)
	if _, err = ParseProgress(append([]byte("CHILL_PROGRESS "), raw...)); err == nil {
		t.Fatal("unproved finish admitted")
	}
	if _, err = ParseProgress([]byte("startup")); !errors.Is(err, ErrNotProgress) {
		t.Fatal("startup was parsed as native progress")
	}
}

func TestRejectedProgressRetainsDiagnosticIdentity(t *testing.T) {
	for _, raw := range []string{
		`{"requestID":"rejected","phase":"running"}`,
		`{"requestID":"rejected","phase":"running","unknown":true}`,
		`{"requestID":"rejected","phase":"running","workloadUID":"uid","definition":"def","at":"2026-10-27T00:00:00Z","backendMS":2,"serviceMS":1}`,
	} {
		p, err := ParseProgress([]byte("CHILL_PROGRESS " + raw))
		if err == nil || p.RequestID != "rejected" || p.Phase != "running" {
			t.Fatalf("rejection lost its diagnostic identity: %+v / %v", p, err)
		}
	}
}
func TestDeferredProgressRemainsQueuedWithoutCompletion(t *testing.T) {
	p := native.Progress{RequestID: "request", WorkloadUID: "uid", Definition: "uid/1/model/1",
		Phase: "deferred", At: time.Now().UTC(), QueueMS: 25, NativeElapsedMS: 25}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := ParseProgress(append([]byte("CHILL_PROGRESS "), raw...))
	if err != nil || observed.Phase != "deferred" || observed.ExecutionComplete || observed.BatchID != 0 {
		t.Fatalf("deferred queue must not become execution completion: %+v / %v", observed, err)
	}
}

func TestDeadlineDropRequiresCorrelatedNoExecutionProof(t *testing.T) {
	definition := &serving.Definition{UID: "uid", Generation: 1,
		Model: serving.Model{UID: "model", Generation: 1}}
	for _, uid := range []string{"uid", "another-registration"} {
		t.Run(uid, func(t *testing.T) {
			p := &native.Progress{RequestID: "request", WorkloadUID: uid, Definition: definition.Identity(),
				Phase: "finished", At: time.Now().UTC(), ProfileID: "profile", Outcome: "deadline_drop",
				QueueMS: 20, NativeElapsedMS: 20, ExecutionComplete: true}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusRequestTimeout)
				if err := json.NewEncoder(w).Encode(native.ErrorResponse{
					Error: "SLO deadline reached", ExecutionComplete: true, Progress: p}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			result, err := (&Client{HTTPClient: server.Client()}).Execute(context.Background(),
				Spec{Endpoint: server.URL, ModelName: "model", Profile: &serving.Profile{ID: "profile"}},
				serving.Dispatch{RequestID: "request", Definition: definition, Input: &serving.InferenceInput{}})
			var domain *serving.Error
			if uid == "uid" {
				if !errors.As(err, &domain) || domain.Code != "SLODeadlineDrop" || !result.Finished ||
					result.StatusCode != http.StatusRequestTimeout || result.Progress == nil ||
					result.Progress.Outcome != "deadline_drop" {
					t.Fatalf("verified drop must retain timeout and physical completion: %+v / %v", result, err)
				}
			} else if err == nil || result.Finished {
				t.Fatalf("another registration cannot release execution: %+v / %v", result, err)
			}
		})
	}
}

func TestModuleFailuresBecomeFinishedRequestOutcomes(t *testing.T) {
	definition := &serving.Definition{UID: "uid", Generation: 1, Model: serving.Model{UID: "model", Generation: 1}}
	for _, tc := range []struct {
		status        int
		outcome, code string
	}{
		{http.StatusUnprocessableEntity, native.OutcomePreprocessFailed, "PreprocessFailed"},
		{http.StatusInternalServerError, native.OutcomeBatchError, "BackendError"},
		{http.StatusUnprocessableEntity, native.OutcomeBatchError, ""},
	} {
		t.Run(tc.outcome+"/"+http.StatusText(tc.status), func(t *testing.T) {
			p := &native.Progress{RequestID: "request", WorkloadUID: "uid", Definition: definition.Identity(), Phase: "finished", At: time.Now().UTC(),
				ProfileID: "profile", Outcome: tc.outcome, BatchID: 3, BatchSize: 2, QueueMS: 1, ServiceMS: 6, PreprocessMS: 2, BackendMS: 3, PostprocessMS: 1, NativeElapsedMS: 8, ExecutionComplete: true}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request native.Request
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Inputs) != 1 || request.Inputs[0].Name != "text" || len(request.Inputs[0].Data) != 1 || string(request.Inputs[0].Data[0]) != "hello" {
					t.Errorf("raw sample was not forwarded as one native field element: %+v %v", request, err)
				}
				w.WriteHeader(tc.status)
				if err := json.NewEncoder(w).Encode(native.ErrorResponse{Error: "module rejected sample", ExecutionComplete: true, Progress: p}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			result, err := (&Client{HTTPClient: server.Client()}).Execute(context.Background(),
				Spec{Endpoint: server.URL, ModelName: "model", Profile: &serving.Profile{ID: "profile"}},
				serving.Dispatch{RequestID: "request", Definition: definition, Input: &serving.InferenceInput{Inputs: []serving.RawInput{{Name: "text", Datatype: "BYTES", Data: []byte("hello")}}}})
			var domain *serving.Error
			if tc.code == "" {
				if errors.As(err, &domain) || result.Finished {
					t.Fatalf("outcome inconsistent with status must not release execution: %+v / %v", result, err)
				}
				return
			}
			if !errors.As(err, &domain) || domain.Code != tc.code || domain.Message != "module rejected sample" || !result.Finished ||
				result.Progress == nil || result.Progress.Outcome != tc.outcome || result.Progress.PreprocessMS != 2 || result.Progress.PostprocessMS != 1 {
				t.Fatalf("module failure must finish with its reason and stage times: %+v / %v", result, err)
			}
		})
	}
}
func TestMismatchedNativeCompletionRemainsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.WriteString(w, `{"model_name":"model","id":"other","outputs":[],"progress":{"requestID":"other","phase":"finished","executionComplete":true},"executionComplete":true}`); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	result, err := (&Client{HTTPClient: server.Client()}).Execute(context.Background(), Spec{Endpoint: server.URL, ModelName: "model", Profile: &serving.Profile{ID: "profile"}}, serving.Dispatch{RequestID: "request", Definition: &serving.Definition{UID: "uid"}, Input: &serving.InferenceInput{}})
	if err == nil || result.Finished || !strings.Contains(err.Error(), "completion proof") {
		t.Fatal("mismatched response freed physical ownership", result, err)
	}
}

func TestNativeDispatchPreservesOriginalLifetimeSenderElapsedAndMargin(t *testing.T) {
	seen := make(chan native.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request native.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		seen <- request
		http.Error(w, "execution not started", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	received := time.Now().Add(-250 * time.Millisecond)
	result, err := (&Client{HTTPClient: server.Client()}).Execute(context.Background(),
		Spec{Endpoint: server.URL, ModelName: "model", Profile: &serving.Profile{ID: "profile"}},
		serving.Dispatch{
			RequestID: "request", Definition: &serving.Definition{UID: "uid", SLO: serving.SLO{Percentile: 95, TargetLatency: time.Second}},
			Input: &serving.InferenceInput{}, ReceivedAt: received, Deadline: received.Add(2 * time.Second),
			Elapsed: 250 * time.Millisecond, CompletionMarginMS: 29,
		})
	request := <-seen
	if request.Context.LifetimeMS != 2000 || request.Context.SenderElapsedMS != 250 ||
		request.Context.CompletionMarginMS != 29 || request.Context.SLO.TargetMS != 1000 ||
		request.Context.RequestID != "request" || request.Context.WorkloadUID != "uid" {
		t.Fatalf("native transport rebased or mixed lifetime/SLO/m: %+v", request.Context)
	}
	if err == nil || result.Finished {
		t.Fatal("unexecuted transport failure completed native ownership")
	}
}

func TestNativeInitializationMatchesRecordedRuntimeNotMeasuredCurve(t *testing.T) {
	profile := &serving.Profile{
		Batches:         []serving.BatchProfile{{Batch: 1}},
		RuntimeMetadata: &native.RuntimeMetadata{InitializedBatches: []int{1, 2}},
	}
	profile.Condition.Preprocessor.Inputs = []string{"image"}
	profile.Condition.ActiveProtocol.WarmupIterations = 2
	for _, tc := range []struct {
		name    string
		batches []int
		valid   bool
	}{
		{"recorded initialization exceeds measured curve", []int{1, 2}, true},
		{"unrecorded initialized batch", []int{1, 3}, false},
		{"initialization reduced to measured curve", []int{1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual := native.Metadata{
				Runtime: native.RuntimeMetadata{
					Inputs:             []string{"image"},
					InitializedBatches: tc.batches,
					WarmupIterations:   2,
				},
				MaxBatchSize: 2,
				Policy:       native.Policy{MaxBatchSize: 1},
			}
			if err := validateProfileInputs(Spec{Profile: profile}, actual); (err == nil) != tc.valid {
				t.Fatalf("recorded initialization and measured policy domain conflated: %v", err)
			}
		})
	}
}
