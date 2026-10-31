package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/worker"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestExecuteRequiresMeasurementEndBeforePublishingResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("frozen_wall_power_watts 5.2\n"))
	}))
	defer server.Close()

	directory := t.TempDir()
	writeTestRunConfig(t, directory, server.URL)
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

func TestExecuteFailsPromptlyWhenRuntimeFails(t *testing.T) {
	directory := t.TempDir()
	writeTestRunConfig(t, directory, "http://127.0.0.1:1/metrics")
	options := testOptions(directory)
	options.endWaitTimeout = 10 * time.Minute
	go func() {
		time.Sleep(time.Second)
		_ = worker.PublishSignal(options.runtimeFailedPath)
	}()

	started := time.Now()
	err := execute(context.Background(), options, &http.Client{}, 10*time.Minute, time.Millisecond)
	if !errors.Is(err, worker.ErrRuntimeFailed) {
		t.Fatalf("execute() error = %v, want runtime failure", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("observer ended %s after start, want within seconds of the runtime failure", elapsed)
	}
}

func TestExecutePublishesPowerDespiteThermalTimeoutAndFinalRetries(t *testing.T) {
	for _, finalFailures := range []int{0, 1, 2, -1} {
		t.Run(fmt.Sprintf("final failures %d", finalFailures), func(t *testing.T) {
			directory := t.TempDir()
			var powerReads, thermalReads atomic.Int32
			var opts options
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/thermal" {
					thermalReads.Add(1)
					<-request.Context().Done()
					return
				}
				read := powerReads.Add(1)
				if read == 1 {
					if err := worker.PublishSignal(opts.endPath); err != nil {
						t.Error(err)
					}
				} else if finalFailures < 0 || int(read) <= finalFailures+1 {
					response.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_, _ = response.Write([]byte("# TYPE frozen_wall_power_watts gauge\n" +
					"frozen_wall_power_watts{hostname=\"lattepanda\",device_type=\"test_device\"} 5.2\n"))
			}))
			defer server.Close()
			writeTestRunConfig(t, directory, server.URL)
			opts = testOptions(directory)
			var config profilingrun.RunConfig
			if err := json.Unmarshal([]byte(opts.runConfig), &config); err != nil {
				t.Fatal(err)
			}
			config.Metrics.ThermalEndpoint = server.URL + "/thermal"
			config.Metrics.IntervalMilliseconds = 200
			config.Metrics.TimeoutMilliseconds = 100
			payload, err := config.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			opts.runConfig = string(payload)
			opts.endWaitTimeout = 2 * time.Second
			if err := worker.PublishSignal(opts.startPath); err != nil {
				t.Fatal(err)
			}
			err = execute(context.Background(), opts, server.Client(), time.Second, time.Millisecond)
			if finalFailures < 0 {
				if !errors.Is(err, context.DeadlineExceeded) || powerReads.Load() < 3 {
					t.Fatalf("watchdog error=%v powerReads=%d", err, powerReads.Load())
				}
				if _, statErr := os.Stat(opts.metricResultPath); !os.IsNotExist(statErr) {
					t.Fatalf("failed final reads published result: %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("power result not published: %v", err)
			}
			payload, err = os.ReadFile(opts.metricResultPath)
			if err != nil {
				t.Fatal(err)
			}
			var result metricobserver.Result
			if err := json.Unmarshal(payload, &result); err != nil {
				t.Fatal(err)
			}
			if int(powerReads.Load()) != finalFailures+2 || len(result.Samples) != 2 ||
				len(result.Failures) != finalFailures+2 || result.Summary.Attempts != 2*int(thermalReads.Load())+finalFailures {
				t.Fatalf("power=%d thermal=%d result=%+v", powerReads.Load(), thermalReads.Load(), result)
			}
			for _, sample := range result.Samples {
				if sample.Watts != 5.2 || sample.Thermal != nil {
					t.Fatalf("power lost or thermal fabricated: %+v", sample)
				}
			}
			var timeouts, unavailable int
			for _, failure := range result.Failures {
				switch failure.Reason {
				case metricobserver.FailureReasonTimeout:
					timeouts++
				case metricobserver.FailureReasonSourceRead:
					unavailable++
				default:
					t.Fatalf("unexpected failure: %+v", failure)
				}
			}
			if timeouts != 2 || unavailable != finalFailures {
				t.Fatalf("timeouts=%d unavailable=%d", timeouts, unavailable)
			}
			if err := result.Validate(); err != nil {
				t.Fatal(err)
			}
		})
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

func TestExecuteRejectsInvalidRunConfigBeforePublishingReady(t *testing.T) {
	directory := t.TempDir()
	writeTestRunConfig(t, directory, "http://127.0.0.1:1/metrics")
	options := testOptions(directory)
	options.runConfig = `{}`
	if err := execute(context.Background(), options, &http.Client{}, time.Second, time.Millisecond); err == nil {
		t.Fatal("invalid RunConfig was accepted")
	}
	if _, err := os.Stat(options.readyPath); !os.IsNotExist(err) {
		t.Fatalf("invalid RunConfig published observer-ready: %v", err)
	}
}

func TestExecutePublishesReadyBeforeBoundedStartWait(t *testing.T) {
	directory := t.TempDir()
	writeTestRunConfig(t, directory, "http://127.0.0.1:1/metrics")
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

func writeTestRunConfig(t *testing.T, directory, endpoint string) {
	t.Helper()
	payload, err := os.ReadFile("testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var runConfig profilingrun.RunConfig
	if err := json.Unmarshal(payload, &runConfig); err != nil {
		t.Fatal(err)
	}
	runConfig.SchemaVersion = profilingrun.RunConfigSchemaVersion
	runConfig.State.ArtifactFiles = []string{"/artifact/mobilenet-v2-050.onnx"}
	runConfig.Images.WorkloadObserver = "registry.example/workload-observer@sha256:" + strings.Repeat("d", 64)
	runConfig.Workload = profilingrun.CurrentWorkloadContract()
	runConfig.Metrics.Endpoint = endpoint
	runConfig.Metrics.PowerMetric = "frozen_wall_power_watts"
	runConfig.Metrics.MetricLabels = map[string]string{"hostname": "lattepanda", "device_type": "test_device"}
	runConfig.Metrics.ThermalEndpoint = endpoint
	runConfig.Metrics.ThermalMetrics = profilingrun.ThermalMetricContract{
		ObservationAvailable:      "frozen_thermal_observation_available",
		Active:                    "frozen_thermal_active",
		CPUCoreThrottleEvents:     "frozen_thermal_cpu_core_events_total",
		CPUPackageThrottleEvents:  "frozen_thermal_cpu_package_events_total",
		Latched:                   "frozen_thermal_latched",
		MaximumTemperatureCelsius: "frozen_thermal_max_celsius",
	}
	runConfig.Measurement.DurationSeconds = 1
	runConfig.Metrics.DurationSeconds = 1
	runConfig.Metrics.IntervalMilliseconds = 20
	runConfig.Metrics.TimeoutMilliseconds = 10
	canonical, err := runConfig.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "run-config.json"), canonical, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testOptions(directory string) options {
	payload, _ := os.ReadFile(filepath.Join(directory, "run-config.json"))
	return options{
		runConfig: string(payload),
		readyPath: filepath.Join(directory, "observer-ready"), startPath: filepath.Join(directory, "measurement-start"),
		endPath:          filepath.Join(directory, "measurement-end"),
		metricResultPath: filepath.Join(directory, "metric-result.json"), startWaitTimeout: time.Second,
		endWaitTimeout: time.Second, maxEvents: 100, runtimeFailedPath: filepath.Join(directory, "runtime-failed"),
		maxResultBytes: 8 << 20,
	}
}
