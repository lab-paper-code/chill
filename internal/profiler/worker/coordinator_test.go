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
	"github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

func TestCoordinatorCommitsCanonicalRawMeasurementInOrder(t *testing.T) {
	intent := testIntent(t)
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	runtimeResult := testRuntimeResult(t, intent)
	metricResult := testMetricResult(intent)
	var calls []string
	store := &fakeStore{put: func(identity evidencestore.ObjectIdentity, payload []byte) (evidencestore.Receipt, error) {
		calls = append(calls, "store")
		if err := evidencestore.Verify(strings.NewReader(string(payload)), identity); err != nil {
			t.Fatal(err)
		}
		var raw evidence.RawMeasurement
		if err := json.Unmarshal(payload, &raw); err != nil {
			t.Fatal(err)
		}
		if raw.IntentDigest != digest || raw.PodUID != "pod-uid" || raw.Runtime.Status != "Succeeded" ||
			raw.Metrics.Source.NodeName != intent.TargetNode.Name {
			t.Fatalf("unexpected raw measurement: %#v", raw)
		}
		return receipt(identity, evidencestore.CreateOutcomeCreated), nil
	}}
	coordinator := mustCoordinator(t,
		runtimeFunc(func(_ context.Context, request RuntimeRequest) (profilingrun.RuntimeResult, error) {
			calls = append(calls, "runtime")
			if request.PodUID != "pod-uid" || request.ScheduledNodeName != intent.TargetNode.Name {
				t.Fatalf("unexpected runtime request: %#v", request)
			}
			return runtimeResult, nil
		}),
		metricReaderFunc(func(context.Context) (metricobserver.Result, error) {
			calls = append(calls, "metrics")
			return metricResult, nil
		}),
		store,
	)

	got, err := coordinator.Execute(context.Background(), Request{
		Intent: intent, ExpectedIntentDigest: digest, PodUID: "pod-uid", ScheduledNodeName: intent.TargetNode.Name,
		RawLocation: evidencestore.ObjectLocation{Store: "chill-evidence", Key: "profiling-runs/run/raw/intent.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "runtime,metrics,store" || got != store.receipt {
		t.Fatalf("unexpected execution: calls=%v receipt=%#v", calls, got)
	}
}

func TestCoordinatorRejectsDriftBeforeExternalWork(t *testing.T) {
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
		&fakeStore{},
	)

	_, err := coordinator.Execute(context.Background(), Request{
		Intent: intent, ExpectedIntentDigest: "sha256:" + strings.Repeat("0", 64), PodUID: "pod-uid",
		ScheduledNodeName: intent.TargetNode.Name,
		RawLocation:       evidencestore.ObjectLocation{Store: "chill-evidence", Key: "raw.json"},
	})
	if !errors.Is(err, ErrExecutionContract) || runtimeCalls != 0 {
		t.Fatalf("expected pre-execution intent drift rejection, got err=%v calls=%d", err, runtimeCalls)
	}
}

func TestCoordinatorRejectsScheduledNodeDriftBeforeExternalWork(t *testing.T) {
	intent := testIntent(t)
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
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
		&fakeStore{},
	)

	_, err = coordinator.Execute(context.Background(), Request{
		Intent: intent, ExpectedIntentDigest: digest, PodUID: "pod-uid", ScheduledNodeName: "other-node",
		RawLocation: evidencestore.ObjectLocation{Store: "chill-evidence", Key: "raw.json"},
	})
	if !errors.Is(err, ErrExecutionContract) || !strings.Contains(err.Error(), "scheduled Node") || runtimeCalls != 0 {
		t.Fatalf("expected pre-execution Node drift rejection, got err=%v calls=%d", err, runtimeCalls)
	}
}

func TestCoordinatorStopsAtInvalidRuntimeOrMetricEvidence(t *testing.T) {
	intent := testIntent(t)
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	request := Request{
		Intent: intent, ExpectedIntentDigest: digest, PodUID: "pod-uid", ScheduledNodeName: intent.TargetNode.Name,
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
			store,
		)
		if _, err := coordinator.Execute(context.Background(), request); !errors.Is(err, ErrMetricObservation) || store.calls != 0 {
			t.Fatalf("expected metric rejection before store, got err=%v calls=%d", err, store.calls)
		}
	})
}

func TestCoordinatorRejectsInvalidWriterReceipt(t *testing.T) {
	intent := testIntent(t)
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{put: func(identity evidencestore.ObjectIdentity, _ []byte) (evidencestore.Receipt, error) {
		identity.Digest = "sha256:" + strings.Repeat("0", 64)
		return receipt(identity, evidencestore.CreateOutcomeCreated), nil
	}}
	coordinator := mustCoordinator(t,
		runtimeFunc(func(context.Context, RuntimeRequest) (profilingrun.RuntimeResult, error) {
			return testRuntimeResult(t, intent), nil
		}),
		metricReaderFunc(func(context.Context) (metricobserver.Result, error) { return testMetricResult(intent), nil }),
		store,
	)
	_, err = coordinator.Execute(context.Background(), Request{
		Intent: intent, ExpectedIntentDigest: digest, PodUID: "pod-uid", ScheduledNodeName: intent.TargetNode.Name,
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
) (profilingrun.RuntimeResult, error) {
	return f(ctx, request)
}

type metricReaderFunc func(context.Context) (metricobserver.Result, error)

func (f metricReaderFunc) Read(ctx context.Context) (metricobserver.Result, error) { return f(ctx) }

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
	store evidencestore.ImmutableWriter,
) *Coordinator {
	t.Helper()
	coordinator, err := NewCoordinator(runtime, power, store)
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
	payload, err := os.ReadFile("../../../spikes/profilingrun/fixtures/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var intent profilingrun.Intent
	if err := json.Unmarshal(payload, &intent); err != nil {
		t.Fatal(err)
	}
	intent.SchemaVersion = profilingrun.SchemaVersion
	intent.State.ArtifactPath = "/artifact/mobilenet-v2-050.onnx"
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
	return intent
}

func testRuntimeResult(t *testing.T, intent profilingrun.Intent) profilingrun.RuntimeResult {
	t.Helper()
	result, err := runtimeResult(intent, "pod-uid")
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func runtimeResult(intent profilingrun.Intent, podUID string) (profilingrun.RuntimeResult, error) {
	digest, err := intent.Digest()
	if err != nil {
		return profilingrun.RuntimeResult{}, err
	}
	started := time.Date(2026, time.July, 16, 3, 0, 0, 0, time.UTC)
	targetDuration := float64(intent.Measurement.DurationSeconds)
	return profilingrun.RuntimeResult{
		SchemaVersion: profilingrun.RuntimeResultSchemaVersion, Status: "Succeeded", SweepID: digest,
		ExperimentID: podUID, NodeName: intent.TargetNode.Name, Model: intent.State.Model,
		ArtifactDigest: intent.State.ArtifactDigest, Architecture: "x86_64",
		Runtime: intent.State.RuntimeFamily, RuntimeVersion: "1.16.0", Provider: intent.State.Backend,
		AvailableProviders: []string{intent.State.Backend}, BatchSize: intent.Measurement.Batch,
		WarmupIterations:  intent.Measurement.WarmupIterations,
		WarmupCompletedAt: started.Add(-time.Second).Format(time.RFC3339Nano),
		InferenceCount:    2, MeasurementMode: "duration", Repetitions: intent.Measurement.Repetitions,
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
		DerivedContract: profilingrun.DerivedContract{RunIntentDigest: digest, NodeSnapshot: intent.TargetNode},
		RepetitionResults: []profilingrun.Repetition{{
			Repetition: 1, MeasurementStartedAt: started.Format(time.RFC3339Nano),
			MeasurementEndedAt:  started.Add(30 * time.Second).Format(time.RFC3339Nano),
			LatencyMilliseconds: []float64{1, 2},
		}},
	}, nil
}

func testMetricResult(intent profilingrun.Intent) metricobserver.Result {
	started := time.Date(2026, time.July, 16, 2, 59, 59, 0, time.UTC)
	return metricobserver.Result{
		Source: metricobserver.SourceIdentity{
			Kind: intent.Metrics.SourceKind, NodeName: intent.Metrics.NodeName,
			Endpoint: intent.Metrics.Endpoint, Metric: intent.Metrics.PowerMetric,
			ThermalMetrics: &metricobserver.ThermalMetricIdentity{
				ObservationAvailable:      "device_thermal_throttle_observation_available",
				Active:                    "device_thermal_throttle_active",
				CPUCoreThrottleEvents:     "device_thermal_cpu_core_throttle_event_count",
				CPUPackageThrottleEvents:  "device_thermal_cpu_package_throttle_event_count",
				Latched:                   "device_thermal_throttle_latched",
				MaximumTemperatureCelsius: "device_thermal_max_celsius",
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
