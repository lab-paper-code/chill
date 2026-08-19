package operatorconsole

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// consoleTestNamespace is the namespace every Console route test addresses.
const consoleTestNamespace = "chill-system"

func TestHTTPHandlerServesEvidencePassportWithoutStoreEndpoint(t *testing.T) {
	t.Parallel()
	reader := &recordingPassportReader{snapshot: httpPassportSnapshot()}
	handler, err := NewHTTPHandler(reader, &recordingCampaignResultReader{})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/namespaces/chill-system/profiling-runs/run/evidence-passport",
		nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response = %d, headers=%v, body=%s", response.Code, response.Header(), response.Body.String())
	}
	if reader.namespace != consoleTestNamespace || reader.name != "run" || reader.calls != 1 {
		t.Fatalf("query = %q/%q calls=%d", reader.namespace, reader.name, reader.calls)
	}
	if strings.Contains(response.Body.String(), "store.internal") || strings.Contains(response.Body.String(), "source.internal") {
		t.Fatalf("response leaked an internal endpoint: %s", response.Body.String())
	}
	var passport EvidencePassport
	if err := json.Unmarshal(response.Body.Bytes(), &passport); err != nil {
		t.Fatal(err)
	}
	if passport.Kind != "EvidencePassport" || passport.Run.Name != "run" ||
		passport.Measurement.Runtime.Latency.P99 != 2 || passport.Measurement.Power.Watts == nil {
		t.Fatalf("passport = %#v", passport)
	}
}

func TestHTTPHandlerMapsQueryErrorsWithoutLeakingDetails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "not found", err: apierrors.NewNotFound(schema.GroupResource{Group: "edge.dacs.io", Resource: "profilingruns"}, "missing"), want: http.StatusNotFound},
		{name: "forbidden", err: apierrors.NewForbidden(schema.GroupResource{Group: "edge.dacs.io", Resource: "profilingruns"}, "run", errors.New("secret detail")), want: http.StatusForbidden},
		{name: "store failure", err: errors.New("http://store.internal:9000 credential rejected"), want: http.StatusBadGateway},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler, err := NewHTTPHandler(&recordingPassportReader{err: test.err}, &recordingCampaignResultReader{})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(
				http.MethodGet,
				"/api/v1/namespaces/chill-system/profiling-runs/run/evidence-passport",
				nil,
			))
			if response.Code != test.want || strings.Contains(response.Body.String(), "secret detail") ||
				strings.Contains(response.Body.String(), "store.internal") {
				t.Fatalf("response = %d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestHTTPHandlerHealthAndMethodBoundary(t *testing.T) {
	t.Parallel()
	handler, err := NewHTTPHandler(&recordingPassportReader{}, &recordingCampaignResultReader{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method string
		path   string
		want   int
	}{
		{method: http.MethodGet, path: "/healthz", want: http.StatusOK},
		{method: http.MethodPost, path: "/api/v1/namespaces/chill-system/profiling-runs/run/evidence-passport", want: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/unknown", want: http.StatusNotFound},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("%s %s status = %d, want %d", test.method, test.path, response.Code, test.want)
		}
	}
}

type recordingPassportReader struct {
	snapshot  EvidencePassportSnapshot
	err       error
	namespace string
	name      string
	calls     int
}

func (r *recordingPassportReader) Read(
	_ context.Context,
	namespace, name string,
) (EvidencePassportSnapshot, error) {
	r.calls++
	r.namespace, r.name = namespace, name
	return r.snapshot, r.err
}

func httpPassportSnapshot() EvidencePassportSnapshot {
	run, _, execution := passportFixture()
	started := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	execution.FinalizedAt = started.Add(time.Minute)
	execution.RawMeasurement.Store = "chill-evidence"
	execution.RawMeasurement.Key = "raw.json"
	execution.RawMeasurement.Size = 512
	return EvidencePassportSnapshot{
		ReadAt: started.Add(2 * time.Minute), Run: *run, Execution: execution,
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

func TestHTTPHandlerServesCampaignResultWithoutStoreCredentials(t *testing.T) {
	t.Parallel()
	campaign, _, record, _ := campaignResultFixture(t)
	reader := &recordingCampaignResultReader{snapshot: BatchSaturationResultSnapshot{
		ReadAt: time.Now(), Campaign: *campaign, Result: record,
	}}
	handler, err := NewHTTPHandler(&recordingPassportReader{}, reader)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/namespaces/chill-system/batch-saturations/campaign/result",
		nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response = %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if reader.namespace != consoleTestNamespace || reader.name != "campaign" || reader.calls != 1 {
		t.Fatalf("query = %q/%q calls=%d", reader.namespace, reader.name, reader.calls)
	}
	var result BatchSaturationResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != "BatchSaturationResult" || result.Campaign.Name != "campaign" ||
		len(result.Result.AdmittedTrials) != 1 || len(result.Result.ExcludedTrials) != 1 {
		t.Fatalf("result = %#v", result)
	}
	// The Evidence Store is reached with credentials the caller never sees.
	if strings.Contains(response.Body.String(), "accessKey") ||
		strings.Contains(response.Body.String(), "endpoint") {
		t.Fatalf("response leaked store access details: %s", response.Body.String())
	}
}

func TestHTTPHandlerAnswersNotFoundForACampaignWithoutAResultYet(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		err  error
		want int
	}{
		"no result published": {
			err:  ErrBatchSaturationResultUnavailable,
			want: http.StatusNotFound,
		},
		"campaign missing": {
			err: apierrors.NewNotFound(
				schema.GroupResource{Group: "edge.dacs.io", Resource: "batchsaturations"}, "missing",
			),
			want: http.StatusNotFound,
		},
		"store unreachable": {
			err:  errors.New("dial evidence store: connection refused"),
			want: http.StatusBadGateway,
		},
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			handler, err := NewHTTPHandler(
				&recordingPassportReader{}, &recordingCampaignResultReader{err: test.err},
			)
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(
				http.MethodGet,
				"/api/v1/namespaces/chill-system/batch-saturations/campaign/result",
				nil,
			))
			if response.Code != test.want {
				t.Fatalf("response = %d body=%s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "connection refused") {
				t.Fatalf("response leaked the underlying failure: %s", response.Body.String())
			}
		})
	}
}

func TestNewHTTPHandlerRequiresBothReaders(t *testing.T) {
	t.Parallel()
	if _, err := NewHTTPHandler(nil, &recordingCampaignResultReader{}); err == nil {
		t.Fatal("nil Evidence Passport reader was accepted")
	}
	if _, err := NewHTTPHandler(&recordingPassportReader{}, nil); err == nil {
		t.Fatal("nil batch-saturation result reader was accepted")
	}
}

type recordingCampaignResultReader struct {
	snapshot  BatchSaturationResultSnapshot
	err       error
	namespace string
	name      string
	calls     int
}

func (r *recordingCampaignResultReader) Read(
	_ context.Context,
	namespace, name string,
) (BatchSaturationResultSnapshot, error) {
	r.calls++
	r.namespace, r.name = namespace, name
	return r.snapshot, r.err
}
