package profiler

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/lab-paper-code/chill/internal/profilingrun"
	"k8s.io/apimachinery/pkg/api/resource"
)

var (
	executionPolicyNamePattern   = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)
	executionPolicyDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

func (f FrozenExecutionPolicy) Validate() error {
	if f.SchemaVersion != FrozenExecutionPolicySchemaVersion {
		return fmt.Errorf("unsupported frozen execution-policy schema %q", f.SchemaVersion)
	}
	if err := f.Policy.Validate(); err != nil {
		return fmt.Errorf("validate frozen execution policy: %w", err)
	}
	if !executionPolicyDigestPattern.MatchString(f.Source.CatalogContentDigest) {
		return errors.New("execution-policy source catalogContentDigest must be a canonical SHA-256 digest")
	}
	switch f.Source.Kind {
	case ConfigMapExecutionPolicySourceKind:
		for name, value := range map[string]string{
			"namespace":       f.Source.Namespace,
			"name":            f.Source.Name,
			"uid":             f.Source.UID,
			"resourceVersion": f.Source.ResourceVersion,
			"key":             f.Source.Key,
		} {
			if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
				return fmt.Errorf("execution-policy ConfigMap source %s is required without surrounding whitespace", name)
			}
		}
	case BuiltInExecutionPolicySourceKind:
		if strings.TrimSpace(f.Source.Name) == "" || strings.TrimSpace(f.Source.Name) != f.Source.Name {
			return errors.New("built-in execution-policy source name is required without surrounding whitespace")
		}
		if f.Source.Namespace != "" || f.Source.UID != "" || f.Source.ResourceVersion != "" || f.Source.Key != "" {
			return errors.New("built-in execution-policy source cannot contain ConfigMap identity")
		}
	default:
		return fmt.Errorf("unsupported execution-policy source kind %q", f.Source.Kind)
	}
	return nil
}

func (p ExecutionPolicy) Validate() error {
	if p.SchemaVersion != ExecutionPolicySchemaVersion {
		return fmt.Errorf("unsupported execution-policy schema %q", p.SchemaVersion)
	}
	if len(p.Name) < 1 || len(p.Name) > 63 || !executionPolicyNamePattern.MatchString(p.Name) {
		return errors.New("execution-policy name must be a lowercase DNS-like identifier of at most 63 characters")
	}
	if strings.TrimSpace(p.DeviceClass) == "" || strings.TrimSpace(p.DeviceClass) != p.DeviceClass {
		return errors.New("execution-policy deviceClass is required without surrounding whitespace")
	}
	if strings.TrimSpace(p.ExecutionPath) == "" || strings.TrimSpace(p.ExecutionPath) != p.ExecutionPath {
		return errors.New("execution-policy executionPath is required without surrounding whitespace")
	}
	if p.WorkloadMode != profilingrun.SaturatedSerialFixedBatchWorkloadMode &&
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
		"maxMetricEvents":                p.MaxMetricEvents,
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
	if err := validateExecutionPolicyCPU("observer", p.ObserverCPURequest, p.ObserverCPULimit); err != nil {
		return err
	}
	return validateExecutionPolicyCPU(
		"workload observer",
		p.WorkloadObserverCPURequest,
		p.WorkloadObserverCPULimit,
	)
}

func validateExecutionPolicyCPU(name, requestValue, limitValue string) error {
	request, err := resource.ParseQuantity(requestValue)
	if err != nil || request.Sign() <= 0 {
		return fmt.Errorf("execution-policy %s CPU request must be a positive Kubernetes quantity", name)
	}
	limit, err := resource.ParseQuantity(limitValue)
	if err != nil || limit.Sign() <= 0 {
		return fmt.Errorf("execution-policy %s CPU limit must be a positive Kubernetes quantity", name)
	}
	if request.Cmp(limit) > 0 {
		return fmt.Errorf("execution-policy %s CPU request must not exceed its limit", name)
	}
	return nil
}
