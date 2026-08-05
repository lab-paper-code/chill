package profiler

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/profilingrun"
)

// ExecutionPlan is the bounded delivery policy for one already-frozen Run.
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

// DeriveExecutionPlan combines a frozen worker envelope with Run-derived
// durations. CPU request and limit remain Node-specific frozen intent facts.
func DeriveExecutionPlan(
	intent profilingrun.Intent,
	frozen FrozenExecutionPolicy,
) (ExecutionPlan, error) {
	if err := intent.Validate(); err != nil {
		return ExecutionPlan{}, fmt.Errorf("validate frozen Run intent: %w", err)
	}
	if err := frozen.Validate(); err != nil {
		return ExecutionPlan{}, err
	}
	if !profilingrun.IsSupportedCPUORTIntent(intent) && !profilingrun.IsSupportedRKNNLiteIntent(intent) {
		return ExecutionPlan{}, errors.New("measurement worker requires a supported explicit runtime intent")
	}
	policy := frozen.Policy
	if (policy.DeviceClass != "*" && policy.DeviceClass != intent.State.DeviceClass) ||
		policy.ExecutionPath != intent.Candidate.ExecutionPath ||
		policy.WorkloadMode != intent.Measurement.WorkloadMode {
		return ExecutionPlan{}, errors.New("frozen execution policy does not match the Run intent")
	}

	metricDuration := time.Duration(intent.Metrics.DurationSeconds) * time.Second
	settlingDuration := time.Duration(intent.Measurement.SettlingDurationSeconds) * time.Second
	interval := time.Duration(intent.Metrics.IntervalMilliseconds) * time.Millisecond
	maxEvents64 := int64(metricDuration/interval) + 2
	if maxEvents64 < 1 || maxEvents64 > int64(policy.MaxMetricEvents) || maxEvents64 > int64(^uint(0)>>1) {
		return ExecutionPlan{}, errors.New("metric observation schedule exceeds worker event capacity")
	}

	policyIdentity, err := frozen.Identity()
	if err != nil {
		return ExecutionPlan{}, err
	}
	policyVersion := strings.TrimPrefix(policyIdentity, policy.Name+"/")
	plan := ExecutionPlan{
		Policy:        policy.Name,
		PolicyVersion: policyVersion,

		ActiveDeadline: metricDuration + settlingDuration +
			time.Duration(policy.ActiveDeadlineOverheadSeconds)*time.Second,
		AttemptTimeout: metricDuration + settlingDuration +
			time.Duration(policy.AttemptTimeoutOverheadSeconds)*time.Second,
		ObserverReadyTimeout: time.Duration(policy.ObserverReadyTimeoutSeconds) * time.Second,
		ObserverStartTimeout: settlingDuration +
			time.Duration(policy.ObserverStartOverheadSeconds)*time.Second,
		MetricResultTimeout: metricDuration - time.Duration(intent.Measurement.DurationSeconds)*time.Second +
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
