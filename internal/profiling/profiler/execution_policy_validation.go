package profiler

import (
	"errors"
	"fmt"
	"math"
	"strings"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func (f FrozenExecutionPolicy) Validate() error {
	if f.SchemaVersion != FrozenExecutionPolicySchemaVersion {
		return fmt.Errorf("unsupported frozen execution-policy schema %q", f.SchemaVersion)
	}
	if err := f.Policy.Validate(); err != nil {
		return fmt.Errorf("validate frozen execution policy: %w", err)
	}
	return nil
}

func (p ExecutionPolicy) Validate() error {
	if p.SchemaVersion != ExecutionPolicySchemaVersion {
		return fmt.Errorf("unsupported execution-policy schema %q", p.SchemaVersion)
	}
	if strings.TrimSpace(p.DeviceClass) == "" || strings.TrimSpace(p.DeviceClass) != p.DeviceClass {
		return errors.New("execution-policy deviceClass is required without surrounding whitespace")
	}
	if strings.TrimSpace(p.ExecutionPath) == "" || strings.TrimSpace(p.ExecutionPath) != p.ExecutionPath {
		return errors.New("execution-policy executionPath is required without surrounding whitespace")
	}
	if p.WorkloadMode != profilingrun.EmpiricalCadenceFixedBatchWorkloadMode &&
		p.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode {
		return fmt.Errorf("unsupported execution-policy workloadMode %q", p.WorkloadMode)
	}
	for name, value := range map[string]int{
		"activeDeadlineOverheadSeconds":  p.ActiveDeadlineOverheadSeconds,
		"attemptTimeoutOverheadSeconds":  p.AttemptTimeoutOverheadSeconds,
		"observerReadyTimeoutSeconds":    p.ObserverReadyTimeoutSeconds,
		"observerStartOverheadSeconds":   p.ObserverStartOverheadSeconds,
		"metricResultOverheadSeconds":    p.MetricResultOverheadSeconds,
		"workloadEndFenceTimeoutSeconds": p.WorkloadEndFenceTimeoutSeconds,
		"workloadResultTimeoutSeconds":   p.WorkloadResultTimeoutSeconds,
		"maxLatencySamples":              p.MaxLatencySamples,
		"maxWorkloadEvents":              p.MaxWorkloadEvents,
	} {
		if value < 1 {
			return fmt.Errorf("execution-policy %s must be positive", name)
		}
	}
	for name, value := range map[string]int{
		"activeDeadlineOverheadSeconds":  p.ActiveDeadlineOverheadSeconds,
		"attemptTimeoutOverheadSeconds":  p.AttemptTimeoutOverheadSeconds,
		"observerReadyTimeoutSeconds":    p.ObserverReadyTimeoutSeconds,
		"observerStartOverheadSeconds":   p.ObserverStartOverheadSeconds,
		"metricResultOverheadSeconds":    p.MetricResultOverheadSeconds,
		"workloadEndFenceTimeoutSeconds": p.WorkloadEndFenceTimeoutSeconds,
		"workloadResultTimeoutSeconds":   p.WorkloadResultTimeoutSeconds,
	} {
		if value > 86_400 {
			return fmt.Errorf("execution-policy %s must not exceed 24 hours", name)
		}
	}
	for name, value := range map[string]int64{
		"maxInputBytes":                      p.MaxInputBytes,
		"maxRuntimeResultBytes":              p.MaxRuntimeResultBytes,
		"maxMetricResultBytes":               p.MaxMetricResultBytes,
		"maxWorkloadResultBytes":             p.MaxWorkloadResultBytes,
		"runtimeMemoryRequestBytes":          p.RuntimeMemoryRequestBytes,
		"runtimeMemoryLimitBytes":            p.RuntimeMemoryLimitBytes,
		"observerMemoryRequestBytes":         p.ObserverMemoryRequestBytes,
		"observerMemoryLimitBytes":           p.ObserverMemoryLimitBytes,
		"workloadObserverMemoryRequestBytes": p.WorkloadObserverMemoryRequestBytes,
		"workloadObserverMemoryLimitBytes":   p.WorkloadObserverMemoryLimitBytes,
		"coordinationReserveBytes":           p.CoordinationReserveBytes,
		"workspaceBytes":                     p.WorkspaceBytes,
	} {
		if value < 1 {
			return fmt.Errorf("execution-policy %s must be positive", name)
		}
	}
	if p.RuntimeMemoryRequestBytes > p.RuntimeMemoryLimitBytes ||
		p.ObserverMemoryRequestBytes > p.ObserverMemoryLimitBytes ||
		p.WorkloadObserverMemoryRequestBytes > p.WorkloadObserverMemoryLimitBytes {
		return errors.New("execution-policy memory requests must not exceed limits")
	}
	if p.MaxInputBytes > math.MaxInt64-p.MaxRuntimeResultBytes ||
		p.MaxInputBytes+p.MaxRuntimeResultBytes >= p.RuntimeMemoryLimitBytes {
		return errors.New("execution-policy runtime capacities do not fit the runtime memory limit")
	}
	if p.MaxRuntimeResultBytes > math.MaxInt64-p.MaxMetricResultBytes ||
		p.MaxRuntimeResultBytes+p.MaxMetricResultBytes > p.WorkspaceBytes {
		return errors.New("execution-policy result capacities do not fit the workspace")
	}
	if p.MaxMetricResultBytes > math.MaxInt64-p.MaxWorkloadResultBytes ||
		p.MaxMetricResultBytes+p.MaxWorkloadResultBytes > math.MaxInt64-p.CoordinationReserveBytes {
		return errors.New("execution-policy coordination capacity overflows int64")
	}
	return nil
}
