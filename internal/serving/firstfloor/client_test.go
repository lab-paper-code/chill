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
	"github.com/lab-paper-code/chill/internal/modelcompat"
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

func TestActorRejectionsRequireCorrelatedNoExecutionProof(t *testing.T) {
	const another = "another"
	definition := &serving.Definition{UID: "uid", Generation: 1,
		Model: serving.Model{UID: "model", Generation: 1}, Admission: serving.Admission{Policy: serving.AdmissionPolicyReject}}
	for _, code := range []string{serving.CodeDeadlineInfeasible, serving.CodeQueueFull} {
		for _, tc := range []struct {
			name   string
			mutate func(*native.ErrorResponse)
		}{
			{"valid", func(*native.ErrorResponse) {}},
			{"wrong request", func(f *native.ErrorResponse) { f.Progress.RequestID = another }},
			{"wrong workload", func(f *native.ErrorResponse) { f.Progress.WorkloadUID = another }},
			{"wrong definition", func(f *native.ErrorResponse) { f.Progress.Definition = another }},
			{"wrong profile", func(f *native.ErrorResponse) { f.Progress.ProfileID = another }},
			{"wrong outcome", func(f *native.ErrorResponse) { f.Progress.Outcome = another }},
			{"missing timestamp", func(f *native.ErrorResponse) { f.Progress.At = time.Time{} }},
			{"not finished", func(f *native.ErrorResponse) { f.Progress.Phase = phaseRunning }},
			{"incomplete progress", func(f *native.ErrorResponse) { f.Progress.ExecutionComplete = false }},
			{"incomplete response", func(f *native.ErrorResponse) { f.ExecutionComplete = false }},
			{"missing progress", func(f *native.ErrorResponse) { f.Progress = nil }},
			{"batch identity", func(f *native.ErrorResponse) { f.Progress.BatchID = 1 }},
			{"batch execution", func(f *native.ErrorResponse) { f.Progress.BatchSize = 1 }},
			{"service execution", func(f *native.ErrorResponse) { f.Progress.ServiceMS = 1 }},
			{"backend execution", func(f *native.ErrorResponse) { f.Progress.BackendMS = 1 }},
			{"preprocess execution", func(f *native.ErrorResponse) { f.Progress.PreprocessMS = 1 }},
			{"postprocess execution", func(f *native.ErrorResponse) { f.Progress.PostprocessMS = 1 }},
			{"token execution", func(f *native.ErrorResponse) { f.Progress.FirstTokenMS = 1 }},
			{"output units", func(f *native.ErrorResponse) { f.Progress.Units = 1 }},
			{"invalid spans", func(f *native.ErrorResponse) { f.Progress.QueueMS = -1 }},
			{"service sample", func(f *native.ErrorResponse) { f.Progress.ServiceSample = &native.ServiceSample{} }},
			{"wrong code status", func(f *native.ErrorResponse) {
				if f.Code == serving.CodeQueueFull {
					f.Code = serving.CodeDeadlineInfeasible
				} else {
					f.Code = serving.CodeQueueFull
				}
			}},
			{"wrong policy", func(f *native.ErrorResponse) { f.Policy = "Delay" }},
			{"missing policy", func(f *native.ErrorResponse) { f.Policy = "" }},
			{"wrong stage", func(f *native.ErrorResponse) { f.Stage = serving.AdmissionStageGateway }},
			{"missing remaining SLO", func(f *native.ErrorResponse) { f.RemainingSLONanos = nil }},
			{"missing or unexpected wait", func(f *native.ErrorResponse) {
				if f.ExecutorWaitNanos == nil {
					f.ExecutorWaitNanos = new(int64)
				} else {
					f.ExecutorWaitNanos = nil
				}
			}},
			{"mismatched progress wait", func(f *native.ErrorResponse) { f.Progress.ExecutorWaitNanos = new(int64) }},
			{"negative wait", func(f *native.ErrorResponse) {
				wait := int64(-1)
				f.ExecutorWaitNanos, f.Progress.ExecutorWaitNanos = &wait, &wait
			}},
			{"not admitted actor", func(f *native.ErrorResponse) { f.NotAdmitted = true }},
		} {
			t.Run(code+"/"+tc.name, func(t *testing.T) {
				wait, remaining := int64(20*time.Millisecond), -int64(time.Millisecond)
				p := &native.Progress{RequestID: "request", WorkloadUID: "uid", Definition: definition.Identity(),
					Phase: "finished", At: time.Now().UTC(), ProfileID: "profile", Outcome: native.OutcomeDeadlineDrop,
					QueueMS: 20, NativeElapsedMS: 20, ExecutorWaitNanos: &wait, ExecutionComplete: true}
				f := native.ErrorResponse{Error: "executor rejected request", Code: code, Stage: serving.AdmissionStageExecutor,
					Policy: "Reject", RemainingSLONanos: &remaining, ExecutorWaitNanos: &wait, ExecutionComplete: true, Progress: p}
				status := http.StatusServiceUnavailable
				if code == serving.CodeQueueFull {
					status, p.Outcome, p.QueueMS, p.ExecutorWaitNanos, f.ExecutorWaitNanos = http.StatusTooManyRequests, "queue_full", 0, nil, nil
				}
				tc.mutate(&f)
				result, err := executeNativeFailure(t, definition, status, f)
				var domain *serving.Error
				if tc.name != "valid" {
					if err == nil || errors.As(err, &domain) || result.Finished || result.Progress != nil {
						t.Fatalf("invalid rejection released execution: %+v / %v", result, err)
					}
					return
				}
				if !errors.As(err, &domain) || domain.Code != code || domain.Stage != serving.AdmissionStageExecutor ||
					domain.Policy != definition.Admission.Policy || domain.RemainingSLONanos == nil || *domain.RemainingSLONanos != remaining ||
					!result.Finished || result.StatusCode != status || result.Progress == nil || result.Progress.Outcome != p.Outcome {
					t.Fatalf("verified rejection lost structured evidence: %+v / %v", result, err)
				}
				if code == serving.CodeDeadlineInfeasible {
					if domain.ExecutorWaitNanos == nil || *domain.ExecutorWaitNanos != wait ||
						result.Progress.ExecutorWaitNanos == nil || *result.Progress.ExecutorWaitNanos != wait {
						t.Fatal("executor queue wait was lost")
					}
				} else if domain.ExecutorWaitNanos != nil || result.Progress.ExecutorWaitNanos != nil {
					t.Fatal("never-enqueued queue rejection acquired queue wait")
				}
			})
		}
	}
}

func TestSemaphoreQueueFullRequiresExplicitUnadmittedProof(t *testing.T) {
	for _, policy := range []serving.AdmissionPolicy{serving.AdmissionPolicyReject, serving.AdmissionPolicyDelay} {
		for _, tc := range []struct {
			name   string
			mutate func(map[string]any)
		}{
			{"valid", func(map[string]any) {}},
			{"missing marker", func(f map[string]any) { delete(f, "notAdmitted") }},
			{"false marker", func(f map[string]any) { f["notAdmitted"] = false }},
			{"incomplete", func(f map[string]any) { f["executionComplete"] = false }},
			{"missing code", func(f map[string]any) { delete(f, "code") }},
			{"wrong stage", func(f map[string]any) { f["stage"] = serving.AdmissionStageGateway }},
			{"native policy", func(f map[string]any) { f["policy"] = string(policy) }},
			{"native remaining SLO", func(f map[string]any) { f["remainingSLONanos"] = 0 }},
			{"native wait", func(f map[string]any) { f["executorWaitNanos"] = 0 }},
			{"native progress", func(f map[string]any) { f["progress"] = native.Progress{} }},
			{"failure metadata", func(f map[string]any) { f["failureReason"] = native.FailureMemoryAllocation }},
			{"unknown metadata", func(f map[string]any) { f["unknown"] = true }},
		} {
			t.Run(string(policy)+"/"+tc.name, func(t *testing.T) {
				definition := &serving.Definition{UID: "uid", Admission: serving.Admission{Policy: policy}}
				f := map[string]any{"error": "queue full", "code": serving.CodeQueueFull,
					"stage": serving.AdmissionStageExecutor, "notAdmitted": true, "executionComplete": true}
				tc.mutate(f)
				result, err := executeNativeFailure(t, definition, http.StatusTooManyRequests, f)
				var domain *serving.Error
				if tc.name != "valid" {
					if err == nil || errors.As(err, &domain) || result.Finished || result.Progress != nil {
						t.Fatalf("invalid predecode evidence released execution: %+v / %v", result, err)
					}
					return
				}
				if !errors.As(err, &domain) || domain.Code != serving.CodeQueueFull || domain.Stage != serving.AdmissionStageExecutor ||
					domain.Policy != policy || domain.RemainingSLONanos != nil || domain.ExecutorWaitNanos != nil ||
					!result.Finished || result.Progress != nil {
					t.Fatalf("predecode rejection invented native evidence: %+v / %v", result, err)
				}
			})
		}
	}
}

func TestNativeLifetimeTimeoutPreservesPhysicalCompletion(t *testing.T) {
	for _, complete := range []bool{false, true} {
		definition := &serving.Definition{UID: "uid", Admission: serving.Admission{Policy: serving.AdmissionPolicyDelay}}
		result, err := executeNativeFailure(t, definition, http.StatusRequestTimeout, native.ErrorResponse{
			Error: "request lifetime expired", Code: "RequestTimeout", Stage: serving.AdmissionStageExecutor, ExecutionComplete: complete})
		var domain *serving.Error
		if !errors.As(err, &domain) || domain.Code != "RequestTimeout" || domain.Stage != serving.AdmissionStageExecutor ||
			result.Finished != complete || result.Progress != nil {
			t.Fatalf("lifetime timeout changed physical completion or fabricated progress: %+v / %v", result, err)
		}
	}
}

func executeNativeFailure(t *testing.T, definition *serving.Definition, status int, failure any) (serving.BackendResult, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(failure); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	return (&Client{HTTPClient: server.Client()}).Execute(context.Background(),
		Spec{Endpoint: server.URL, ModelName: "model", Profile: &serving.Profile{ID: "profile"}},
		serving.Dispatch{RequestID: "request", Definition: definition, Input: &serving.InferenceInput{}})
}

func TestModuleFailuresBecomeFinishedRequestOutcomes(t *testing.T) {
	definition := &serving.Definition{UID: "uid", Generation: 1, Model: serving.Model{UID: "model", Generation: 1}}
	for _, tc := range []struct {
		status        int
		outcome, code string
		failureReason string
	}{
		{http.StatusUnprocessableEntity, native.OutcomePreprocessFailed, "PreprocessFailed", ""},
		{http.StatusInternalServerError, native.OutcomeBatchError, "BackendError", ""},
		{http.StatusInternalServerError, native.OutcomeBatchError, "BackendError", native.FailureMemoryAllocation},
		{http.StatusUnprocessableEntity, native.OutcomeBatchError, "", ""},
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
				if err := json.NewEncoder(w).Encode(native.ErrorResponse{Error: "module rejected sample", FailureReason: tc.failureReason, ExecutionComplete: true, Progress: p}); err != nil {
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
	for _, policy := range []serving.AdmissionPolicy{serving.AdmissionPolicyReject, serving.AdmissionPolicyDelay} {
		received := time.Now().Add(-250 * time.Millisecond)
		result, err := (&Client{HTTPClient: server.Client()}).Execute(context.Background(),
			Spec{Endpoint: server.URL, ModelName: "model", Profile: &serving.Profile{ID: "profile"}},
			serving.Dispatch{
				RequestID: "request", Definition: &serving.Definition{UID: "uid", SLO: serving.SLO{Percentile: 95, TargetLatency: time.Second}, Admission: serving.Admission{Policy: policy}},
				Input: &serving.InferenceInput{}, ReceivedAt: received, Deadline: received.Add(2 * time.Second),
				Elapsed: 250 * time.Millisecond, CompletionMarginMS: 29,
			})
		request := <-seen
		if request.Context.LifetimeMS != 2000 || request.Context.SenderElapsedMS != 250 ||
			request.Context.CompletionMarginMS != 29 || request.Context.SLO.TargetMS != 1000 ||
			request.Context.RequestID != "request" || request.Context.WorkloadUID != "uid" ||
			request.Context.AdmissionPolicy != string(policy) {
			t.Fatalf("native transport changed frozen lifetime/SLO/m/policy: %+v", request.Context)
		}
		if err == nil || result.Finished {
			t.Fatal("unexecuted transport failure completed native ownership")
		}
	}
}

func TestNativeProbeUsesSelectedExecutionNotInitializationHistory(t *testing.T) {
	measured := native.RuntimeMetadata{Runtime: "ort-cpu", RuntimeVersion: "old", NumpyVersion: "old",
		ModelFiles: []string{"/model/model.onnx"}, Inputs: []string{"image"},
		Providers: []string{"CPUExecutionProvider"}, Device: "cpu", InitializedBatches: []int{1, 2}, WarmupIterations: 2}
	profile := &serving.Profile{ID: "profile", NodeName: "node",
		Batches: []serving.BatchProfile{{Batch: 1}}, RuntimeMetadata: &measured}
	profile.Condition.RuntimeFamily, profile.Condition.NodeUID = "firstfloor", "node-uid"
	profile.Condition.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 2}
	profile.Condition.Preprocessor.Inputs = []string{"image"}
	for _, tc := range []struct {
		name   string
		mutate func(*native.Metadata)
		valid  bool
	}{
		{"history drift", func(m *native.Metadata) {
			m.Runtime.RuntimeVersion, m.Runtime.NumpyVersion = "new", "new"
			m.Runtime.WarmupIterations = 9
			m.Runtime.InitializedBatches = []int{1, 3}
			m.MaxBatchSize = 3
		}, true},
		{"selected domain only", func(m *native.Metadata) { m.Runtime.InitializedBatches = []int{1} }, true},
		{"unrestricted measured domain", func(m *native.Metadata) { m.Policy.MaxBatchSize = 0 }, true},
		{"upper bound above measured batch", func(m *native.Metadata) { m.Policy.MaxBatchSize = 2 }, true},
		{"fixed batch without upper bound", func(m *native.Metadata) {
			m.Policy.MaxBatchSize, m.Policy.FixedBatchSize = 0, 1
		}, true},
		{"negative upper bound", func(m *native.Metadata) { m.Policy.MaxBatchSize = -1 }, false},
		{"unrestricted missing selected batch", func(m *native.Metadata) {
			m.Policy.MaxBatchSize, m.Runtime.InitializedBatches = 0, []int{2}
		}, false},
		{"unmeasured fixed batch", func(m *native.Metadata) { m.Policy.FixedBatchSize = 2 }, false},
		{"missing selected batch", func(m *native.Metadata) { m.Runtime.InitializedBatches = []int{2} }, false},
		{"wrong input", func(m *native.Metadata) { m.Runtime.Inputs = []string{"other"} }, false},
		{"wrong model", func(m *native.Metadata) { m.Model = "other" }, false},
		{"wrong backend", func(m *native.Metadata) { m.Runtime.Runtime = "ort-cuda" }, false},
		{"wrong GPU", func(m *native.Metadata) { m.Runtime.GPUUUID = "other" }, false},
		{"wrong mode", func(m *native.Metadata) { m.Mode = "profiling" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual := native.Metadata{Model: "model", Mode: "serving", ProfileID: profile.ID,
				AppliedAt: time.Now().UTC(), Runtime: measured, MaxBatchSize: 2,
				MaxQueueRequests: 4, ModelInstanceCount: 1, Policy: native.Policy{MaxBatchSize: 1}}
			tc.mutate(&actual)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if err := json.NewEncoder(w).Encode(native.State{At: time.Now().UTC(), Ready: true, Metadata: actual}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			ready, err := (&Client{HTTPClient: server.Client()}).Probe(context.Background(), Spec{
				Endpoint: server.URL, ModelName: "model", InstanceID: "instance", Profile: profile,
				Node: serving.NodeReference{Name: "node", UID: "node-uid"}, Capacity: 4, Weight: 1, ExpectedRuntime: &measured})
			if (err == nil) != tc.valid || ready.State.Ready != tc.valid {
				t.Fatalf("readiness did not preserve selected execution boundary: %+v %v", ready, err)
			}
		})
	}
}

func TestNativeSharedCPUReadbackIsNotFrozenAllocation(t *testing.T) {
	measured := native.RuntimeMetadata{Runtime: "ort-cpu", RuntimeVersion: "fixture", ModelFiles: []string{"/model/model.onnx"},
		Providers: []string{"CPUExecutionProvider"}, Device: "cpu", CPUAffinity: []int{0}, IntraOpThreads: 2, InterOpThreads: 1}
	actual := measured
	actual.CPUAffinity, actual.CPUSet, actual.CgroupCPUMax = []int{7}, "4-7", "max 100000"
	actual.IntraOpThreads, actual.InterOpThreads = 0, 0
	spec := Spec{ExpectedRuntime: &measured}
	if err := validateMeasuredRuntime(spec, actual); err != nil {
		t.Fatal("shared CPU observations blocked measured runtime", err)
	}
	actual.GPUUUID = "unmeasured-device"
	if err := validateMeasuredRuntime(spec, actual); err == nil {
		t.Fatal("device identity guard removed with CPU allocation")
	}
	actual.GPUUUID, actual.IntraOpThreads = "", -1
	if err := validateMeasuredRuntime(spec, actual); err == nil {
		t.Fatal("negative observed thread setting accepted")
	}
}
