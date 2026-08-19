package readapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/batchsaturation"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const testNamespace = "chill-system"

func TestHandlerServesEvidencePassportWithoutStoreEndpoint(t *testing.T) {
	t.Parallel()
	reader := &recordingPassportReader{snapshot: passportSnapshot()}
	handler, err := NewHandler(reader, &recordingCampaignResultReader{})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/namespaces/"+testNamespace+"/profiling-runs/run/evidence-passport", nil,
	))

	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
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
	handler, err := NewHandler(&recordingPassportReader{}, reader)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/namespaces/"+testNamespace+"/batch-saturations/campaign/result", nil,
	))

	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response = %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if reader.namespace != testNamespace || reader.name != "campaign" || reader.calls != 1 {
		t.Fatalf("query = %q/%q calls=%d", reader.namespace, reader.name, reader.calls)
	}
	var result operatorconsole.BatchSaturationResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != "BatchSaturationResult" || result.Campaign.Name != "campaign" ||
		len(result.Result.AdmittedTrials) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if strings.Contains(response.Body.String(), "accessKey") {
		t.Fatalf("response leaked store access details: %s", response.Body.String())
	}
}

func TestHandlerMapsReadFailuresWithoutLeakingDetails(t *testing.T) {
	t.Parallel()
	notFound := apierrors.NewNotFound(
		schema.GroupResource{Group: "edge.dacs.io", Resource: "batchsaturations"}, "missing",
	)
	tests := map[string]struct {
		err  error
		want int
	}{
		// Nothing is broken when a campaign has not published a result yet, so
		// it must not report the Bad Gateway a failing store earns.
		"no result published": {err: operatorconsole.ErrBatchSaturationResultUnavailable, want: http.StatusNotFound},
		"object missing":      {err: notFound, want: http.StatusNotFound},
		"forbidden":           {err: apierrors.NewForbidden(schema.GroupResource{}, "x", errors.New("denied")), want: http.StatusForbidden},
		"store unreachable":   {err: errors.New("dial evidence store: connection refused"), want: http.StatusBadGateway},
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			handler, err := NewHandler(
				&recordingPassportReader{err: test.err}, &recordingCampaignResultReader{err: test.err},
			)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{
				"/api/v1/namespaces/" + testNamespace + "/profiling-runs/run/evidence-passport",
				"/api/v1/namespaces/" + testNamespace + "/batch-saturations/campaign/result",
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
	handler, err := NewHandler(&recordingPassportReader{}, &recordingCampaignResultReader{})
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
		{method: http.MethodPost, path: "/api/v1/namespaces/" + testNamespace + "/batch-saturations/campaign/result", want: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/unknown", want: http.StatusNotFound},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("%s %s status = %d, want %d", test.method, test.path, response.Code, test.want)
		}
	}
}

func TestNewHandlerRequiresBothReaders(t *testing.T) {
	t.Parallel()
	if _, err := NewHandler(nil, &recordingCampaignResultReader{}); err == nil {
		t.Fatal("nil Evidence Passport reader was accepted")
	}
	if _, err := NewHandler(&recordingPassportReader{}, nil); err == nil {
		t.Fatal("nil batch-saturation result reader was accepted")
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
	snapshot  operatorconsole.BatchSaturationResultSnapshot
	err       error
	namespace string
	name      string
	calls     int
}

func (r *recordingCampaignResultReader) Read(
	_ context.Context,
	namespace, name string,
) (operatorconsole.BatchSaturationResultSnapshot, error) {
	r.calls++
	r.namespace, r.name = namespace, name
	return r.snapshot, r.err
}

func passportSnapshot() operatorconsole.EvidencePassportSnapshot {
	started := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	identity := evidencestore.ObjectIdentity{
		Store: "chill-evidence", Key: "profiling-runs/run/execution.json", Size: 1024,
	}
	run := edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: "run", UID: "run-uid"},
		Status: edgev1alpha1.ProfilingRunStatus{
			IntentFrozen: true,
			Job:          &edgev1alpha1.ProfilingRunObjectReference{Name: "job", UID: "job-uid"},
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

func campaignResultSnapshot(t *testing.T) operatorconsole.BatchSaturationResultSnapshot {
	t.Helper()
	record, err := batchsaturation.NewResultRecord(batchsaturation.ResultRecordSpec{
		Outcome: "Stopped/NoFurtherImprovement",
		CurvePlan: batchsaturation.StoredObjectReference{
			Store: "chill-evidence-store", Key: "batch-saturations/campaign/curve-plan.json",
			SchemaVersion: "chill.dacs.io/curve-plan.v1alpha4", Size: 2459,
		},
		AdmittedTrials: []batchsaturation.AdmittedTrialReference{{
			Run: batchsaturation.RunReference{
				Namespace: testNamespace, Name: "campaign-active-b1-a1", UID: "run-uid",
			},
			Measurement: batchsaturation.ResultMeasurement{
				WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 1,
			},
			Evidence: batchsaturation.StoredObjectReference{
				Store: "chill-evidence-store", Key: "profiling-runs/run-uid/execution.json",
				SchemaVersion: "chill.dacs.io/profiling-execution-evidence.v1alpha8", Size: 23188,
			},
		}},
		Detail: json.RawMessage(`{"selectedBatch":1}`),
	})
	if err != nil {
		t.Fatalf("build result record: %v", err)
	}
	return operatorconsole.BatchSaturationResultSnapshot{
		ReadAt: time.Unix(0, 0).UTC(),
		Campaign: edgev1alpha1.BatchSaturation{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: "campaign", UID: "campaign-uid"},
			Status: edgev1alpha1.BatchSaturationStatus{
				Phase: "Complete", Outcome: "Stopped/NoFurtherImprovement", AcceptedTrials: 3,
				Result: &edgev1alpha1.ProfilingRunEvidenceReference{
					Store: "chill-evidence-store", Key: "batch-saturations/campaign/result.json",
					Size: 1024, SchemaVersion: record.SchemaVersion(),
				},
			},
		},
		Result: record,
	}
}
