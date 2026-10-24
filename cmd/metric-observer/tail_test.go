package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/worker"
)

// This intentionally puts producer completion after the nominal 1s window.
func TestExecuteCapturesProducerTailBeyondNominal(t *testing.T) {
	runLateTailSmoke(t, "")
}

// Run against the actual standalone executable, not a mocked Observer. The
// coordinated gate supplies the freshly built binary; ordinary tests skip it.
func TestStandaloneMetricObserverLateTail(t *testing.T) {
	binary := os.Getenv("CHILL_METRIC_OBSERVER_BIN")
	if binary == "" {
		t.Skip("set CHILL_METRIC_OBSERVER_BIN to the built standalone observer")
	}
	runLateTailSmoke(t, binary)
}

func runLateTailSmoke(t *testing.T, binary string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "# TYPE frozen_wall_power_watts gauge\n"+
			"frozen_wall_power_watts{hostname=\"lattepanda\",device_type=\"test_device\"} 5.2\n"+
			"# TYPE frozen_thermal_observation_available gauge\nfrozen_thermal_observation_available 1\n"+
			"# TYPE frozen_thermal_active gauge\nfrozen_thermal_active 0\n"+
			"# TYPE frozen_thermal_cpu_core_events_total counter\nfrozen_thermal_cpu_core_events_total 0\n"+
			"# TYPE frozen_thermal_cpu_package_events_total counter\nfrozen_thermal_cpu_package_events_total 0\n"+
			"# TYPE frozen_thermal_latched gauge\nfrozen_thermal_latched 0\n"+
			"# TYPE frozen_thermal_max_celsius gauge\nfrozen_thermal_max_celsius 40\n")
	}))
	defer server.Close()
	directory := t.TempDir()
	writeTestRunConfig(t, directory, server.URL)
	opts := testOptions(directory)
	opts.endWaitTimeout, opts.maxEvents = 2*time.Second, 103
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	completion := make(chan error, 1)
	if binary == "" {
		go func() { completion <- execute(ctx, opts, server.Client(), time.Second, time.Millisecond) }()
	} else {
		command := exec.CommandContext(ctx, binary,
			"--run-config="+opts.runConfig, "--ready-file="+opts.readyPath,
			"--start-signal-file="+opts.startPath, "--end-signal-file="+opts.endPath,
			"--runtime-failed-file="+opts.runtimeFailedPath, "--result-file="+opts.metricResultPath,
			"--start-wait-timeout=1s", "--end-wait-timeout=2s", "--max-events=103", "--max-result-bytes=8388608")
		go func() {
			output, err := command.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("%w: %s", err, output)
			}
			completion <- err
		}()
	}
	if err := worker.WaitForSignal(ctx, opts.readyPath, time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := worker.PublishSignal(opts.startPath); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	time.Sleep(1250 * time.Millisecond)
	ended := time.Now()
	if err := worker.PublishSignal(opts.endPath); err != nil {
		t.Fatal(err)
	}
	if err := <-completion; err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(opts.metricResultPath)
	if err != nil {
		t.Fatal(err)
	}
	var result metricobserver.Result
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Samples) < 3 || result.Samples[len(result.Samples)-1].ObservedAt.Before(ended) {
		t.Fatalf("missing real final read after producer end: %+v", result)
	}
	tail := 0
	for _, sample := range result.Samples {
		if sample.ObservedAt.After(started.Add(time.Second)) && sample.ObservedAt.Before(ended) {
			tail++
		}
	}
	if tail == 0 {
		t.Fatal("nominal-window tail contains no real power samples")
	}
	if result.Summary.Attempts > opts.maxEvents || int64(len(payload)) > opts.maxResultBytes {
		t.Fatal("result exceeds projected capacity")
	}
	t.Logf("nominal=1s actual=%s tailSamples=%d final=%s events=%d/%d bytes=%d/%d",
		ended.Sub(started), tail, result.Samples[len(result.Samples)-1].ObservedAt,
		result.Summary.Attempts, opts.maxEvents, len(payload), opts.maxResultBytes)
}
