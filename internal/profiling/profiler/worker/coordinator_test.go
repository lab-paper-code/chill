package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

func TestCoordinatorCommitsCanonicalRawMeasurementInOrder(t *testing.T) {
	intent := testIntent(t)
	runtimeResult := testRuntimeResult(t, intent)
	metricResult := testMetricResult(intent)
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
			raw.Metrics.Source.NodeName != intent.TargetNode.Name || raw.Workload == nil ||
			raw.Workload.Target.NodeUID != intent.TargetNode.UID {
			t.Fatalf("unexpected raw measurement: %#v", raw)
		}
		return receipt(identity, evidencestore.CreateOutcomeCreated), nil
	}}
	coordinator := mustCoordinator(t,
		runtimeFunc(func(_ context.Context, request RuntimeRequest) (profilingrun.RuntimeResult, error) {
			calls = append(calls, "runtime")
			if request.PodUID != testPodUID || request.ScheduledNodeName != intent.TargetNode.Name {
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
			return testWorkloadResult(t, intent, runtimeResult), nil
		}),
		store,
	)

	got, err := coordinator.Execute(context.Background(), Request{
		Intent: testVerifiedIntent(t, intent), PodUID: testPodUID, ScheduledNodeName: intent.TargetNode.Name,
		RawLocation: evidencestore.ObjectLocation{Store: "chill-evidence", Key: "profiling-runs/run/raw/intent.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "runtime,metrics,workload,store" || got != store.receipt {
		t.Fatalf("unexpected execution: calls=%v receipt=%#v", calls, got)
	}
}

func TestCoordinatorCommitsCanonicalIdleRawMeasurement(t *testing.T) {
	intent := testIdleIntent(t)
	idle := idleRuntimeResult(intent, testPodUID)
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
				request.Intent, request.PodUID, idle,
			)
			if err != nil {
				return RuntimeMeasurement{}, err
			}
			return idleRuntimeMeasurement(verified), nil
		}),
		metricReaderFunc(func(context.Context) (metricobserver.Result, error) { return testMetricResult(intent), nil }),
		workloadReaderFunc(func(context.Context) (workloadobserver.Result, error) {
			return testIdleWorkloadResult(t, intent, idle), nil
		}),
		store,
	)
	if _, err := coordinator.Execute(context.Background(), Request{
		Intent: testVerifiedIntent(t, intent), PodUID: testPodUID, ScheduledNodeName: intent.TargetNode.Name,
		RawLocation: evidencestore.ObjectLocation{Store: "chill-evidence", Key: "idle/raw.json"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorRejectsEmptyVerifiedIntentBeforeExternalWork(t *testing.T) {
	intent := testIntent(t)
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
		testWorkloadReader(t, intent),
		&fakeStore{},
	)

	_, err := coordinator.Execute(context.Background(), Request{
		Intent: profilingrun.VerifiedIntent{}, PodUID: testPodUID,
		ScheduledNodeName: intent.TargetNode.Name,
		RawLocation:       evidencestore.ObjectLocation{Store: "chill-evidence", Key: "raw.json"},
	})
	if !errors.Is(err, ErrExecutionContract) || runtimeCalls != 0 {
		t.Fatalf("expected empty frozen intent rejection, got err=%v calls=%d", err, runtimeCalls)
	}
}

func TestCoordinatorRejectsScheduledNodeDriftBeforeExternalWork(t *testing.T) {
	intent := testIntent(t)
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
		testWorkloadReader(t, intent),
		&fakeStore{},
	)

	_, err := coordinator.Execute(context.Background(), Request{
		Intent: testVerifiedIntent(t, intent), PodUID: testPodUID, ScheduledNodeName: "other-node",
		RawLocation: evidencestore.ObjectLocation{Store: "chill-evidence", Key: "raw.json"},
	})
	if !errors.Is(err, ErrExecutionContract) || !strings.Contains(err.Error(), "scheduled Node") || runtimeCalls != 0 {
		t.Fatalf("expected pre-execution Node drift rejection, got err=%v calls=%d", err, runtimeCalls)
	}
}

func TestCoordinatorStopsAtInvalidRuntimeOrMetricEvidence(t *testing.T) {
	intent := testIntent(t)
	request := Request{
		Intent: testVerifiedIntent(t, intent), PodUID: testPodUID, ScheduledNodeName: intent.TargetNode.Name,
		RawLocation: evidencestore.ObjectLocation{Store: "chill-evidence", Key: "raw.json"},
	}

	t.Run("runtime", func(t *testing.T) {
		runtimeResult := testRuntimeResult(t, intent)
		runtimeResult.NodeName = "other"
		metricCalls := 0
		coordinator := mustCoordinator(t,
			runtimeFunc(func(context.Context, RuntimeRequest) (profilingrun.RuntimeResult, error) {
				return runtimeResult, nil
			}),
			metricReaderFunc(func(context.Context) (metricobserver.Result, error) {
				metricCalls++
				return metricobserver.Result{}, nil
			}),
			testWorkloadReader(t, intent),
			&fakeStore{},
		)
		if _, err := coordinator.Execute(context.Background(), request); !errors.Is(err, ErrRuntimeMeasurement) || metricCalls != 0 {
			t.Fatalf("expected runtime rejection before metric read, got err=%v calls=%d", err, metricCalls)
		}
	})

	t.Run("metrics", func(t *testing.T) {
		metricResult := testMetricResult(intent)
		metricResult.Source.NodeName = "other"
		store := &fakeStore{}
		coordinator := mustCoordinator(t,
			runtimeFunc(func(context.Context, RuntimeRequest) (profilingrun.RuntimeResult, error) {
				return testRuntimeResult(t, intent), nil
			}),
			metricReaderFunc(func(context.Context) (metricobserver.Result, error) { return metricResult, nil }),
			testWorkloadReader(t, intent),
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
				return testRuntimeResult(t, intent), nil
			}),
			metricReaderFunc(func(context.Context) (metricobserver.Result, error) {
				return testMetricResult(intent), nil
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
	intent := testIntent(t)
	store := &fakeStore{put: func(identity evidencestore.ObjectIdentity, _ []byte) (evidencestore.Receipt, error) {
		identity.Size++
		return receipt(identity, evidencestore.CreateOutcomeCreated), nil
	}}
	coordinator := mustCoordinator(t,
		runtimeFunc(func(context.Context, RuntimeRequest) (profilingrun.RuntimeResult, error) {
			return testRuntimeResult(t, intent), nil
		}),
		metricReaderFunc(func(context.Context) (metricobserver.Result, error) { return testMetricResult(intent), nil }),
		testWorkloadReader(t, intent),
		store,
	)
	_, err := coordinator.Execute(context.Background(), Request{
		Intent: testVerifiedIntent(t, intent), PodUID: testPodUID, ScheduledNodeName: intent.TargetNode.Name,
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
	verified, err := profilingrun.VerifyRuntimeResult(request.Intent, request.PodUID, result)
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

func testIntent(t *testing.T) profilingrun.Intent {
	t.Helper()
	payload, err := os.ReadFile("testdata/lattepanda-ort-cpu-bs1.json")
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

func testVerifiedIntent(t *testing.T, intent profilingrun.Intent) profilingrun.VerifiedIntent {
	t.Helper()
	verified, err := profilingrun.ValidateIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	return verified
}

func testRuntimeResult(t *testing.T, intent profilingrun.Intent) profilingrun.RuntimeResult {
	t.Helper()
	return runtimeResult(intent, testPodUID)
}

func testIdleIntent(t *testing.T) profilingrun.Intent {
	t.Helper()
	intent := testIntent(t)
	intent.Measurement.WarmupIterations = 0
	intent.Measurement.SettlingDurationSeconds = 30
	intent.Measurement.WorkloadMode = profilingrun.IdleModelLoadedWorkloadMode
	intent.Measurement.InputGenerator = profilingrun.NoInputGenerator
	intent.Measurement.OutputValidation = profilingrun.NoOutputValidation
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
	return intent
}

func idleRuntimeResult(intent profilingrun.Intent, podUID string) profilingrun.IdleRuntimeResult {
	modelLoaded := time.Date(2026, time.July, 16, 2, 59, 0, 0, time.UTC)
	settlingStarted := modelLoaded.Add(time.Second)
	settlingCompleted := settlingStarted.Add(time.Duration(intent.Measurement.SettlingDurationSeconds) * time.Second)
	measurementEnded := settlingCompleted.Add(time.Duration(intent.Measurement.DurationSeconds) * time.Second)
	return profilingrun.IdleRuntimeResult{
		SchemaVersion:                profilingrun.IdleRuntimeResultSchemaVersion,
		Status:                       "Succeeded",
		PodUID:                       podUID,
		NodeName:                     intent.TargetNode.Name,
		Model:                        intent.State.Model,
		Architecture:                 "x86_64",
		Runtime:                      intent.State.RuntimeFamily,
		RuntimeVersion:               "1.16.0",
		Backend:                      intent.State.Backend,
		Provider:                     intent.State.Backend,
		AvailableProviders:           []string{intent.State.Backend},
		ModelLoadCompletedAt:         modelLoaded.Format(time.RFC3339Nano),
		SettlingStartedAt:            settlingStarted.Format(time.RFC3339Nano),
		SettlingCompletedAt:          settlingCompleted.Format(time.RFC3339Nano),
		MeasurementStartedAt:         settlingCompleted.Format(time.RFC3339Nano),
		MeasurementEndedAt:           measurementEnded.Format(time.RFC3339Nano),
		MeasurementElapsedSeconds:    float64(intent.Measurement.DurationSeconds),
		InferenceCountAfterModelLoad: 0,
		ModelResidentAtEnd:           true,
		CPUContract: profilingrun.RuntimeCPU{
			CgroupCPUMax: "400000 100000", ORTIntraOpThreads: intent.CPU.ORTIntraOpThreads,
			ORTInterOpThreads: intent.CPU.ORTInterOpThreads, AffinityCPUCount: intent.CPU.ORTIntraOpThreads,
			CPUSetEffective: "0-3",
		},
		DerivedContract: profilingrun.DerivedContract{
			Backend: intent.State.Backend, Provider: intent.State.Backend,
			NodeSnapshot: intent.TargetNode,
		},
	}
}

func runtimeResult(intent profilingrun.Intent, podUID string) profilingrun.RuntimeResult {
	started := time.Date(2026, time.July, 16, 3, 0, 0, 0, time.UTC)
	targetDuration := float64(intent.Measurement.DurationSeconds)
	return profilingrun.RuntimeResult{
		SchemaVersion: profilingrun.RuntimeResultSchemaVersion, Status: "Succeeded",
		ExperimentID: podUID, NodeName: intent.TargetNode.Name, Model: intent.State.Model,
		Architecture: "x86_64",
		Runtime:      intent.State.RuntimeFamily, RuntimeVersion: "1.16.0",
		Backend: intent.State.Backend, Provider: intent.State.Backend,
		AvailableProviders: []string{intent.State.Backend}, BatchSize: intent.Measurement.Batch,
		WarmupIterations:  intent.Measurement.WarmupIterations,
		WarmupCompletedAt: started.Add(-time.Second).Format(time.RFC3339Nano),
		InferenceCount:    2, MeasurementMode: "duration",
		MeasurementStartedAt:      started.Format(time.RFC3339Nano),
		MeasurementEndedAt:        started.Add(30 * time.Second).Format(time.RFC3339Nano),
		MeasurementElapsedSeconds: 30, TargetDurationSeconds: &targetDuration,
		OutputValidation: profilingrun.OutputValidation{
			Method: intent.Measurement.OutputValidation, Passed: true,
			OutputShapes: [][]int{{intent.Measurement.Batch, 1000}},
		},
		CPUContract: profilingrun.RuntimeCPU{
			CgroupCPUMax: "400000 100000", ORTIntraOpThreads: intent.CPU.ORTIntraOpThreads,
			ORTInterOpThreads: intent.CPU.ORTInterOpThreads, AffinityCPUCount: intent.CPU.ORTIntraOpThreads,
			CPUSetEffective: "0-3",
		},
		DerivedContract: profilingrun.DerivedContract{
			Backend: intent.State.Backend, Provider: intent.State.Backend,
			NodeSnapshot: intent.TargetNode,
		},
		RepetitionResults: []profilingrun.Repetition{{
			Repetition: 1, MeasurementStartedAt: started.Format(time.RFC3339Nano),
			MeasurementEndedAt:  started.Add(30 * time.Second).Format(time.RFC3339Nano),
			LatencyMilliseconds: []float64{1, 2},
		}},
	}
}

func testMetricResult(intent profilingrun.Intent) metricobserver.Result {
	started := time.Date(2026, time.July, 16, 2, 59, 59, 0, time.UTC)
	return metricobserver.Result{
		Source: metricobserver.SourceIdentity{
			Kind: intent.Metrics.SourceKind, NodeName: intent.Metrics.NodeName,
			Endpoint: intent.Metrics.Endpoint, Metric: intent.Metrics.PowerMetric,
			MetricLabels: intent.Metrics.MetricLabels, ThermalEndpoint: intent.Metrics.ThermalEndpoint,
			ThermalMetrics: &metricobserver.ThermalMetricIdentity{
				ObservationAvailable:      intent.Metrics.ThermalMetrics.ObservationAvailable,
				Active:                    intent.Metrics.ThermalMetrics.Active,
				CPUCoreThrottleEvents:     intent.Metrics.ThermalMetrics.CPUCoreThrottleEvents,
				CPUPackageThrottleEvents:  intent.Metrics.ThermalMetrics.CPUPackageThrottleEvents,
				Latched:                   intent.Metrics.ThermalMetrics.Latched,
				MaximumTemperatureCelsius: intent.Metrics.ThermalMetrics.MaximumTemperatureCelsius,
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

func testWorkloadReader(t *testing.T, intent profilingrun.Intent) WorkloadResultReader {
	t.Helper()
	runtime := testRuntimeResult(t, intent)
	return workloadReaderFunc(func(context.Context) (workloadobserver.Result, error) {
		return testWorkloadResult(t, intent, runtime), nil
	})
}

func testWorkloadResult(
	t *testing.T,
	intent profilingrun.Intent,
	runtime profilingrun.RuntimeResult,
) workloadobserver.Result {
	t.Helper()
	started, ended, err := runtime.MeasurementWindow()
	if err != nil {
		t.Fatal(err)
	}
	pod := workloadobserver.Pod{
		Namespace: "chill-system", Name: "run", UID: runtime.ExperimentID, Phase: "Running",
		Images: []string{intent.Images.Runtime},
	}
	return workloadobserver.Result{
		SchemaVersion: workloadobserver.ResultSchemaVersion,
		Target: workloadobserver.TargetIdentity{
			NodeName: intent.TargetNode.Name, NodeUID: intent.TargetNode.UID,
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
	intent profilingrun.Intent,
	runtime profilingrun.IdleRuntimeResult,
) workloadobserver.Result {
	t.Helper()
	started, ended, err := runtime.MeasurementWindow()
	if err != nil {
		t.Fatal(err)
	}
	pod := workloadobserver.Pod{
		Namespace: "chill-system", Name: "run", UID: runtime.PodUID, Phase: "Running",
		Images: []string{intent.Images.Runtime},
	}
	return workloadobserver.Result{
		SchemaVersion: workloadobserver.ResultSchemaVersion,
		Target: workloadobserver.TargetIdentity{
			NodeName: intent.TargetNode.Name, NodeUID: intent.TargetNode.UID,
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
