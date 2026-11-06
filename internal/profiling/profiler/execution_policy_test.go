package profiler

import (
	"context"
	"strings"
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

	frozen, err := FreezeConfiguredExecutionPolicy(executionPolicyCatalogFixture(t), config, validExecutionPolicySource())
	if err != nil {
		t.Fatal(err)
	}
	frozen.Source = ExecutionPolicySource{}
	frozen.Policy.Name = "Display Policy / opaque"
	plan, err := DeriveExecutionPlan(config, frozen)
	if err != nil {
		t.Fatal(err)
	}
	wantAttempt := time.Duration(config.Metrics.DurationSeconds+config.Measurement.SettlingDurationSeconds+
		frozen.Policy.AttemptTimeoutOverheadSeconds) * time.Second
	wantDeadline := time.Duration(config.Metrics.DurationSeconds+config.Measurement.SettlingDurationSeconds+
		frozen.Policy.ActiveDeadlineOverheadSeconds) * time.Second
	if plan.AttemptTimeout != wantAttempt || plan.ActiveDeadline != wantDeadline ||
		plan.MetricObservationTimeout != wantAttempt || plan.MetricResultTimeout != wantAttempt ||
		plan.WorkloadEndFenceTimeout != wantAttempt || plan.WorkloadResultTimeout != wantAttempt {
		t.Fatal("donor-free execution changed the common configured deadline lifecycle")
	}
	for _, resource := range []string{"result", "memory"} {
		t.Run(resource, func(t *testing.T) {
			limited := frozen
			if resource == "result" {
				limited.Policy.MaxMetricResultBytes = 1024
			} else {
				limited.Policy.ObserverMemoryRequestBytes = 1 << 20
				limited.Policy.ObserverMemoryLimitBytes = 1 << 20
			}
			if _, err := DeriveExecutionPlan(config, limited); err == nil ||
				!strings.Contains(err.Error(), "admit metric observation") ||
				!strings.Contains(err.Error(), resource+" byte budget") {
				t.Fatalf("insufficient %s capacity was not rejected by the metric consumer: %v", resource, err)
			}
		})
	}
}

func TestDeriveExecutionPlanRejectsUnpromotedRuntimePath(t *testing.T) {
	config := testRunConfig(t)
	config.SchemaVersion = profilingrun.RunConfigSchemaVersion
	addWorkloadObservation(&config)
	config.Candidate.ExecutionPath = "tensorrt"
	if err := deriveConfiguredExecutionPlan(t, config); err == nil {
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
	if _, err := DeriveExecutionPlan(config, frozen); err != nil {
		t.Fatal(err)
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

	if err := deriveConfiguredExecutionPlan(t, config); err != nil {
		t.Fatal(err)
	}
	config.Measurement.SettlingDurationSeconds = 60_000
	if err := deriveConfiguredExecutionPlan(t, config); err == nil ||
		!strings.Contains(err.Error(), "admit metric observation") {
		t.Fatalf("idle settling escaped the metric capacity envelope: %v", err)
	}
}

func TestDeriveExecutionPlanRejectsMetricScheduleOutsideBoundedEnvelope(t *testing.T) {
	config := testRunConfig(t)
	config.SchemaVersion = profilingrun.RunConfigSchemaVersion
	addWorkloadObservation(&config)
	config.Metrics.DurationSeconds = 60_000
	config.Metrics.IntervalMilliseconds = 1000
	if err := deriveConfiguredExecutionPlan(t, config); err == nil {
		t.Fatal("DeriveExecutionPlan() error = nil, want bounded event-capacity rejection")
	}
}

func deriveConfiguredExecutionPlan(t *testing.T, config profilingrun.RunConfig) error {
	t.Helper()
	policy, err := FreezeConfiguredExecutionPolicy(
		executionPolicyCatalogFixture(t),
		config,
		validExecutionPolicySource(),
	)
	if err != nil {
		return err
	}
	_, err = DeriveExecutionPlan(config, policy)
	return err
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
	assertMetricRequestsCappedByPlan(t, plan)
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

func assertMetricRequestsCappedByPlan(t *testing.T, plan ExecutionPlan) {
	t.Helper()
	source := &executionPolicyDeadlineSource{t: t, maximum: plan.MetricObservationTimeout}
	observer, err := metricobserver.New(source, metricobserver.Limits{
		MaxResultBytes: plan.MaxMetricResultBytes, MemoryBudgetBytes: plan.ObserverMemoryLimitBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	end := make(chan struct{})
	// A valid source request may outlive the whole observation. The actual
	// context received by the source must still be capped by the plan watchdog.
	result, err := observer.ObserveUntil(context.Background(), metricobserver.Request{
		Duration: plan.MetricObservationTimeout, Interval: 3 * plan.MetricObservationTimeout,
		RequestTimeout: 2 * plan.MetricObservationTimeout,
	}, end, func() { close(end) })
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Samples) == 0 {
		t.Fatal("bounded metric requests did not produce evidence")
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}

type executionPolicyDeadlineSource struct {
	t       *testing.T
	maximum time.Duration
}

func (s *executionPolicyDeadlineSource) Identity() metricobserver.SourceIdentity {
	return metricobserver.SourceIdentity{
		Kind: "edge-metrics", Endpoint: "http://meter.test/metrics", Metric: "power_watts",
	}
}

func (s *executionPolicyDeadlineSource) ReadMetrics(ctx context.Context) (metricobserver.Reading, error) {
	deadline, ok := ctx.Deadline()
	if remaining := time.Until(deadline); !ok || remaining <= 0 || remaining > s.maximum {
		s.t.Fatalf("metric request escaped the plan watchdog: deadline=%v, bounded=%v", deadline, ok)
	}
	return metricobserver.Reading{Watts: 1}, nil
}
