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

func TestExecutePublishesFrozenMetricObservation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(strings.Join([]string{
			"shelly_power_total_watts 5.2",
			"device_thermal_throttle_observation_available 1",
			"device_thermal_throttle_active 0",
			"device_thermal_max_celsius 41",
		}, "\n") + "\n"))
	}))
	defer server.Close()

	directory := t.TempDir()
	intent := writeTestIntent(t, directory, server.URL)
	options := testOptions(directory)
	if err := worker.PublishSignal(options.startPath); err != nil {
		t.Fatal(err)
	}
	if err := worker.PublishSignal(options.endPath); err != nil {
		t.Fatal(err)
	}
	if err := execute(context.Background(), options, server.Client(), time.Second, time.Millisecond); err != nil {
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
	if result.Source.NodeName != intent.TargetNode.Name || result.Summary.SuccessfulSamples < 1 ||
		result.Summary.Failures != 0 {
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
		_, _ = response.Write([]byte("shelly_power_total_watts 5.2\n"))
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

func writeTestIntent(t *testing.T, directory, endpoint string) profilingrun.Intent {
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
