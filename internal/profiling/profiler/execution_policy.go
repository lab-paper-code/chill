package profiler

import (
	"errors"
	"fmt"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

// ExecutionPlan is the bounded delivery policy for one resolved Run.
// It contains no Kubernetes objects; the controller adapter projects it into
// one native Job without inventing independent resource or timeout values.
type ExecutionPlan struct {
	Policy        string
	PolicyVersion string

	ActiveDeadline           time.Duration
	AttemptTimeout           time.Duration
	ObserverReadyTimeout     time.Duration
	ObserverStartTimeout     time.Duration
	MetricObservationTimeout time.Duration
	MetricResultTimeout      time.Duration
	WorkloadEndFenceTimeout  time.Duration
	WorkloadResultTimeout    time.Duration

	MaxLatencySamples      int
	MaxInputBytes          int64
	MaxRuntimeResultBytes  int64
	MaxMetricResultBytes   int64
	MaxWorkloadEvents      int
	MaxWorkloadResultBytes int64

	RuntimeMemoryRequestBytes          int64
	RuntimeMemoryLimitBytes            int64
	ObserverMemoryRequestBytes         int64
	ObserverMemoryLimitBytes           int64
	WorkloadObserverMemoryRequestBytes int64
	WorkloadObserverMemoryLimitBytes   int64
	CoordinationBytes                  int64
	WorkspaceBytes                     int64
}

// DeriveExecutionPlan combines a selected worker envelope with Run-derived
// durations. CPU scheduling and thread counts are left to the platform/runtime.
func DeriveExecutionPlan(
	config profilingrun.RunConfig,
	frozen FrozenExecutionPolicy,
) (ExecutionPlan, error) {
	if err := config.Validate(); err != nil {
		return ExecutionPlan{}, fmt.Errorf("validate RunConfig: %w", err)
	}
	if err := frozen.Validate(); err != nil {
		return ExecutionPlan{}, err
	}
	if !profilingrun.IsSupportedFirstfloorRunConfig(config) {
		return ExecutionPlan{}, errors.New("measurement worker requires a supported firstfloor RunConfig")
	}
	policy := frozen.Policy
	if (policy.DeviceClass != "*" && policy.DeviceClass != config.State.DeviceClass) ||
		policy.ExecutionPath != config.Candidate.ExecutionPath ||
		policy.WorkloadMode != config.Measurement.WorkloadMode {
		return ExecutionPlan{}, errors.New("selected execution policy does not match the RunConfig")
	}

	metricDuration := time.Duration(config.Metrics.DurationSeconds) * time.Second
	settlingDuration := time.Duration(config.Measurement.SettlingDurationSeconds) * time.Second
	if config.Measurement.Cadence != nil {
		pacing, err := config.Measurement.Cadence.PacingBudget(config.Measurement.MinServiceSamples)
		if err != nil {
			return ExecutionPlan{}, err
		}
		metricDuration += time.Duration(pacing)
	}
	interval := time.Duration(config.Metrics.IntervalMilliseconds) * time.Millisecond
	// Pacing is part of the variable active wall. The frozen draw allowance
	// covers the known-gap stop and an elapsed-floor overshoot; the existing
	// startup/service/final-read envelope remains the failure bound. All
	// observers, result readers, capacity and the Job use this same lifecycle.
	attemptTimeout := metricDuration + settlingDuration +
		time.Duration(policy.AttemptTimeoutOverheadSeconds)*time.Second
	metrics := config.Metrics
	thermal := metricobserver.ThermalMetricIdentity{
		ObservationAvailable: metrics.ThermalMetrics.ObservationAvailable,
		Active:               metrics.ThermalMetrics.Active, Latched: metrics.ThermalMetrics.Latched,
		CPUCoreThrottleEvents:     metrics.ThermalMetrics.CPUCoreThrottleEvents,
		CPUPackageThrottleEvents:  metrics.ThermalMetrics.CPUPackageThrottleEvents,
		MaximumTemperatureCelsius: metrics.ThermalMetrics.MaximumTemperatureCelsius,
	}
	_, err := metricobserver.RequiredCapacity(metricobserver.Request{
		Duration: attemptTimeout, Interval: interval,
		RequestTimeout: time.Duration(metrics.TimeoutMilliseconds) * time.Millisecond,
	}, metricobserver.SourceIdentity{
		Kind: metrics.SourceKind, Endpoint: metrics.Endpoint, Metric: metrics.PowerMetric,
		NodeName: metrics.NodeName, MetricLabels: metrics.MetricLabels,
		ThermalEndpoint: metrics.ThermalEndpoint, ThermalMetrics: &thermal,
	}, metricobserver.Limits{
		MaxResultBytes:    policy.MaxMetricResultBytes,
		MemoryBudgetBytes: policy.ObserverMemoryLimitBytes,
	}, true)
	if err != nil {
		return ExecutionPlan{}, fmt.Errorf("admit metric observation: %w", err)
	}

	plan := ExecutionPlan{
		Policy:        policy.Name,
		PolicyVersion: policy.Name,

		ActiveDeadline: metricDuration + settlingDuration +
			time.Duration(policy.ActiveDeadlineOverheadSeconds)*time.Second,
		AttemptTimeout:       attemptTimeout,
		ObserverReadyTimeout: time.Duration(policy.ObserverReadyTimeoutSeconds) * time.Second,
		ObserverStartTimeout: settlingDuration +
			time.Duration(policy.ObserverStartOverheadSeconds)*time.Second,
		MetricObservationTimeout: attemptTimeout,
		// Result readers share the deadline established before runtime startup;
		// neither handoff gets a fresh allowance after the measurement ends.
		MetricResultTimeout:     attemptTimeout,
		WorkloadEndFenceTimeout: attemptTimeout,
		WorkloadResultTimeout:   attemptTimeout,

		MaxLatencySamples:      policy.MaxLatencySamples,
		MaxInputBytes:          policy.MaxInputBytes,
		MaxRuntimeResultBytes:  policy.MaxRuntimeResultBytes,
		MaxMetricResultBytes:   policy.MaxMetricResultBytes,
		MaxWorkloadEvents:      policy.MaxWorkloadEvents,
		MaxWorkloadResultBytes: policy.MaxWorkloadResultBytes,

		RuntimeMemoryRequestBytes:          policy.RuntimeMemoryRequestBytes,
		RuntimeMemoryLimitBytes:            policy.RuntimeMemoryLimitBytes,
		ObserverMemoryRequestBytes:         policy.ObserverMemoryRequestBytes,
		ObserverMemoryLimitBytes:           policy.ObserverMemoryLimitBytes,
		WorkloadObserverMemoryRequestBytes: policy.WorkloadObserverMemoryRequestBytes,
		WorkloadObserverMemoryLimitBytes:   policy.WorkloadObserverMemoryLimitBytes,
		CoordinationBytes: policy.MaxMetricResultBytes + policy.MaxWorkloadResultBytes +
			policy.CoordinationReserveBytes,
		WorkspaceBytes: policy.WorkspaceBytes,
	}
	if err := validateExecutionPlan(plan); err != nil {
		return ExecutionPlan{}, err
	}
	return plan, nil
}

func validateExecutionPlan(plan ExecutionPlan) error {
	if plan.ActiveDeadline <= plan.AttemptTimeout {
		return errors.New("execution policy active deadline must exceed the attempt timeout")
	}
	for name, timeout := range map[string]time.Duration{
		"observer ready timeout": plan.ObserverReadyTimeout,
		"observer start timeout": plan.ObserverStartTimeout,
	} {
		if timeout <= 0 || timeout >= plan.AttemptTimeout {
			return fmt.Errorf("execution policy %s must be positive and below the attempt timeout", name)
		}
	}
	for name, timeout := range map[string]time.Duration{
		"metric result timeout":      plan.MetricResultTimeout,
		"workload result timeout":    plan.WorkloadResultTimeout,
		"workload end-fence timeout": plan.WorkloadEndFenceTimeout,
	} {
		if timeout <= 0 || timeout > plan.AttemptTimeout {
			return fmt.Errorf("execution policy %s must be positive and at most the attempt timeout", name)
		}
	}
	return nil
}
