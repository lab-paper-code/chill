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

func TestHTTPHandlerServesEvidencePassportWithoutStoreEndpoint(t *testing.T) {
	t.Parallel()
	reader := &recordingPassportReader{snapshot: httpPassportSnapshot()}
	handler, err := NewHTTPHandler(reader)
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
	if reader.namespace != "chill-system" || reader.name != "run" || reader.calls != 1 {
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
			handler, err := NewHTTPHandler(&recordingPassportReader{err: test.err})
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
	handler, err := NewHTTPHandler(&recordingPassportReader{})
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
