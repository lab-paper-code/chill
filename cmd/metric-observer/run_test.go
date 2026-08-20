package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/worker"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestExecutePublishesFrozenCompositeMetricObservation(t *testing.T) {
	powerServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("hostname") != "lattepanda" {
			t.Errorf("power query = %q", request.URL.RawQuery)
		}
		_, _ = response.Write([]byte(strings.Join([]string{
			"# TYPE frozen_wall_power_watts gauge",
			"frozen_wall_power_watts{hostname=\"lattepanda\",device_type=\"test_device\"} 5.2",
		}, "\n") + "\n"))
	}))
	defer powerServer.Close()
	thermalServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.RawQuery != "" {
			t.Errorf("thermal query = %q", request.URL.RawQuery)
		}
		_, _ = response.Write([]byte(strings.Join([]string{
			"frozen_thermal_observation_available 1",
			"frozen_thermal_active 0",
			"frozen_thermal_max_celsius 41",
		}, "\n") + "\n"))
	}))
	defer thermalServer.Close()

	directory := t.TempDir()
	intent := writeTestIntent(
		t, directory, powerServer.URL+"/prometheus?hostname=lattepanda", thermalServer.URL+"/metrics",
	)
	options := testOptions(directory)
	if err := worker.PublishSignal(options.startPath); err != nil {
		t.Fatal(err)
	}
	if err := worker.PublishSignal(options.endPath); err != nil {
		t.Fatal(err)
	}
	if err := execute(context.Background(), options, powerServer.Client(), time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(options.readyPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("observer-ready was not published: info=%v err=%v", info, err)
	}
	payload, err := os.ReadFile(options.metricResultPath)
	if err != nil {
		t.Fatal(err)
	}
	var result metricobserver.Result
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	assertCompositeObservation(t, intent, result)
}

func assertCompositeObservation(t *testing.T, intent profilingrun.Intent, result metricobserver.Result) {
	t.Helper()
	if result.Source.Kind != intent.Metrics.SourceKind || result.Source.NodeName != intent.TargetNode.Name ||
		result.Source.Endpoint != intent.Metrics.Endpoint || result.Source.Metric != intent.Metrics.PowerMetric ||
		result.Source.ThermalEndpoint != intent.Metrics.ThermalEndpoint ||
		len(result.Source.MetricLabels) != 2 || result.Source.MetricLabels["hostname"] != "lattepanda" ||
		result.Source.MetricLabels["device_type"] != "test_device" ||
		result.Source.ThermalMetrics == nil ||
		result.Source.ThermalMetrics.ObservationAvailable != intent.Metrics.ThermalMetrics.ObservationAvailable ||
		result.Source.ThermalMetrics.Active != intent.Metrics.ThermalMetrics.Active ||
		result.Source.ThermalMetrics.CPUCoreThrottleEvents != intent.Metrics.ThermalMetrics.CPUCoreThrottleEvents ||
		result.Source.ThermalMetrics.CPUPackageThrottleEvents != intent.Metrics.ThermalMetrics.CPUPackageThrottleEvents ||
		result.Source.ThermalMetrics.Latched != intent.Metrics.ThermalMetrics.Latched ||
		result.Source.ThermalMetrics.MaximumTemperatureCelsius != intent.Metrics.ThermalMetrics.MaximumTemperatureCelsius ||
		result.Summary.SuccessfulSamples < 1 || result.Summary.Failures != 0 {
		t.Fatalf("unexpected observation result: %#v", result)
	}
	if result.Samples[0].Thermal == nil || result.Samples[0].Thermal.ObservationAvailable == nil ||
		!*result.Samples[0].Thermal.ObservationAvailable || result.Samples[0].Thermal.Active == nil ||
		*result.Samples[0].Thermal.Active {
		t.Fatalf("typed thermal evidence was not retained: %#v", result.Samples[0].Thermal)
	}
}

func TestExecuteRequiresMeasurementEndBeforePublishingResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("frozen_wall_power_watts 5.2\n"))
	}))
	defer server.Close()

	directory := t.TempDir()
	writeTestIntent(t, directory, server.URL)
	options := testOptions(directory)
	options.endWaitTimeout = 20 * time.Millisecond
	if err := worker.PublishSignal(options.startPath); err != nil {
		t.Fatal(err)
	}

	err := execute(context.Background(), options, server.Client(), time.Second, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "wait for measurement-end") {
		t.Fatalf("expected bounded measurement-end failure, got %v", err)
	}
	if _, statErr := os.Stat(options.metricResultPath); !os.IsNotExist(statErr) {
		t.Fatalf("missing measurement-end produced a metric result: %v", statErr)
	}
}

func TestSummarizeThermalReportsOnlyRetainedFacts(t *testing.T) {
	available, inactive, active := true, false, true
	low, high := 41.0, 47.5
	result := metricobserver.Result{Samples: []metricobserver.Sample{
		{Thermal: &metricobserver.ThermalReading{
			ObservationAvailable: &available, Active: &inactive, MaximumTemperatureCelsius: &low,
		}},
		{Thermal: &metricobserver.ThermalReading{
			ObservationAvailable: &available, Active: &active, MaximumTemperatureCelsius: &high,
			Issues: []metricobserver.MetricIssue{{Metric: "temperature", Reason: metricobserver.FailureReasonInvalidReading}},
		}},
		{},
	}}
	got := summarizeThermal(result)
	if got.typed != 2 || got.available != 2 || got.active != 1 || got.issues != 1 ||
		got.maximumTemperature != "47.500 C" {
		t.Fatalf("thermal log summary = %#v", got)
	}
}

func TestExecuteRejectsInvalidIntentBeforePublishingReady(t *testing.T) {
	directory := t.TempDir()
	writeTestIntent(t, directory, "http://127.0.0.1:1/metrics")
	options := testOptions(directory)
	if err := os.WriteFile(options.intentPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := execute(context.Background(), options, &http.Client{}, time.Second, time.Millisecond); err == nil {
		t.Fatal("invalid intent was accepted")
	}
	if _, err := os.Stat(options.readyPath); !os.IsNotExist(err) {
		t.Fatalf("invalid intent published observer-ready: %v", err)
	}
}

func TestExecutePublishesReadyBeforeBoundedStartWait(t *testing.T) {
	directory := t.TempDir()
	writeTestIntent(t, directory, "http://127.0.0.1:1/metrics")
	options := testOptions(directory)
	err := execute(context.Background(), options, &http.Client{}, 20*time.Millisecond, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("expected bounded start wait failure, got %v", err)
	}
	if info, statErr := os.Stat(options.readyPath); statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("observer-ready was not published before waiting: info=%v err=%v", info, statErr)
	}
	if _, statErr := os.Stat(options.metricResultPath); !os.IsNotExist(statErr) {
		t.Fatalf("missing measurement signal produced a result: %v", statErr)
	}
}

func writeTestIntent(t *testing.T, directory, endpoint string, thermalEndpoints ...string) profilingrun.Intent {
	t.Helper()
	payload, err := os.ReadFile("testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var intent profilingrun.Intent
	if err := json.Unmarshal(payload, &intent); err != nil {
		t.Fatal(err)
	}
	intent.SchemaVersion = profilingrun.SchemaVersion
	intent.State.ArtifactPath = "/artifact/mobilenet-v2-050.onnx"
	intent.Images.WorkloadObserver = "registry.example/workload-observer@sha256:" + strings.Repeat("d", 64)
	intent.Workload = profilingrun.CurrentWorkloadContract()
	intent.Metrics.Endpoint = endpoint
	intent.Metrics.PowerMetric = "frozen_wall_power_watts"
	intent.Metrics.MetricLabels = map[string]string{"hostname": "lattepanda", "device_type": "test_device"}
	intent.Metrics.ThermalEndpoint = endpoint
	if len(thermalEndpoints) != 0 {
		intent.Metrics.ThermalEndpoint = thermalEndpoints[0]
	}
	intent.Metrics.ThermalMetrics = profilingrun.ThermalMetricContract{
		ObservationAvailable:      "frozen_thermal_observation_available",
		Active:                    "frozen_thermal_active",
		CPUCoreThrottleEvents:     "frozen_thermal_cpu_core_events_total",
		CPUPackageThrottleEvents:  "frozen_thermal_cpu_package_events_total",
		Latched:                   "frozen_thermal_latched",
		MaximumTemperatureCelsius: "frozen_thermal_max_celsius",
	}
	intent.Measurement.DurationSeconds = 1
	intent.Metrics.DurationSeconds = 1
	intent.Metrics.IntervalMilliseconds = 20
	intent.Metrics.TimeoutMilliseconds = 10
	canonical, err := intent.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "intent.json"), canonical, 0o600); err != nil {
		t.Fatal(err)
	}
	return intent
}

func testOptions(directory string) options {
	return options{
		intentPath: filepath.Join(directory, "intent.json"),
		readyPath:  filepath.Join(directory, "observer-ready"), startPath: filepath.Join(directory, "measurement-start"),
		endPath:          filepath.Join(directory, "measurement-end"),
		metricResultPath: filepath.Join(directory, "metric-result.json"), startWaitTimeout: time.Second,
		endWaitTimeout: time.Second, maxEvents: 100,
	}
}
