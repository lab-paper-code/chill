package profiler

import (
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/profilingrun"
)

const testArtifactPath = "/artifact/model.onnx"

func TestDeriveExecutionPlanBindsOneCoherentCPUORTEnvelope(t *testing.T) {
	intent := testIntent(t)
	intent.SchemaVersion = profilingrun.SchemaVersion
	intent.State.ArtifactPath = testArtifactPath
	addWorkloadObservation(&intent)

	plan, err := deriveLegacyExecutionPlan(intent)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Policy != CPUORTWorkerPolicy || plan.PolicyVersion != CPUORTWorkerPolicyVersion {
		t.Fatalf("unexpected policy identity: %#v", plan)
	}
	if plan.MaxMetricEvents != 33 {
		t.Fatalf("MaxMetricEvents = %d, want 33", plan.MaxMetricEvents)
	}
	if plan.ActiveDeadline != 211*time.Second || plan.AttemptTimeout != 181*time.Second {
		t.Fatalf("unexpected timing envelope: %#v", plan)
	}
	if plan.MetricResultTimeout != 31*time.Second || plan.ObserverReadyTimeout >= plan.AttemptTimeout ||
		plan.ObserverStartTimeout >= plan.AttemptTimeout || plan.WorkloadResultTimeout >= plan.AttemptTimeout {
		t.Fatalf("incoherent handoff timing: %#v", plan)
	}
	if plan.MaxInputBytes+plan.MaxRuntimeResultBytes >= plan.RuntimeMemoryLimitBytes ||
		plan.MaxMetricResultBytes+plan.MaxWorkloadResultBytes >= plan.CoordinationBytes ||
		plan.MaxRuntimeResultBytes+plan.MaxMetricResultBytes > plan.WorkspaceBytes {
		t.Fatalf("capacities do not fit the resource envelope: %#v", plan)
	}
}

func TestDeriveExecutionPlanRejectsUnpromotedRuntimePath(t *testing.T) {
	intent := testIntent(t)
	intent.SchemaVersion = profilingrun.SchemaVersion
	intent.State.ArtifactPath = testArtifactPath
	addWorkloadObservation(&intent)
	intent.Candidate.ExecutionPath = "tensorrt"
	if _, err := deriveLegacyExecutionPlan(intent); err == nil {
		t.Fatal("DeriveExecutionPlan() error = nil, want unsupported path rejection")
	}
}

func TestDeriveExecutionPlanKeepsRetainedV1Alpha5ActiveIntentRunnable(t *testing.T) {
	intent := testIntent(t)
	intent.SchemaVersion = "chill.dacs.io/profiling-run-intent.v1alpha5"
	intent.State.ArtifactPath = testArtifactPath
	addWorkloadObservation(&intent)
	if _, err := deriveLegacyExecutionPlan(intent); err != nil {
		t.Fatalf("retained active intent became unrunnable: %v", err)
	}
}

func TestDeriveExecutionPlanIncludesIdleSettlingEnvelope(t *testing.T) {
	intent := testIntent(t)
	intent.SchemaVersion = profilingrun.SchemaVersion
	intent.State.ArtifactPath = testArtifactPath
	addWorkloadObservation(&intent)
	intent.Measurement.WarmupIterations = 0
	intent.Measurement.SettlingDurationSeconds = 60
	intent.Measurement.WorkloadMode = profilingrun.IdleModelLoadedWorkloadMode
	intent.Measurement.InputGenerator = profilingrun.NoInputGenerator
	intent.Measurement.OutputValidation = profilingrun.NoOutputValidation

	plan, err := deriveLegacyExecutionPlan(intent)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Policy != CPUORTWorkerPolicy || plan.PolicyVersion != CPUORTIdlePolicyVersion {
		t.Fatalf("unexpected idle policy identity: %#v", plan)
	}
	if plan.ActiveDeadline != 271*time.Second || plan.AttemptTimeout != 241*time.Second ||
		plan.ObserverStartTimeout != 180*time.Second {
		t.Fatalf("idle settling time is not bound into the Job envelope: %#v", plan)
	}
}

func TestDeriveExecutionPlanRejectsMetricScheduleOutsideBoundedEnvelope(t *testing.T) {
	intent := testIntent(t)
	intent.SchemaVersion = profilingrun.SchemaVersion
	intent.State.ArtifactPath = testArtifactPath
	addWorkloadObservation(&intent)
	intent.Metrics.DurationSeconds = 60_000
	intent.Metrics.IntervalMilliseconds = 1000
	if _, err := deriveLegacyExecutionPlan(intent); err == nil {
		t.Fatal("DeriveExecutionPlan() error = nil, want bounded event-capacity rejection")
	}
}

func TestDeriveExecutionPlanUsesFrozenConfiguredEnvelope(t *testing.T) {
	intent := currentExecutionPolicyTestIntent(t)
	frozen, err := FreezeConfiguredExecutionPolicy(
		executionPolicyCatalogFixture(t, intent),
		intent,
		validExecutionPolicySource(),
	)
	if err != nil {
		t.Fatal(err)
	}
	frozen.Policy.MaxLatencySamples = 2048
	frozen.Policy.ObserverCPURequest = "10m"
	frozen.Policy.ObserverCPULimit = "200m"
	plan, err := DeriveExecutionPlan(intent, frozen)
	if err != nil {
		t.Fatal(err)
	}
	if plan.MaxLatencySamples != 2048 || plan.ObserverCPURequest != "10m" || plan.ObserverCPULimit != "200m" {
		t.Fatalf("configured execution envelope was not projected: %#v", plan)
	}
	if !strings.HasPrefix(plan.PolicyVersion, "sha256:") {
		t.Fatalf("configured policy version = %q", plan.PolicyVersion)
	}
}

func deriveLegacyExecutionPlan(intent profilingrun.Intent) (ExecutionPlan, error) {
	policy, err := LegacyFrozenExecutionPolicy(intent)
	if err != nil {
		return ExecutionPlan{}, err
	}
	return DeriveExecutionPlan(intent, policy)
}

func addWorkloadObservation(intent *profilingrun.Intent) {
	intent.Images.WorkloadObserver = "registry.example/workload-observer@sha256:" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	intent.Workload = profilingrun.CurrentWorkloadContract()
}
