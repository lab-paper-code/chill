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
	if i.SchemaVersion == SchemaVersion || i.SchemaVersion == previousCurrentIntentVersion ||
		i.SchemaVersion == allowedBackgroundIntentVersion {
		if !imagePattern.MatchString(i.Images.WorkloadObserver) {
			return errors.New("images.workloadObserver must be repository@sha256")
		}
		if err := i.Workload.Validate(); err != nil {
			return fmt.Errorf("validate workload contract: %w", err)
		}
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
	switch i.SchemaVersion {
	case SchemaVersion:
		if !validArtifactPath(i.State.ArtifactPath) {
			return errors.New("state.artifactPath must be a clean absolute container path")
		}
		if i.Workload.SchemaVersion != WorkloadContractSchemaVersion || i.Workload.AllowedBackground != nil {
			return errors.New("current intent requires the current workload observation contract")
		}
	case previousCurrentIntentVersion:
		if !validArtifactPath(i.State.ArtifactPath) {
			return errors.New("state.artifactPath must be a clean absolute container path")
		}
		if i.Workload.SchemaVersion != WorkloadContractSchemaVersion || i.Workload.AllowedBackground != nil {
			return errors.New("retained v1alpha5 intent requires the current workload observation contract")
		}
		if i.Measurement.SettlingDurationSeconds != 0 {
			return errors.New("retained v1alpha5 intent cannot contain settlingDurationSeconds")
		}
	case allowedBackgroundIntentVersion:
		if !validArtifactPath(i.State.ArtifactPath) {
			return errors.New("state.artifactPath must be a clean absolute container path")
		}
		if i.Images.WorkloadObserver == "" || i.Workload.SchemaVersion != "" || i.Workload.AllowedBackground == nil {
			return errors.New("retained v1alpha4 intent requires its workload observation fields")
		}
		if err := i.Workload.Validate(); err != nil {
			return fmt.Errorf("validate retained workload contract: %w", err)
		}
	case workloadFreeIntentSchemaVersion:
		if !validArtifactPath(i.State.ArtifactPath) {
			return errors.New("state.artifactPath must be a clean absolute container path")
		}
		if i.Images.WorkloadObserver != "" || i.Workload.SchemaVersion != "" || i.Workload.AllowedBackground != nil {
			return errors.New("retained workload-free intent must not contain workload observation fields")
		}
	case previousIntentSchemaVersion:
		if !validArtifactPath(i.State.ArtifactPath) {
			return errors.New("state.artifactPath must be a clean absolute container path")
		}
		if hasWorkloadObservationFields(i) {
			return errors.New("retained intent must not contain workload observation fields")
		}
	case legacyIntentSchemaVersion:
		if i.State.ArtifactPath != "" {
			return errors.New("legacy intent must not contain state.artifactPath")
		}
		if hasWorkloadObservationFields(i) {
			return errors.New("legacy intent must not contain workload observation fields")
		}
	default:
		return fmt.Errorf("unsupported schemaVersion %q", i.SchemaVersion)
	}
	return nil
}

func (i Intent) validateMeasurementMode() error {
	if i.SchemaVersion != SchemaVersion {
		if i.Measurement.SettlingDurationSeconds != 0 {
			return errors.New("retained intent cannot contain settlingDurationSeconds")
		}
		return nil
	}
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
// family can execute a newly frozen or retained intent. Callers must validate
// the intent separately.
func IsSupportedActiveIntent(i Intent) bool {
	return (i.SchemaVersion == SchemaVersion || i.SchemaVersion == previousCurrentIntentVersion) &&
		i.Measurement.WorkloadMode != IdleModelLoadedWorkloadMode
}

func hasWorkloadObservationFields(intent Intent) bool {
	return intent.Images.WorkloadObserver != "" || intent.Workload.SchemaVersion != "" ||
		intent.Workload.AllowedBackground != nil
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

// MarshalJSON preserves the exact retained wire shape of older immutable
// intents while emitting only the current component name for new intents.
func (i Intent) MarshalJSON() ([]byte, error) {
	if i.SchemaVersion == legacyIntentSchemaVersion || i.SchemaVersion == previousIntentSchemaVersion {
		return json.Marshal(struct {
			SchemaVersion string               `json:"schemaVersion"`
			Candidate     CandidateIdentity    `json:"candidate"`
			State         ExecutionState       `json:"state"`
			Images        legacyImages         `json:"images"`
			TargetNode    NodeSnapshot         `json:"targetNode"`
			CPU           CPUContract          `json:"cpu"`
			Measurement   MeasurementContract  `json:"measurement"`
			Metrics       legacyMetricContract `json:"power"`
		}{
			i.SchemaVersion, i.Candidate, i.State,
			legacyImages{i.Images.Runtime, i.Images.Artifact, i.Images.MetricObserver},
			i.TargetNode, i.CPU, i.Measurement, legacyMetricContractFromCurrent(i.Metrics),
		})
	}
	if i.SchemaVersion == workloadFreeIntentSchemaVersion {
		return json.Marshal(struct {
			SchemaVersion string              `json:"schemaVersion"`
			Candidate     CandidateIdentity   `json:"candidate"`
			State         ExecutionState      `json:"state"`
			Images        workloadFreeImages  `json:"images"`
			TargetNode    NodeSnapshot        `json:"targetNode"`
			CPU           CPUContract         `json:"cpu"`
			Measurement   MeasurementContract `json:"measurement"`
			Metrics       MetricContract      `json:"metrics"`
		}{
			i.SchemaVersion, i.Candidate, i.State,
			workloadFreeImages{i.Images.Runtime, i.Images.Artifact, i.Images.MetricObserver},
			i.TargetNode, i.CPU, i.Measurement, i.Metrics,
		})
	}
	type wire Intent
	return json.Marshal(wire(i))
}

type legacyImages struct {
	Runtime  string `json:"runtime"`
	Artifact string `json:"artifact"`
	Observer string `json:"powerObserver"`
}

type workloadFreeImages struct {
	Runtime        string `json:"runtime"`
	Artifact       string `json:"artifact"`
	MetricObserver string `json:"metricObserver"`
}

func (i Intent) Digest() (string, error) {
	payload, err := i.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
