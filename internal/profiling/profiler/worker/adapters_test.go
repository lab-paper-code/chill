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

	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

const (
	testPodUID            = "pod-uid"
	idleSuccessTestMode   = "idle-success"
	mutatedCopyName       = "mutated-copy"
	testDifferentIdentity = "other"
)

func TestFirstfloorRuntimeUsesDedicatedResultFile(t *testing.T) {
	t.Setenv("CHILL_FIRSTFLOOR_HELPER", "success")
	directory := t.TempDir()
	resultPath := filepath.Join(directory, "runtime-result.json")
	runtime := newTestFirstfloorRuntime(t, resultPath)
	config := testRunConfig(t)

	result, err := runtime.Measure(context.Background(), runtimeRequest(config))
	if err != nil {
		t.Fatal(err)
	}
	verified, err := result.activeResult()
	if err != nil {
		t.Fatal(err)
	}
	active, err := verified.Record()
	if err != nil {
		t.Fatal(err)
	}
	if result.mode != runtimeMeasurementActive || active.Provider != testCPUProvider || active.ExperimentID != testPodUID {
		t.Fatalf("unexpected runtime result: %#v", result)
	}
	active.NodeName = mutatedCopyName
	sealedValue, err := result.activeResult()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealedValue.Record()
	if err != nil {
		t.Fatal(err)
	}
	if sealed.NodeName != config.TargetNode.Name {
		t.Fatalf("sealed runtime result was changed through a detached record: %#v", sealed)
	}
}

func TestFirstfloorRuntimeDecodesIdleResultByRunConfigMode(t *testing.T) {
	t.Setenv("CHILL_FIRSTFLOOR_HELPER", idleSuccessTestMode)
	directory := t.TempDir()
	runtime := newTestFirstfloorRuntime(t, filepath.Join(directory, "runtime-result.json"))
	config := testIdleRunConfig(t)

	result, err := runtime.Measure(context.Background(), runtimeRequest(config))
	if err != nil {
		t.Fatal(err)
	}
	verified, err := result.idleResult()
	if err != nil {
		t.Fatal(err)
	}
	idle, err := verified.Record()
	if err != nil {
		t.Fatal(err)
	}
	if result.mode != runtimeMeasurementIdle || idle.PodUID != testPodUID ||
		idle.InferenceCountAfterModelLoad != 0 || !idle.ModelResidentAtEnd {
		t.Fatalf("unexpected idle runtime result: %#v", result)
	}
}

func TestFirstfloorRequestMatchesPythonContract(t *testing.T) {
	multiInput := testRunConfig(t)
	multiInput.Delivery.ArtifactCapability.Tensors = []modelcompat.InputTensor{
		{Name: "input", Datatype: "FP32", SampleShape: []int32{3, 224, 224}},
		{Name: "mask", Datatype: "INT64", SampleShape: []int32{224, 224}},
	}
	rknn := testRunConfig(t)
	rknn.State.Backend = profilingrun.FirstfloorRKNNLiteBackend
	rknn.State.Artifact, rknn.State.ArtifactPath = "model-rk3588-bs1", "/artifact/model.rknn"
	rknn.TargetNode.Architecture = "arm64"
	rknn.CPU.ORTIntraOpThreads, rknn.CPU.ORTInterOpThreads = 0, 0
	rknn.Delivery = profilingrun.CandidateRuntimeDelivery{
		ArtifactCapability: &profilingrun.CandidateArtifactCapability{
			TargetPlatform: "rk3588", DeclaredBatchSizes: []int32{1},
			Tensors: []modelcompat.InputTensor{{Name: "input", Datatype: "FP32", SampleShape: []int32{224, 224, 3}}},
		},
		RuntimePackages: []profilingrun.CandidateReportRuntimePackage{{Name: "rknn-toolkit-lite2", Version: "2.3.2"}},
		HostContract: &profilingrun.CandidateReportHostContract{
			Libraries: []profilingrun.CandidateReportHostLibrary{{
				Name: "librknnrt", HostPath: "/usr/lib/librknnrt.so", ContainerPath: "/usr/lib/librknnrt.so",
			}},
			Driver:  profilingrun.CandidateReportHostDriver{Name: "rknpu", VersionPath: "/sys/module/rknpu/version", Version: "0.9.6"},
			Devices: []profilingrun.CandidateReportHostDevice{{Path: "/dev/dri/renderD128"}},
			Platform: &profilingrun.CandidateReportHostPlatform{
				CompatiblePath: "/proc/device-tree/compatible", CompatibleToken: "rockchip,rk3588",
			},
		},
		RuntimeAccess: &profilingrun.CandidateReportRuntimeAccess{Privileged: true, SupplementalGroups: []int64{44}},
	}
	rknn.State.HostContract = rknn.Delivery.HostContract
	for _, test := range []struct {
		name   string
		config profilingrun.RunConfig
	}{
		{name: "active", config: testRunConfig(t)},
		{name: "idle", config: testIdleRunConfig(t)},
		{name: "multiple typed inputs", config: multiInput},
		{name: "RKNN omits ORT threads", config: rknn},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := newTestFirstfloorRuntime(t, filepath.Join(t.TempDir(), "runtime-result.json"))
			payload, err := runtime.encodeRequest(runtimeRequest(test.config))
			if err != nil {
				t.Fatal(err)
			}
			python, err := exec.LookPath("python3")
			if err != nil {
				t.Fatal("python3 is required for the Go-to-Python runtime contract test")
			}
			runtimeDirectory, err := filepath.Abs("../../../../runtime/firstfloor")
			if err != nil {
				t.Fatal(err)
			}
			const validator = `
import sys
sys.path.insert(0, sys.argv[1])
import measurement
request = measurement.common.decode_request(sys.stdin.buffer)
measurement.validate_request(request)
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

func TestFirstfloorRuntimeFailsClosedOnStaleOrInvalidResult(t *testing.T) {
	config := testRunConfig(t)

	t.Run("stale result", func(t *testing.T) {
		resultPath := filepath.Join(t.TempDir(), "runtime-result.json")
		if err := os.WriteFile(resultPath, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		runtime := newTestFirstfloorRuntime(t, resultPath)
		if _, err := runtime.Measure(context.Background(), runtimeRequest(config)); err == nil ||
			!strings.Contains(err.Error(), "already exists") {
			t.Fatalf("expected stale result rejection, got %v", err)
		}
	})

	t.Run("unknown result field", func(t *testing.T) {
		t.Setenv("CHILL_FIRSTFLOOR_HELPER", "unknown-field")
		runtime := newTestFirstfloorRuntime(t, filepath.Join(t.TempDir(), "runtime-result.json"))
		if _, err := runtime.Measure(context.Background(), runtimeRequest(config)); err == nil ||
			!strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("expected strict result rejection, got %v", err)
		}
	})
}

func TestFirstfloorRuntimeRejectsUnsupportedPathBeforeExecution(t *testing.T) {
	resultPath := filepath.Join(t.TempDir(), "runtime-result.json")
	runtime := newTestFirstfloorRuntime(t, resultPath)
	config := testRunConfig(t)
	config.State.Backend = profilingrun.FirstfloorORTCUDABackend
	config.Delivery.RuntimeAccess = &profilingrun.CandidateReportRuntimeAccess{
		GPUUUID: "GPU-a", Resources: map[string]string{"nvidia.com/gpu": "1"},
	}
	if _, err := runtime.Measure(context.Background(), runtimeRequest(config)); err == nil ||
		!strings.Contains(err.Error(), "unsupported runtime path") {
		t.Fatalf("expected unsupported path rejection, got %v", err)
	}
	if _, err := os.Stat(resultPath); !os.IsNotExist(err) {
		t.Fatalf("unsupported path started the runtime process: %v", err)
	}
}

func TestFirstfloorRuntimeRequiresJobTimingBinding(t *testing.T) {
	directory := t.TempDir()
	_, err := NewFirstfloorRuntime(CPURuntimeConfig{
		Command: []string{"firstfloor"}, ModelPath: filepath.Join(directory, "model.onnx"),
		ObserverReadyPaths:      []string{filepath.Join(directory, "observer-ready")},
		WorkloadWindowStartPath: filepath.Join(directory, "workload-window-start"),
		MeasurementStartPath:    filepath.Join(directory, "measurement-start"),
		WorkloadWindowReadyPath: filepath.Join(directory, "workload-window-ready"),
		MeasurementEndPath:      filepath.Join(directory, "measurement-end"),
		ResultPath:              filepath.Join(directory, "result.json"), MaxResultBytes: 1 << 20,
		MaxLatencySamples: 100, MaxInputBytes: 1 << 20,
		Stdout: io.Discard, Stderr: io.Discard,
	}, runtimeRequest(testRunConfig(t)).RunConfig)
	if err == nil || !strings.Contains(err.Error(), "supplied by the Job") {
		t.Fatalf("expected missing observer-ready timeout rejection, got %v", err)
	}
}

func TestFirstfloorRuntimeRequiresJobResourceBudgets(t *testing.T) {
	directory := t.TempDir()
	_, err := NewFirstfloorRuntime(CPURuntimeConfig{
		Command: []string{"firstfloor"}, ModelPath: filepath.Join(directory, "model.onnx"),
		ObserverReadyPaths:      []string{filepath.Join(directory, "observer-ready")},
		WorkloadWindowStartPath: filepath.Join(directory, "workload-window-start"),
		MeasurementStartPath:    filepath.Join(directory, "measurement-start"),
		WorkloadWindowReadyPath: filepath.Join(directory, "workload-window-ready"),
		MeasurementEndPath:      filepath.Join(directory, "measurement-end"),
		ResultPath:              filepath.Join(directory, "result.json"), MaxResultBytes: 1 << 20,
		ObserverReadyTimeout: time.Second, Stdout: io.Discard, Stderr: io.Discard,
	}, runtimeRequest(testRunConfig(t)).RunConfig)
	if err == nil || !strings.Contains(err.Error(), "resource budgets") {
		t.Fatalf("expected missing resource budget rejection, got %v", err)
	}
}

func TestMetricResultFileWaitsForAtomicResult(t *testing.T) {
	config := testRunConfig(t)
	result := testMetricResult(config)
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
	record, err := got.Record()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(record.Source, result.Source) || record.Summary != result.Summary {
		t.Fatalf("unexpected metric result: %#v", got)
	}
	record.Source.NodeName = mutatedCopyName
	sealed, err := got.Record()
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Source.NodeName != result.Source.NodeName {
		t.Fatalf("sealed metric result was changed through a detached record: %#v", sealed)
	}
}

func TestPublishMetricResultDoesNotReplaceExistingHandoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metric-result.json")
	result := testMetricResult(testRunConfig(t))
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

func TestFirstfloorHelperProcess(t *testing.T) {
	mode := os.Getenv("CHILL_FIRSTFLOOR_HELPER")
	if mode == "" {
		return
	}
	var request cpuRuntimeRequest
	decoder := json.NewDecoder(os.Stdin)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		os.Exit(10)
	}
	if request.SchemaVersion != firstfloorRequestSchemaVersion || request.ModelPath == "" ||
		len(request.ObserverReadyPaths) == 0 || request.MeasurementStartPath == "" || request.MeasurementEndPath == "" ||
		request.ObserverReadyTimeoutMilliseconds < 1 || request.MaxLatencySamples < 1 ||
		request.MaxInputBytes < 1 || request.MaxResultBytes < 1 {
		os.Exit(11)
	}
	resultPath := helperResultPath(os.Args)
	if resultPath == "" {
		os.Exit(12)
	}
	if request.ScheduledNodeName != request.RunConfig.TargetNode.Name {
		os.Exit(11)
	}
	var result any
	if mode == idleSuccessTestMode {
		result = idleRuntimeResult(request.RunConfig, request.PodUID)
	} else {
		result = runtimeResult(request.RunConfig, request.PodUID)
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

func newTestFirstfloorRuntime(t *testing.T, resultPath string) *CPURuntime {
	t.Helper()
	directory := filepath.Dir(resultPath)
	runtime, err := NewFirstfloorRuntime(CPURuntimeConfig{
		Command:                 []string{os.Args[0], "-test.run=TestFirstfloorHelperProcess", "--"},
		ModelPath:               filepath.Join(directory, "model.onnx"),
		ObserverReadyPaths:      []string{filepath.Join(directory, "observer-ready")},
		WorkloadWindowStartPath: filepath.Join(directory, "workload-window-start"),
		MeasurementStartPath:    filepath.Join(directory, "measurement-start"),
		WorkloadWindowReadyPath: filepath.Join(directory, "workload-window-ready"),
		MeasurementEndPath:      filepath.Join(directory, "measurement-end"), ResultPath: resultPath,
		ObserverReadyTimeout: time.Second, MaxLatencySamples: 100, MaxInputBytes: 1 << 20,
		MaxResultBytes: 1 << 20, Stdout: io.Discard, Stderr: io.Discard,
	}, runtimeRequest(testRunConfig(t)).RunConfig)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func runtimeRequest(config profilingrun.RunConfig) RuntimeRequest {
	verified, err := profilingrun.ValidateRunConfig(config)
	if err != nil {
		panic(err)
	}
	return RuntimeRequest{RunConfig: verified, PodUID: testPodUID, ScheduledNodeName: config.TargetNode.Name}
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
