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

const (
	testPodUID              = "pod-uid"
	rknnIdleSuccessTestMode = "idle-success"
	mutatedCopyName         = "mutated-copy"
)

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
	active, err := result.activeResult()
	if err != nil {
		t.Fatal(err)
	}
	if result.idle || active.Provider != intent.State.Backend || active.ExperimentID != testPodUID {
		t.Fatalf("unexpected runtime result: %#v", result)
	}
	active.NodeName = mutatedCopyName
	sealed, err := result.activeResult()
	if err != nil {
		t.Fatal(err)
	}
	if sealed.NodeName != intent.TargetNode.Name {
		t.Fatalf("sealed runtime result was changed through a detached record: %#v", sealed)
	}
}

func TestORTCPURuntimeDecodesIdleResultByFrozenMode(t *testing.T) {
	t.Setenv("CHILL_ORT_CPU_HELPER", "idle-success")
	directory := t.TempDir()
	runtime := newTestORTCPURuntime(t, filepath.Join(directory, "runtime-result.json"))
	intent := testIdleIntent(t)

	result, err := runtime.Measure(context.Background(), runtimeRequest(intent))
	if err != nil {
		t.Fatal(err)
	}
	idle, err := result.idleResult()
	if err != nil {
		t.Fatal(err)
	}
	if !result.idle || idle.PodUID != testPodUID ||
		idle.InferenceCountAfterModelLoad != 0 || !idle.ModelResidentAtEnd {
		t.Fatalf("unexpected idle runtime result: %#v", result)
	}
}

func TestORTCPURequestMatchesPythonContract(t *testing.T) {
	for _, test := range []struct {
		name   string
		intent profilingrun.Intent
	}{
		{name: "active", intent: testIntent(t)},
		{name: "idle", intent: testIdleIntent(t)},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := newTestORTCPURuntime(t, filepath.Join(t.TempDir(), "runtime-result.json"))
			payload, err := runtime.encodeRequest(runtimeRequest(test.intent))
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
		})
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
	record, err := got.record()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(record.Source, result.Source) || record.Summary != result.Summary {
		t.Fatalf("unexpected metric result: %#v", got)
	}
	record.Source.NodeName = mutatedCopyName
	sealed, err := got.record()
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Source.NodeName != result.Source.NodeName {
		t.Fatalf("sealed metric result was changed through a detached record: %#v", sealed)
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
	loadedIntent, err := loaded.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if loadedIntent.State != intent.State || loaded.Digest() != digest {
		t.Fatalf("loaded intent drifted: %#v", loaded)
	}
	loadedIntent.TargetNode.Name = mutatedCopyName
	target, err := loaded.TargetNode()
	if err != nil {
		t.Fatal(err)
	}
	if target != intent.TargetNode {
		t.Fatalf("sealed intent was changed through a detached snapshot: %#v", target)
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
	loadedIntent, err := loaded.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if loadedIntent.State != intent.State || loaded.Digest() != digest {
		t.Fatalf("loaded projected intent drifted: %#v", loaded)
	}
}

func TestLoadVerifiedCandidateReportBindsExactProjectedBytes(t *testing.T) {
	intent := testIntent(t)
	payload, err := os.ReadFile("../controller/testdata/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	revision := filepath.Join(directory, "..2026_08_05_00_00_00")
	if err := os.Mkdir(revision, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(revision, "candidate-report.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(revision), filepath.Join(directory, "..data")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "candidate-report.json")
	if err := os.Symlink(filepath.Join("..data", "candidate-report.json"), path); err != nil {
		t.Fatal(err)
	}
	verified, err := LoadVerifiedCandidateReport(path, 1<<20, testFrozenIntent(t, intent))
	if err != nil {
		t.Fatal(err)
	}
	if delivery := verified.RuntimeDelivery(); delivery.HostContract != nil || delivery.ArtifactCapability != nil {
		t.Fatalf("CPU candidate exposed accelerator delivery: %#v", delivery)
	}

	intent.Candidate.ReportDigest = "sha256:" + strings.Repeat("0", 64)
	if _, err := LoadVerifiedCandidateReport(path, 1<<20, testFrozenIntent(t, intent)); err == nil ||
		!strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected candidate digest rejection, got %v", err)
	}
}

func TestRKNNLiteRuntimeUsesVerifiedDeliveryForActiveAndIdle(t *testing.T) {
	for _, idle := range []bool{false, true} {
		name := "active"
		mode := "success"
		if idle {
			name = "idle"
			mode = rknnIdleSuccessTestMode
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("CHILL_RKNN_LITE_HELPER", mode)
			resultPath := filepath.Join(t.TempDir(), "runtime-result.json")
			runtime := newTestRKNNLiteRuntime(t, resultPath, testRKNNDelivery())
			result, err := runtime.Measure(context.Background(), runtimeRequest(testRKNNIntent(t, idle)))
			if err != nil {
				t.Fatal(err)
			}
			if idle {
				if _, err := result.idleResult(); err != nil || !result.idle {
					t.Fatalf("unexpected idle result: %#v err=%v", result, err)
				}
			} else {
				active, err := result.activeResult()
				if err != nil || result.idle || active.RKNN == nil {
					t.Fatalf("unexpected active result: %#v err=%v", result, err)
				}
			}
		})
	}
}

func TestRKNNLiteRequestMatchesPythonContract(t *testing.T) {
	for _, idle := range []bool{false, true} {
		name := "active"
		if idle {
			name = "idle"
		}
		t.Run(name, func(t *testing.T) {
			runtime := newTestRKNNLiteRuntime(
				t,
				filepath.Join(t.TempDir(), "runtime-result.json"),
				testRKNNDelivery(),
			)
			payload, err := runtime.encodeRequest(runtimeRequest(testRKNNIntent(t, idle)))
			if err != nil {
				t.Fatal(err)
			}
			python, err := exec.LookPath("python3")
			if err != nil {
				t.Fatal("python3 is required for the Go-to-Python RKNNLite contract test")
			}
			runtimeDirectory, err := filepath.Abs("../../../runtime/rknnlite")
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
				t.Fatalf("Python rejected the Go RKNNLite request: %v\n%s", err, output)
			}
		})
	}
}

func TestRKNNLiteRuntimeRejectsObservedHostDrift(t *testing.T) {
	t.Setenv("CHILL_RKNN_LITE_HELPER", "host-drift")
	runtime := newTestRKNNLiteRuntime(t, filepath.Join(t.TempDir(), "runtime-result.json"), testRKNNDelivery())
	if _, err := runtime.Measure(context.Background(), runtimeRequest(testRKNNIntent(t, false))); err == nil ||
		!strings.Contains(err.Error(), "host libraries") {
		t.Fatalf("expected observed host-contract rejection, got %v", err)
	}
}

func TestRKNNLiteRuntimeRequiresCompleteVerifiedDelivery(t *testing.T) {
	directory := t.TempDir()
	_, err := NewRKNNLiteRuntime(RKNNLiteRuntimeConfig{
		Command: []string{"rknnlite"}, ModelPath: filepath.Join(directory, "model.rknn"),
		ObserverReadyPaths:      []string{filepath.Join(directory, "observer-ready")},
		WorkloadWindowStartPath: filepath.Join(directory, "workload-window-start"),
		MeasurementStartPath:    filepath.Join(directory, "measurement-start"),
		WorkloadWindowReadyPath: filepath.Join(directory, "workload-window-ready"),
		MeasurementEndPath:      filepath.Join(directory, "measurement-end"),
		ResultPath:              filepath.Join(directory, "result.json"),
		ObserverReadyTimeout:    time.Second, MaxLatencySamples: 100, MaxInputBytes: 1 << 20,
		MaxResultBytes: 1 << 20, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "delivery contract") {
		t.Fatalf("expected incomplete verified delivery rejection, got %v", err)
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
	var result any
	if mode == rknnIdleSuccessTestMode {
		idle, err := idleRuntimeResult(request.Intent, request.PodUID)
		if err != nil {
			os.Exit(13)
		}
		result = idle
	} else {
		active, err := runtimeResult(request.Intent, request.PodUID)
		if err != nil {
			os.Exit(13)
		}
		result = active
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

func TestRKNNLiteHelperProcess(t *testing.T) {
	mode := os.Getenv("CHILL_RKNN_LITE_HELPER")
	if mode == "" {
		return
	}
	var request rknnLiteRequest
	decoder := json.NewDecoder(os.Stdin)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.SchemaVersion != rknnLiteRequestSchemaVersion {
		os.Exit(20)
	}
	resultPath := helperResultPath(os.Args)
	if resultPath == "" {
		os.Exit(21)
	}
	var result any
	if mode == "idle-success" {
		idle, err := idleRuntimeResult(request.Intent, request.PodUID)
		if err != nil {
			os.Exit(22)
		}
		idle.Architecture = "aarch64"
		idle.RuntimeVersion = "2.3.2"
		idle.Provider = ""
		idle.AvailableProviders = nil
		idle.CPUContract.AffinityCPUCount = 4
		idle.CPUContract.ORTIntraOpThreads = 0
		idle.CPUContract.ORTInterOpThreads = 0
		idle.DerivedContract.Provider = ""
		idle.RKNN = testRKNNEvidence()
		result = idle
	} else {
		active, err := runtimeResult(request.Intent, request.PodUID)
		if err != nil {
			os.Exit(22)
		}
		active.Architecture = "aarch64"
		active.RuntimeVersion = "2.3.2"
		active.Provider = ""
		active.AvailableProviders = nil
		active.CPUContract.AffinityCPUCount = 4
		active.CPUContract.ORTIntraOpThreads = 0
		active.CPUContract.ORTInterOpThreads = 0
		active.DerivedContract.Provider = ""
		active.RKNN = testRKNNEvidence()
		active.InputShape = []int{1, 224, 224, 3}
		if mode == "host-drift" {
			active.RKNN.HostLibraries[0].Digest = "sha256:" + strings.Repeat("f", 64)
		}
		result = active
	}
	if err := writeAtomicJSON(resultPath, result); err != nil {
		os.Exit(23)
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

func newTestRKNNLiteRuntime(
	t *testing.T,
	resultPath string,
	delivery profilingrun.CandidateRuntimeDelivery,
) *RKNNLiteRuntime {
	t.Helper()
	directory := filepath.Dir(resultPath)
	runtime, err := NewRKNNLiteRuntime(RKNNLiteRuntimeConfig{
		Command:                 []string{os.Args[0], "-test.run=TestRKNNLiteHelperProcess", "--"},
		ModelPath:               filepath.Join(directory, "model.rknn"),
		ObserverReadyPaths:      []string{filepath.Join(directory, "observer-ready")},
		WorkloadWindowStartPath: filepath.Join(directory, "workload-window-start"),
		MeasurementStartPath:    filepath.Join(directory, "measurement-start"),
		WorkloadWindowReadyPath: filepath.Join(directory, "workload-window-ready"),
		MeasurementEndPath:      filepath.Join(directory, "measurement-end"),
		ResultPath:              resultPath,
		ObserverReadyTimeout:    time.Second, MaxLatencySamples: 100, MaxInputBytes: 1 << 20,
		MaxResultBytes: 1 << 20, Delivery: delivery, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func testRKNNIntent(t *testing.T, idle bool) profilingrun.Intent {
	t.Helper()
	intent := testIntent(t)
	intent.Candidate.ExecutionPath = profilingrun.RKNNLiteExecutionPath
	intent.State.Artifact = "model-rk3588-bs1"
	intent.State.ArtifactPath = "/artifact/model.rknn"
	intent.State.RuntimeFamily = profilingrun.RKNNLiteRuntimeFamily
	intent.State.Backend = profilingrun.RKNNLiteBackend
	intent.State.HostContractDigest = "sha256:" + strings.Repeat("b", 64)
	intent.TargetNode.Architecture = "arm64"
	intent.CPU.ORTIntraOpThreads = 0
	intent.CPU.ORTInterOpThreads = 0
	if idle {
		intent.Measurement.WarmupIterations = 0
		intent.Measurement.SettlingDurationSeconds = 30
		intent.Measurement.WorkloadMode = profilingrun.IdleModelLoadedWorkloadMode
		intent.Measurement.InputGenerator = profilingrun.NoInputGenerator
		intent.Measurement.OutputValidation = profilingrun.NoOutputValidation
	}
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
	return intent
}

func testRKNNDelivery() profilingrun.CandidateRuntimeDelivery {
	return profilingrun.CandidateRuntimeDelivery{
		ArtifactCapability: &profilingrun.CandidateArtifactCapability{
			TargetPlatform: "rk3588", FixedBatch: 1, InputShape: []int32{1, 224, 224, 3},
			InputLayout: profilingrun.RKNNLiteInputLayout, InputDataType: profilingrun.RKNNLiteInputDataType,
		},
		RuntimePackages: []profilingrun.CandidateReportRuntimePackage{{Name: "rknn-toolkit-lite2", Version: "2.3.2"}},
		HostContract: &profilingrun.CandidateReportHostContract{
			Libraries: []profilingrun.CandidateReportHostLibrary{{
				Name: "librknnrt", HostPath: "/usr/lib/librknnrt.so", ContainerPath: "/usr/lib/librknnrt.so",
				Digest: "sha256:" + strings.Repeat("b", 64),
			}},
			Driver: profilingrun.CandidateReportHostDriver{
				Name: "rknpu", VersionPath: "/sys/module/rknpu/version", Version: "0.9.6",
			},
			Devices: []profilingrun.CandidateReportHostDevice{
				{Path: "/dev/dri/card0"}, {Path: "/dev/dri/renderD128"},
			},
			Platform: &profilingrun.CandidateReportHostPlatform{
				CompatiblePath: "/proc/device-tree/compatible", CompatibleToken: "rockchip,rk3588",
			},
		},
		RuntimeAccess: &profilingrun.CandidateReportRuntimeAccess{Privileged: true, SupplementalGroups: []int64{44, 110}},
	}
}

func testRKNNEvidence() *profilingrun.RKNNRuntimeEvidence {
	return &profilingrun.RKNNRuntimeEvidence{
		Package: profilingrun.RKNNPackageObservation{Name: "rknn-toolkit-lite2", Version: "2.3.2"},
		HostLibraries: []profilingrun.RKNNHostLibraryObservation{{
			Name: "librknnrt", Path: "/usr/lib/librknnrt.so", Digest: "sha256:" + strings.Repeat("b", 64),
		}},
		Driver: profilingrun.RKNNDriverObservation{
			Name: "rknpu", VersionPath: "/sys/module/rknpu/version", Version: "0.9.6",
		},
		Platform: profilingrun.RKNNPlatformObservation{
			CompatiblePath: "/proc/device-tree/compatible", CompatibleToken: "rockchip,rk3588", Matched: true,
		},
		Devices: []profilingrun.RKNNDeviceObservation{
			{Path: "/dev/dri/card0", CharacterDevice: true, Opened: true},
			{Path: "/dev/dri/renderD128", CharacterDevice: true, Opened: true},
		},
		CoreMask: profilingrun.RKNNLiteCoreMask, InputLayout: profilingrun.RKNNLiteInputLayout,
		InitSucceeded: true, LoadSucceeded: true, SelectedBatch: 1,
	}
}

func runtimeRequest(intent profilingrun.Intent) RuntimeRequest {
	digest, err := intent.Digest()
	if err != nil {
		panic(err)
	}
	return RuntimeRequest{Intent: newFrozenIntent(intent, digest), PodUID: testPodUID, ScheduledNodeName: intent.TargetNode.Name}
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
