package profilingrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/prometheus/common/model"
	"k8s.io/apimachinery/pkg/api/resource"
)

var artifactPathPattern = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

func (i Intent) Validate() error {
	if err := i.validateSchema(); err != nil {
		return err
	}

	if err := validateCandidateInputs(i.Candidate.Inputs); err != nil {
		return err
	}
	if err := i.validateActuationContract(); err != nil {
		return err
	}
	if i.Candidate.Verdict != "Compatible" {
		return fmt.Errorf("candidate verdict must be Compatible, got %q", i.Candidate.Verdict)
	}
	for name, value := range map[string]string{
		"candidate.executionPath":      i.Candidate.ExecutionPath,
		"state.deviceClass":            i.State.DeviceClass,
		"state.model":                  i.State.Model,
		"state.artifact":               i.State.Artifact,
		"state.runtimeFamily":          i.State.RuntimeFamily,
		"state.backend":                i.State.Backend,
		"state.powerMode":              i.State.PowerMode,
		"targetNode.name":              i.TargetNode.Name,
		"targetNode.uid":               i.TargetNode.UID,
		"targetNode.resourceVersion":   i.TargetNode.ResourceVersion,
		"targetNode.architecture":      i.TargetNode.Architecture,
		"targetNode.allocatableCPU":    i.TargetNode.AllocatableCPU,
		"cpu.policy":                   i.CPU.Policy,
		"cpu.policyVersion":            i.CPU.PolicyVersion,
		"measurement.workloadMode":     i.Measurement.WorkloadMode,
		"measurement.inputGenerator":   i.Measurement.InputGenerator,
		"measurement.outputValidation": i.Measurement.OutputValidation,
		"metrics.sourceKind":           i.Metrics.SourceKind,
		"metrics.nodeName":             i.Metrics.NodeName,
		"metrics.endpoint":             i.Metrics.Endpoint,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	for name, value := range map[string]string{
		"images.runtime":        i.Images.Runtime,
		"images.artifact":       i.Images.Artifact,
		"images.metricObserver": i.Images.MetricObserver,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	if value := i.Images.WorkloadObserver; strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
		return errors.New("images.workloadObserver is required without surrounding whitespace")
	}
	if err := i.Workload.Validate(); err != nil {
		return fmt.Errorf("validate workload contract: %w", err)
	}
	if i.Metrics.NodeName != i.TargetNode.Name {
		return errors.New("metrics nodeName must match target Node")
	}
	if strings.TrimSpace(i.Metrics.ThermalEndpoint) == "" ||
		strings.TrimSpace(i.Metrics.ThermalEndpoint) != i.Metrics.ThermalEndpoint {
		return errors.New("metrics.thermalEndpoint is required without surrounding whitespace")
	}
	if err := i.Metrics.ValidateMetricIdentities(); err != nil {
		return err
	}
	if err := i.validateCPUContract(); err != nil {
		return err
	}
	if i.Measurement.Batch < 1 || i.Measurement.WarmupIterations < 0 || i.Measurement.DurationSeconds < 1 {
		return errors.New("measurement batch, duration, and warm-up iterations must be valid")
	}
	if err := i.validateMeasurementMode(); err != nil {
		return err
	}
	if i.Metrics.IntervalMilliseconds < 1 || i.Metrics.TimeoutMilliseconds < 1 ||
		i.Metrics.TimeoutMilliseconds >= i.Metrics.IntervalMilliseconds ||
		i.Metrics.DurationSeconds < i.Measurement.DurationSeconds {
		return errors.New("invalid or under-covering metric observation request")
	}
	return nil
}

// ValidateMetricIdentities checks the configured Prometheus names and ensures
// that every typed slot denotes a distinct metric.
func (m MetricContract) ValidateMetricIdentities() error {
	if len(m.MetricLabels) == 0 {
		return errors.New("metrics.metricLabels is required")
	}
	for name, value := range m.MetricLabels {
		if !model.LabelName(name).IsValid() {
			return fmt.Errorf("metrics.metricLabels name %q is invalid", name)
		}
		if value == "" || !model.LabelValue(value).IsValid() {
			return fmt.Errorf("metrics.metricLabels value for %q must be nonempty valid UTF-8", name)
		}
	}
	metrics := []struct {
		name  string
		value string
	}{
		{name: "metrics.powerMetric", value: m.PowerMetric},
		{name: "metrics.thermalMetrics.observationAvailable", value: m.ThermalMetrics.ObservationAvailable},
		{name: "metrics.thermalMetrics.active", value: m.ThermalMetrics.Active},
		{name: "metrics.thermalMetrics.cpuCoreThrottleEvents", value: m.ThermalMetrics.CPUCoreThrottleEvents},
		{name: "metrics.thermalMetrics.cpuPackageThrottleEvents", value: m.ThermalMetrics.CPUPackageThrottleEvents},
		{name: "metrics.thermalMetrics.latched", value: m.ThermalMetrics.Latched},
		{name: "metrics.thermalMetrics.maximumTemperatureCelsius", value: m.ThermalMetrics.MaximumTemperatureCelsius},
	}
	seen := make(map[string]struct{}, len(metrics))
	for _, metric := range metrics {
		if strings.TrimSpace(metric.value) == "" || strings.TrimSpace(metric.value) != metric.value {
			return fmt.Errorf("%s is required without surrounding whitespace", metric.name)
		}
		if !model.IsValidMetricName(model.LabelValue(metric.value)) {
			return fmt.Errorf("%s must be a valid Prometheus metric name", metric.name)
		}
		if _, duplicate := seen[metric.value]; duplicate {
			return fmt.Errorf("metric identity %q is duplicated", metric.value)
		}
		seen[metric.value] = struct{}{}
	}
	return nil
}

func (i Intent) validateSchema() error {
	if i.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schemaVersion %q", i.SchemaVersion)
	}
	if !validArtifactPath(i.State.ArtifactPath) {
		return errors.New("state.artifactPath must be a clean absolute container path")
	}
	if i.Workload.SchemaVersion != WorkloadContractSchemaVersion {
		return errors.New("intent requires the current workload observation contract")
	}
	return nil
}

func (i Intent) validateMeasurementMode() error {
	if i.Measurement.WorkloadMode == IdleModelLoadedWorkloadMode {
		if (i.State.RuntimeFamily != RKNNLiteRuntimeFamily && i.Measurement.Batch != 1) ||
			i.Measurement.WarmupIterations != 0 ||
			i.Measurement.SettlingDurationSeconds < 1 ||
			i.Measurement.InputGenerator != NoInputGenerator ||
			i.Measurement.OutputValidation != NoOutputValidation {
			return errors.New("model-loaded idle measurement protocol is invalid")
		}
		return nil
	}
	if i.Measurement.SettlingDurationSeconds != 0 {
		return errors.New("active measurement cannot contain settlingDurationSeconds")
	}
	return nil
}

// IsSupportedActiveIntent reports whether the CPU ORT active worker can
// execute the current intent. Callers must validate the intent separately.
func IsSupportedActiveIntent(i Intent) bool {
	return supportedCPUORTIntentSchema(i.SchemaVersion) &&
		i.Measurement.WorkloadMode != IdleModelLoadedWorkloadMode &&
		isCPUORTExecution(i)
}

// IsSupportedCPUORTIntent covers the active and model-loaded-idle paths
// implemented by the explicit CPU ORT worker.
func IsSupportedCPUORTIntent(i Intent) bool {
	return IsSupportedActiveIntent(i) ||
		(supportedCPUORTIdleIntentSchema(i.SchemaVersion) &&
			i.Measurement.WorkloadMode == IdleModelLoadedWorkloadMode &&
			isCPUORTExecution(i))
}

// IsSupportedRKNNLiteIntent covers the current active and model-loaded-idle
// contracts implemented by the explicit host-bound RKNNLite worker.
func IsSupportedRKNNLiteIntent(i Intent) bool {
	return i.SchemaVersion == SchemaVersion &&
		hasExecutionPath(i) &&
		i.State.RuntimeFamily == RKNNLiteRuntimeFamily &&
		i.State.Backend == RKNNLiteBackend &&
		i.State.HostContract != nil &&
		(i.Measurement.WorkloadMode == SaturatedSerialFixedBatchWorkloadMode ||
			i.Measurement.WorkloadMode == IdleModelLoadedWorkloadMode)
}

func isCPUORTExecution(i Intent) bool {
	return hasExecutionPath(i) &&
		i.State.RuntimeFamily == "onnxruntime" &&
		i.State.Backend == "CPUExecutionProvider"
}

func hasExecutionPath(i Intent) bool {
	return i.Candidate.ExecutionPath != "" &&
		strings.TrimSpace(i.Candidate.ExecutionPath) == i.Candidate.ExecutionPath
}

func supportedCPUORTIntentSchema(schemaVersion string) bool {
	return schemaVersion == SchemaVersion
}

func supportedCPUORTIdleIntentSchema(schemaVersion string) bool {
	return schemaVersion == SchemaVersion
}

func (i Intent) validateCPUContract() error {
	if i.CPU.ExclusivityClaimed {
		return errors.New("first CPU policy must not claim Node exclusivity")
	}
	requestCPU, err := resource.ParseQuantity(i.CPU.Request)
	if err != nil || requestCPU.Sign() <= 0 {
		return errors.New("CPU request must be a positive Kubernetes quantity")
	}
	limitCPU, err := resource.ParseQuantity(i.CPU.Limit)
	if err != nil || limitCPU.Sign() <= 0 {
		return errors.New("CPU limit must be a positive Kubernetes quantity")
	}
	if requestCPU.Cmp(limitCPU) > 0 {
		return errors.New("CPU request must not exceed CPU limit")
	}
	cpu, err := strconv.Atoi(i.TargetNode.AllocatableCPU)
	if err != nil || cpu < 1 {
		return errors.New("target allocatableCPU must be a positive integer")
	}
	if i.CPU.Limit != i.TargetNode.AllocatableCPU {
		return errors.New("CPU contract does not match the AllocatableCPUQuota policy")
	}
	if i.State.RuntimeFamily == RKNNLiteRuntimeFamily {
		if i.CPU.ORTIntraOpThreads != 0 || i.CPU.ORTInterOpThreads != 0 {
			return errors.New("RKNNLite CPU contract cannot contain ONNX Runtime thread settings")
		}
		return nil
	}
	if i.CPU.ORTIntraOpThreads != cpu || i.CPU.ORTInterOpThreads != 1 {
		return errors.New("CPU contract does not match the AllocatableCPUQuota policy")
	}
	return nil
}

func validateCandidateInputs(inputs CandidateReportInputs) error {
	for name, value := range map[string]string{
		"candidate ModelSpec name":                 inputs.ModelSpec.Name,
		"candidate ModelSpec UID":                  inputs.ModelSpec.UID,
		"candidate ModelSpec resourceVersion":      inputs.ModelSpec.ResourceVersion,
		"candidate DeviceClass name":               inputs.DeviceClass.Name,
		"candidate DeviceClass UID":                inputs.DeviceClass.UID,
		"candidate DeviceClass resourceVersion":    inputs.DeviceClass.ResourceVersion,
		"candidate runtime namespace":              inputs.RuntimeDeclaration.Namespace,
		"candidate runtime source name":            inputs.RuntimeDeclaration.Name,
		"candidate runtime source UID":             inputs.RuntimeDeclaration.UID,
		"candidate runtime source resourceVersion": inputs.RuntimeDeclaration.ResourceVersion,
		"candidate runtime source key":             inputs.RuntimeDeclaration.Key,
		"candidate runtime image":                  inputs.RuntimeDeclaration.Image,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	return nil
}

func (i Intent) validateActuationContract() error {
	if i.State.PowerMode == FixedPowerMode {
		if i.Actuation != nil {
			return errors.New("fixed power mode cannot contain actuation")
		}
		return nil
	}
	if i.SchemaVersion != SchemaVersion {
		return errors.New("actuated power mode requires the current intent schema")
	}
	if i.Actuation == nil {
		return errors.New("non-fixed power mode requires actuation")
	}
	if strings.TrimSpace(i.Actuation.PowerMode) == "" ||
		i.Actuation.PowerMode != strings.TrimSpace(i.Actuation.PowerMode) ||
		i.Actuation.PowerMode != i.State.PowerMode {
		return errors.New("actuation powerMode must match execution state")
	}
	if strings.TrimSpace(i.Actuation.PolicyName) == "" ||
		strings.TrimSpace(i.Actuation.PolicyName) != i.Actuation.PolicyName {
		return errors.New("actuation.policyName is required without surrounding whitespace")
	}
	if err := i.Actuation.Actuator.Validate(); err != nil {
		return fmt.Errorf("validate actuation target: %w", err)
	}
	return nil
}

// Validate checks the strict typed target independently of any mutable host
// observation. Fixed mode deliberately has no ActuatorTarget value.
func (a ActuatorTarget) Validate() error {
	switch a.Kind {
	case ActuatorKindJetsonNVPModel:
		if a.JetsonNVPModel == nil ||
			a.NvidiaLockedGPUClock != nil ||
			a.JetsonNVPModel.ID < 0 {
			return errors.New("JetsonNVPModel actuation requires only a non-negative ID")
		}
	case ActuatorKindNvidiaLockedGPUClock:
		target := a.NvidiaLockedGPUClock
		if target == nil || a.JetsonNVPModel != nil ||
			target.MinMHz < 1 || target.MaxMHz < target.MinMHz {
			return errors.New("NvidiaLockedGPUClock actuation requires a valid clock range")
		}
		if target.Mode != nil && *target.Mode != 0 && *target.Mode != 1 {
			return errors.New("NvidiaLockedGPUClock actuation mode must be 0 or 1")
		}
	default:
		return fmt.Errorf("unsupported actuation kind %q", a.Kind)
	}
	return nil
}

func validArtifactPath(value string) bool {
	return value != "/" && artifactPathPattern.MatchString(value) && path.Clean(value) == value
}

func (i Intent) CanonicalJSON() ([]byte, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(i)
}
