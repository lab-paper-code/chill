package worker

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestCoordinatorResultHandoffsConsumeRemainingAttemptBudget(t *testing.T) {
	// Explicit wall-time scaling only: each scenario second is 10ms. The real
	// Coordinator, file readers, atomic publishers, verification and raw commit
	// boundary execute unchanged; retained runtime timestamps still span 118s.
	const second = 10 * time.Millisecond
	const attempt = 181 * second
	for _, test := range []struct {
		name          string
		metricDelay   time.Duration
		workloadDelay time.Duration
		wantFailure   error
	}{
		{"118s runtime plus 35s final read commits", 35 * second, 0, nil},
		{"metric exceeds remaining 63s", 80 * second, 0, ErrMetricObservation},
		{"workload cannot reset budget after metric", 35 * second, 50 * second, ErrWorkloadObservation},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := testRunConfig(t)
			runtimeResult := testRuntimeResult(t, config)
			started, _, err := runtimeResult.MeasurementWindow()
			if err != nil {
				t.Fatal(err)
			}
			runtimeResult.MeasurementElapsedSeconds = 118
			runtimeResult.MeasurementEndedAt = started.Add(118 * time.Second).Format(time.RFC3339Nano)
			runtimeResult.RepetitionResults[0].MeasurementEndedAt = runtimeResult.MeasurementEndedAt
			for i := range runtimeResult.RepetitionResults[0].LatencyMilliseconds {
				runtimeResult.RepetitionResults[0].LatencyMilliseconds[i] = 2000
			}
			metricResult := testMetricResult(config)
			metricResult.EndedAt = started.Add(153 * time.Second)
			metricResult.Summary.ObservationDurationSeconds = metricResult.EndedAt.Sub(metricResult.StartedAt).Seconds()
			workloadResult := testWorkloadResult(t, config, runtimeResult)
			workloadResult.CoverageEndedAt = metricResult.EndedAt.Add(time.Second)
			directory := t.TempDir()
			metrics, err := NewMetricResultFile(MetricResultFileConfig{
				Path: filepath.Join(directory, "metric.json"), WaitTimeout: attempt,
				PollInterval: time.Millisecond, MaxBytes: 1 << 20,
			})
			if err != nil {
				t.Fatal(err)
			}
			workload, err := NewWorkloadResultFile(WorkloadResultFileConfig{
				Path: filepath.Join(directory, "workload.json"), WaitTimeout: attempt,
				PollInterval: time.Millisecond, MaxBytes: 1 << 20,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), attempt)
			defer cancel()
			published := make(chan error, 1)
			store := &fakeStore{put: func(identity evidencestore.ObjectIdentity, payload []byte) (evidencestore.Receipt, error) {
				if err := ctx.Err(); err != nil {
					return evidencestore.Receipt{}, err
				}
				var raw evidence.RawMeasurement
				if err := json.Unmarshal(payload, &raw); err != nil {
					t.Fatal(err)
				}
				if raw.Runtime.MeasurementElapsedSeconds != 118 || raw.Workload == nil {
					t.Fatalf("raw measurement lost long runtime/workload: %#v", raw)
				}
				return createdReceipt(identity), nil
			}}
			coordinator := mustCoordinator(t, runtimeFunc(func(ctx context.Context, _ RuntimeRequest) (profilingrun.RuntimeResult, error) {
				select {
				case <-time.After(118 * second):
				case <-ctx.Done():
					published <- nil
					return profilingrun.RuntimeResult{}, ctx.Err()
				}
				go func() {
					select {
					case <-time.After(test.metricDelay):
					case <-ctx.Done():
						published <- nil
						return
					}
					if err := PublishMetricResult(metrics.config.Path, metrics.config.MaxBytes, metricResult); err != nil {
						published <- err
						return
					}
					select {
					case <-time.After(test.workloadDelay):
					case <-ctx.Done():
						published <- nil
						return
					}
					published <- PublishWorkloadResult(workload.config.Path, workload.config.MaxBytes, workloadResult)
				}()
				return runtimeResult, nil
			}), metrics, workload, store)
			_, err = coordinator.Execute(ctx, Request{
				RunConfig: testVerifiedRunConfig(t, config), PodUID: testPodUID,
				ScheduledNodeName: config.TargetNode.Name,
				RawLocation:       evidencestore.ObjectLocation{Store: "chill-evidence", Key: "raw.json"},
			})
			cancel()
			if publishErr := <-published; publishErr != nil {
				t.Fatal(publishErr)
			}
			if test.wantFailure == nil {
				if err != nil || store.calls != 1 {
					t.Fatalf("within-budget handoff: err=%v commits=%d", err, store.calls)
				}
			} else if !errors.Is(err, test.wantFailure) || !errors.Is(err, context.DeadlineExceeded) || store.calls != 0 {
				t.Fatalf("over-budget handoff: err=%v commits=%d", err, store.calls)
			}
		})
	}
}

func TestResultReadersRejectPublishedFilesAfterAttemptDeadline(t *testing.T) {
	config := testRunConfig(t)
	directory := t.TempDir()
	metrics, err := NewMetricResultFile(MetricResultFileConfig{
		Path: filepath.Join(directory, "metric.json"), WaitTimeout: time.Second,
		PollInterval: time.Millisecond, MaxBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	workload, err := NewWorkloadResultFile(WorkloadResultFileConfig{
		Path: filepath.Join(directory, "workload.json"), WaitTimeout: time.Second,
		PollInterval: time.Millisecond, MaxBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := PublishMetricResult(metrics.config.Path, metrics.config.MaxBytes, testMetricResult(config)); err != nil {
		t.Fatal(err)
	}
	if err := PublishWorkloadResult(workload.config.Path, workload.config.MaxBytes,
		testWorkloadResult(t, config, testRuntimeResult(t, config))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := metrics.Read(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired attempt accepted metric handoff: %v", err)
	}
	if _, err := workload.Read(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired attempt accepted workload handoff: %v", err)
	}
}
