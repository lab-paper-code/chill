package admission

import (
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

func TestProjectTrialPreservesFactsAndDoesNotInferWindowFactsFromTerminalSnapshot(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	intent := admissionIntent()
	execution := profilerevidence.ExecutionEvidence{
		Intent: intent,
		Terminal: profilerevidence.TerminalFacts{
			Pod:  profilerevidence.PodTerminalFacts{UID: "run-pod"},
			Node: profilerevidence.NodeTerminalFacts{UID: "node-uid"},
			NodeWorkload: profilerevidence.NodeWorkload{Pods: []profilerevidence.WorkloadPod{
				{Namespace: "chill-system", Name: "run", UID: "run-pod", Phase: "Succeeded"},
				{Namespace: "monitoring", Name: "edge-metric", UID: "metrics", Phase: "Running"},
				{Namespace: "default", Name: "finished", UID: "finished", Phase: "Succeeded"},
			}},
		},
	}
	raw := profilerevidence.RawMeasurement{
		Runtime: profilingrun.RuntimeResult{
			MeasurementStartedAt: started.Format(time.RFC3339Nano),
			MeasurementEndedAt:   started.Add(30 * time.Second).Format(time.RFC3339Nano),
			InferenceCount:       2,
			RepetitionResults: []profilingrun.Repetition{{
				LatencyMilliseconds: []float64{1, 2},
			}},
		},
		Metrics: metricobserver.Result{
			Samples:  []metricobserver.Sample{{ObservedAt: started, Watts: 4}},
			Failures: []metricobserver.Failure{{Reason: metricobserver.FailureReasonTimeout}},
			Summary:  metricobserver.Summary{SourceTimestampAbsent: true},
		},
	}

	trial, err := projectTrial(execution, raw)
	if err != nil {
		t.Fatal(err)
	}
	if trial.CompletedCalls != 2 || trial.CompletedItems != 4 || len(trial.LatencyMilliseconds) != 2 ||
		len(trial.Samples) != 1 || trial.Failures != 1 || !trial.SourceTimestampAbsent ||
		!trial.SteadyWindowVerified || trial.ThermalStateKnown || trial.WorkloadWindowKnown {
		t.Fatalf("projected trial = %#v", trial)
	}
	if trial.StateIdentity == "" || trial.EnvironmentIdentity == "" ||
		trial.MeasurementProtocolIdentity == "" {
		t.Fatalf("projected identities = %#v", trial)
	}
	if len(trial.ConcurrentCHILLWorkloads) != 0 || len(trial.ExternalWorkloadChanges) != 0 {
		t.Fatalf("terminal snapshot was treated as measurement-window evidence: %#v", trial)
	}
}

func TestProjectIdleObservationPreservesRawProvenanceAndRuntimeWindow(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	intent := admissionIntent()
	intent.Measurement.SettlingDurationSeconds = 60
	execution := profilerevidence.ExecutionEvidence{
		Intent: intent,
		RawMeasurement: evidencestore.ObjectIdentity{
			Store: "chill-evidence", Key: "runs/idle/raw.json",
			Digest: "sha256:" + strings.Repeat("a", 64), Size: 1024,
		},
		Terminal: profilerevidence.TerminalFacts{
			Pod:  profilerevidence.PodTerminalFacts{UID: "idle-pod"},
			Node: profilerevidence.NodeTerminalFacts{UID: "node-uid"},
		},
	}
	raw := profilerevidence.IdleRawMeasurement{
		Runtime: profilingrun.IdleRuntimeResult{
			ModelLoadCompletedAt: started.Add(-61 * time.Second).Format(time.RFC3339Nano),
			SettlingStartedAt:    started.Add(-60 * time.Second).Format(time.RFC3339Nano),
			SettlingCompletedAt:  started.Format(time.RFC3339Nano),
			MeasurementStartedAt: started.Format(time.RFC3339Nano),
			MeasurementEndedAt:   started.Add(30 * time.Second).Format(time.RFC3339Nano),
			ModelResidentAtEnd:   true,
		},
		Metrics: metricobserver.Result{Samples: []metricobserver.Sample{{ObservedAt: started, Watts: 4}}},
	}

	observation, err := projectIdleObservation(execution, raw)
	if err != nil {
		t.Fatal(err)
	}
	if observation.TrialID != "idle-pod" || observation.RawEvidence.Store != "chill-evidence" ||
		observation.RawEvidence.Key != "runs/idle/raw.json" ||
		!observation.ModelLoadedBeforeSettling || !observation.SettlingWindowVerified ||
		!observation.ModelResidentAtEnd || observation.InferenceCountAfterModelLoad != 0 ||
		!observation.WindowStart.Equal(started) || !observation.WindowEnd.Equal(started.Add(30*time.Second)) {
		t.Fatalf("projected idle observation = %#v", observation)
	}
}

func TestThermalStateProjectsOnlyObservedWindowFacts(t *testing.T) {
	start := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	sample := func(offset time.Duration, thermal *metricobserver.ThermalReading) metricobserver.Sample {
		return metricobserver.Sample{ObservedAt: start.Add(offset), Watts: 4, Thermal: thermal}
	}
	direct := func(active bool) *metricobserver.ThermalReading {
		return &metricobserver.ThermalReading{
			ObservationAvailable: admissionBool(true), Active: admissionBool(active),
		}
	}
	counters := func(value uint64) *metricobserver.ThermalReading {
		return &metricobserver.ThermalReading{
			ObservationAvailable: admissionBool(true), CPUCoreThrottleEvents: admissionUint(value),
		}
	}
	tests := []struct {
		name       string
		samples    []metricobserver.Sample
		wantKnown  bool
		wantActive bool
	}{
		{name: "direct stable", samples: []metricobserver.Sample{sample(time.Second, direct(false)), sample(2*time.Second, direct(false))}, wantKnown: true},
		{name: "direct active", samples: []metricobserver.Sample{sample(time.Second, direct(false)), sample(2*time.Second, direct(true))}, wantKnown: true, wantActive: true},
		{name: "counter stable", samples: []metricobserver.Sample{sample(time.Second, counters(7)), sample(2*time.Second, counters(7))}, wantKnown: true},
		{name: "counter increase", samples: []metricobserver.Sample{sample(time.Second, counters(7)), sample(2*time.Second, counters(8))}, wantKnown: true, wantActive: true},
		{name: "counter reset", samples: []metricobserver.Sample{sample(time.Second, counters(7)), sample(2*time.Second, counters(1))}},
		{
			name: "diagnostic issue does not override direct evidence",
			samples: []metricobserver.Sample{
				sample(time.Second, &metricobserver.ThermalReading{
					ObservationAvailable: admissionBool(true), Active: admissionBool(false),
					Issues: []metricobserver.MetricIssue{{Metric: "device_thermal_max_celsius", Reason: metricobserver.FailureReasonInvalidReading}},
				}),
				sample(2*time.Second, direct(false)),
			},
			wantKnown: true,
		},
		{name: "missing thermal", samples: []metricobserver.Sample{sample(time.Second, nil), sample(2*time.Second, nil)}},
		{name: "outside window ignored", samples: []metricobserver.Sample{sample(-time.Second, direct(true)), sample(time.Second, direct(false))}, wantKnown: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			known, active := thermalState(test.samples, start, start.Add(3*time.Second))
			if known != test.wantKnown || active != test.wantActive {
				t.Fatalf("thermalState() = %t, %t; want %t, %t", known, active, test.wantKnown, test.wantActive)
			}
		})
	}
}

func admissionBool(value bool) *bool     { return &value }
func admissionUint(value uint64) *uint64 { return &value }

func TestRunIdentitiesSeparateExecutionStateFromMeasurementProtocol(t *testing.T) {
	t.Parallel()
	intent := admissionIntentWithWorkloadContract()
	base, err := ProjectRunIdentities(intent, "node-a")
	if err != nil {
		t.Fatal(err)
	}

	batch := intent
	batch.Measurement.Batch = 8
	batchIdentities, err := ProjectRunIdentities(batch, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if batchIdentities != base {
		t.Fatalf("batch changed identity family: %#v != %#v", batchIdentities, base)
	}

	idle := intent
	idle.Measurement.WorkloadMode = "idle-model-loaded"
	idle.Measurement.InputGenerator = "none"
	idle.Measurement.OutputValidation = "none"
	idle.Measurement.WarmupIterations = 0
	idle.Measurement.DurationSeconds = 60
	idle.Metrics.DurationSeconds = 61
	idleIdentities, err := ProjectRunIdentities(idle, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if idleIdentities.StateIdentity != base.StateIdentity ||
		idleIdentities.MeasurementProtocolIdentity == base.MeasurementProtocolIdentity {
		t.Fatalf("idle split = %#v, active = %#v", idleIdentities, base)
	}

	observer := intent
	observer.Images.MetricObserver = "registry.example/metric-observer@sha256:" + strings.Repeat("b", 64)
	observerIdentities, err := ProjectRunIdentities(observer, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if observerIdentities.StateIdentity != base.StateIdentity ||
		observerIdentities.MeasurementProtocolIdentity == base.MeasurementProtocolIdentity {
		t.Fatalf("observer split = %#v, base = %#v", observerIdentities, base)
	}

	runtime := intent
	runtime.Images.Runtime = "registry.example/runtime@sha256:" + strings.Repeat("c", 64)
	runtimeIdentities, err := ProjectRunIdentities(runtime, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if runtimeIdentities.StateIdentity == base.StateIdentity {
		t.Fatal("runtime image must change execution-state identity")
	}

	metricSource := intent
	metricSource.Metrics.Endpoint = "http://other-meter"
	metricSourceIdentities, err := ProjectRunIdentities(metricSource, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if metricSourceIdentities.StateIdentity == base.StateIdentity {
		t.Fatal("wall-meter source must change execution-state identity")
	}

	nodeIdentities, err := ProjectRunIdentities(intent, "node-b")
	if err != nil {
		t.Fatal(err)
	}
	if nodeIdentities.StateIdentity == base.StateIdentity {
		t.Fatal("actual Node UID must change execution-state identity")
	}
	if !strings.HasPrefix(base.StateIdentity, stateIdentitySchemaVersion+"@sha256:") ||
		!strings.HasPrefix(base.MeasurementProtocolIdentity, measurementProtocolIdentitySchemaVersion+"@sha256:") {
		t.Fatalf("unversioned identities: %#v", base)
	}
}

func admissionIntent() profilingrun.Intent {
	return profilingrun.Intent{
		State: profilingrun.ExecutionState{
			DeviceClass: "lattepanda", Model: "mobilenet", ArtifactDigest: "sha256:" + strings.Repeat("a", 64),
			RuntimeFamily: "onnxruntime", Backend: "CPUExecutionProvider", PowerMode: "fixed",
		},
		Images:     profilingrun.Images{Runtime: "runtime", Artifact: "artifact", MetricObserver: "observer"},
		TargetNode: profilingrun.NodeSnapshot{Name: "lattepanda", UID: "node-uid"},
		CPU:        profilingrun.CPUContract{Policy: "allocatable", PolicyVersion: "v1", Limit: "4"},
		Measurement: profilingrun.MeasurementContract{
			Batch: 2, WarmupIterations: 20, DurationSeconds: 30, Repetitions: 1,
			WorkloadMode: "saturated", InputGenerator: "seeded", OutputValidation: "shape",
		},
		Metrics: profilingrun.MetricContract{
			SourceKind: "edge-metrics", NodeName: "lattepanda", Endpoint: "http://metrics", PowerMetric: "watts",
			IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31,
		},
	}
}
