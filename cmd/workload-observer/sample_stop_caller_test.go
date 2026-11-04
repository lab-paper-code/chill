package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/worker"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

type sampleStopRuntimeFunc func(context.Context, worker.RuntimeRequest) (worker.RuntimeMeasurement, error)

func (f sampleStopRuntimeFunc) Measure(
	ctx context.Context, request worker.RuntimeRequest,
) (worker.RuntimeMeasurement, error) {
	return f(ctx, request)
}

// This is the simulated inference boundary, not native hardware evidence. The
// observer executable, HTTP retries, LIST/WATCH, file readers and Coordinator are real.
func sampleStopRuntime(
	t *testing.T, directory string, opts options, config profilingrun.RunConfig,
	plan profiler.ExecutionPlan, started, ended time.Time,
) *worker.CPURuntime {
	t.Helper()
	payload, err := json.Marshal(sampleStopRuntimeRecord(config, opts.podUID, started, ended))
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(directory, "simulated-runtime.json")
	if err := os.WriteFile(fixture, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	verified, err := profilingrun.ValidateRunConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := worker.NewFirstfloorRuntime(worker.CPURuntimeConfig{
		// The adapter appends --result-file PATH; copy only this generated fixture.
		Command:                 []string{"sh", "-c", `cp "$1" "$3"`, "sample-stop", fixture},
		ObserverReadyPaths:      []string{opts.readyPath},
		WorkloadWindowStartPath: opts.windowStartPath, WorkloadWindowReadyPath: opts.windowReadyPath,
		MeasurementStartPath: filepath.Join(directory, "measurement-start"), MeasurementEndPath: opts.endPath,
		ResultPath: filepath.Join(directory, "runtime-result.json"), ObserverReadyTimeout: plan.ObserverReadyTimeout,
		MaxLatencySamples: plan.MaxLatencySamples, MaxInputBytes: plan.MaxInputBytes,
		MaxResultBytes: plan.MaxRuntimeResultBytes, Stdout: io.Discard, Stderr: io.Discard,
	}, verified)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func sampleStopCommit(
	t *testing.T, ctx context.Context, runtime worker.Runtime, config profilingrun.RunConfig,
	opts options, metricPath, directory string, plan profiler.ExecutionPlan,
) {
	t.Helper()
	metrics, err := worker.NewMetricResultFile(worker.MetricResultFileConfig{
		Path: metricPath, WaitTimeout: plan.MetricResultTimeout,
		PollInterval: time.Millisecond, MaxBytes: plan.MaxMetricResultBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	workload, err := worker.NewWorkloadResultFile(worker.WorkloadResultFileConfig{
		Path: opts.resultPath, WaitTimeout: plan.WorkloadResultTimeout,
		PollInterval: time.Millisecond, MaxBytes: plan.MaxWorkloadResultBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &sampleStopRawStore{original: ctx, path: filepath.Join(directory, "raw.json")}
	coordinator, err := worker.NewCoordinator(runtime, metrics, workload, store)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := profilingrun.ValidateRunConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := coordinator.Execute(ctx, worker.Request{
		RunConfig: verified, PodUID: opts.podUID, ScheduledNodeName: config.TargetNode.Name,
		RawLocation: evidencestore.ObjectLocation{Store: "chill-evidence", Key: "sample-stop/raw.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := receipt.Validate(); err != nil || receipt.Outcome != evidencestore.CreateOutcomeCreated {
		t.Fatalf("raw commit receipt: %+v err=%v", receipt, err)
	}
	if store.commits != 1 || receipt.Object.Size != int64(len(store.payload)) {
		t.Fatalf("expected one complete immutable raw commit: commits=%d receipt=%+v", store.commits, receipt)
	}
	var raw evidence.RawMeasurement
	if err := json.Unmarshal(store.payload, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Runtime.InferenceCount != 59 || raw.Workload == nil || !raw.Workload.Continuous ||
		raw.Runtime.MeasurementElapsedSeconds < 118 || raw.Metrics.Summary.ObservationDurationSeconds < 153 {
		t.Fatalf("raw commit lost real observer/caller coverage: %+v", raw)
	}
}

// Fake immutable object storage is the sole persistence boundary; use O_EXCL so
// a repeated write cannot silently replace the original raw object.
type sampleStopRawStore struct {
	original context.Context
	path     string
	payload  []byte
	commits  int
}

func (s *sampleStopRawStore) PutImmutable(
	ctx context.Context, identity evidencestore.ObjectIdentity, reader io.Reader,
) (evidencestore.Receipt, error) {
	if ctx != s.original || ctx.Err() != nil {
		return evidencestore.Receipt{}, fmt.Errorf("raw commit did not retain the live original attempt context")
	}
	payload, err := io.ReadAll(reader)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	if int64(len(payload)) != identity.Size {
		return evidencestore.Receipt{}, fmt.Errorf("raw identity size mismatch")
	}
	var raw evidence.RawMeasurement
	if err := json.Unmarshal(payload, &raw); err != nil {
		return evidencestore.Receipt{}, err
	}
	if err := raw.Validate(); err != nil {
		return evidencestore.Receipt{}, err
	}
	file, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	_, writeErr := file.Write(payload)
	closeErr := file.Close()
	if writeErr != nil {
		return evidencestore.Receipt{}, writeErr
	}
	if closeErr != nil {
		return evidencestore.Receipt{}, closeErr
	}
	s.payload, s.commits = payload, s.commits+1
	return evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: identity, Outcome: evidencestore.CreateOutcomeCreated,
	}, nil
}

func sampleStopRuntimeRecord(
	config profilingrun.RunConfig, podUID string, started, ended time.Time,
) profilingrun.RuntimeResult {
	const provider = "CPUExecutionProvider"
	cpu := profilingrun.RuntimeCPU{
		CgroupCPUMax: "400000 100000", AffinityCPUCount: 4,
		AffinityCPUs: []int{0, 1, 2, 3}, CPUSetEffective: "0-3",
	}
	recordIDs, units := make([]string, config.Measurement.Batch), make([]int, config.Measurement.Batch)
	for i := range recordIDs {
		recordIDs[i], units[i] = "record-"+strconv.Itoa(i), 1
	}
	latencies := make([]float64, 59)
	native := make([]profilingrun.NativeServiceSample, 59)
	for i := range native {
		latencies[i] = 2000
		native[i] = profilingrun.NativeServiceSample{
			BatchID: uint64(i + 1), BatchSize: config.Measurement.Batch, RecordIDs: recordIDs, Units: units,
			RunnerServiceMilliseconds: 2000, BackendCallMilliseconds: 2000, NativeRequestMilliseconds: 2000,
		}
	}
	target := float64(config.Measurement.DurationSeconds)
	return profilingrun.RuntimeResult{
		SchemaVersion: profilingrun.RuntimeResultSchemaVersion, Status: "Succeeded", ExperimentID: podUID,
		NodeName: config.TargetNode.Name, Model: config.State.Model, Architecture: "x86_64",
		Runtime: config.State.RuntimeFamily, RuntimeVersion: "1.16.0", Backend: config.State.Backend,
		Provider: provider, AvailableProviders: []string{provider}, BatchSize: config.Measurement.Batch,
		Inputs: config.State.Preprocessor.Inputs, WarmupIterations: config.Measurement.WarmupIterations,
		WarmupCompletedAt: started.Add(-time.Second).Format(time.RFC3339Nano),
		InferenceCount:    59, MeasurementMode: "duration", MeasurementStartedAt: started.Format(time.RFC3339Nano),
		MeasurementEndedAt: ended.Format(time.RFC3339Nano), MeasurementElapsedSeconds: ended.Sub(started).Seconds(),
		TargetDurationSeconds: &target, CPUContract: cpu,
		DerivedContract: profilingrun.DerivedContract{
			Backend: config.State.Backend, Provider: provider, NodeSnapshot: config.TargetNode,
		},
		MemoryBytes: 123456, MemorySource: "cgroup-v2-memory.peak:runtime-container-lifetime",
		Firstfloor: &profilingrun.FirstfloorRuntimeEvidence{
			Mode: "profiling", ModelInstanceCount: 1, MaxBatchSize: 4, MaxQueueSize: 32,
			DeadlinePolicy: modelcompat.EffectiveDeadlinePolicy(config.State.Firstfloor.DeadlinePolicy),
			ExecutionCount: 59, InferenceCount: 59 * uint64(config.Measurement.Batch),
			BatchExecutionCounts: map[string]uint64{strconv.Itoa(config.Measurement.Batch): 59},
			Runtime: firstfloor.RuntimeMetadata{
				Runtime: config.State.Backend, RuntimeVersion: "1.16.0", ModelFiles: config.State.ModelFiles(),
				Inputs: config.State.Preprocessor.Inputs, Outputs: config.State.Preprocessor.Outputs,
				Providers: []string{provider}, AvailableProviders: []string{provider}, Device: "cpu",
				CPUAffinity: cpu.AffinityCPUs, InitializedBatches: []int{config.Measurement.Batch},
				CgroupCPUMax: cpu.CgroupCPUMax, CPUSet: cpu.CPUSetEffective,
			},
		},
		RepetitionResults: []profilingrun.Repetition{{
			Repetition: 1, MeasurementStartedAt: started.Format(time.RFC3339Nano),
			MeasurementEndedAt: ended.Format(time.RFC3339Nano), LatencyMilliseconds: latencies, NativeSamples: native,
		}},
	}
}
