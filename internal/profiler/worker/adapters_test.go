package worker

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestORTCPURuntimeUsesDedicatedResultFile(t *testing.T) {
	t.Setenv("CHILL_ORT_CPU_HELPER", "success")
	directory := t.TempDir()
	resultPath := filepath.Join(directory, "runtime-result.json")
	runtime := newTestORTCPURuntime(t, resultPath)
	intent := testIntent(t)

	result, err := runtime.Measure(context.Background(), intent, "pod-uid")
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != intent.State.Backend || result.ExperimentID != "pod-uid" {
		t.Fatalf("unexpected runtime result: %#v", result)
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
		if _, err := runtime.Measure(context.Background(), intent, "pod-uid"); err == nil ||
			!strings.Contains(err.Error(), "already exists") {
			t.Fatalf("expected stale result rejection, got %v", err)
		}
	})

	t.Run("unknown result field", func(t *testing.T) {
		t.Setenv("CHILL_ORT_CPU_HELPER", "unknown-field")
		runtime := newTestORTCPURuntime(t, filepath.Join(t.TempDir(), "runtime-result.json"))
		if _, err := runtime.Measure(context.Background(), intent, "pod-uid"); err == nil ||
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
	if _, err := runtime.Measure(context.Background(), intent, "pod-uid"); err == nil ||
		!strings.Contains(err.Error(), "unsupported runtime path") {
		t.Fatalf("expected unsupported path rejection, got %v", err)
	}
	if _, err := os.Stat(resultPath); !os.IsNotExist(err) {
		t.Fatalf("unsupported path started the runtime process: %v", err)
	}
}

func TestPowerResultFileWaitsForAtomicResult(t *testing.T) {
	intent := testIntent(t)
	result := testPowerResult(intent)
	directory := t.TempDir()
	path := filepath.Join(directory, "power-result.json")
	reader := newTestPowerResultFile(t, path, time.Second)

	done := make(chan error, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		done <- writeAtomicJSON(path, result)
	}()
	got, err := reader.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got.Source != result.Source || got.Summary != result.Summary {
		t.Fatalf("unexpected power result: %#v", got)
	}
}

func TestPowerResultFileRejectsInvalidHandoff(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		match   string
	}{
		{name: "unknown field", payload: `{"unknown":true}`, match: "unknown field"},
		{name: "trailing value", payload: `{}` + "\n{}", match: "trailing JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "power-result.json")
			if err := os.WriteFile(path, []byte(test.payload), 0o600); err != nil {
				t.Fatal(err)
			}
			reader := newTestPowerResultFile(t, path, time.Second)
			if _, err := reader.Read(context.Background()); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("expected %q rejection, got %v", test.match, err)
			}
		})
	}
}

func TestPowerResultFileRejectsOversizedOrNonRegularHandoff(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "power-result.json")
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		reader, err := NewPowerResultFile(PowerResultFileConfig{
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
		path := filepath.Join(directory, "power-result.json")
		if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		reader := newTestPowerResultFile(t, path, time.Second)
		if _, err := reader.Read(context.Background()); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("expected non-regular handoff rejection, got %v", err)
		}
	})
}

func TestPowerResultFileHasBoundedWait(t *testing.T) {
	reader := newTestPowerResultFile(t, filepath.Join(t.TempDir(), "missing.json"), 20*time.Millisecond)
	started := time.Now()
	_, err := reader.Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("expected bounded wait failure, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("power result wait exceeded its bounded margin")
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
	if request.SchemaVersion != ortCPURequestSchemaVersion || request.ModelPath == "" ||
		request.ObserverReadyPath == "" || request.MeasurementStartPath == "" {
		os.Exit(11)
	}
	resultPath := helperResultPath(os.Args)
	if resultPath == "" {
		os.Exit(12)
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
		Command:   []string{os.Args[0], "-test.run=TestORTCPUHelperProcess", "--"},
		ModelPath: filepath.Join(directory, "model.onnx"), ObserverReadyPath: filepath.Join(directory, "observer-ready"),
		MeasurementStartPath: filepath.Join(directory, "measurement-start"), ResultPath: resultPath,
		MaxResultBytes: 1 << 20, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func newTestPowerResultFile(t *testing.T, path string, timeout time.Duration) *PowerResultFile {
	t.Helper()
	reader, err := NewPowerResultFile(PowerResultFileConfig{
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
