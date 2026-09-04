package profiler

import (
	"testing"
	"time"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"sigs.k8s.io/yaml"
)

const testArtifactPath = "/artifact/model.onnx"

func TestDeriveExecutionPlanBindsOneCoherentCPUORTEnvelope(t *testing.T) {
	config := testRunConfig(t)
	config.SchemaVersion = profilingrun.RunConfigSchemaVersion
	config.State.ArtifactPath = testArtifactPath
	addWorkloadObservation(&config)

	plan, err := deriveConfiguredExecutionPlan(t, config)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Policy != testConfiguredActivePolicy || plan.PolicyVersion != testConfiguredActivePolicy {
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
	config := testRunConfig(t)
	config.SchemaVersion = profilingrun.RunConfigSchemaVersion
	config.State.ArtifactPath = testArtifactPath
	addWorkloadObservation(&config)
	config.Candidate.ExecutionPath = "tensorrt"
	if _, err := deriveConfiguredExecutionPlan(t, config); err == nil {
		t.Fatal("DeriveExecutionPlan() error = nil, want unsupported path rejection")
	}
}

func TestDeriveExecutionPlanAcceptsExplicitRKNNLitePath(t *testing.T) {
	config, _ := rknnPreparationFixture(t)
	config.Candidate.ExecutionPath = profilingrun.RKNNLiteExecutionPath
	policy := testExecutionPolicySpec(config.Measurement.WorkloadMode)
	policy.ExecutionPath = profilingrun.RKNNLiteExecutionPath
	raw, err := yaml.Marshal(ExecutionPolicyCatalog{
		SchemaVersion: ExecutionPolicyCatalogSchemaVersion,
		Policies:      []ExecutionPolicySpec{policy},
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := FreezeConfiguredExecutionPolicy(raw, config, validExecutionPolicySource())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := DeriveExecutionPlan(config, frozen)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Policy != policy.Name || plan.MaxLatencySamples != policy.MaxLatencySamples {
		t.Fatalf("RKNNLite execution plan drifted: %#v", plan)
	}
}

func TestDeriveExecutionPlanIncludesIdleSettlingEnvelope(t *testing.T) {
	config := testRunConfig(t)
	config.SchemaVersion = profilingrun.RunConfigSchemaVersion
	config.State.ArtifactPath = testArtifactPath
	addWorkloadObservation(&config)
	config.Measurement.WarmupIterations = 0
	config.Measurement.SettlingDurationSeconds = 60
	config.Measurement.WorkloadMode = profilingrun.IdleModelLoadedWorkloadMode
	config.Measurement.InputGenerator = profilingrun.NoInputGenerator
	config.Measurement.OutputValidation = profilingrun.NoOutputValidation

	plan, err := deriveConfiguredExecutionPlan(t, config)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Policy != testConfiguredIdlePolicy || plan.PolicyVersion != testConfiguredIdlePolicy {
		t.Fatalf("unexpected idle policy identity: %#v", plan)
	}
	if plan.ActiveDeadline != 271*time.Second || plan.AttemptTimeout != 241*time.Second ||
		plan.ObserverStartTimeout != 180*time.Second {
		t.Fatalf("idle settling time is not bound into the Job envelope: %#v", plan)
	}
}

func TestDeriveExecutionPlanRejectsMetricScheduleOutsideBoundedEnvelope(t *testing.T) {
	config := testRunConfig(t)
	config.SchemaVersion = profilingrun.RunConfigSchemaVersion
	config.State.ArtifactPath = testArtifactPath
	addWorkloadObservation(&config)
	config.Metrics.DurationSeconds = 60_000
	config.Metrics.IntervalMilliseconds = 1000
	if _, err := deriveConfiguredExecutionPlan(t, config); err == nil {
		t.Fatal("DeriveExecutionPlan() error = nil, want bounded event-capacity rejection")
	}
}

func TestDeriveExecutionPlanUsesFrozenConfiguredEnvelope(t *testing.T) {
	config := currentExecutionPolicyTestRunConfig(t)
	frozen, err := FreezeConfiguredExecutionPolicy(
		executionPolicyCatalogFixture(t),
		config,
		validExecutionPolicySource(),
	)
	if err != nil {
		t.Fatal(err)
	}
	frozen.Policy.MaxLatencySamples = 2048
	frozen.Policy.ObserverCPURequest = "10m"
	frozen.Policy.ObserverCPULimit = "200m"
	plan, err := DeriveExecutionPlan(config, frozen)
	if err != nil {
		t.Fatal(err)
	}
	if plan.MaxLatencySamples != 2048 || plan.ObserverCPURequest != "10m" || plan.ObserverCPULimit != "200m" {
		t.Fatalf("configured execution envelope was not projected: %#v", plan)
	}
	if plan.PolicyVersion != frozen.Policy.Name {
		t.Fatalf("configured policy version = %q", plan.PolicyVersion)
	}
}

func deriveConfiguredExecutionPlan(t *testing.T, config profilingrun.RunConfig) (ExecutionPlan, error) {
	t.Helper()
	policy, err := FreezeConfiguredExecutionPolicy(
		executionPolicyCatalogFixture(t),
		config,
		validExecutionPolicySource(),
	)
	if err != nil {
		return ExecutionPlan{}, err
	}
	return DeriveExecutionPlan(config, policy)
}

func addWorkloadObservation(config *profilingrun.RunConfig) {
	config.Images.WorkloadObserver = "registry.example/workload-observer@sha256:" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	config.Workload = profilingrun.CurrentWorkloadContract()
}
