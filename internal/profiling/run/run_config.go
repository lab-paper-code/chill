package profilingrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/lab-paper-code/chill/internal/modelcompat"
	"github.com/prometheus/common/model"
	"k8s.io/apimachinery/pkg/api/resource"
)

const inputDatatypeFP32 = "FP32"

var artifactPathPattern = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

func (i RunConfig) Validate() error {
	if err := i.validateSchema(); err != nil {
		return err
	}

	if err := validateCandidateInputs(i.Candidate.Inputs); err != nil {
		return err
	}
	if err := i.validateActuationContract(); err != nil {
		return err
	}
	if err := i.validateDelivery(); err != nil {
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

func (i RunConfig) validateDelivery() error {
	delivery := i.Delivery
	if err := validateCandidateRuntimePackages(delivery.RuntimePackages); err != nil {
		return fmt.Errorf("validate RunConfig delivery: %w", err)
	}
	if err := validateArtifactCapability(delivery.ArtifactCapability); err != nil {
		return fmt.Errorf("RunConfig delivery artifact capability: %w", err)
	}
	if delivery.HostContract != nil {
		if err := validateCandidateHostContract(*delivery.HostContract); err != nil {
			return fmt.Errorf("validate RunConfig delivery: %w", err)
		}
	}
	if delivery.RuntimeAccess != nil {
		if err := validateCandidateRuntimeAccess(*delivery.RuntimeAccess); err != nil {
			return fmt.Errorf("validate RunConfig delivery: %w", err)
		}
	}
	if err := i.validateFirstfloorDelivery(); err != nil {
		return err
	}
	if !isRKNNExecution(i) {
		return nil
	}
	if delivery.ArtifactCapability == nil || len(delivery.RuntimePackages) != 1 ||
		delivery.RuntimePackages[0].Name != "rknn-toolkit-lite2" ||
		delivery.HostContract == nil || delivery.HostContract.Platform == nil || delivery.RuntimeAccess == nil ||
		len(delivery.ArtifactCapability.Tensors) == 0 || delivery.ArtifactCapability.TargetPlatform == "" {
		return errors.New("RKNNLite RunConfig delivery is incomplete")
	}
	if !slices.Contains(delivery.ArtifactCapability.DeclaredBatchSizes, int32(i.Measurement.Batch)) {
		return errors.New("RKNN artifact does not declare the measurement batch as a supported batch size")
	}
	if len(delivery.ArtifactCapability.Tensors) != 1 ||
		delivery.ArtifactCapability.Tensors[0].Datatype != inputDatatypeFP32 ||
		len(delivery.ArtifactCapability.Tensors[0].SampleShape) != 3 ||
		delivery.ArtifactCapability.TargetPlatform != "rk3588" {
		return errors.New("RKNNLite requires one FP32 rank-3 NHWC sample input for rk3588")
	}
	if !reflect.DeepEqual(i.State.HostContract, delivery.HostContract) {
		return errors.New("RKNNLite RunConfig state and delivery host contracts differ")
	}
	return nil
}

func (i RunConfig) validateFirstfloorDelivery() error {
	delivery := i.Delivery
	if i.State.RuntimeFamily == FirstfloorRuntimeFamily {
		switch i.State.Backend {
		case FirstfloorORTCPUBackend, FirstfloorORTCUDABackend, FirstfloorRKNNLiteBackend:
		default:
			return errors.New("firstfloor backend must be ort-cpu, ort-cuda, or rknnlite")
		}
		if err := modelcompat.ValidateFirstfloorRuntimeSettings(i.State.Firstfloor); err != nil {
			return err
		}
		if i.Measurement.Batch > int(i.State.Firstfloor.MaxBatchSize) {
			return errors.New("measurement batch exceeds the selected firstfloor maxBatchSize")
		}
		if delivery.ArtifactCapability == nil || len(delivery.ArtifactCapability.Tensors) == 0 {
			return errors.New("firstfloor runtime requires frozen artifact inputs")
		}
		for _, tensor := range delivery.ArtifactCapability.Tensors {
			if tensor.Datatype == "BYTES" || tensor.Datatype == "BF16" {
				return errors.New("firstfloor requires supported numeric or bool tensor datatypes")
			}
		}
		if i.State.Backend == FirstfloorORTCUDABackend {
			if delivery.RuntimeAccess == nil || delivery.RuntimeAccess.GPUUUID == "" ||
				len(delivery.RuntimeAccess.Resources) != 1 || delivery.RuntimeAccess.Resources["nvidia.com/gpu"] != "1" {
				return errors.New("firstfloor ort-cuda requires an explicit GPU UUID and exactly one nvidia.com/gpu resource")
			}
			if !reflect.DeepEqual(i.State.HostContract, delivery.HostContract) {
				return errors.New("CUDA RunConfig state and delivery host contracts differ")
			}
		} else if delivery.RuntimeAccess != nil &&
			(delivery.RuntimeAccess.GPUUUID != "" || len(delivery.RuntimeAccess.Resources) != 0) {
			return errors.New("non-CUDA firstfloor backend cannot contain CUDA allocation")
		}
	} else if i.State.Firstfloor != nil {
		return errors.New("non-firstfloor RunConfig cannot contain firstfloor settings")
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

func (i RunConfig) validateSchema() error {
	if i.SchemaVersion != RunConfigSchemaVersion {
		return fmt.Errorf("unsupported schemaVersion %q", i.SchemaVersion)
	}
	if !validArtifactPath(i.State.ArtifactPath) {
		return errors.New("state.artifactPath must be a clean absolute container path")
	}
	if i.Workload.SchemaVersion != WorkloadContractSchemaVersion {
		return errors.New("RunConfig requires the current workload observation contract")
	}
	return nil
}

func (i RunConfig) validateMeasurementMode() error {
	if i.Measurement.WorkloadMode == IdleModelLoadedWorkloadMode {
		if (!isRKNNExecution(i) && i.Measurement.Batch != 1) ||
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

// IsSupportedActiveRunConfig reports whether the CPU ORT active worker can
// execute the current RunConfig. Callers must validate the config separately.
func IsSupportedActiveRunConfig(i RunConfig) bool {
	return supportedCPUORTRunConfigSchema(i.SchemaVersion) &&
		i.Measurement.WorkloadMode != IdleModelLoadedWorkloadMode &&
		isCPUORTExecution(i)
}

// IsSupportedCPUORTRunConfig covers the active and model-loaded-idle paths
// implemented by the explicit CPU ORT worker.
func IsSupportedCPUORTRunConfig(i RunConfig) bool {
	return IsSupportedActiveRunConfig(i) ||
		(supportedCPUORTIdleRunConfigSchema(i.SchemaVersion) &&
			i.Measurement.WorkloadMode == IdleModelLoadedWorkloadMode &&
			isCPUORTExecution(i))
}

// IsSupportedRKNNLiteRunConfig covers the current active and model-loaded-idle
// contracts implemented by the explicit host-bound RKNNLite worker.
func IsSupportedRKNNLiteRunConfig(i RunConfig) bool {
	return i.SchemaVersion == RunConfigSchemaVersion &&
		hasExecutionPath(i) &&
		i.State.RuntimeFamily == RKNNLiteRuntimeFamily &&
		i.State.Backend == RKNNLiteBackend &&
		i.State.HostContract != nil &&
		(i.Measurement.WorkloadMode == SaturatedSerialFixedBatchWorkloadMode ||
			i.Measurement.WorkloadMode == IdleModelLoadedWorkloadMode)
}

// IsSupportedFirstfloorRunConfig covers native firstfloor execution only;
// standalone ORT and RKNNLite measurements retain independent identities.
// Callers must validate the frozen config separately.
func IsSupportedFirstfloorRunConfig(i RunConfig) bool {
	return i.SchemaVersion == RunConfigSchemaVersion && hasExecutionPath(i) &&
		i.State.RuntimeFamily == FirstfloorRuntimeFamily &&
		(i.State.Backend == FirstfloorORTCPUBackend || i.State.Backend == FirstfloorORTCUDABackend ||
			i.State.Backend == FirstfloorRKNNLiteBackend) &&
		i.State.Firstfloor != nil &&
		(i.Measurement.WorkloadMode == SaturatedSerialFixedBatchWorkloadMode ||
			i.Measurement.WorkloadMode == IdleModelLoadedWorkloadMode)
}

func isRKNNExecution(i RunConfig) bool {
	return i.State.RuntimeFamily == RKNNLiteRuntimeFamily ||
		(i.State.RuntimeFamily == FirstfloorRuntimeFamily && i.State.Backend == FirstfloorRKNNLiteBackend)
}

func isCPUORTExecution(i RunConfig) bool {
	return hasExecutionPath(i) &&
		i.State.RuntimeFamily == cpuORTRuntimeFamily &&
		i.State.Backend == cpuExecutionProvider
}

func hasExecutionPath(i RunConfig) bool {
	return i.Candidate.ExecutionPath != "" &&
		strings.TrimSpace(i.Candidate.ExecutionPath) == i.Candidate.ExecutionPath
}

func supportedCPUORTRunConfigSchema(schemaVersion string) bool {
	return schemaVersion == RunConfigSchemaVersion
}

func supportedCPUORTIdleRunConfigSchema(schemaVersion string) bool {
	return schemaVersion == RunConfigSchemaVersion
}

func (i RunConfig) validateCPUContract() error {
	if i.CPU.ExclusivityClaimed {
		return errors.New("CPU quota policy must not claim Node exclusivity")
	}
	if i.CPU.PolicyVersion != CPUQuotaPolicyVersion {
		return errors.New("unsupported CPU quota policy version")
	}
	if len(i.CPU.AffinityCPUs) > 0 {
		if i.State.RuntimeFamily != FirstfloorRuntimeFamily {
			return errors.New("explicit CPU affinity requires firstfloor execution")
		}
		for index, cpu := range i.CPU.AffinityCPUs {
			if cpu < 0 || (index > 0 && cpu <= i.CPU.AffinityCPUs[index-1]) {
				return errors.New("CPU affinity IDs must be nonnegative, unique, and sorted")
			}
		}
		if len(i.CPU.AffinityCPUs) < i.CPU.ORTIntraOpThreads {
			return errors.New("CPU affinity mask is smaller than the selected ONNX Runtime thread count")
		}
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
	var threadCount int
	switch i.CPU.Policy {
	case AllocatableCPUQuotaPolicy:
		threadCount, err = strconv.Atoi(i.TargetNode.AllocatableCPU)
		if err != nil || threadCount < 1 {
			return errors.New("target allocatableCPU must be a positive integer")
		}
		if i.CPU.Limit != i.TargetNode.AllocatableCPU {
			return errors.New("CPU contract does not match the AllocatableCPUQuota policy")
		}
	case ConfiguredCPUQuotaPolicy:
		threadCount, err = strconv.Atoi(i.CPU.Limit)
		if err != nil || threadCount < 1 {
			return errors.New("configured CPU quota must be a positive whole-core count")
		}
		allocatableCPU, parseErr := resource.ParseQuantity(i.TargetNode.AllocatableCPU)
		if parseErr != nil || allocatableCPU.Sign() <= 0 {
			return errors.New("target allocatableCPU must be a positive Kubernetes quantity")
		}
		if limitCPU.Cmp(allocatableCPU) > 0 {
			return errors.New("configured CPU quota exceeds target allocatableCPU")
		}
	default:
		return errors.New("unsupported CPU quota policy")
	}
	if isRKNNExecution(i) {
		if i.CPU.ORTIntraOpThreads != 0 || i.CPU.ORTInterOpThreads != 0 {
			return errors.New("RKNNLite CPU contract cannot contain ONNX Runtime thread settings")
		}
		return nil
	}
	if i.CPU.ORTIntraOpThreads != threadCount || i.CPU.ORTInterOpThreads != 1 {
		return errors.New("CPU contract does not match the selected CPU quota policy")
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

func (i RunConfig) validateActuationContract() error {
	if i.State.PowerMode == FixedPowerMode {
		if i.Actuation != nil {
			return errors.New("fixed power mode cannot contain actuation")
		}
		return nil
	}
	if i.SchemaVersion != RunConfigSchemaVersion {
		return errors.New("actuated power mode requires the current RunConfig schema")
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

func (i RunConfig) CanonicalJSON() ([]byte, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	i.State.Firstfloor = modelcompat.CloneFirstfloorRuntimeSettings(i.State.Firstfloor)
	return json.Marshal(i)
}
