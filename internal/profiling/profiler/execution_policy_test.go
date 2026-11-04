package profiler

import (
	"testing"
	"time"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"sigs.k8s.io/yaml"
)

func TestDeriveExecutionPlanBindsOneCoherentCPUORTEnvelope(t *testing.T) {
	config := testRunConfig(t)
	config.SchemaVersion = profilingrun.RunConfigSchemaVersion
	addWorkloadObservation(&config)

	plan, err := deriveConfiguredExecutionPlan(t, config)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Policy != testConfiguredActivePolicy || plan.PolicyVersion != testConfiguredActivePolicy {
		t.Fatalf("unexpected policy identity: %#v", plan)
	}
	if plan.ActiveDeadline != 211*time.Second || plan.AttemptTimeout != 181*time.Second {
		t.Fatalf("unexpected timing envelope: %#v", plan)
	}
	if plan.MetricObservationTimeout != plan.AttemptTimeout {
		t.Fatalf("metric watchdog does not cover the producer failure bound: %#v", plan)
	}
	if plan.MetricResultTimeout != plan.AttemptTimeout || plan.WorkloadResultTimeout != plan.AttemptTimeout ||
		plan.WorkloadEndFenceTimeout != plan.AttemptTimeout || plan.ObserverReadyTimeout >= plan.AttemptTimeout ||
		plan.ObserverStartTimeout >= plan.AttemptTimeout {
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
	addWorkloadObservation(&config)
	config.Measurement.WarmupIterations = 0
	config.Measurement.MinServiceSamples = 0
	config.Measurement.SLOPercentile = 0
	config.Measurement.SettlingDurationSeconds = 60
	config.Measurement.WorkloadMode = profilingrun.IdleModelLoadedWorkloadMode
	config.Measurement.Dataset = nil

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
	plan, err := DeriveExecutionPlan(config, frozen)
	if err != nil {
		t.Fatal(err)
	}
	if plan.MaxLatencySamples != 2048 {
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

func TestCaptureWatchdogAlignsEventsResultAndOuterEnvelopes(t *testing.T) {
	config := currentExecutionPolicyTestRunConfig(t)
	config.Measurement.DurationSeconds = 600
	// 1160s is the explicit frozen-runtime batch envelope when the search can
	// reach a batch size with no completed empirical tail yet; not a measured tail.
	config.Metrics.DurationSeconds = 600 + 1160 + 2 // final-read/coordination allowance
	config.Metrics.IntervalMilliseconds = 1000
	config.Metrics.TimeoutMilliseconds = 500
	frozen, err := FreezeConfiguredExecutionPolicy(executionPolicyCatalogFixture(t), config, validExecutionPolicySource())
	if err != nil {
		t.Fatal(err)
	}
	frozen.Policy.ObserverReadyTimeoutSeconds = 1160
	frozen.Policy.ObserverStartOverheadSeconds = 1160
	frozen.Policy.AttemptTimeoutOverheadSeconds = 1235
	frozen.Policy.ActiveDeadlineOverheadSeconds = 1265
	// This long fixture must retain the full producer failure budget, not only
	// its nominal metric window. Keep an explicitly adequate test envelope.
	frozen.Policy.MaxMetricResultBytes = 32 << 20
	frozen.Policy.ObserverMemoryLimitBytes = 256 << 20
	plan, err := DeriveExecutionPlan(config, frozen)
	if err != nil {
		t.Fatal(err)
	}
	if plan.MetricObservationTimeout != 2997*time.Second ||
		plan.AttemptTimeout != 2997*time.Second || plan.ActiveDeadline != 3027*time.Second ||
		plan.MetricResultTimeout != plan.AttemptTimeout || plan.WorkloadResultTimeout != plan.AttemptTimeout {
		t.Fatalf("misaligned capture envelope: %+v", plan)
	}
	if plan.MaxMetricResultBytes != frozen.Policy.MaxMetricResultBytes ||
		plan.ObserverMemoryLimitBytes != frozen.Policy.ObserverMemoryLimitBytes {
		t.Fatalf("resource limits were changed: %+v", plan)
	}
}

func TestDeriveExecutionPlanAdmitsFullProducerBoundNotOnlyNominalMetrics(t *testing.T) {
	config := currentExecutionPolicyTestRunConfig(t)
	frozen, err := FreezeConfiguredExecutionPolicy(executionPolicyCatalogFixture(t), config, validExecutionPolicySource())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DeriveExecutionPlan(config, frozen); err != nil {
		t.Fatal(err)
	}
	// The nominal schedule is unchanged and fits, but this immutable attempt
	// budget cannot be retained at that same interval in the selected resources.
	frozen.Policy.AttemptTimeoutOverheadSeconds = 60_000
	frozen.Policy.ActiveDeadlineOverheadSeconds = 60_030
	if _, err := DeriveExecutionPlan(config, frozen); err == nil {
		t.Fatal("admitted only the nominal schedule instead of the full producer failure bound")
	}
}
