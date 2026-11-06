package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/worker"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestExecuteClassifiesOnlyFrozenBudgetDuringActualExecution(t *testing.T) {
	for _, phase := range []string{
		evidence.StageStartup, evidence.StageObserver, evidence.StageExecution,
		evidence.StageResultValidation, evidence.StageEvidenceCommit,
	} {
		t.Run(phase, func(t *testing.T) {
			opts := validOptions(t)
			opts.attemptTimeout = 10 * time.Millisecond
			opts.runtimeFailedPath = filepath.Join(t.TempDir(), "runtime-failed")
			deps := successfulDependencies(validReceipt())
			deps.buildCoordinator = func(
				options, profilingrun.VerifiedRunConfig, io.Writer, io.Writer,
			) (measurementCoordinator, error) {
				return deadlinePhaseCoordinator{stage: phase}, nil
			}
			err := execute(context.Background(), opts, io.Discard, io.Discard, deps)
			if !errors.Is(err, context.DeadlineExceeded) || exitCode(err) != 3 {
				t.Fatalf("deadline error=%v exit=%d", err, exitCode(err))
			}
			payload, err := os.ReadFile(opts.terminationMessagePath)
			if err != nil {
				t.Fatal(err)
			}
			record, ok := evidence.DecodeTerminationMessage(string(payload))
			wantFailure := ""
			if phase == evidence.StageExecution {
				wantFailure = evidence.FailureMeasurementBudget
			}
			if !ok || record.Stage != phase || record.FailureReason != wantFailure {
				t.Fatalf("deadline phase=%s record=%#v", phase, record)
			}
		})
	}
	t.Run("earlier parent deadline", func(t *testing.T) {
		opts := validOptions(t)
		opts.runtimeFailedPath = filepath.Join(t.TempDir(), "runtime-failed")
		deps := successfulDependencies(validReceipt())
		deps.buildCoordinator = func(
			options, profilingrun.VerifiedRunConfig, io.Writer, io.Writer,
		) (measurementCoordinator, error) {
			return deadlinePhaseCoordinator{stage: evidence.StageExecution}, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		if err := execute(ctx, opts, io.Discard, io.Discard, deps); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("parent deadline=%v", err)
		}
		payload, err := os.ReadFile(opts.terminationMessagePath)
		if err != nil {
			t.Fatal(err)
		}
		record, ok := evidence.DecodeTerminationMessage(string(payload))
		if !ok || record.FailureReason != "" {
			t.Fatalf("parent deadline became measurement budget: %#v", record)
		}
	})
}

func TestNativeAllocationTerminationSurvivesWorkerCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination.json")
	payload := []byte(`{
		"schemaVersion":"chill.dacs.io/measurement-worker-termination.v1alpha1",
		"stage":"Execution",
		"failureReason":"MemoryAllocationFailed"
	}`)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := worker.WithTerminationMessagePath(context.Background(), path)
	for _, failure := range []error{worker.ErrRuntimeMeasurement, worker.ErrExecutionContract} {
		if err := worker.RecordTerminationFailure(ctx, failure); err != nil {
			t.Fatal(err)
		}
		retained, err := os.ReadFile(path)
		record, ok := evidence.DecodeTerminationMessage(string(retained))
		if err != nil || !ok ||
			record.Stage != evidence.StageExecution || record.FailureReason != evidence.FailureMemoryAllocation {
			t.Fatalf("cleanup replaced native failure: %s %v", retained, err)
		}
	}
}

type deadlinePhaseCoordinator struct{ stage string }

func (c deadlinePhaseCoordinator) Execute(ctx context.Context, _ worker.Request) (evidencestore.Receipt, error) {
	if err := worker.RecordTerminationStage(ctx, c.stage); err != nil {
		return evidencestore.Receipt{}, err
	}
	<-ctx.Done()
	return evidencestore.Receipt{}, errors.Join(worker.ErrRuntimeMeasurement, ctx.Err())
}
