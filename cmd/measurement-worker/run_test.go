package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/worker"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestExecuteCoordinatesOneRunConfigAndWritesReceipt(t *testing.T) {
	opts := validOptions(t)
	verified := validVerifiedCPUConfig(t)
	wantReceipt := validReceipt()
	coordinator := &fakeCoordinator{execute: func(request worker.Request) (evidencestore.Receipt, error) {
		if request.PodUID != opts.podUID || request.ScheduledNodeName != opts.scheduledNodeName ||
			request.RawLocation.Store != opts.storeName || request.RawLocation.Key != opts.rawObjectKey {
			t.Fatalf("request = %#v", request)
		}
		return wantReceipt, nil
	}}
	written := false
	deps := dependencies{
		parseRunConfig: func(raw string) (profilingrun.VerifiedRunConfig, error) {
			if raw != opts.runConfig {
				t.Fatalf("RunConfig parse = %q", raw)
			}
			return verified, nil
		},
		buildCoordinator: func(
			got options,
			_ profilingrun.VerifiedRunConfig,
			stdout, stderr io.Writer,
		) (measurementCoordinator, error) {
			if got.storeBucket != opts.storeBucket || stdout == nil || stderr == nil {
				t.Fatalf("builder options = %#v", got)
			}
			return coordinator, nil
		},
		writeReceipt: func(path string, receipt evidencestore.Receipt) error {
			written = true
			if path != opts.terminationMessagePath || receipt != wantReceipt {
				t.Fatalf("receipt write = %q %#v", path, receipt)
			}
			return nil
		},
	}
	if err := execute(context.Background(), opts, io.Discard, io.Discard, deps); err != nil {
		t.Fatal(err)
	}
	if coordinator.calls != 1 || !written {
		t.Fatalf("coordinator calls=%d receipt written=%t", coordinator.calls, written)
	}
}

func TestExecutePreservesStableFailureClass(t *testing.T) {
	opts := validOptions(t)
	t.Run("RunConfig", func(t *testing.T) {
		deps := successfulDependencies(validReceipt())
		deps.parseRunConfig = func(string) (profilingrun.VerifiedRunConfig, error) {
			return profilingrun.VerifiedRunConfig{}, errors.New("invalid")
		}
		err := execute(context.Background(), opts, io.Discard, io.Discard, deps)
		if !errors.Is(err, worker.ErrExecutionContract) {
			t.Fatalf("execute() error = %v", err)
		}
	})
	t.Run("runtime", func(t *testing.T) {
		deps := successfulDependencies(validReceipt())
		deps.buildCoordinator = func(
			options,
			profilingrun.VerifiedRunConfig,
			io.Writer,
			io.Writer,
		) (measurementCoordinator, error) {
			return &fakeCoordinator{execute: func(worker.Request) (evidencestore.Receipt, error) {
				return evidencestore.Receipt{}, worker.ErrRuntimeMeasurement
			}}, nil
		}
		err := execute(context.Background(), opts, io.Discard, io.Discard, deps)
		if !errors.Is(err, worker.ErrRuntimeMeasurement) {
			t.Fatalf("execute() error = %v", err)
		}
	})
}

func TestBuildCoordinatorAcceptsCompleteOptionsWithoutExternalIO(t *testing.T) {
	if _, err := buildCoordinator(validOptions(t), validVerifiedCPUConfig(t), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestWriteReceiptWritesOnlyBoundedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	if err := writeReceipt(path, validReceipt()); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	prefix := `{"schemaVersion":"chill.dacs.io/evidence-store-receipt.v1alpha1"`
	if !strings.HasPrefix(string(payload), prefix) || strings.HasSuffix(string(payload), "\n") {
		t.Fatalf("termination message = %q", payload)
	}
}

func TestExitCodeMapsWorkerFailureClasses(t *testing.T) {
	for _, test := range []struct {
		want    int
		failure error
	}{
		{2, worker.ErrExecutionContract}, {3, worker.ErrRuntimeMeasurement},
		{4, worker.ErrMetricObservation}, {4, worker.ErrWorkloadObservation}, {5, worker.ErrEvidenceCommit},
	} {
		if got := exitCode(test.failure); got != test.want {
			t.Fatalf("exitCode(%v) = %d, want %d", test.failure, got, test.want)
		}
	}
}

func TestExecutePublishesRuntimeFailedOnlyOnFailure(t *testing.T) {
	opts := validOptions(t)
	opts.runtimeFailedPath = filepath.Join(t.TempDir(), "runtime-failed")
	err := execute(context.Background(), opts, io.Discard, io.Discard, successfulDependencies(validReceipt()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(opts.runtimeFailedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful attempt published runtime-failed: %v", err)
	}

	deps := successfulDependencies(validReceipt())
	deps.buildCoordinator = func(
		options,
		profilingrun.VerifiedRunConfig,
		io.Writer,
		io.Writer,
	) (measurementCoordinator, error) {
		return &fakeCoordinator{execute: func(worker.Request) (evidencestore.Receipt, error) {
			return evidencestore.Receipt{}, worker.ErrRuntimeMeasurement
		}}, nil
	}
	err = execute(context.Background(), opts, io.Discard, io.Discard, deps)
	if !errors.Is(err, worker.ErrRuntimeMeasurement) {
		t.Fatalf("execute() error = %v", err)
	}
	if info, err := os.Stat(opts.runtimeFailedPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("failed attempt did not publish runtime-failed: %v", err)
	}
}

func validOptions(t *testing.T) options {
	t.Helper()
	config, err := validCPUConfig(t).CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return options{
		runConfig: string(config), podUID: "pod-uid", scheduledNodeName: "lattepanda",
		workspacePath:           defaultWorkspacePath,
		observerReadyPaths:      []string{defaultObserverReadyPath, defaultWorkloadObserverReadyPath},
		workloadWindowStartPath: defaultWorkloadWindowStartPath, measurementStartPath: defaultMeasurementStartPath,
		workloadWindowReadyPath: defaultWorkloadWindowReadyPath, measurementEndPath: defaultMeasurementEndPath,
		metricResultPath: defaultMetricResultPath, workloadResultPath: defaultWorkloadResultPath,
		terminationMessagePath: defaultTerminationMessagePath, runtimeFailedPath: defaultRuntimeFailedPath,
		firstfloorRuntimeCommand: []string{"python3", "/opt/chill/runtime/firstfloor/measurement.py"},
		attemptTimeout:           10 * time.Minute, observerReadyTimeout: time.Minute,
		metricResultTimeout: time.Minute, workloadResultTimeout: time.Minute,
		maxLatencySamples: 1000, maxInputBytes: 1 << 20,
		maxRuntimeResultBytes: 1 << 20, maxMetricResultBytes: 1 << 20, maxWorkloadResultBytes: 1 << 20,
		storeName: "chill-evidence-store", storeEndpoint: "http://chill-evidence-store.chill-system.svc:9000",
		storeBucket: "chill-profiling-evidence", storeRegion: "us-east-1",
		rawObjectKey: "profiling-runs/run/raw/result.json", accessKeyID: "access-key", secretAccessKey: "secret-key",
	}
}

func validCPUConfig(t *testing.T) profilingrun.RunConfig {
	t.Helper()
	payload, err := os.ReadFile("../../internal/profiling/profiler/worker/testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var config profilingrun.RunConfig
	if err := json.Unmarshal(payload, &config); err != nil {
		t.Fatal(err)
	}
	config.SchemaVersion = profilingrun.RunConfigSchemaVersion
	config.State.ArtifactFiles = []string{"/artifact/mobilenet-v2-050.onnx"}
	config.Images.WorkloadObserver = "registry.example/workload-observer@sha256:" + strings.Repeat("d", 64)
	config.Workload = profilingrun.CurrentWorkloadContract()
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	return config
}

func validVerifiedCPUConfig(t *testing.T) profilingrun.VerifiedRunConfig {
	t.Helper()
	verified, err := profilingrun.ValidateRunConfig(validCPUConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	return verified
}

func validReceipt() evidencestore.Receipt {
	return evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion,
		Object: evidencestore.ObjectIdentity{
			Store: "chill-evidence-store", Key: "profiling-runs/run/raw/result.json", Size: 100,
		},
		Outcome: evidencestore.CreateOutcomeCreated,
	}
}

func successfulDependencies(receipt evidencestore.Receipt) dependencies {
	return dependencies{
		parseRunConfig: func(string) (profilingrun.VerifiedRunConfig, error) {
			return profilingrun.VerifiedRunConfig{}, nil
		},
		buildCoordinator: func(
			options,
			profilingrun.VerifiedRunConfig,
			io.Writer,
			io.Writer,
		) (measurementCoordinator, error) {
			return &fakeCoordinator{execute: func(worker.Request) (evidencestore.Receipt, error) { return receipt, nil }}, nil
		},
		writeReceipt: func(string, evidencestore.Receipt) error { return nil },
	}
}

type fakeCoordinator struct {
	calls   int
	execute func(worker.Request) (evidencestore.Receipt, error)
}

func (c *fakeCoordinator) Execute(_ context.Context, request worker.Request) (evidencestore.Receipt, error) {
	c.calls++
	return c.execute(request)
}
