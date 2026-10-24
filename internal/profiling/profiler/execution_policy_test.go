package profiler

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
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
	if plan.MaxMetricEvents != 34 {
		t.Fatalf("MaxMetricEvents = %d, want 34", plan.MaxMetricEvents)
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
	plan, err := DeriveExecutionPlan(config, frozen)
	if err != nil {
		t.Fatal(err)
	}
	if plan.MetricObservationTimeout != 1762*time.Second || plan.MaxMetricEvents != 1765 ||
		plan.AttemptTimeout != 2997*time.Second || plan.ActiveDeadline != 3027*time.Second ||
		plan.MetricResultTimeout != 1192*time.Second {
		t.Fatalf("misaligned capture envelope: %+v", plan)
	}
	// Serialized capacity evidence uses a representative fully typed sample at
	// every admitted event. Source errors remain bounded by the result writer;
	// this calculation is not a worst-case bound on arbitrary error strings/RSS.
	available, active, latched := true, false, false
	counter, temperature := uint64(1234), 80.5
	start := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	result := metricobserver.Result{Source: metricobserver.SourceIdentity{Kind: config.Metrics.SourceKind, Endpoint: config.Metrics.Endpoint, Metric: config.Metrics.PowerMetric}, StartedAt: start}
	result.Samples = make([]metricobserver.Sample, plan.MaxMetricEvents)
	for i := range result.Samples {
		result.Samples[i] = metricobserver.Sample{ObservedAt: start.Add(time.Duration(i) * time.Second), Watts: 123.456, RequestLatencySeconds: .248723980,
			Thermal: &metricobserver.ThermalReading{ObservationAvailable: &available, Active: &active, Latched: &latched, CPUCoreThrottleEvents: &counter, CPUPackageThrottleEvents: &counter, MaximumTemperatureCelsius: &temperature}}
	}
	result.EndedAt = result.Samples[len(result.Samples)-1].ObservedAt.Add(time.Millisecond)
	result.Summary = metricobserver.Summarize(result)
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(payload)) > plan.MaxMetricResultBytes {
		t.Fatalf("representative %d-event result=%d exceeds %d", plan.MaxMetricEvents, len(payload), plan.MaxMetricResultBytes)
	}
	t.Logf("watchdog=%s events=%d/%d representativeJSON=%d/%d observerMemoryLimit=%d attempt=%s Job=%s",
		plan.MetricObservationTimeout, plan.MaxMetricEvents, frozen.Policy.MaxMetricEvents, len(payload), plan.MaxMetricResultBytes, plan.ObserverMemoryLimitBytes, plan.AttemptTimeout, plan.ActiveDeadline)
}
