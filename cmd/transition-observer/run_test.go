package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	transitionworker "github.com/lab-paper-code/chill/internal/profiling/transitioncost/worker"
)

func TestExecuteLoadsBeforeBuildingExternalComponents(t *testing.T) {
	buildCalls := 0
	deps := testDependencies(testAttempt(), nil, &writeStub{})
	deps.load = func([]byte, int64) (attemptContract, error) { return attemptContract{}, errors.New("bad contract") }
	deps.build = func(options, attemptContract) (components, error) {
		buildCalls++
		return components{}, nil
	}
	if err := execute(context.Background(), testOptions(t), deps); err == nil || buildCalls != 0 {
		t.Fatalf("execute() error/build calls = %v/%d", err, buildCalls)
	}
}

func TestProbeReadyFileModeUsesOnlyTheMarker(t *testing.T) {
	opts := testOptions(t)
	opts.probeReadyFile = true
	opts.contractJSON = ""
	opts.maxContractBytes = 0
	opts.terminationPath = ""
	if err := opts.validate(); err != nil {
		t.Fatalf("probe options: %v", err)
	}
	if err := probeReadyFile(opts.readyPath); err == nil {
		t.Fatal("missing ready marker was accepted")
	}
	if err := os.WriteFile(opts.readyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := probeReadyFile(opts.readyPath); err != nil {
		t.Fatalf("regular ready marker: %v", err)
	}
}

func TestExecutePublishesReadyAndWritesRawAndReceiptOnce(t *testing.T) {
	opts := testOptions(t)
	marker := time.Now().UTC()
	writer := &writeStub{receipt: testReceipt()}
	err := execute(context.Background(), opts, testDependencies(
		testAttempt(), observationStub(true, transitioncost.RawObservation{ObservationStartedAt: marker}, nil), writer,
	))
	if err != nil {
		t.Fatal(err)
	}
	if writer.calls != 1 || !writer.raw.ObservationStartedAt.Equal(marker) {
		t.Fatalf("raw writes/marker = %d/%v", writer.calls, writer.raw.ObservationStartedAt)
	}
	if info, err := os.Stat(opts.readyPath); err != nil || info.Size() != 0 {
		t.Fatalf("ready file = %v/%v", info, err)
	}
	payload, err := os.ReadFile(opts.terminationPath)
	if err != nil {
		t.Fatal(err)
	}
	var receipt evidencestore.Receipt
	if err := json.Unmarshal(payload, &receipt); err != nil || receipt != testReceipt() {
		t.Fatalf("receipt = %#v, error %v", receipt, err)
	}
}

func TestExecuteDoesNotWriteBeforeReady(t *testing.T) {
	opts := testOptions(t)
	writer := &writeStub{}
	err := execute(context.Background(), opts, testDependencies(
		testAttempt(), observationStub(false, transitioncost.RawObservation{}, errors.New("not ready")), writer,
	))
	if err == nil || writer.calls != 0 || pathExists(opts.terminationPath) || pathExists(opts.readyPath) {
		t.Fatalf(
			"error/writes/receipt/ready = %v/%d/%t/%t",
			err, writer.calls, pathExists(opts.terminationPath), pathExists(opts.readyPath),
		)
	}
}

func TestExecuteWritesNoReceiptAfterObservationOrWriteError(t *testing.T) {
	for _, test := range []struct {
		name       string
		observeErr error
		writeErr   error
	}{
		{name: "observation", observeErr: errors.New("observation failed")},
		{name: "write", writeErr: errors.New("write failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := testOptions(t)
			writer := &writeStub{receipt: testReceipt(), err: test.writeErr}
			err := execute(context.Background(), opts, testDependencies(
				testAttempt(), observationStub(true, transitioncost.RawObservation{}, test.observeErr), writer,
			))
			if err == nil || writer.calls != 1 || pathExists(opts.terminationPath) {
				t.Fatalf("error/writes/receipt = %v/%d/%t", err, writer.calls, pathExists(opts.terminationPath))
			}
		})
	}
}

func TestExecuteDoesNotReplaceExistingReadyFile(t *testing.T) {
	opts := testOptions(t)
	if err := os.WriteFile(opts.readyPath, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	writer := &writeStub{}
	err := execute(context.Background(), opts, testDependencies(
		testAttempt(), observationStub(true, transitioncost.RawObservation{}, nil), writer,
	))
	payload, readErr := os.ReadFile(opts.readyPath)
	if err == nil || readErr != nil || string(payload) != "owned" ||
		writer.calls != 0 || pathExists(opts.terminationPath) {
		t.Fatalf("error/content/writes/receipt = %v/%q/%d/%t", err, payload, writer.calls, pathExists(opts.terminationPath))
	}
}

type writeStub struct {
	calls   int
	raw     transitioncost.RawObservation
	receipt evidencestore.Receipt
	err     error
}

func (s *writeStub) write(
	_ context.Context,
	_ evidencestore.ImmutableWriter,
	_ evidencestore.ObjectLocation,
	raw transitioncost.RawObservation,
) (evidencestore.Receipt, error) {
	s.calls++
	s.raw = raw
	return s.receipt, s.err
}

func testDependencies(
	attempt attemptContract,
	observe func(
		context.Context,
		transitionworker.PowerObserver,
		metricobserver.Request,
		transitionworker.FactObserver,
		func(),
	) (transitioncost.RawObservation, error),
	writer *writeStub,
) dependencies {
	return dependencies{
		load:    func([]byte, int64) (attemptContract, error) { return attempt, nil },
		build:   func(options, attemptContract) (components, error) { return components{}, nil },
		observe: observe, writeRaw: writer.write,
	}
}

func observationStub(
	ready bool,
	raw transitioncost.RawObservation,
	err error,
) func(
	context.Context,
	transitionworker.PowerObserver,
	metricobserver.Request,
	transitionworker.FactObserver,
	func(),
) (transitioncost.RawObservation, error) {
	return func(
		_ context.Context,
		_ transitionworker.PowerObserver,
		_ metricobserver.Request,
		_ transitionworker.FactObserver,
		callback func(),
	) (transitioncost.RawObservation, error) {
		if ready {
			callback()
		}
		return raw, err
	}
}

func testOptions(t *testing.T) options {
	dir := t.TempDir()
	return options{
		contractJSON: "{}", maxContractBytes: 2,
		readyPath: filepath.Join(dir, "ready"), terminationPath: filepath.Join(dir, "termination"),
	}
}

func testAttempt() attemptContract {
	return attemptContract{rawLocation: evidencestore.ObjectLocation{Store: "evidence", Key: "raw.json"}}
}

func testReceipt() evidencestore.Receipt {
	return evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion,
		Object:        evidencestore.ObjectIdentity{Store: "evidence", Key: "raw.json", Size: 10},
		Outcome:       evidencestore.CreateOutcomeCreated,
	}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
