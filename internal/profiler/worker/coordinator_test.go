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
	"github.com/lab-paper-code/chill/internal/powerobserver"
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
	powerResult := testPowerResult(intent)
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
			raw.Power.Source.NodeName != intent.TargetNode.Name {
			t.Fatalf("unexpected raw measurement: %#v", raw)
		}
		return receipt(identity, evidencestore.CreateOutcomeCreated), nil
	}}
	coordinator := mustCoordinator(t,
		runtimeFunc(func(context.Context, profilingrun.Intent, string) (profilingrun.RuntimeResult, error) {
			calls = append(calls, "runtime")
			return runtimeResult, nil
		}),
		powerReaderFunc(func(context.Context) (powerobserver.Result, error) {
			calls = append(calls, "power")
			return powerResult, nil
		}),
		store,
	)

	got, err := coordinator.Execute(context.Background(), Request{
		Intent: intent, ExpectedIntentDigest: digest, PodUID: "pod-uid",
		RawLocation: evidencestore.ObjectLocation{Store: "chill-evidence", Key: "profiling-runs/run/raw/intent.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "runtime,power,store" || got != store.receipt {
		t.Fatalf("unexpected execution: calls=%v receipt=%#v", calls, got)
	}
}

func TestCoordinatorRejectsDriftBeforeExternalWork(t *testing.T) {
	intent := testIntent(t)
	runtimeCalls := 0
	coordinator := mustCoordinator(t,
		runtimeFunc(func(context.Context, profilingrun.Intent, string) (profilingrun.RuntimeResult, error) {
			runtimeCalls++
			return profilingrun.RuntimeResult{}, nil
		}),
		powerReaderFunc(func(context.Context) (powerobserver.Result, error) {
			t.Fatal("power reader must not be called")
			return powerobserver.Result{}, nil
		}),
		&fakeStore{},
	)

	_, err := coordinator.Execute(context.Background(), Request{
		Intent: intent, ExpectedIntentDigest: "sha256:" + strings.Repeat("0", 64), PodUID: "pod-uid",
		RawLocation: evidencestore.ObjectLocation{Store: "chill-evidence", Key: "raw.json"},
	})
	if err == nil || runtimeCalls != 0 {
		t.Fatalf("expected pre-execution intent drift rejection, got err=%v calls=%d", err, runtimeCalls)
	}
}

func TestCoordinatorStopsAtInvalidRuntimeOrPowerEvidence(t *testing.T) {
	intent := testIntent(t)
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	request := Request{
		Intent: intent, ExpectedIntentDigest: digest, PodUID: "pod-uid",
		RawLocation: evidencestore.ObjectLocation{Store: "chill-evidence", Key: "raw.json"},
	}

	t.Run("runtime", func(t *testing.T) {
		runtimeResult := testRuntimeResult(t, intent)
		runtimeResult.NodeName = "other"
		powerCalls := 0
		coordinator := mustCoordinator(t,
			runtimeFunc(func(context.Context, profilingrun.Intent, string) (profilingrun.RuntimeResult, error) {
				return runtimeResult, nil
			}),
			powerReaderFunc(func(context.Context) (powerobserver.Result, error) {
				powerCalls++
				return powerobserver.Result{}, nil
			}),
			&fakeStore{},
		)
		if _, err := coordinator.Execute(context.Background(), request); err == nil || powerCalls != 0 {
			t.Fatalf("expected runtime rejection before power read, got err=%v calls=%d", err, powerCalls)
		}
	})

	t.Run("power", func(t *testing.T) {
		powerResult := testPowerResult(intent)
		powerResult.Source.NodeName = "other"
		store := &fakeStore{}
		coordinator := mustCoordinator(t,
			runtimeFunc(func(context.Context, profilingrun.Intent, string) (profilingrun.RuntimeResult, error) {
				return testRuntimeResult(t, intent), nil
			}),
			powerReaderFunc(func(context.Context) (powerobserver.Result, error) { return powerResult, nil }),
			store,
		)
		if _, err := coordinator.Execute(context.Background(), request); err == nil || store.calls != 0 {
			t.Fatalf("expected power rejection before store, got err=%v calls=%d", err, store.calls)
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
		runtimeFunc(func(context.Context, profilingrun.Intent, string) (profilingrun.RuntimeResult, error) {
			return testRuntimeResult(t, intent), nil
		}),
		powerReaderFunc(func(context.Context) (powerobserver.Result, error) { return testPowerResult(intent), nil }),
		store,
	)
	_, err = coordinator.Execute(context.Background(), Request{
		Intent: intent, ExpectedIntentDigest: digest, PodUID: "pod-uid",
		RawLocation: evidencestore.ObjectLocation{Store: "chill-evidence", Key: "raw.json"},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid receipt") {
		t.Fatalf("expected invalid receipt rejection, got %v", err)
	}
}

type runtimeFunc func(context.Context, profilingrun.Intent, string) (profilingrun.RuntimeResult, error)

func (f runtimeFunc) Measure(
	ctx context.Context,
	intent profilingrun.Intent,
	podUID string,
) (profilingrun.RuntimeResult, error) {
	return f(ctx, intent, podUID)
}

type powerReaderFunc func(context.Context) (powerobserver.Result, error)

func (f powerReaderFunc) Read(ctx context.Context) (powerobserver.Result, error) { return f(ctx) }

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
	power PowerResultReader,
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
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, time.July, 16, 3, 0, 0, 0, time.UTC)
	targetDuration := float64(intent.Measurement.DurationSeconds)
	return profilingrun.RuntimeResult{
		SchemaVersion: profilingrun.RuntimeResultSchemaVersion, Status: "Succeeded", SweepID: digest,
		ExperimentID: "pod-uid", NodeName: intent.TargetNode.Name, Model: intent.State.Model,
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
	}
}

func testPowerResult(intent profilingrun.Intent) powerobserver.Result {
	started := time.Date(2026, time.July, 16, 2, 59, 59, 0, time.UTC)
	return powerobserver.Result{
		Source: powerobserver.SourceIdentity{
			Kind: intent.Power.SourceKind, NodeName: intent.Power.NodeName,
			Endpoint: intent.Power.Endpoint, Metric: intent.Power.Metric,
		},
		StartedAt: started, EndedAt: started.Add(31 * time.Second),
		Samples: []powerobserver.Sample{{ObservedAt: started.Add(time.Second), Watts: 5.2}},
		Summary: powerobserver.Summary{
			Attempts: 1, SuccessfulSamples: 1, ObservationDurationSeconds: 31,
			SourceTimestampAbsent: true,
		},
	}
}
