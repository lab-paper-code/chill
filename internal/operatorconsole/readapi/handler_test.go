package readapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

const (
	testNamespace     = "chill-system"
	testPreflightPath = "/api/v1/namespaces/" + testNamespace + "/batch-energy-optimizations/preflight"
	testNoStore       = "no-store"
)

func TestHandlerServesEvidencePassportWithoutStoreEndpoint(t *testing.T) {
	t.Parallel()
	reader := &recordingPassportReader{snapshot: passportSnapshot()}
	handler, err := NewHandler(reader, &recordingCampaignResultReader{}, &recordingPreflightReader{})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/namespaces/"+testNamespace+"/profiling-runs/run/evidence-passport", nil,
	))

	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != testNoStore {
		t.Fatalf("response = %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if reader.namespace != testNamespace || reader.name != "run" || reader.calls != 1 {
		t.Fatalf("query = %q/%q calls=%d", reader.namespace, reader.name, reader.calls)
	}
	// The store endpoint reaches the Console but must never reach a caller.
	if strings.Contains(response.Body.String(), "source.internal") {
		t.Fatalf("response leaked an internal endpoint: %s", response.Body.String())
	}
	var passport operatorconsole.EvidencePassport
	if err := json.Unmarshal(response.Body.Bytes(), &passport); err != nil {
		t.Fatal(err)
	}
	if passport.Kind != "EvidencePassport" || passport.Run.Name != "run" {
		t.Fatalf("passport = %#v", passport)
	}
}

func TestHandlerServesCampaignResultWithoutStoreCredentials(t *testing.T) {
	t.Parallel()
	reader := &recordingCampaignResultReader{snapshot: campaignResultSnapshot(t)}
	handler, err := NewHandler(&recordingPassportReader{}, reader, &recordingPreflightReader{})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/namespaces/"+testNamespace+"/batch-energy-optimizations/campaign/result", nil,
	))

	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != testNoStore {
		t.Fatalf("response = %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if reader.namespace != testNamespace || reader.name != "campaign" || reader.calls != 1 {
		t.Fatalf("query = %q/%q calls=%d", reader.namespace, reader.name, reader.calls)
	}
	var result operatorconsole.BatchEnergyOptimizationResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != "BatchEnergyOptimizationResult" || result.Campaign.Name != "campaign" ||
		len(result.Result.CompletedTrials) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if strings.Contains(response.Body.String(), "accessKey") {
		t.Fatalf("response leaked store access details: %s", response.Body.String())
	}
}

func TestHandlerServesResolvingBatchEnergyOptimizationPreflight(t *testing.T) {
	t.Parallel()
	reader := &recordingPreflightReader{snapshot: resolvingPreflightSnapshot(t)}
	handler, err := NewHandler(
		&recordingPassportReader{}, &recordingCampaignResultReader{}, reader,
	)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, testPreflightPath, preflightCampaignBody(t, testNamespace),
	))

	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != testNoStore {
		t.Fatalf("response = %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var preflight operatorconsole.BatchEnergyOptimizationPreflight
	if err := json.Unmarshal(response.Body.Bytes(), &preflight); err != nil {
		t.Fatal(err)
	}
	if !preflight.Resolves || len(preflight.Plan) == 0 {
		t.Fatalf("preflight = %#v", preflight)
	}
}

func TestHandlerServesUnresolvedPreflightAsASuccessfulRead(t *testing.T) {
	t.Parallel()
	const reason = "referenced DeviceClass does not exist"
	reader := &recordingPreflightReader{
		snapshot: operatorconsole.BatchEnergyOptimizationPreflightSnapshot{
			ReadAt: time.Now(), Campaign: preflightCampaign(testNamespace), Reason: reason,
		},
	}
	handler, err := NewHandler(
		&recordingPassportReader{}, &recordingCampaignResultReader{}, reader,
	)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, testPreflightPath, preflightCampaignBody(t, testNamespace),
	))

	if response.Code >= http.StatusBadRequest {
		t.Fatalf("unresolved preflight returned an error status: %d body=%s", response.Code, response.Body.String())
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	var preflight operatorconsole.BatchEnergyOptimizationPreflight
	if err := json.Unmarshal(response.Body.Bytes(), &preflight); err != nil {
		t.Fatal(err)
	}
	if preflight.Resolves || preflight.Reason != reason {
		t.Fatalf("preflight = %#v", preflight)
	}
}

func TestHandlerRejectsInvalidBatchEnergyOptimizationPreflightRequests(t *testing.T) {
	t.Parallel()
	reader := &recordingPreflightReader{}
	handler, err := NewHandler(
		&recordingPassportReader{}, &recordingCampaignResultReader{}, reader,
	)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		"undecodable body":   `{`,
		"unknown field":      `{"metadata":{"name":"campaign"},"unexpected":true}`,
		"wrong kind":         `{"kind":"ProfilingRun","metadata":{"name":"campaign"}}`,
		"wrong API version":  `{"apiVersion":"edge.dacs.io/v2","metadata":{"name":"campaign"}}`,
		"namespace mismatch": `{"metadata":{"namespace":"another-namespace","name":"campaign"}}`,
	}
	for name, body := range tests {
		name, body := name, body
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(
				http.MethodPost, testPreflightPath, strings.NewReader(body),
			))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body %s)", response.Code, http.StatusBadRequest, response.Body.String())
			}
			var failure struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
				t.Fatal(err)
			}
			if failure.Code != "BatchEnergyOptimizationPreflightRequestInvalid" {
				t.Fatalf("error code = %q", failure.Code)
			}
		})
	}
	if reader.calls != 0 {
		t.Fatalf("invalid requests reached the reader: calls = %d", reader.calls)
	}
}

func TestHandlerUsesPathNamespaceForBatchEnergyOptimizationPreflight(t *testing.T) {
	t.Parallel()
	reader := &recordingPreflightReader{
		snapshot: operatorconsole.BatchEnergyOptimizationPreflightSnapshot{
			ReadAt: time.Now(), Campaign: preflightCampaign(testNamespace), Reason: "unresolved",
		},
	}
	handler, err := NewHandler(
		&recordingPassportReader{}, &recordingCampaignResultReader{}, reader,
	)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, testPreflightPath, preflightCampaignBody(t, ""),
	))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", response.Code, http.StatusOK, response.Body.String())
	}
	if reader.calls != 1 || reader.campaign == nil || reader.campaign.Namespace != testNamespace {
		t.Fatalf("reader campaign = %#v, calls = %d", reader.campaign, reader.calls)
	}
}

func TestPreflightVariantContextUsesOnlyOptionalQuery(t *testing.T) {
	queries := []string{
		"?variantId=chosen", "?variantId=", "?variantId=a&variantId=b", "?other=a", "?variantId=%20a", "?variantId=%zz",
	}
	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			reader := &recordingPreflightReader{snapshot: resolvingPreflightSnapshot(t)}
			handler, err := NewHandler(&recordingPassportReader{}, &recordingCampaignResultReader{}, reader)
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(
				http.MethodPost, testPreflightPath+query, preflightCampaignBody(t, testNamespace),
			))
			if query != "?variantId=chosen" {
				if response.Code != http.StatusBadRequest || reader.calls != 0 {
					t.Fatalf("invalid variant query = %d, calls %d", response.Code, reader.calls)
				}
				return
			}
			var view operatorconsole.BatchEnergyOptimizationPreflight
			if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || reader.campaign.Annotations != nil || view.BatchPlan.VariantID != nil {
				t.Fatalf("variant context changed submitted CR or was trusted: %#v", view)
			}
			found := false
			for _, reason := range view.BatchPlan.Reasons {
				found = found || reason.Code == "VariantConditionChanged"
			}
			if !found {
				t.Fatal("unmatched variant context was lost")
			}
		})
	}
}

func TestHandlerMapsPreflightReadFailuresWithoutLeakingDetails(t *testing.T) {
	t.Parallel()
	notFound := apierrors.NewNotFound(
		schema.GroupResource{Group: "edge.dacs.io", Resource: "deviceclasses"}, "missing",
	)
	tests := map[string]struct {
		err  error
		want int
	}{
		"object missing":     {err: notFound, want: http.StatusNotFound},
		"forbidden":          {err: apierrors.NewForbidden(schema.GroupResource{}, "x", errors.New("denied")), want: http.StatusForbidden},
		"reader unavailable": {err: errors.New("dial Kubernetes API: connection refused"), want: http.StatusBadGateway},
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			handler, err := NewHandler(
				&recordingPassportReader{}, &recordingCampaignResultReader{},
				&recordingPreflightReader{err: test.err},
			)
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(
				http.MethodPost, testPreflightPath, preflightCampaignBody(t, testNamespace),
			))
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d (body %s)", response.Code, test.want, response.Body.String())
			}
			if strings.Contains(response.Body.String(), test.err.Error()) {
				t.Fatalf("response leaked the underlying failure: %s", response.Body.String())
			}
			var failure struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
				t.Fatal(err)
			}
			if failure.Code != "BatchEnergyOptimizationPreflightUnavailable" {
				t.Fatalf("error code = %q", failure.Code)
			}
		})
	}
}

func TestHandlerMapsReadFailuresWithoutLeakingDetails(t *testing.T) {
	t.Parallel()
	notFound := apierrors.NewNotFound(
		schema.GroupResource{Group: "edge.dacs.io", Resource: "batchenergyoptimizations"}, "missing",
	)
	tests := map[string]struct {
		err  error
		want int
	}{
		"object missing":    {err: notFound, want: http.StatusNotFound},
		"forbidden":         {err: apierrors.NewForbidden(schema.GroupResource{}, "x", errors.New("denied")), want: http.StatusForbidden},
		"store unreachable": {err: errors.New("dial evidence store: connection refused"), want: http.StatusBadGateway},
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			handler, err := NewHandler(
				&recordingPassportReader{err: test.err}, &recordingCampaignResultReader{err: test.err},
				&recordingPreflightReader{},
			)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{
				"/api/v1/namespaces/" + testNamespace + "/profiling-runs/run/evidence-passport",
				"/api/v1/namespaces/" + testNamespace + "/batch-energy-optimizations/campaign/result",
			} {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				if response.Code != test.want {
					t.Fatalf("%s = %d, want %d (body %s)", path, response.Code, test.want, response.Body.String())
				}
				if strings.Contains(response.Body.String(), "connection refused") ||
					strings.Contains(response.Body.String(), "denied") {
					t.Fatalf("%s leaked the underlying failure: %s", path, response.Body.String())
				}
			}
		})
	}
}

func TestHandlerHealthAndMethodBoundary(t *testing.T) {
	t.Parallel()
	handler, err := NewHandler(
		&recordingPassportReader{}, &recordingCampaignResultReader{}, &recordingPreflightReader{},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method string
		path   string
		want   int
	}{
		{method: http.MethodGet, path: "/healthz", want: http.StatusOK},
		{method: http.MethodPost, path: "/api/v1/namespaces/" + testNamespace + "/profiling-runs/run/evidence-passport", want: http.StatusMethodNotAllowed},
		{method: http.MethodPost, path: "/api/v1/namespaces/" + testNamespace + "/batch-energy-optimizations/campaign/result", want: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: testPreflightPath, want: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/unknown", want: http.StatusNotFound},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("%s %s status = %d, want %d", test.method, test.path, response.Code, test.want)
		}
	}
}

func TestNewHandlerRequiresAllReaders(t *testing.T) {
	t.Parallel()
	if _, err := NewHandler(nil, &recordingCampaignResultReader{}, &recordingPreflightReader{}); err == nil {
		t.Fatal("nil Evidence Passport reader was accepted")
	}
	if _, err := NewHandler(&recordingPassportReader{}, nil, &recordingPreflightReader{}); err == nil {
		t.Fatal("nil batch-energy-optimization result reader was accepted")
	}
	if _, err := NewHandler(&recordingPassportReader{}, &recordingCampaignResultReader{}, nil); err == nil {
		t.Fatal("nil batch-energy-optimization preflight reader was accepted")
	}
}

type recordingPassportReader struct {
	snapshot  operatorconsole.EvidencePassportSnapshot
	err       error
	namespace string
	name      string
	calls     int
}

func (r *recordingPassportReader) Read(
	_ context.Context,
	namespace, name string,
) (operatorconsole.EvidencePassportSnapshot, error) {
	r.calls++
	r.namespace, r.name = namespace, name
	return r.snapshot, r.err
}

type recordingCampaignResultReader struct {
	snapshot  operatorconsole.BatchEnergyOptimizationResultSnapshot
	err       error
	namespace string
	name      string
	calls     int
}

func (r *recordingCampaignResultReader) Read(
	_ context.Context,
	namespace, name string,
) (operatorconsole.BatchEnergyOptimizationResultSnapshot, error) {
	r.calls++
	r.namespace, r.name = namespace, name
	return r.snapshot, r.err
}

type recordingPreflightReader struct {
	snapshot operatorconsole.BatchEnergyOptimizationPreflightSnapshot
	err      error
	campaign *edgev1alpha1.BatchEnergyOptimization
	calls    int
}

func (r *recordingPreflightReader) Read(
	_ context.Context,
	campaign *edgev1alpha1.BatchEnergyOptimization,
) (operatorconsole.BatchEnergyOptimizationPreflightSnapshot, error) {
	r.calls++
	r.campaign = campaign
	return r.snapshot, r.err
}

func preflightCampaign(namespace string) edgev1alpha1.BatchEnergyOptimization {
	return edgev1alpha1.BatchEnergyOptimization{
		TypeMeta: metav1.TypeMeta{
			APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "BatchEnergyOptimization",
		},
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "campaign"},
		Spec: edgev1alpha1.BatchEnergyOptimizationSpec{
			State: edgev1alpha1.MeasurementStateSpec{TargetNode: "orangepi"}, BatchSizes: []int32{1, 2, 4},
		},
	}
}

func preflightCampaignBody(t *testing.T, namespace string) *bytes.Reader {
	t.Helper()
	encoded, err := json.Marshal(preflightCampaign(namespace))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(encoded)
}

func resolvingPreflightSnapshot(t *testing.T) operatorconsole.BatchEnergyOptimizationPreflightSnapshot {
	t.Helper()
	return operatorconsole.BatchEnergyOptimizationPreflightSnapshot{
		ReadAt: time.Now(), Campaign: preflightCampaign(testNamespace), Plan: preflightPlan(t),
	}
}

func preflightPlan(t *testing.T) profilederivation.CurvePlan {
	t.Helper()
	active := preflightProtocol("saturated")
	active.Dataset = &profilederivation.CurveDatasetContract{Name: "vqav2-val-subset", UID: "dataset-uid"}
	plan, err := profilederivation.NewCurvePlan(profilederivation.CurvePlanSpec{
		Name: "mobilenet-curve", LogicalModel: "mobilenet-v2", DeviceClass: "orangepi-5-plus",
		NodeUID: "node-uid", RuntimeFamily: "rknnlite", Backend: "not-applicable",
		RuntimeImage: "registry.example/chill/runtime:v1", PowerMode: "fixed",
		CPU: profilederivation.CurveCPUContract{
			Policy: "allocatable", PolicyVersion: "v1", Request: "7", Limit: "8",
		},
		MetricSource: profilederivation.CurveMetricSourceContract{
			SourceKind: "prometheus", NodeName: "orangepi", Endpoint: "http://metrics",
			PowerMetric: "power_watts",
		},
		ActiveProtocol: active,
		IdleProtocol:   preflightProtocol("idle-model-loaded"),
		Artifact: profilederivation.CurveArtifactContract{
			ExecutionPath: "rknnlite", Name: "artifact", Files: []string{"/artifact/model.rknn"},
			Image: "registry.example/chill/artifact:v1",
		},
		Preprocessor: profilederivation.CurvePreprocessorContract{
			Name: "mobilenet-imagenet", Image: "registry.example/chill/preprocessor:v1", Architecture: "arm64",
			Path: "/artifact/mobilenet-nhwc", Inputs: []string{"image"}, Outputs: []string{"label"},
		},
		PlannedBatchSizes: []int{1, 2, 4},
	})
	if err != nil {
		t.Fatalf("build preflight plan: %v", err)
	}
	return plan
}

func preflightProtocol(mode string) profilederivation.CurveProtocolContract {
	return profilederivation.CurveProtocolContract{
		WorkloadMode:    mode,
		DurationSeconds: 30, MetricIntervalMilliseconds: 1000, MetricTimeoutMilliseconds: 900,
		MetricDurationSeconds: 31, MetricObserverImage: "registry.example/chill/metric-observer:v1",
		WorkloadObserverImage: "registry.example/chill/workload-observer:v1",
		WorkloadSchemaVersion: "chill.dacs.io/workload-contract.v1alpha1",
	}
}

func passportSnapshot() operatorconsole.EvidencePassportSnapshot {
	started := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	identity := evidencestore.ObjectIdentity{
		Store: "chill-evidence", Key: "profiling-runs/run/execution.json", Size: 1024,
	}
	run := edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: "run", UID: "run-uid"},
		Status: edgev1alpha1.ProfilingRunStatus{
			ObservedGeneration: 1,
			Job:                &edgev1alpha1.ProfilingRunObjectReference{Name: "job", UID: "job-uid"},
			Evidence: &edgev1alpha1.ProfilingRunEvidenceReference{
				Store: identity.Store, Key: identity.Key, Size: identity.Size,
				SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
			},
		},
	}
	execution := profilerevidence.ExecutionEvidence{
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
		FinalizedAt:   started.Add(time.Minute),
		Terminal: profilerevidence.TerminalFacts{
			Job:  profilerevidence.JobTerminalFacts{Namespace: testNamespace, Name: "job", UID: "job-uid"},
			Pod:  profilerevidence.PodTerminalFacts{Namespace: testNamespace, Name: "pod", UID: "pod-uid"},
			Node: profilerevidence.NodeTerminalFacts{Name: "node", UID: "node-uid"},
		},
	}
	execution.RawMeasurement.Store = "chill-evidence"
	execution.RawMeasurement.Key = "raw.json"
	execution.RawMeasurement.Size = 512
	return operatorconsole.EvidencePassportSnapshot{
		ReadAt: started.Add(2 * time.Minute), Run: run, Execution: execution,
		Measurement: profilerevidence.RawMeasurement{
			Runtime: profilingrun.RuntimeResult{
				Runtime: "onnxruntime", RuntimeVersion: "1.16.0", Provider: "CPUExecutionProvider",
				Architecture: "x86_64", BatchSize: 1, WarmupIterations: 20,
				MeasurementStartedAt:      started.Format(time.RFC3339Nano),
				MeasurementEndedAt:        started.Add(30 * time.Second).Format(time.RFC3339Nano),
				MeasurementElapsedSeconds: 30, InferenceCount: 2,
				ThroughputItemsPerSecond: 100,
				RepetitionResults:        []profilingrun.Repetition{{LatencyMilliseconds: []float64{1, 2}}},
			},
			Metrics: metricobserver.Result{
				Source: metricobserver.SourceIdentity{
					Kind: "edge-metrics", NodeName: "lattepanda", Metric: "watts",
					Endpoint: "http://source.internal/metrics",
				},
				StartedAt: started, EndedAt: started.Add(31 * time.Second),
				Samples: []metricobserver.Sample{{Watts: 8}, {Watts: 10}},
				Summary: metricobserver.Summary{SuccessfulSamples: 2, ObservationDurationSeconds: 31},
			},
		},
	}
}

func campaignResultSnapshot(t *testing.T) operatorconsole.BatchEnergyOptimizationResultSnapshot {
	t.Helper()
	record, err := batchenergyoptimization.NewResultRecord(batchenergyoptimization.ResultRecordSpec{
		Outcome: "MinimumFound/LocalMinimum",
		MeasurementState: batchenergyoptimization.ResultMeasurementState{
			TargetNode: "jetsono", ModelSpec: "mobilenet-v2-050",
			DeviceClass: "jetson-agx-orin-64g", ExecutionPath: "ort-cpu", PowerMode: "MODE_50W",
		},
		CurvePlan: batchenergyoptimization.StoredObjectReference{
			Store: "chill-evidence-store", Key: "batch-energy-optimizations/campaign/curve-plan.json",
			SchemaVersion: "chill.dacs.io/curve-plan.v1alpha7", Size: 2459,
		},
		CompletedTrials: []batchenergyoptimization.CompletedTrialReference{{
			Run: batchenergyoptimization.RunReference{
				Namespace: testNamespace, Name: "campaign-active-b1-a1", UID: "run-uid",
			},
			Measurement: batchenergyoptimization.ResultMeasurement{
				WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 1,
			},
			Evidence: batchenergyoptimization.StoredObjectReference{
				Store: "chill-evidence-store", Key: "profiling-runs/run-uid/execution.json",
				SchemaVersion: "chill.dacs.io/profiling-execution-evidence.v1alpha14", Size: 23188,
			},
		}},
		Detail: json.RawMessage(`{"selectedBatch":1}`),
	})
	if err != nil {
		t.Fatalf("build result record: %v", err)
	}
	return operatorconsole.BatchEnergyOptimizationResultSnapshot{
		ReadAt: time.Unix(0, 0).UTC(),
		Campaign: edgev1alpha1.BatchEnergyOptimization{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: "campaign", UID: "campaign-uid"},
			Status: edgev1alpha1.BatchEnergyOptimizationStatus{
				Phase: "Complete", Outcome: "MinimumFound/LocalMinimum", CompletedTrials: 3,
				Result: &edgev1alpha1.ProfilingRunEvidenceReference{
					Store: "chill-evidence-store", Key: "batch-energy-optimizations/campaign/result.json",
					Size: 1024, SchemaVersion: record.SchemaVersion(),
				},
			},
		},
		Result: record,
	}
}

func TestQueryHandlerBoundsRealNamespaceProjection(t *testing.T) {
	calls := 0
	handler := NewQueryHandler(QueryReaders{
		ProfilingRuns: func(_ context.Context, namespace string) (operatorconsole.ProfilingRunsSnapshot, error) {
			calls++
			if namespace != testNamespace {
				t.Fatalf("query namespace = %q", namespace)
			}
			snapshot := operatorconsole.ProfilingRunsSnapshot{ReadAt: time.Unix(42, 0).UTC(), ResourceVersion: "list-version", Namespace: namespace}
			for _, name := range []string{"measure-a", "measure-b", "other"} {
				snapshot.Runs = append(snapshot.Runs, edgev1alpha1.ProfilingRun{
					ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: "uid-" + types.UID(name), ResourceVersion: "run-version"},
					Status:     edgev1alpha1.ProfilingRunStatus{Conditions: []metav1.Condition{{Type: "EvidenceVerified", Status: metav1.ConditionTrue}}},
				})
			}
			// Never expose another namespace even if a faulty reader returned it.
			snapshot.Runs = append(snapshot.Runs, edgev1alpha1.ProfilingRun{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "measure-leak"}})
			return snapshot, nil
		},
	})
	base := "/api/v1/namespaces/" + testNamespace + "/profiling-runs"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+"?q=MEASURE&condition=EvidenceVerified%3DTrue&limit=1&offset=1", nil))
	var body struct {
		operatorconsole.ProfilingRunsView
		Page queryPage `json:"page"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || calls != 1 || body.Namespace != testNamespace || body.AllNamespaces ||
		body.ResourceVersion != "list-version" || body.ReadAt != time.Unix(42, 0).UTC().Format(time.RFC3339Nano) ||
		len(body.Runs) != 1 || body.Runs[0].Name != "measure-b" || body.Runs[0].ResourceVersion != "run-version" ||
		body.Page.Total != 2 || body.Page.Offset != 1 || body.Page.NextOffset != nil {
		t.Fatalf("bounded real projection = %d %s", response.Code, response.Body.String())
	}
	for _, query := range []string{"limit=201", "limit=0", "offset=-1", "offset=1000001", "limit=1&limit=2", "phase=Complete", "condition=EvidenceVerified%3Dinvalid", "q=%XX", "q=a;b=c", "q=" + strings.Repeat("a", 257)} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+"?"+query, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("unbounded query %q = %d", query, response.Code)
		}
	}
	if calls != 1 {
		t.Fatal("invalid list/filter/page input reached producer")
	}
}

func TestQueryHandlerMissingDependencyIsUnavailableAndDenialIsReal(t *testing.T) {
	paths := []string{"/api/v1/systems", "/api/v1/inventory", "/api/v1/namespaces/" + testNamespace + "/profiling-runs"}
	for _, path := range paths {
		response := httptest.NewRecorder()
		NewQueryHandler(QueryReaders{}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("missing %s = %d", path, response.Code)
		}
	}
	handler := NewQueryHandler(QueryReaders{Systems: func(context.Context) (operatorconsole.SystemSnapshot, error) {
		return operatorconsole.SystemSnapshot{}, apierrors.NewForbidden(schema.GroupResource{Group: "chill.dacs.io", Resource: "chillsystems"}, "", errors.New("private credential denied"))
	}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/systems", nil))
	if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "private credential") {
		t.Fatalf("denial classification/leak: %d %s", response.Code, response.Body.String())
	}
}
