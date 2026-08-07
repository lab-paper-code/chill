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
	"github.com/lab-paper-code/chill/internal/profiler/worker"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

func TestExecuteCoordinatesOneRequestAndWritesReceipt(t *testing.T) {
	opts := validOptions()
	verifiedIntent := validVerifiedCPUIntent(t)
	wantReceipt := validReceipt()
	coordinator := &fakeCoordinator{execute: func(request worker.Request) (evidencestore.Receipt, error) {
		if request.PodUID != opts.podUID ||
			request.ScheduledNodeName != opts.scheduledNodeName ||
			request.RawLocation.Store != opts.storeName || request.RawLocation.Key != opts.rawObjectKey {
			t.Fatalf("request = %#v", request)
		}
		return wantReceipt, nil
	}}
	written := false
	deps := dependencies{
		loadIntent: func(path string, maximum int64) (profilingrun.VerifiedIntent, error) {
			if path != opts.intentPath || maximum != maximumIntentBytes {
				t.Fatalf("intent load = %q %d", path, maximum)
			}
			return verifiedIntent, nil
		},
		loadCandidateReport: func(
			path string,
			maximum int64,
			_ profilingrun.VerifiedIntent,
		) (profilingrun.VerifiedCandidateReport, error) {
			if path != opts.candidateReportPath || maximum != maximumCandidateReportBytes {
				t.Fatalf("candidate report load = %q %d", path, maximum)
			}
			return profilingrun.VerifiedCandidateReport{}, nil
		},
		buildCoordinator: func(
			got options,
			_ profilingrun.VerifiedIntent,
			_ profilingrun.VerifiedCandidateReport,
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
	opts := validOptions()
	for name, test := range map[string]struct {
		failure error
		want    error
	}{
		"intent":    {failure: errors.New("load failed"), want: worker.ErrExecutionContract},
		"candidate": {failure: errors.New("candidate failed"), want: worker.ErrExecutionContract},
		"runtime":   {failure: worker.ErrRuntimeMeasurement, want: worker.ErrRuntimeMeasurement},
	} {
		t.Run(name, func(t *testing.T) {
			deps := successfulDependencies(validReceipt())
			if name == "intent" {
				deps.loadIntent = func(string, int64) (profilingrun.VerifiedIntent, error) {
					return profilingrun.VerifiedIntent{}, test.failure
				}
			} else if name == "candidate" {
				deps.loadCandidateReport = func(
					string,
					int64,
					profilingrun.VerifiedIntent,
				) (profilingrun.VerifiedCandidateReport, error) {
					return profilingrun.VerifiedCandidateReport{}, test.failure
				}
			} else {
				deps.buildCoordinator = func(
					options,
					profilingrun.VerifiedIntent,
					profilingrun.VerifiedCandidateReport,
					io.Writer,
					io.Writer,
				) (measurementCoordinator, error) {
					return &fakeCoordinator{execute: func(worker.Request) (evidencestore.Receipt, error) {
						return evidencestore.Receipt{}, test.failure
					}}, nil
				}
			}
			err := execute(context.Background(), opts, io.Discard, io.Discard, deps)
			if !errors.Is(err, test.want) {
				t.Fatalf("execute() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestBuildCoordinatorAcceptsCompleteOptionsWithoutExternalIO(t *testing.T) {
	if _, err := buildCoordinator(
		validOptions(),
		validVerifiedCPUIntent(t),
		profilingrun.VerifiedCandidateReport{},
		io.Discard,
		io.Discard,
	); err != nil {
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
	if !strings.HasPrefix(string(payload), `{"schemaVersion":"chill.dacs.io/evidence-store-receipt.v1alpha1"`) ||
		strings.HasSuffix(string(payload), "\n") {
		t.Fatalf("termination message = %q", payload)
	}
}

func TestExitCodeMapsWorkerFailureClasses(t *testing.T) {
	tests := []struct {
		want    int
		failure error
	}{
		{2, worker.ErrExecutionContract},
		{3, worker.ErrRuntimeMeasurement},
		{4, worker.ErrMetricObservation},
		{4, worker.ErrWorkloadObservation},
		{5, worker.ErrEvidenceCommit},
	}
	for _, test := range tests {
		if got := exitCode(test.failure); got != test.want {
			t.Fatalf("exitCode(%v) = %d, want %d", test.failure, got, test.want)
		}
	}
}

func TestDiagnosticDoesNotExposeEvidenceTransportDetails(t *testing.T) {
	failure := errors.Join(
		worker.ErrEvidenceCommit,
		evidencestore.ErrTransport,
		errors.New("request http://internal-store:9000 failed"),
	)
	if got := diagnostic(failure); got != worker.ErrEvidenceCommit.Error()+": "+evidencestore.ErrTransport.Error() {
		t.Fatalf("diagnostic = %q", got)
	}
}

func validOptions() options {
	return options{
		intentPath: defaultIntentPath, candidateReportPath: defaultCandidateReportPath,
		podUID:            "pod-uid",
		scheduledNodeName: "lattepanda",
		modelPath:         defaultModelPath, workspacePath: defaultWorkspacePath,
		observerReadyPaths:      []string{defaultObserverReadyPath, defaultWorkloadObserverReadyPath},
		workloadWindowStartPath: defaultWorkloadWindowStartPath,
		measurementStartPath:    defaultMeasurementStartPath,
		workloadWindowReadyPath: defaultWorkloadWindowReadyPath,
		measurementEndPath:      defaultMeasurementEndPath,
		metricResultPath:        defaultMetricResultPath, workloadResultPath: defaultWorkloadResultPath,
		terminationMessagePath: defaultTerminationMessagePath,
		runtimeCommand:         []string{"python3", "/opt/chill/runtime/ortcpu/runner.py"},
		attemptTimeout:         10 * time.Minute, observerReadyTimeout: time.Minute,
		metricResultTimeout: time.Minute, workloadResultTimeout: time.Minute,
		maxLatencySamples: 1000, maxInputBytes: 1 << 20,
		maxRuntimeResultBytes: 1 << 20, maxMetricResultBytes: 1 << 20, maxWorkloadResultBytes: 1 << 20,
		storeName: "chill-evidence-store", storeEndpoint: "http://chill-evidence-store.chill-system.svc:9000",
		storeBucket: "chill-profiling-evidence", storeRegion: "us-east-1",
		rawObjectKey: "profiling-runs/run/raw/intent.json",
		accessKeyID:  "access-key", secretAccessKey: "secret-key",
	}
}

func validCPUIntent(t *testing.T) profilingrun.Intent {
	t.Helper()
	payload, err := os.ReadFile("../../internal/profiler/worker/testdata/lattepanda-ort-cpu-bs1.json")
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
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
	return intent
}

func validVerifiedCPUIntent(t *testing.T) profilingrun.VerifiedIntent {
	t.Helper()
	intent := validCPUIntent(t)
	payload, err := intent.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "intent.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := worker.LoadVerifiedIntent(path, int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func validReceipt() evidencestore.Receipt {
	return evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion,
		Object: evidencestore.ObjectIdentity{
			Store: "chill-evidence-store", Key: "profiling-runs/run/raw/intent.json",
			Size: 100,
		},
		Outcome: evidencestore.CreateOutcomeCreated,
	}
}

func successfulDependencies(receipt evidencestore.Receipt) dependencies {
	return dependencies{
		loadIntent: func(string, int64) (profilingrun.VerifiedIntent, error) {
			return profilingrun.VerifiedIntent{}, nil
		},
		loadCandidateReport: func(string, int64, profilingrun.VerifiedIntent) (profilingrun.VerifiedCandidateReport, error) {
			return profilingrun.VerifiedCandidateReport{}, nil
		},
		buildCoordinator: func(
			options,
			profilingrun.VerifiedIntent,
			profilingrun.VerifiedCandidateReport,
			io.Writer,
			io.Writer,
		) (measurementCoordinator, error) {
			return &fakeCoordinator{execute: func(worker.Request) (evidencestore.Receipt, error) {
				return receipt, nil
			}}, nil
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
