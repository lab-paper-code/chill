package profiler

import (
	"errors"
	"fmt"
	"time"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

// ExecutionPlan is the bounded delivery policy for one resolved Run.
// It contains no Kubernetes objects; the controller adapter projects it into
// one native Job without inventing independent resource or timeout values.
type ExecutionPlan struct {
	Policy        string
	PolicyVersion string

	ActiveDeadline          time.Duration
	AttemptTimeout          time.Duration
	ObserverReadyTimeout    time.Duration
	ObserverStartTimeout    time.Duration
	MetricResultTimeout     time.Duration
	WorkloadEndFenceTimeout time.Duration
	WorkloadResultTimeout   time.Duration

	MaxLatencySamples      int
	MaxInputBytes          int64
	MaxRuntimeResultBytes  int64
	MaxMetricResultBytes   int64
	MaxMetricEvents        int
	MaxWorkloadEvents      int
	MaxWorkloadResultBytes int64

	RuntimeMemoryRequestBytes          int64
	RuntimeMemoryLimitBytes            int64
	ObserverCPURequest                 string
	ObserverCPULimit                   string
	ObserverMemoryRequestBytes         int64
	ObserverMemoryLimitBytes           int64
	WorkloadObserverCPURequest         string
	WorkloadObserverCPULimit           string
	WorkloadObserverMemoryRequestBytes int64
	WorkloadObserverMemoryLimitBytes   int64
	CoordinationBytes                  int64
	WorkspaceBytes                     int64
}

// DeriveExecutionPlan combines a selected worker envelope with Run-derived
// durations. CPU request and limit remain Node-specific RunConfig facts.
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
	if !profilingrun.IsSupportedCPUORTRunConfig(config) && !profilingrun.IsSupportedFirstfloorRunConfig(config) &&
		!profilingrun.IsSupportedRKNNLiteRunConfig(config) {
		return ExecutionPlan{}, errors.New("measurement worker requires a supported explicit RunConfig")
	}
	policy := frozen.Policy
	if (policy.DeviceClass != "*" && policy.DeviceClass != config.State.DeviceClass) ||
		policy.ExecutionPath != config.Candidate.ExecutionPath ||
		policy.WorkloadMode != config.Measurement.WorkloadMode {
		return ExecutionPlan{}, errors.New("selected execution policy does not match the RunConfig")
	}

	metricDuration := time.Duration(config.Metrics.DurationSeconds) * time.Second
	settlingDuration := time.Duration(config.Measurement.SettlingDurationSeconds) * time.Second
	interval := time.Duration(config.Metrics.IntervalMilliseconds) * time.Millisecond
	maxEvents64 := int64(metricDuration/interval) + 2
	if maxEvents64 < 1 || maxEvents64 > int64(policy.MaxMetricEvents) || maxEvents64 > int64(^uint(0)>>1) {
		return ExecutionPlan{}, errors.New("metric observation schedule exceeds worker event capacity")
	}

	plan := ExecutionPlan{
		Policy:        policy.Name,
		PolicyVersion: policy.Name,

		ActiveDeadline: metricDuration + settlingDuration +
			time.Duration(policy.ActiveDeadlineOverheadSeconds)*time.Second,
		AttemptTimeout: metricDuration + settlingDuration +
			time.Duration(policy.AttemptTimeoutOverheadSeconds)*time.Second,
		ObserverReadyTimeout: time.Duration(policy.ObserverReadyTimeoutSeconds) * time.Second,
		ObserverStartTimeout: settlingDuration +
			time.Duration(policy.ObserverStartOverheadSeconds)*time.Second,
		MetricResultTimeout: metricDuration - time.Duration(config.Measurement.DurationSeconds)*time.Second +
			time.Duration(policy.MetricResultOverheadSeconds)*time.Second,
		WorkloadEndFenceTimeout: time.Duration(policy.WorkloadEndFenceTimeoutSeconds) * time.Second,
		WorkloadResultTimeout:   time.Duration(policy.WorkloadResultTimeoutSeconds) * time.Second,

		MaxLatencySamples:      policy.MaxLatencySamples,
		MaxInputBytes:          policy.MaxInputBytes,
		MaxRuntimeResultBytes:  policy.MaxRuntimeResultBytes,
		MaxMetricResultBytes:   policy.MaxMetricResultBytes,
		MaxMetricEvents:        int(maxEvents64),
		MaxWorkloadEvents:      policy.MaxWorkloadEvents,
		MaxWorkloadResultBytes: policy.MaxWorkloadResultBytes,

		RuntimeMemoryRequestBytes:          policy.RuntimeMemoryRequestBytes,
		RuntimeMemoryLimitBytes:            policy.RuntimeMemoryLimitBytes,
		ObserverCPURequest:                 policy.ObserverCPURequest,
		ObserverCPULimit:                   policy.ObserverCPULimit,
		ObserverMemoryRequestBytes:         policy.ObserverMemoryRequestBytes,
		ObserverMemoryLimitBytes:           policy.ObserverMemoryLimitBytes,
		WorkloadObserverCPURequest:         policy.WorkloadObserverCPURequest,
		WorkloadObserverCPULimit:           policy.WorkloadObserverCPULimit,
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
		"observer ready timeout":     plan.ObserverReadyTimeout,
		"observer start timeout":     plan.ObserverStartTimeout,
		"metric result timeout":      plan.MetricResultTimeout,
		"workload end-fence timeout": plan.WorkloadEndFenceTimeout,
		"workload result timeout":    plan.WorkloadResultTimeout,
	} {
		if timeout <= 0 || timeout >= plan.AttemptTimeout {
			return fmt.Errorf("execution policy %s must be positive and below the attempt timeout", name)
		}
	}
	return nil
}
