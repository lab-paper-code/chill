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

	"github.com/lab-paper-code/chill/internal/profiler/worker"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

func TestExecutePublishesFrozenPowerObservation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("shelly_power_total_watts 5.2\n"))
	}))
	defer server.Close()

	directory := t.TempDir()
	intent, digest := writeTestIntent(t, directory, server.URL)
	options := testOptions(directory, digest)
	if err := worker.PublishSignal(options.startPath); err != nil {
		t.Fatal(err)
	}
	if err := execute(context.Background(), options, server.Client(), time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(options.readyPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("observer-ready was not published: info=%v err=%v", info, err)
	}
	reader, err := worker.NewPowerResultFile(worker.PowerResultFileConfig{
		Path: options.powerResultPath, WaitTimeout: time.Second,
		PollInterval: time.Millisecond, MaxBytes: maximumResultBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Source.NodeName != intent.TargetNode.Name || result.Summary.SuccessfulSamples < 1 ||
		result.Summary.Failures != 0 {
		t.Fatalf("unexpected observation result: %#v", result)
	}
}

func TestExecuteRejectsIntentBeforePublishingReady(t *testing.T) {
	directory := t.TempDir()
	_, digest := writeTestIntent(t, directory, "http://127.0.0.1:1/metrics")
	options := testOptions(directory, digest)
	options.intentDigest = "sha256:" + strings.Repeat("0", 64)
	if err := execute(context.Background(), options, &http.Client{}, time.Second, time.Millisecond); err == nil ||
		!strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected intent digest rejection, got %v", err)
	}
	if _, err := os.Stat(options.readyPath); !os.IsNotExist(err) {
		t.Fatalf("invalid intent published observer-ready: %v", err)
	}
}

func TestExecutePublishesReadyBeforeBoundedStartWait(t *testing.T) {
	directory := t.TempDir()
	_, digest := writeTestIntent(t, directory, "http://127.0.0.1:1/metrics")
	options := testOptions(directory, digest)
	err := execute(context.Background(), options, &http.Client{}, 20*time.Millisecond, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("expected bounded start wait failure, got %v", err)
	}
	if info, statErr := os.Stat(options.readyPath); statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("observer-ready was not published before waiting: info=%v err=%v", info, statErr)
	}
	if _, statErr := os.Stat(options.powerResultPath); !os.IsNotExist(statErr) {
		t.Fatalf("missing measurement signal produced a result: %v", statErr)
	}
}

func writeTestIntent(t *testing.T, directory, endpoint string) (profilingrun.Intent, string) {
	t.Helper()
	payload, err := os.ReadFile("../../spikes/profilingrun/fixtures/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var intent profilingrun.Intent
	if err := json.Unmarshal(payload, &intent); err != nil {
		t.Fatal(err)
	}
	intent.SchemaVersion = profilingrun.SchemaVersion
	intent.State.ArtifactPath = "/artifact/mobilenet-v2-050.onnx"
	intent.Power.Endpoint = endpoint
	intent.Measurement.DurationSeconds = 1
	intent.Power.DurationSeconds = 1
	intent.Power.IntervalMilliseconds = 20
	intent.Power.TimeoutMilliseconds = 10
	canonical, err := intent.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "intent.json"), canonical, 0o600); err != nil {
		t.Fatal(err)
	}
	return intent, digest
}

func testOptions(directory, digest string) options {
	return options{
		intentPath: filepath.Join(directory, "intent.json"), intentDigest: digest,
		readyPath: filepath.Join(directory, "observer-ready"), startPath: filepath.Join(directory, "measurement-start"),
		powerResultPath: filepath.Join(directory, "power-result.json"), startWaitTimeout: time.Second,
	}
}
