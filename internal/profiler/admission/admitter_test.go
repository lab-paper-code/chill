package admission

import (
	"strings"
	"testing"
	"time"

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
		!trial.SteadyWindowVerified || trial.ThermalStateKnown || trial.CoResidentWindowKnown {
		t.Fatalf("projected trial = %#v", trial)
	}
	if len(trial.UnexpectedCoResidents) != 0 {
		t.Fatalf("terminal snapshot was treated as measurement-window contamination: %#v", trial.UnexpectedCoResidents)
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

func TestMaterialStateIdentityExcludesBatchButBindsNode(t *testing.T) {
	t.Parallel()
	intent := admissionIntent()
	first, err := materialStateIdentity(intent, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	intent.Measurement.Batch = 8
	second, err := materialStateIdentity(intent, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	third, err := materialStateIdentity(intent, "node-b")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == third || !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("state identities = %q, %q, %q", first, second, third)
	}
}

func TestBuiltInPolicyIsExplicitAndClosed(t *testing.T) {
	t.Parallel()
	policy, err := builtInPolicy(WallPowerPolicyV1)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Version != WallPowerPolicyV1 || policy.MinimumSamples != 20 ||
		policy.MaximumGapSeconds != 1.5 || policy.MaximumBoundaryDistanceSeconds != 1 ||
		policy.MinimumCoverage != 0.95 || !policy.AllowReceiptTimestamps ||
		!policy.RejectUnexpectedCoResidents {
		t.Fatalf("policy = %#v", policy)
	}
	if _, err := builtInPolicy("unknown"); err == nil {
		t.Fatal("unsupported policy was accepted")
	}
}

func admissionIntent() profilingrun.Intent {
	return profilingrun.Intent{
		State: profilingrun.ExecutionState{
			DeviceClass: "lattepanda", Model: "mobilenet", ArtifactDigest: "sha256:" + strings.Repeat("a", 64),
			RuntimeFamily: "onnxruntime", Backend: "CPUExecutionProvider", PowerMode: "fixed",
		},
		Images:     profilingrun.Images{Runtime: "runtime", Artifact: "artifact", MetricObserver: "observer"},
		TargetNode: profilingrun.NodeSnapshot{UID: "node-uid"},
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
