package admission

import (
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/powerobserver"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

func TestProjectTrialPreservesFactsAndLeavesUnknownFactsUnknown(t *testing.T) {
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
		Power: powerobserver.Result{
			Samples:  []powerobserver.Sample{{ObservedAt: started, Watts: 4}},
			Failures: []powerobserver.Failure{{Reason: powerobserver.FailureReasonTimeout}},
			Summary:  powerobserver.Summary{SourceTimestampAbsent: true},
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
	if len(trial.UnexpectedCoResidents) != 1 || trial.UnexpectedCoResidents[0] != "monitoring/edge-metric" {
		t.Fatalf("unexpected co-residents = %#v", trial.UnexpectedCoResidents)
	}
}

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
		Images:     profilingrun.Images{Runtime: "runtime", Artifact: "artifact", PowerObserver: "observer"},
		TargetNode: profilingrun.NodeSnapshot{UID: "node-uid"},
		CPU:        profilingrun.CPUContract{Policy: "allocatable", PolicyVersion: "v1", Limit: "4"},
		Measurement: profilingrun.MeasurementContract{
			Batch: 2, WarmupIterations: 20, DurationSeconds: 30, Repetitions: 1,
			WorkloadMode: "saturated", InputGenerator: "seeded", OutputValidation: "shape",
		},
		Power: profilingrun.PowerContract{
			SourceKind: "edge-metrics", NodeName: "lattepanda", Endpoint: "http://metrics", Metric: "watts",
			IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31,
		},
	}
}
