package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/profilingrun"
)

const testPodUID = "pod-uid"

func TestORTCPURuntimeUsesDedicatedResultFile(t *testing.T) {
	t.Setenv("CHILL_ORT_CPU_HELPER", "success")
	directory := t.TempDir()
	resultPath := filepath.Join(directory, "runtime-result.json")
	runtime := newTestORTCPURuntime(t, resultPath)
	intent := testIntent(t)

	result, err := runtime.Measure(context.Background(), runtimeRequest(intent))
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != intent.State.Backend || result.ExperimentID != testPodUID {
		t.Fatalf("unexpected runtime result: %#v", result)
	}
}

func TestORTCPURequestMatchesPythonContract(t *testing.T) {
	runtime := newTestORTCPURuntime(t, filepath.Join(t.TempDir(), "runtime-result.json"))
	payload, err := runtime.encodeRequest(runtimeRequest(testIntent(t)))
	if err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required for the Go-to-Python runtime contract test")
	}
	runtimeDirectory, err := filepath.Abs("../../../runtime/ortcpu")
	if err != nil {
		t.Fatal(err)
	}
	const validator = `
import sys
sys.path.insert(0, sys.argv[1])
import runner
request = runner.decode_request(sys.stdin.buffer)
runner.validate_request(request)
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, python, "-c", validator, runtimeDirectory)
	command.Stdin = bytes.NewReader(payload)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python rejected the Go runtime request: %v\n%s", err, output)
	}
}

func TestORTCPURuntimeFailsClosedOnStaleOrInvalidResult(t *testing.T) {
	intent := testIntent(t)

	t.Run("stale result", func(t *testing.T) {
		resultPath := filepath.Join(t.TempDir(), "runtime-result.json")
		if err := os.WriteFile(resultPath, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		runtime := newTestORTCPURuntime(t, resultPath)
		if _, err := runtime.Measure(context.Background(), runtimeRequest(intent)); err == nil ||
			!strings.Contains(err.Error(), "already exists") {
			t.Fatalf("expected stale result rejection, got %v", err)
		}
	})

	t.Run("unknown result field", func(t *testing.T) {
		t.Setenv("CHILL_ORT_CPU_HELPER", "unknown-field")
		runtime := newTestORTCPURuntime(t, filepath.Join(t.TempDir(), "runtime-result.json"))
		if _, err := runtime.Measure(context.Background(), runtimeRequest(intent)); err == nil ||
			!strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("expected strict result rejection, got %v", err)
		}
	})
}

func TestORTCPURuntimeRejectsUnsupportedPathBeforeExecution(t *testing.T) {
	resultPath := filepath.Join(t.TempDir(), "runtime-result.json")
	runtime := newTestORTCPURuntime(t, resultPath)
	intent := testIntent(t)
	intent.State.Backend = "CUDAExecutionProvider"
	if _, err := runtime.Measure(context.Background(), runtimeRequest(intent)); err == nil ||
		!strings.Contains(err.Error(), "unsupported runtime path") {
		t.Fatalf("expected unsupported path rejection, got %v", err)
	}
	if _, err := os.Stat(resultPath); !os.IsNotExist(err) {
		t.Fatalf("unsupported path started the runtime process: %v", err)
	}
}

func TestORTCPURuntimeRequiresJobTimingBinding(t *testing.T) {
	directory := t.TempDir()
	_, err := NewORTCPURuntime(ORTCPURuntimeConfig{
		Command: []string{"ort-cpu"}, ModelPath: filepath.Join(directory, "model.onnx"),
		ObserverReadyPaths:      []string{filepath.Join(directory, "observer-ready")},
		WorkloadWindowStartPath: filepath.Join(directory, "workload-window-start"),
		MeasurementStartPath:    filepath.Join(directory, "measurement-start"),
		WorkloadWindowReadyPath: filepath.Join(directory, "workload-window-ready"),
		MeasurementEndPath:      filepath.Join(directory, "measurement-end"),
		ResultPath:              filepath.Join(directory, "result.json"), MaxResultBytes: 1 << 20,
		MaxLatencySamples: 100, MaxInputBytes: 1 << 20,
		Stdout: io.Discard, Stderr: io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "supplied by the Job") {
		t.Fatalf("expected missing observer-ready timeout rejection, got %v", err)
	}
}

func TestORTCPURuntimeRequiresJobResourceBudgets(t *testing.T) {
	directory := t.TempDir()
	_, err := NewORTCPURuntime(ORTCPURuntimeConfig{
		Command: []string{"ort-cpu"}, ModelPath: filepath.Join(directory, "model.onnx"),
		ObserverReadyPaths:      []string{filepath.Join(directory, "observer-ready")},
		WorkloadWindowStartPath: filepath.Join(directory, "workload-window-start"),
		MeasurementStartPath:    filepath.Join(directory, "measurement-start"),
		WorkloadWindowReadyPath: filepath.Join(directory, "workload-window-ready"),
		MeasurementEndPath:      filepath.Join(directory, "measurement-end"),
		ResultPath:              filepath.Join(directory, "result.json"), MaxResultBytes: 1 << 20,
		ObserverReadyTimeout: time.Second, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "resource budgets") {
		t.Fatalf("expected missing resource budget rejection, got %v", err)
	}
}

func TestMetricResultFileWaitsForAtomicResult(t *testing.T) {
	intent := testIntent(t)
	result := testMetricResult(intent)
	directory := t.TempDir()
	path := filepath.Join(directory, "metric-result.json")
	reader := newTestMetricResultFile(t, path, time.Second)

	done := make(chan error, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		done <- PublishMetricResult(path, 1<<20, result)
	}()
	got, err := reader.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Source, result.Source) || got.Summary != result.Summary {
		t.Fatalf("unexpected metric result: %#v", got)
	}
}

func TestLoadFrozenIntentVerifiesExactDigest(t *testing.T) {
	intent := testIntent(t)
	payload, err := intent.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "intent.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFrozenIntent(path, digest, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != intent.State {
		t.Fatalf("loaded intent drifted: %#v", loaded)
	}
	if _, err := LoadFrozenIntent(path, "sha256:"+strings.Repeat("0", 64), 1<<20); err == nil ||
		!strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected digest mismatch, got %v", err)
	}
}

func TestLoadFrozenIntentAcceptsProjectedConfigMapSymlink(t *testing.T) {
	intent := testIntent(t)
	payload, err := intent.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}

	directory := t.TempDir()
	revision := filepath.Join(directory, "..2026_07_16_00_00_00")
	if err := os.Mkdir(revision, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(revision, "intent.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(revision), filepath.Join(directory, "..data")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "intent.json")
	if err := os.Symlink(filepath.Join("..data", "intent.json"), path); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadFrozenIntent(path, digest, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != intent.State {
		t.Fatalf("loaded projected intent drifted: %#v", loaded)
	}
}

func TestPublishMetricResultDoesNotReplaceExistingHandoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metric-result.json")
	result := testMetricResult(testIntent(t))
	if err := PublishMetricResult(path, 1<<20, result); err != nil {
		t.Fatal(err)
	}
	if err := PublishMetricResult(path, 1<<20, result); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected immutable handoff rejection, got %v", err)
	}
}

func TestSignalHandoffIsAtomicAndBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "measurement-start")
	done := make(chan error, 1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		done <- PublishSignal(path)
	}()
	if err := WaitForSignal(context.Background(), path, time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := PublishSignal(path); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected duplicate signal rejection, got %v", err)
	}
}

func TestMetricResultFileRejectsInvalidHandoff(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		match   string
	}{
		{name: "unknown field", payload: `{"unknown":true}`, match: "unknown field"},
		{name: "trailing value", payload: `{}` + "\n{}", match: "trailing JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "metric-result.json")
			if err := os.WriteFile(path, []byte(test.payload), 0o600); err != nil {
				t.Fatal(err)
			}
			reader := newTestMetricResultFile(t, path, time.Second)
			if _, err := reader.Read(context.Background()); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("expected %q rejection, got %v", test.match, err)
			}
		})
	}
}

func TestMetricResultFileRejectsOversizedOrNonRegularHandoff(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "metric-result.json")
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		reader, err := NewMetricResultFile(MetricResultFileConfig{
			Path: path, WaitTimeout: time.Second, PollInterval: time.Millisecond, MaxBytes: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reader.Read(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("expected oversized handoff rejection, got %v", err)
		}
	})

	t.Run("symbolic link", func(t *testing.T) {
		directory := t.TempDir()
		target := filepath.Join(directory, "target.json")
		path := filepath.Join(directory, "metric-result.json")
		if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		reader := newTestMetricResultFile(t, path, time.Second)
		if _, err := reader.Read(context.Background()); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("expected non-regular handoff rejection, got %v", err)
		}
	})
}

func TestMetricResultFileHasBoundedWait(t *testing.T) {
	reader := newTestMetricResultFile(t, filepath.Join(t.TempDir(), "missing.json"), 20*time.Millisecond)
	started := time.Now()
	_, err := reader.Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("expected bounded wait failure, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("metric result wait exceeded its bounded margin")
	}
}

func TestORTCPUHelperProcess(t *testing.T) {
	mode := os.Getenv("CHILL_ORT_CPU_HELPER")
	if mode == "" {
		return
	}
	var request ortCPURequest
	decoder := json.NewDecoder(os.Stdin)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		os.Exit(10)
	}
	if request.SchemaVersion != ortCPURequestSchemaVersion || request.IntentDigest == "" || request.ModelPath == "" ||
		len(request.ObserverReadyPaths) == 0 || request.MeasurementStartPath == "" || request.MeasurementEndPath == "" ||
		request.ObserverReadyTimeoutMilliseconds < 1 || request.MaxLatencySamples < 1 ||
		request.MaxInputBytes < 1 || request.MaxResultBytes < 1 {
		os.Exit(11)
	}
	digest, err := request.Intent.Digest()
	if err != nil || request.IntentDigest != digest {
		os.Exit(11)
	}
	resultPath := helperResultPath(os.Args)
	if resultPath == "" {
		os.Exit(12)
	}
	if request.ScheduledNodeName != request.Intent.TargetNode.Name {
		os.Exit(11)
	}
	result, err := runtimeResult(request.Intent, request.PodUID)
	if err != nil {
		os.Exit(13)
	}
	if mode == "unknown-field" {
		payload, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			os.Exit(14)
		}
		payload[len(payload)-1] = ','
		payload = append(payload, []byte(`"unknown":true}`)...)
		if err := os.WriteFile(resultPath, payload, 0o600); err != nil {
			os.Exit(15)
		}
		os.Exit(0)
	}
	if err := writeAtomicJSON(resultPath, result); err != nil {
		os.Exit(16)
	}
	os.Exit(0)
}

func newTestORTCPURuntime(t *testing.T, resultPath string) *ORTCPURuntime {
	t.Helper()
	directory := filepath.Dir(resultPath)
	runtime, err := NewORTCPURuntime(ORTCPURuntimeConfig{
		Command:                 []string{os.Args[0], "-test.run=TestORTCPUHelperProcess", "--"},
		ModelPath:               filepath.Join(directory, "model.onnx"),
		ObserverReadyPaths:      []string{filepath.Join(directory, "observer-ready")},
		WorkloadWindowStartPath: filepath.Join(directory, "workload-window-start"),
		MeasurementStartPath:    filepath.Join(directory, "measurement-start"),
		WorkloadWindowReadyPath: filepath.Join(directory, "workload-window-ready"),
		MeasurementEndPath:      filepath.Join(directory, "measurement-end"), ResultPath: resultPath,
		ObserverReadyTimeout: time.Second, MaxLatencySamples: 100, MaxInputBytes: 1 << 20,
		MaxResultBytes: 1 << 20, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func runtimeRequest(intent profilingrun.Intent) RuntimeRequest {
	return RuntimeRequest{Intent: intent, PodUID: testPodUID, ScheduledNodeName: intent.TargetNode.Name}
}

func newTestMetricResultFile(t *testing.T, path string, timeout time.Duration) *MetricResultFile {
	t.Helper()
	reader, err := NewMetricResultFile(MetricResultFileConfig{
		Path: path, WaitTimeout: timeout, PollInterval: time.Millisecond, MaxBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func helperResultPath(arguments []string) string {
	for index := range arguments {
		if arguments[index] == "--result-file" && index+1 < len(arguments) {
			return arguments[index+1]
		}
	}
	return ""
}

func writeAtomicJSON(path string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".handoff-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
