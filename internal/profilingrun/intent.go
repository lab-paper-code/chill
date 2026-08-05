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
	if i.SchemaVersion != SchemaVersion && i.SchemaVersion != previousIntentSchemaVersion {
		return fmt.Errorf("unsupported schemaVersion %q", i.SchemaVersion)
	}
	if i.SchemaVersion == previousIntentSchemaVersion && i.State.HostContractDigest != "" {
		return errors.New("v1alpha6 intent cannot contain state.hostContractDigest")
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
		if i.Measurement.Batch != 1 || i.Measurement.WarmupIterations != 0 ||
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
// family can execute a current intent. Callers must validate the intent separately.
func IsSupportedActiveIntent(i Intent) bool {
	return (i.SchemaVersion == SchemaVersion || i.SchemaVersion == previousIntentSchemaVersion) &&
		i.Measurement.WorkloadMode != IdleModelLoadedWorkloadMode
}

// IsSupportedCPUORTIntent covers the active and model-loaded-idle paths
// implemented by the explicit CPU ORT worker.
func IsSupportedCPUORTIntent(i Intent) bool {
	return IsSupportedActiveIntent(i) ||
		((i.SchemaVersion == SchemaVersion || i.SchemaVersion == previousIntentSchemaVersion) &&
			i.Measurement.WorkloadMode == IdleModelLoadedWorkloadMode)
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
	if i.CPU.Limit != i.TargetNode.AllocatableCPU || i.CPU.ORTIntraOpThreads != cpu || i.CPU.ORTInterOpThreads != 1 {
		return errors.New("CPU contract does not match the AllocatableCPUQuota policy")
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
