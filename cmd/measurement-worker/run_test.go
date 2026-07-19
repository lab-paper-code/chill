package main

import (
	"context"
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
	wantReceipt := validReceipt()
	coordinator := &fakeCoordinator{execute: func(request worker.Request) (evidencestore.Receipt, error) {
		if request.ExpectedIntentDigest != opts.intentDigest || request.PodUID != opts.podUID ||
			request.ScheduledNodeName != opts.scheduledNodeName ||
			request.RawLocation.Store != opts.storeName || request.RawLocation.Key != opts.rawObjectKey {
			t.Fatalf("request = %#v", request)
		}
		return wantReceipt, nil
	}}
	written := false
	deps := dependencies{
		loadIntent: func(path, digest string, maximum int64) (profilingrun.Intent, error) {
			if path != opts.intentPath || digest != opts.intentDigest || maximum != maximumIntentBytes {
				t.Fatalf("intent load = %q %q %d", path, digest, maximum)
			}
			return profilingrun.Intent{}, nil
		},
		buildCoordinator: func(got options, stdout, stderr io.Writer) (measurementCoordinator, error) {
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
		"intent":  {failure: errors.New("load failed"), want: worker.ErrExecutionContract},
		"runtime": {failure: worker.ErrRuntimeMeasurement, want: worker.ErrRuntimeMeasurement},
	} {
		t.Run(name, func(t *testing.T) {
			deps := successfulDependencies(validReceipt())
			if name == "intent" {
				deps.loadIntent = func(string, string, int64) (profilingrun.Intent, error) {
					return profilingrun.Intent{}, test.failure
				}
			} else {
				deps.buildCoordinator = func(options, io.Writer, io.Writer) (measurementCoordinator, error) {
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
	if _, err := buildCoordinator(validOptions(), io.Discard, io.Discard); err != nil {
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
	for want, failure := range map[int]error{
		2: worker.ErrExecutionContract,
		3: worker.ErrRuntimeMeasurement,
		4: worker.ErrPowerObservation,
		5: worker.ErrEvidenceCommit,
	} {
		if got := exitCode(failure); got != want {
			t.Fatalf("exitCode(%v) = %d, want %d", failure, got, want)
		}
	}
}

func TestDiagnosticDoesNotExposeEvidenceTransportDetails(t *testing.T) {
	failure := errors.Join(worker.ErrEvidenceCommit, errors.New("request http://internal-store:9000 failed"))
	if got := diagnostic(failure); got != worker.ErrEvidenceCommit.Error() {
		t.Fatalf("diagnostic = %q", got)
	}
}

func validOptions() options {
	return options{
		intentPath: defaultIntentPath, intentDigest: "sha256:" + strings.Repeat("a", 64), podUID: "pod-uid",
		scheduledNodeName: "lattepanda",
		modelPath:         defaultModelPath, workspacePath: defaultWorkspacePath,
		observerReadyPath: defaultObserverReadyPath, measurementStartPath: defaultMeasurementStartPath,
		powerResultPath: defaultPowerResultPath, terminationMessagePath: defaultTerminationMessagePath,
		runtimeCommand: []string{"python3", "/opt/chill/runtime/ortcpu/runner.py"},
		attemptTimeout: 10 * time.Minute, observerReadyTimeout: time.Minute, powerResultTimeout: time.Minute,
		maxLatencySamples: 1000, maxInputBytes: 1 << 20,
		maxRuntimeResultBytes: 1 << 20, maxPowerResultBytes: 1 << 20,
		storeName: "chill-evidence-store", storeEndpoint: "http://chill-evidence-store.chill-system.svc:9000",
		storeBucket: "chill-profiling-evidence", storeRegion: "us-east-1",
		rawObjectKey: "profiling-runs/run/raw/intent.json",
		accessKeyID:  "access-key", secretAccessKey: "secret-key",
	}
}

func validReceipt() evidencestore.Receipt {
	return evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion,
		Object: evidencestore.ObjectIdentity{
			Store: "chill-evidence-store", Key: "profiling-runs/run/raw/intent.json",
			Digest: "sha256:" + strings.Repeat("b", 64), Size: 100,
		},
		Outcome: evidencestore.CreateOutcomeCreated,
	}
}

func successfulDependencies(receipt evidencestore.Receipt) dependencies {
	return dependencies{
		loadIntent: func(string, string, int64) (profilingrun.Intent, error) {
			return profilingrun.Intent{}, nil
		},
		buildCoordinator: func(options, io.Writer, io.Writer) (measurementCoordinator, error) {
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
