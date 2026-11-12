package profilingrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/lab-paper-code/chill/internal/modelcompat"
	"github.com/prometheus/common/model"
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
		"candidate.executionPath":    i.Candidate.ExecutionPath,
		"state.deviceClass":          i.State.DeviceClass,
		"state.model":                i.State.Model,
		"state.artifact":             i.State.Artifact,
		"state.runtimeFamily":        i.State.RuntimeFamily,
		"state.backend":              i.State.Backend,
		"state.powerMode":            i.State.PowerMode,
		"targetNode.name":            i.TargetNode.Name,
		"targetNode.uid":             i.TargetNode.UID,
		"targetNode.resourceVersion": i.TargetNode.ResourceVersion,
		"targetNode.architecture":    i.TargetNode.Architecture,
		"measurement.workloadMode":   i.Measurement.WorkloadMode,
		"metrics.sourceKind":         i.Metrics.SourceKind,
		"metrics.nodeName":           i.Metrics.NodeName,
		"metrics.endpoint":           i.Metrics.Endpoint,
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
	if i.Measurement.Batch < 1 || i.Measurement.WarmupIterations < 0 || i.Measurement.DurationSeconds < 1 {
		return errors.New("measurement batch, duration, and warm-up iterations must be valid")
	}
	if err := i.Measurement.ValidateDurationTolerance(); err != nil {
		return err
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

func (m MeasurementContract) ValidateDurationTolerance() error {
	if math.IsNaN(m.DurationToleranceSeconds) || math.IsInf(m.DurationToleranceSeconds, 0) ||
		m.DurationToleranceSeconds < 0 || m.DurationToleranceSeconds >= float64(m.DurationSeconds) {
		return errors.New("measurement duration tolerance must be finite, nonnegative and less than duration")
	}
	return nil
}

// MinimumServiceSamples is quantile coverage at the existing 95% confidence
// convention, not precision of a percentile estimate or a capacity proof.
func MinimumServiceSamples(percentile float64) (int, error) {
	if math.IsNaN(percentile) || math.IsInf(percentile, 0) || percentile <= 0 || percentile >= 100 {
		return 0, errors.New("Workload SLO percentile must be finite and in (0, 100) for finite sample coverage")
	}
	count := math.Ceil(math.Log(.05) / math.Log(percentile/100))
	if count > float64(int(^uint(0)>>1)) {
		return 0, errors.New("Workload SLO sample count exceeds the supported range")
	}
	return int(count), nil
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
		if (delivery.ArtifactCapability != nil && len(delivery.ArtifactCapability.Tensors) != 0) != isRKNNExecution(i) {
			return errors.New("firstfloor artifact input tensors are frozen for RKNN artifacts only")
		}
		if i.State.Backend == FirstfloorORTCUDABackend {
			if delivery.RuntimeAccess == nil || delivery.RuntimeAccess.GPUUUID == "" ||
				len(delivery.RuntimeAccess.Resources) != 1 || delivery.RuntimeAccess.Resources["nvidia.com/gpu"] != "1" {
				return errors.New("firstfloor ort-cuda requires an explicit GPU UUID and exactly one nvidia.com/gpu resource")
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
	if err := validateArtifactFiles(i.State.ArtifactFiles, isRKNNExecution(i)); err != nil {
		return err
	}
	if err := validatePreprocessorState(i.State.Preprocessor); err != nil {
		return err
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
			i.Measurement.Dataset != nil || i.Measurement.MinServiceSamples != 0 ||
			i.Measurement.SLOPercentile != 0 || i.Measurement.OperatingPoints != nil ||
			i.LoadOriginNode != "" || i.LoadOriginGatewayPod != nil {
			return errors.New("model-loaded idle measurement protocol is invalid")
		}
		return nil
	}
	if i.Measurement.SettlingDurationSeconds != 0 {
		return errors.New("active measurement cannot contain settlingDurationSeconds")
	}
	minimum, err := MinimumServiceSamples(i.Measurement.SLOPercentile)
	if err != nil {
		return err
	}
	if i.Measurement.MinServiceSamples != minimum || i.Measurement.DurationSeconds < 30 {
		return errors.New("active measurement requires at least 30 seconds and the derived native service sample count")
	}
	if !cleanValue(i.Measurement.WorkloadName) || !cleanValue(i.Measurement.WorkloadUID) ||
		i.Measurement.SLOTargetLatencyNanos <= 0 {
		return errors.New("active measurement requires the frozen source Workload identity and positive SLO target")
	}
	dataset := i.Measurement.Dataset
	if dataset == nil || !cleanValue(dataset.Name) || !cleanValue(dataset.UID) || !cleanValue(dataset.Image) ||
		!artifactPathPattern.MatchString(dataset.Path) || path.Clean(dataset.Path) == "/" {
		return errors.New("active measurement requires a frozen dataset name, UID, image, and absolute path")
	}
	if err := i.Measurement.OperatingPoints.Validate(); err != nil {
		return err
	}
	if i.Measurement.DurationSeconds != i.Measurement.OperatingPoints.PhaseDurationSeconds*(len(i.Measurement.OperatingPoints.Utilizations)+2) {
		return errors.New("active measurement duration must cover exactly its named phase duration floor")
	}
	if !cleanValue(i.LoadOriginNode) || i.LoadOriginNode == i.TargetNode.Name || i.LoadOriginGatewayPod == nil ||
		!cleanValue(i.LoadOriginGatewayPod.Namespace) || !cleanValue(i.LoadOriginGatewayPod.Name) || !cleanValue(i.LoadOriginGatewayPod.UID) {
		return errors.New("active measurement requires the frozen off-node serving gateway origin and Pod identity")
	}
	return nil
}

// validateArtifactFiles requires absolute model files whose names the
// runner can load, by the same rule the catalog applies.
func validateArtifactFiles(files []string, rknn bool) error {
	if len(files) == 0 {
		return errors.New("state.artifactFiles is required")
	}
	for _, file := range files {
		if !artifactPathPattern.MatchString(file) || path.Clean(file) == "/" {
			return errors.New("state.artifactFiles must be absolute container paths")
		}
	}
	if err := modelcompat.ValidateModelFileNames(files, rknn); err != nil {
		return fmt.Errorf("state.artifactFiles: %w", err)
	}
	return nil
}

func validatePreprocessorState(p PreprocessorState) error {
	if !cleanValue(p.Name) || !cleanValue(p.UID) || !cleanValue(p.Image) || !cleanValue(p.Architecture) ||
		!artifactPathPattern.MatchString(p.Path) || path.Clean(p.Path) == "/" || p.MaxNewTokens < 0 {
		return errors.New("state.preprocessor requires name, UID, image, architecture, and an absolute path")
	}
	for _, fields := range [][]string{p.Inputs, p.Outputs} {
		if len(fields) == 0 {
			return errors.New("state.preprocessor input and output field names are required")
		}
		for index, name := range fields {
			if !cleanValue(name) || slices.Contains(fields[:index], name) {
				return errors.New("state.preprocessor field names must be unique and nonempty")
			}
		}
	}
	return nil
}

func cleanValue(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}

// ModelFiles returns the sorted /model/<basename> paths the runtime loads.
func (s ExecutionState) ModelFiles() []string {
	files := make([]string, len(s.ArtifactFiles))
	for index, file := range s.ArtifactFiles {
		files[index] = path.Join("/model", path.Base(file))
	}
	slices.Sort(files)
	return files
}

// IsSupportedFirstfloorRunConfig reports whether the measurement worker can
// execute the RunConfig; firstfloor is the only measurement path.
// Callers must validate the frozen config separately.
func IsSupportedFirstfloorRunConfig(i RunConfig) bool {
	return i.SchemaVersion == RunConfigSchemaVersion && hasExecutionPath(i) &&
		i.State.RuntimeFamily == FirstfloorRuntimeFamily &&
		(i.State.Backend == FirstfloorORTCPUBackend || i.State.Backend == FirstfloorORTCUDABackend ||
			i.State.Backend == FirstfloorRKNNLiteBackend) &&
		i.State.Firstfloor != nil &&
		(i.Measurement.WorkloadMode == EmpiricalCadenceFixedBatchWorkloadMode ||
			i.Measurement.WorkloadMode == IdleModelLoadedWorkloadMode)
}

func isRKNNExecution(i RunConfig) bool {
	return i.State.RuntimeFamily == FirstfloorRuntimeFamily && i.State.Backend == FirstfloorRKNNLiteBackend
}

func hasExecutionPath(i RunConfig) bool {
	return i.Candidate.ExecutionPath != "" &&
		strings.TrimSpace(i.Candidate.ExecutionPath) == i.Candidate.ExecutionPath
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
