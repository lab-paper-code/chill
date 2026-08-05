package profilingrun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

var (
	digestPattern       = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	imagePattern        = regexp.MustCompile(`^[^@[:space:]]+@sha256:[0-9a-f]{64}$`)
	artifactPathPattern = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)
)

func (i Intent) Validate() error {
	if err := i.validateSchema(); err != nil {
		return err
	}

	for name, value := range map[string]string{
		"candidate.reportDigest":             i.Candidate.ReportDigest,
		"candidate.modelSpecContentDigest":   i.Candidate.ModelSpecContentDigest,
		"candidate.deviceClassContentDigest": i.Candidate.DeviceClassContentDigest,
		"candidate.runtimeDeclarationDigest": i.Candidate.RuntimeDeclarationDigest,
		"state.artifactDigest":               i.State.ArtifactDigest,
	} {
		if !digestPattern.MatchString(value) {
			return fmt.Errorf("%s must be a canonical SHA-256 digest", name)
		}
	}
	if i.Metrics.ConfigurationContentDigest != "" && !digestPattern.MatchString(i.Metrics.ConfigurationContentDigest) {
		return errors.New("metrics.configurationContentDigest must be a canonical SHA-256 digest")
	}
	if i.State.HostContractDigest != "" && !digestPattern.MatchString(i.State.HostContractDigest) {
		return errors.New("state.hostContractDigest must be a canonical SHA-256 digest")
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
		"metrics.powerMetric":          i.Metrics.PowerMetric,
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
		if !imagePattern.MatchString(value) {
			return fmt.Errorf("%s must be repository@sha256", name)
		}
	}
	if !imagePattern.MatchString(i.Images.WorkloadObserver) {
		return errors.New("images.workloadObserver must be repository@sha256")
	}
	if err := i.Workload.Validate(); err != nil {
		return fmt.Errorf("validate workload contract: %w", err)
	}
	if i.Metrics.NodeName != i.TargetNode.Name {
		return errors.New("metrics nodeName must match target Node")
	}
	if err := i.validateCPUContract(); err != nil {
		return err
	}
	if i.Measurement.Batch < 1 || i.Measurement.WarmupIterations < 0 || i.Measurement.DurationSeconds < 1 {
		return errors.New("measurement batch, duration, and warm-up iterations must be valid")
	}
	if i.Measurement.Repetitions != 1 {
		return errors.New("first profiling path requires exactly one measurement repetition")
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

func (i Intent) validateSchema() error {
	if i.SchemaVersion != SchemaVersion &&
		i.SchemaVersion != intentSchemaVersionV1Alpha8 &&
		i.SchemaVersion != intentSchemaVersionV1Alpha7 &&
		i.SchemaVersion != intentSchemaVersionV1Alpha6 {
		return fmt.Errorf("unsupported schemaVersion %q", i.SchemaVersion)
	}
	if i.SchemaVersion == intentSchemaVersionV1Alpha6 && i.State.HostContractDigest != "" {
		return errors.New("v1alpha6 intent cannot contain state.hostContractDigest")
	}
	if i.SchemaVersion == intentSchemaVersionV1Alpha6 &&
		i.Measurement.WorkloadMode == IdleModelLoadedWorkloadMode {
		return errors.New("v1alpha6 intent cannot contain model-loaded idle measurement")
	}
	if i.SchemaVersion != SchemaVersion && i.Actuation != nil {
		return errors.New("retained intent schema cannot contain actuation")
	}
	if i.SchemaVersion != SchemaVersion && i.State.PowerMode != FixedPowerMode {
		return errors.New("retained intent schema cannot select an actuated power mode")
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

// IsSupportedActiveIntent reports whether the first CPU ORT active worker
// family can execute a current or retained intent. Callers must validate the
// intent separately.
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
	return (i.SchemaVersion == SchemaVersion || i.SchemaVersion == intentSchemaVersionV1Alpha8) &&
		hasExecutionPath(i) &&
		i.State.RuntimeFamily == RKNNLiteRuntimeFamily &&
		i.State.Backend == RKNNLiteBackend &&
		i.State.HostContractDigest != "" &&
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
	return schemaVersion == SchemaVersion ||
		schemaVersion == intentSchemaVersionV1Alpha8 ||
		schemaVersion == intentSchemaVersionV1Alpha7 ||
		schemaVersion == intentSchemaVersionV1Alpha6
}

func supportedCPUORTIdleIntentSchema(schemaVersion string) bool {
	return schemaVersion == SchemaVersion ||
		schemaVersion == intentSchemaVersionV1Alpha8 ||
		schemaVersion == intentSchemaVersionV1Alpha7
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
		if i.SchemaVersion != SchemaVersion && i.SchemaVersion != intentSchemaVersionV1Alpha8 {
			return errors.New("RKNNLite requires current or retained v1alpha8 intent schema")
		}
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
	if !digestPattern.MatchString(i.Actuation.PolicyDigest) {
		return errors.New("actuation.policyDigest must be a canonical SHA-256 digest")
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

// MarshalJSON fixes the canonical field order used by intent digests.
func (i Intent) MarshalJSON() ([]byte, error) {
	type wire Intent
	return json.Marshal(wire(i))
}

func (i Intent) Digest() (string, error) {
	payload, err := i.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
