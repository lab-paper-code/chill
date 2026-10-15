package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

func TestCoordinatorCommitsCanonicalRawMeasurementInOrder(t *testing.T) {
	config := testRunConfig(t)
	runtimeResult := testRuntimeResult(t, config)
	metricResult := testMetricResult(config)
	var calls []string
	store := &fakeStore{put: func(identity evidencestore.ObjectIdentity, payload []byte) (evidencestore.Receipt, error) {
		calls = append(calls, "store")
		if err := evidencestore.ValidateSize(strings.NewReader(string(payload)), identity.Size); err != nil {
			t.Fatal(err)
		}
		var raw evidence.RawMeasurement
		if err := json.Unmarshal(payload, &raw); err != nil {
			t.Fatal(err)
		}
		if raw.PodUID != testPodUID || raw.Runtime.Status != "Succeeded" ||
			raw.Metrics.Source.NodeName != config.TargetNode.Name || raw.Workload == nil ||
			raw.Workload.Target.NodeUID != config.TargetNode.UID {
			t.Fatalf("unexpected raw measurement: %#v", raw)
		}
		return receipt(identity, evidencestore.CreateOutcomeCreated), nil
	}}
	coordinator := mustCoordinator(t,
		runtimeFunc(func(_ context.Context, request RuntimeRequest) (profilingrun.RuntimeResult, error) {
			calls = append(calls, "runtime")
			if request.PodUID != testPodUID || request.ScheduledNodeName != config.TargetNode.Name {
				t.Fatalf("unexpected runtime request: %#v", request)
			}
			return runtimeResult, nil
		}),
		metricReaderFunc(func(context.Context) (metricobserver.Result, error) {
			calls = append(calls, "metrics")
			return metricResult, nil
		}),
		workloadReaderFunc(func(context.Context) (workloadobserver.Result, error) {
			calls = append(calls, "workload")
			return testWorkloadResult(t, config, runtimeResult), nil
		}),
		store,
	)

	got, err := coordinator.Execute(context.Background(), Request{
		RunConfig: testVerifiedRunConfig(t, config), PodUID: testPodUID, ScheduledNodeName: config.TargetNode.Name,
		RawLocation: evidencestore.ObjectLocation{Store: "chill-evidence", Key: "profiling-runs/run/raw/result.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "runtime,metrics,workload,store" || got != store.receipt {
		t.Fatalf("unexpected execution: calls=%v receipt=%#v", calls, got)
	}
}

func TestCoordinatorCommitsCanonicalIdleRawMeasurement(t *testing.T) {
	config := testIdleRunConfig(t)
	idle := idleRuntimeResult(config, testPodUID)
	store := &fakeStore{put: func(identity evidencestore.ObjectIdentity, payload []byte) (evidencestore.Receipt, error) {
		var raw evidence.IdleRawMeasurement
		if err := json.Unmarshal(payload, &raw); err != nil {
			t.Fatal(err)
		}
		if raw.PodUID != testPodUID || raw.Runtime.InferenceCountAfterModelLoad != 0 ||
			!raw.Runtime.ModelResidentAtEnd || raw.Workload == nil {
			t.Fatalf("unexpected idle raw measurement: %#v", raw)
		}
		return receipt(identity, evidencestore.CreateOutcomeCreated), nil
	}}
	coordinator := mustCoordinator(t,
		runtimeMeasurementFunc(func(_ context.Context, request RuntimeRequest) (RuntimeMeasurement, error) {
			verified, err := profilingrun.VerifyIdleRuntimeResult(
				request.RunConfig, request.PodUID, idle,
			)
			if err != nil {
				return RuntimeMeasurement{}, err
			}
			return idleRuntimeMeasurement(verified), nil
		}),
		metricReaderFunc(func(context.Context) (metricobserver.Result, error) { return testMetricResult(config), nil }),
		workloadReaderFunc(func(context.Context) (workloadobserver.Result, error) {
			return testIdleWorkloadResult(t, config, idle), nil
		}),
		store,
	)
	if _, err := coordinator.Execute(context.Background(), Request{
		RunConfig: testVerifiedRunConfig(t, config), PodUID: testPodUID, ScheduledNodeName: config.TargetNode.Name,
		RawLocation: evidencestore.ObjectLocation{Store: "chill-evidence", Key: "idle/raw.json"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorRejectsEmptyVerifiedRunConfigBeforeExternalWork(t *testing.T) {
	config := testRunConfig(t)
	runtimeCalls := 0
	coordinator := mustCoordinator(t,
		runtimeFunc(func(context.Context, RuntimeRequest) (profilingrun.RuntimeResult, error) {
			runtimeCalls++
			return profilingrun.RuntimeResult{}, nil
		}),
		metricReaderFunc(func(context.Context) (metricobserver.Result, error) {
			t.Fatal("metric reader must not be called")
			return metricobserver.Result{}, nil
		}),
		testWorkloadReader(t, config),
		&fakeStore{},
	)

	_, err := coordinator.Execute(context.Background(), Request{
		RunConfig: profilingrun.VerifiedRunConfig{}, PodUID: testPodUID,
		ScheduledNodeName: config.TargetNode.Name,
		RawLocation:       evidencestore.ObjectLocation{Store: "chill-evidence", Key: "raw.json"},
	})
	if !errors.Is(err, ErrExecutionContract) || runtimeCalls != 0 {
		t.Fatalf("expected empty RunConfig rejection, got err=%v calls=%d", err, runtimeCalls)
	}
}

func TestCoordinatorRejectsScheduledNodeDriftBeforeExternalWork(t *testing.T) {
	config := testRunConfig(t)
	runtimeCalls := 0
	coordinator := mustCoordinator(t,
		runtimeFunc(func(context.Context, RuntimeRequest) (profilingrun.RuntimeResult, error) {
			runtimeCalls++
			return profilingrun.RuntimeResult{}, nil
		}),
		metricReaderFunc(func(context.Context) (metricobserver.Result, error) {
			t.Fatal("metric reader must not be called")
			return metricobserver.Result{}, nil
		}),
		testWorkloadReader(t, config),
		&fakeStore{},
	)

	_, err := coordinator.Execute(context.Background(), Request{
		RunConfig: testVerifiedRunConfig(t, config), PodUID: testPodUID, ScheduledNodeName: "other-node",
		RawLocation: evidencestore.ObjectLocation{Store: "chill-evidence", Key: "raw.json"},
	})
	if !errors.Is(err, ErrExecutionContract) || !strings.Contains(err.Error(), "scheduled Node") || runtimeCalls != 0 {
		t.Fatalf("expected pre-execution Node drift rejection, got err=%v calls=%d", err, runtimeCalls)
	}
}

func TestCoordinatorStopsAtInvalidRuntimeOrMetricEvidence(t *testing.T) {
	config := testRunConfig(t)
	request := Request{
		RunConfig: testVerifiedRunConfig(t, config), PodUID: testPodUID, ScheduledNodeName: config.TargetNode.Name,
		RawLocation: evidencestore.ObjectLocation{Store: "chill-evidence", Key: "raw.json"},
	}

	t.Run("runtime", func(t *testing.T) {
		runtimeResult := testRuntimeResult(t, config)
		runtimeResult.NodeName = testDifferentIdentity
		metricCalls := 0
		coordinator := mustCoordinator(t,
			runtimeFunc(func(context.Context, RuntimeRequest) (profilingrun.RuntimeResult, error) {
				return runtimeResult, nil
			}),
			metricReaderFunc(func(context.Context) (metricobserver.Result, error) {
				metricCalls++
				return metricobserver.Result{}, nil
			}),
			testWorkloadReader(t, config),
			&fakeStore{},
		)
		if _, err := coordinator.Execute(context.Background(), request); !errors.Is(err, ErrRuntimeMeasurement) || metricCalls != 0 {
			t.Fatalf("expected runtime rejection before metric read, got err=%v calls=%d", err, metricCalls)
		}
	})

	t.Run("metrics", func(t *testing.T) {
		metricResult := testMetricResult(config)
		metricResult.Source.NodeName = testDifferentIdentity
		store := &fakeStore{}
		coordinator := mustCoordinator(t,
			runtimeFunc(func(context.Context, RuntimeRequest) (profilingrun.RuntimeResult, error) {
				return testRuntimeResult(t, config), nil
			}),
			metricReaderFunc(func(context.Context) (metricobserver.Result, error) { return metricResult, nil }),
			testWorkloadReader(t, config),
			store,
		)
		if _, err := coordinator.Execute(context.Background(), request); !errors.Is(err, ErrMetricObservation) || store.calls != 0 {
			t.Fatalf("expected metric rejection before store, got err=%v calls=%d", err, store.calls)
		}
	})

	t.Run("workload", func(t *testing.T) {
		store := &fakeStore{}
		coordinator := mustCoordinator(t,
			runtimeFunc(func(context.Context, RuntimeRequest) (profilingrun.RuntimeResult, error) {
				return testRuntimeResult(t, config), nil
			}),
			metricReaderFunc(func(context.Context) (metricobserver.Result, error) {
				return testMetricResult(config), nil
			}),
			workloadReaderFunc(func(context.Context) (workloadobserver.Result, error) {
				return workloadobserver.Result{}, errors.New("handoff failed")
			}),
			store,
		)
		if _, err := coordinator.Execute(context.Background(), request); !errors.Is(err, ErrWorkloadObservation) || store.calls != 0 {
			t.Fatalf("expected workload rejection before store, got err=%v calls=%d", err, store.calls)
		}
	})
}

func TestCoordinatorRejectsInvalidWriterReceipt(t *testing.T) {
	config := testRunConfig(t)
	store := &fakeStore{put: func(identity evidencestore.ObjectIdentity, _ []byte) (evidencestore.Receipt, error) {
		identity.Size++
		return receipt(identity, evidencestore.CreateOutcomeCreated), nil
	}}
	coordinator := mustCoordinator(t,
		runtimeFunc(func(context.Context, RuntimeRequest) (profilingrun.RuntimeResult, error) {
			return testRuntimeResult(t, config), nil
		}),
		metricReaderFunc(func(context.Context) (metricobserver.Result, error) { return testMetricResult(config), nil }),
		testWorkloadReader(t, config),
		store,
	)
	_, err := coordinator.Execute(context.Background(), Request{
		RunConfig: testVerifiedRunConfig(t, config), PodUID: testPodUID, ScheduledNodeName: config.TargetNode.Name,
		RawLocation: evidencestore.ObjectLocation{Store: "chill-evidence", Key: "raw.json"},
	})
	if !errors.Is(err, ErrEvidenceCommit) || !strings.Contains(err.Error(), "invalid receipt") {
		t.Fatalf("expected invalid receipt rejection, got %v", err)
	}
}

type runtimeFunc func(context.Context, RuntimeRequest) (profilingrun.RuntimeResult, error)

func (f runtimeFunc) Measure(
	ctx context.Context,
	request RuntimeRequest,
) (RuntimeMeasurement, error) {
	result, err := f(ctx, request)
	if err != nil {
		return RuntimeMeasurement{}, err
	}
	verified, err := profilingrun.VerifyRuntimeResult(request.RunConfig, request.PodUID, result)
	if err != nil {
		return RuntimeMeasurement{}, err
	}
	return activeRuntimeMeasurement(verified), nil
}

type runtimeMeasurementFunc func(context.Context, RuntimeRequest) (RuntimeMeasurement, error)

func (f runtimeMeasurementFunc) Measure(
	ctx context.Context,
	request RuntimeRequest,
) (RuntimeMeasurement, error) {
	return f(ctx, request)
}

type metricReaderFunc func(context.Context) (metricobserver.Result, error)

func (f metricReaderFunc) Read(ctx context.Context) (metricobserver.VerifiedResult, error) {
	result, err := f(ctx)
	if err != nil {
		return metricobserver.VerifiedResult{}, err
	}
	return metricobserver.VerifyResult(result)
}

type workloadReaderFunc func(context.Context) (workloadobserver.Result, error)

func (f workloadReaderFunc) Read(ctx context.Context) (workloadobserver.VerifiedResult, error) {
	result, err := f(ctx)
	if err != nil {
		return workloadobserver.VerifiedResult{}, err
	}
	return workloadobserver.VerifyResult(result)
}

type fakeStore struct {
	calls   int
	receipt evidencestore.Receipt
	put     func(evidencestore.ObjectIdentity, []byte) (evidencestore.Receipt, error)
}

func (s *fakeStore) PutImmutable(
	_ context.Context,
	identity evidencestore.ObjectIdentity,
	content io.Reader,
) (evidencestore.Receipt, error) {
	s.calls++
	payload, err := io.ReadAll(content)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	if s.put == nil {
		return evidencestore.Receipt{}, errors.New("unexpected store call")
	}
	s.receipt, err = s.put(identity, payload)
	return s.receipt, err
}

func mustCoordinator(
	t *testing.T,
	runtime Runtime,
	power MetricResultReader,
	workload WorkloadResultReader,
	store evidencestore.ImmutableWriter,
) *Coordinator {
	t.Helper()
	coordinator, err := NewCoordinator(runtime, power, workload, store)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func receipt(identity evidencestore.ObjectIdentity, outcome evidencestore.CreateOutcome) evidencestore.Receipt {
	return evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion,
		Object:        identity,
		Outcome:       outcome,
	}
}

func testRunConfig(t *testing.T) profilingrun.RunConfig {
	t.Helper()
	payload, err := os.ReadFile("testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var config profilingrun.RunConfig
	if err := json.Unmarshal(payload, &config); err != nil {
		t.Fatal(err)
	}
	config.SchemaVersion = profilingrun.RunConfigSchemaVersion
	config.State.ArtifactPath = "/artifact/mobilenet-v2-050.onnx"
	config.Images.WorkloadObserver = "registry.example/workload-observer@sha256:" + strings.Repeat("d", 64)
	config.Workload = profilingrun.CurrentWorkloadContract()
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	return config
}

func testVerifiedRunConfig(t *testing.T, config profilingrun.RunConfig) profilingrun.VerifiedRunConfig {
	t.Helper()
	verified, err := profilingrun.ValidateRunConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	return verified
}

func testRuntimeResult(t *testing.T, config profilingrun.RunConfig) profilingrun.RuntimeResult {
	t.Helper()
	return runtimeResult(config, testPodUID)
}

func testIdleRunConfig(t *testing.T) profilingrun.RunConfig {
	t.Helper()
	config := testRunConfig(t)
	config.Measurement.WarmupIterations = 0
	config.Measurement.SettlingDurationSeconds = 30
	config.Measurement.WorkloadMode = profilingrun.IdleModelLoadedWorkloadMode
	config.Measurement.InputGenerator = profilingrun.NoInputGenerator
	config.Measurement.OutputValidation = profilingrun.NoOutputValidation
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	return config
}

func idleRuntimeResult(config profilingrun.RunConfig, podUID string) profilingrun.IdleRuntimeResult {
	modelLoaded := time.Date(2026, time.July, 16, 2, 59, 0, 0, time.UTC)
	settlingStarted := modelLoaded.Add(time.Second)
	settlingCompleted := settlingStarted.Add(time.Duration(config.Measurement.SettlingDurationSeconds) * time.Second)
	measurementEnded := settlingCompleted.Add(time.Duration(config.Measurement.DurationSeconds) * time.Second)
	return profilingrun.IdleRuntimeResult{
		SchemaVersion:                profilingrun.IdleRuntimeResultSchemaVersion,
		Status:                       "Succeeded",
		PodUID:                       podUID,
		NodeName:                     config.TargetNode.Name,
		Model:                        config.State.Model,
		Architecture:                 "x86_64",
		Runtime:                      config.State.RuntimeFamily,
		RuntimeVersion:               "1.16.0",
		Backend:                      config.State.Backend,
		Provider:                     testCPUProvider,
		AvailableProviders:           []string{testCPUProvider},
		Inputs:                       modelcompat.CloneInputTensors(config.Delivery.ArtifactCapability.Tensors),
		ModelLoadCompletedAt:         modelLoaded.Format(time.RFC3339Nano),
		SettlingStartedAt:            settlingStarted.Format(time.RFC3339Nano),
		SettlingCompletedAt:          settlingCompleted.Format(time.RFC3339Nano),
		MeasurementStartedAt:         settlingCompleted.Format(time.RFC3339Nano),
		MeasurementEndedAt:           measurementEnded.Format(time.RFC3339Nano),
		MeasurementElapsedSeconds:    float64(config.Measurement.DurationSeconds),
		InferenceCountAfterModelLoad: 0,
		ModelResidentAtEnd:           true,
		CPUContract:                  testRuntimeCPU(config),
		DerivedContract: profilingrun.DerivedContract{
			Backend: config.State.Backend, Provider: testCPUProvider,
			NodeSnapshot: config.TargetNode,
		},
		MemoryBytes: testMemoryBytes, MemorySource: testMemorySource,
		Firstfloor: testFirstfloorEvidence(config, "1.16.0", 0),
	}
}

const (
	testCPUProvider  = "CPUExecutionProvider"
	testMemoryBytes  = 123456
	testMemorySource = "cgroup-v2-memory.peak:runtime-container-lifetime"
)

func testRuntimeCPU(config profilingrun.RunConfig) profilingrun.RuntimeCPU {
	return profilingrun.RuntimeCPU{
		CgroupCPUMax: "400000 100000", ORTIntraOpThreads: config.CPU.ORTIntraOpThreads,
		ORTInterOpThreads: config.CPU.ORTInterOpThreads, AffinityCPUCount: 4,
		AffinityCPUs: []int{0, 1, 2, 3}, CPUSetEffective: "0-3",
	}
}

func testFirstfloorEvidence(
	config profilingrun.RunConfig, runtimeVersion string, executions uint64,
) *profilingrun.FirstfloorRuntimeEvidence {
	tensors := config.Delivery.ArtifactCapability.Tensors
	inputs := make([]firstfloor.InputSpec, len(tensors))
	for i, tensor := range tensors {
		shape := make([]int, len(tensor.SampleShape))
		for j, dimension := range tensor.SampleShape {
			shape[j] = int(dimension)
		}
		inputs[i] = firstfloor.InputSpec{Name: tensor.Name, Datatype: tensor.Datatype, Shape: shape}
	}
	counts := map[string]uint64{}
	if executions > 0 {
		counts[strconv.Itoa(config.Measurement.Batch)] = executions
	}
	cpu := testRuntimeCPU(config)
	return &profilingrun.FirstfloorRuntimeEvidence{
		Mode: "profiling", ModelInstanceCount: 1, MaxBatchSize: 4, MaxQueueSize: 32,
		DeadlinePolicy: modelcompat.EffectiveDeadlinePolicy(config.State.Firstfloor.DeadlinePolicy),
		ExecutionCount: executions, InferenceCount: executions * uint64(config.Measurement.Batch),
		BatchExecutionCounts: counts,
		Runtime: firstfloor.RuntimeMetadata{
			Runtime: config.State.Backend, RuntimeVersion: runtimeVersion, ModelPath: "/model/model.onnx",
			Inputs: inputs, Providers: []string{testCPUProvider}, AvailableProviders: []string{testCPUProvider},
			Device: "cpu", CPUAffinity: cpu.AffinityCPUs, IntraOpThreads: cpu.ORTIntraOpThreads,
			InterOpThreads: cpu.ORTInterOpThreads, InitializedBatches: []int{config.Measurement.Batch},
			CgroupCPUMax: cpu.CgroupCPUMax, CPUSet: cpu.CPUSetEffective,
		},
	}
}

func runtimeResult(config profilingrun.RunConfig, podUID string) profilingrun.RuntimeResult {
	started := time.Date(2026, time.July, 16, 3, 0, 0, 0, time.UTC)
	targetDuration := float64(config.Measurement.DurationSeconds)
	return profilingrun.RuntimeResult{
		SchemaVersion: profilingrun.RuntimeResultSchemaVersion, Status: "Succeeded",
		ExperimentID: podUID, NodeName: config.TargetNode.Name, Model: config.State.Model,
		Architecture: "x86_64",
		Runtime:      config.State.RuntimeFamily, RuntimeVersion: "1.16.0",
		Backend: config.State.Backend, Provider: testCPUProvider,
		AvailableProviders: []string{testCPUProvider}, BatchSize: config.Measurement.Batch,
		Inputs:            modelcompat.CloneInputTensors(config.Delivery.ArtifactCapability.Tensors),
		WarmupIterations:  config.Measurement.WarmupIterations,
		WarmupCompletedAt: started.Add(-time.Second).Format(time.RFC3339Nano),
		InferenceCount:    2, MeasurementMode: "duration",
		MeasurementStartedAt:      started.Format(time.RFC3339Nano),
		MeasurementEndedAt:        started.Add(30 * time.Second).Format(time.RFC3339Nano),
		MeasurementElapsedSeconds: 30, TargetDurationSeconds: &targetDuration,
		OutputValidation: profilingrun.OutputValidation{
			Method: config.Measurement.OutputValidation, Passed: true,
			OutputShapes: [][]int{{config.Measurement.Batch, 1000}},
		},
		CPUContract: testRuntimeCPU(config),
		DerivedContract: profilingrun.DerivedContract{
			Backend: config.State.Backend, Provider: testCPUProvider,
			NodeSnapshot: config.TargetNode,
		},
		MemoryBytes: testMemoryBytes, MemorySource: testMemorySource,
		Firstfloor: testFirstfloorEvidence(config, "1.16.0", 2),
		RepetitionResults: []profilingrun.Repetition{{
			Repetition: 1, MeasurementStartedAt: started.Format(time.RFC3339Nano),
			MeasurementEndedAt:  started.Add(30 * time.Second).Format(time.RFC3339Nano),
			LatencyMilliseconds: []float64{1, 2},
			NativeSamples: []profilingrun.NativeServiceSample{
				{BatchID: 1, BatchSize: config.Measurement.Batch, RunnerServiceMilliseconds: 0.7,
					BackendCallMilliseconds: 0.4, NativeRequestMilliseconds: 0.9},
				{BatchID: 2, BatchSize: config.Measurement.Batch, RunnerServiceMilliseconds: 0.7,
					BackendCallMilliseconds: 0.4, NativeRequestMilliseconds: 0.9},
			},
		}},
	}
}

func testMetricResult(config profilingrun.RunConfig) metricobserver.Result {
	started := time.Date(2026, time.July, 16, 2, 59, 59, 0, time.UTC)
	return metricobserver.Result{
		Source: metricobserver.SourceIdentity{
			Kind: config.Metrics.SourceKind, NodeName: config.Metrics.NodeName,
			Endpoint: config.Metrics.Endpoint, Metric: config.Metrics.PowerMetric,
			MetricLabels: config.Metrics.MetricLabels, ThermalEndpoint: config.Metrics.ThermalEndpoint,
			ThermalMetrics: &metricobserver.ThermalMetricIdentity{
				ObservationAvailable:      config.Metrics.ThermalMetrics.ObservationAvailable,
				Active:                    config.Metrics.ThermalMetrics.Active,
				CPUCoreThrottleEvents:     config.Metrics.ThermalMetrics.CPUCoreThrottleEvents,
				CPUPackageThrottleEvents:  config.Metrics.ThermalMetrics.CPUPackageThrottleEvents,
				Latched:                   config.Metrics.ThermalMetrics.Latched,
				MaximumTemperatureCelsius: config.Metrics.ThermalMetrics.MaximumTemperatureCelsius,
			},
		},
		StartedAt: started, EndedAt: started.Add(31 * time.Second),
		Samples: []metricobserver.Sample{{ObservedAt: started.Add(time.Second), Watts: 5.2}},
		Summary: metricobserver.Summary{
			Attempts: 1, SuccessfulSamples: 1, ObservationDurationSeconds: 31,
			SourceTimestampAbsent: true,
		},
	}
}

func testWorkloadReader(t *testing.T, config profilingrun.RunConfig) WorkloadResultReader {
	t.Helper()
	runtime := testRuntimeResult(t, config)
	return workloadReaderFunc(func(context.Context) (workloadobserver.Result, error) {
		return testWorkloadResult(t, config, runtime), nil
	})
}

func testWorkloadResult(
	t *testing.T,
	config profilingrun.RunConfig,
	runtime profilingrun.RuntimeResult,
) workloadobserver.Result {
	t.Helper()
	started, ended, err := runtime.MeasurementWindow()
	if err != nil {
		t.Fatal(err)
	}
	pod := workloadobserver.Pod{
		Namespace: "chill-system", Name: "run", UID: runtime.ExperimentID, Phase: "Running",
		Images: []string{config.Images.Runtime},
	}
	return workloadobserver.Result{
		SchemaVersion: workloadobserver.ResultSchemaVersion,
		Target: workloadobserver.TargetIdentity{
			NodeName: config.TargetNode.Name, NodeUID: config.TargetNode.UID,
		},
		Run: workloadobserver.RunIdentity{
			Namespace: "chill-system", JobUID: "job-uid", PodName: "run", PodUID: runtime.ExperimentID,
		},
		CoverageStartedAt: started.Add(-time.Second), CoverageEndedAt: ended.Add(time.Second),
		InitialResourceVersion: "10", LastResourceVersion: "11", EventLimit: 10,
		InitialActivePods: []workloadobserver.Pod{pod}, FinalActivePods: []workloadobserver.Pod{pod},
		Continuous: true,
	}
}

func testIdleWorkloadResult(
	t *testing.T,
	config profilingrun.RunConfig,
	runtime profilingrun.IdleRuntimeResult,
) workloadobserver.Result {
	t.Helper()
	started, ended, err := runtime.MeasurementWindow()
	if err != nil {
		t.Fatal(err)
	}
	pod := workloadobserver.Pod{
		Namespace: "chill-system", Name: "run", UID: runtime.PodUID, Phase: "Running",
		Images: []string{config.Images.Runtime},
	}
	return workloadobserver.Result{
		SchemaVersion: workloadobserver.ResultSchemaVersion,
		Target: workloadobserver.TargetIdentity{
			NodeName: config.TargetNode.Name, NodeUID: config.TargetNode.UID,
		},
		Run: workloadobserver.RunIdentity{
			Namespace: "chill-system", JobUID: "job-uid", PodName: "run", PodUID: runtime.PodUID,
		},
		CoverageStartedAt: started.Add(-time.Second), CoverageEndedAt: ended.Add(time.Second),
		InitialResourceVersion: "10", LastResourceVersion: "11", EventLimit: 10,
		InitialActivePods: []workloadobserver.Pod{pod}, FinalActivePods: []workloadobserver.Pod{pod},
		Continuous: true,
	}
}
