package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/worker"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestExecuteEstablishesAttemptDeadlineBeforePreparation(t *testing.T) {
	for _, earlierParent := range []bool{false, true} {
		t.Run(map[bool]string{false: "frozen attempt", true: "earlier parent"}[earlierParent], func(t *testing.T) {
			opts := validOptions(t)
			opts.attemptTimeout = 181 * time.Millisecond
			opts.metricResultTimeout = opts.attemptTimeout
			opts.workloadResultTimeout = opts.attemptTimeout
			opts.runtimeFailedPath = filepath.Join(t.TempDir(), "runtime-failed")
			opts.metricResultPath = filepath.Join(t.TempDir(), "metric.json")
			opts.workloadResultPath = filepath.Join(t.TempDir(), "workload.json")
			verified := validVerifiedCPUConfig(t)
			ctx := context.Background()
			before := time.Now()
			latestDeadline := before.Add(opts.attemptTimeout)
			if earlierParent {
				var cancel context.CancelFunc
				latestDeadline = before.Add(90 * time.Millisecond)
				ctx, cancel = context.WithDeadline(ctx, latestDeadline)
				defer cancel()
			}
			preparedAt := time.Time{}
			deps := productionDependencies()
			deps.parseRunConfig = func(string) (profilingrun.VerifiedRunConfig, error) {
				time.Sleep(30 * time.Millisecond)
				preparedAt = time.Now()
				return verified, nil
			}
			deps.buildCoordinator = func(
				opts options, _ profilingrun.VerifiedRunConfig, _, _ io.Writer,
			) (measurementCoordinator, error) {
				metrics, err := worker.NewMetricResultFile(worker.MetricResultFileConfig{
					Path: opts.metricResultPath, WaitTimeout: opts.metricResultTimeout,
					PollInterval: time.Millisecond, MaxBytes: opts.maxMetricResultBytes,
				})
				if err != nil {
					return nil, err
				}
				workload, err := worker.NewWorkloadResultFile(worker.WorkloadResultFileConfig{
					Path: opts.workloadResultPath, WaitTimeout: opts.workloadResultTimeout,
					PollInterval: time.Millisecond, MaxBytes: opts.maxWorkloadResultBytes,
				})
				if err != nil {
					return nil, err
				}
				runtime := attemptBudgetRuntime(func(
					ctx context.Context, _ worker.RuntimeRequest,
				) (worker.RuntimeMeasurement, error) {
					deadline, ok := ctx.Deadline()
					if !ok || deadline.After(latestDeadline.Add(10*time.Millisecond)) ||
						!deadline.Before(preparedAt.Add(opts.attemptTimeout-10*time.Millisecond)) {
						t.Fatalf("attempt deadline was omitted/reset: deadline=%v prepared=%v latest=%v",
							deadline, preparedAt, latestDeadline)
					}
					// Runtime completes; the real Coordinator/file reader must still
					// stop on that same deadline, not start a new result allowance.
					return worker.RuntimeMeasurement{}, nil
				})
				return worker.NewCoordinator(runtime, metrics, workload, attemptBudgetStore{t: t})
			}
			deps.writeReceipt = func(string, evidencestore.Receipt) error {
				t.Fatal("expired attempt wrote a receipt")
				return nil
			}
			if err := execute(ctx, opts, io.Discard, io.Discard, deps); !errors.Is(err, context.DeadlineExceeded) ||
				!errors.Is(err, worker.ErrMetricObservation) {
				t.Fatalf("execute() = %v, want deadline at metric handoff", err)
			}
		})
	}
}

type attemptBudgetRuntime func(context.Context, worker.RuntimeRequest) (worker.RuntimeMeasurement, error)

func (f attemptBudgetRuntime) Measure(
	ctx context.Context, request worker.RuntimeRequest,
) (worker.RuntimeMeasurement, error) {
	return f(ctx, request)
}

type attemptBudgetStore struct{ t *testing.T }

func (s attemptBudgetStore) PutImmutable(
	context.Context, evidencestore.ObjectIdentity, io.Reader,
) (evidencestore.Receipt, error) {
	s.t.Fatal("expired attempt committed evidence")
	return evidencestore.Receipt{}, nil
}
