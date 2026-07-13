package profilingrun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	imagePattern  = regexp.MustCompile(`^[^@[:space:]]+@sha256:[0-9a-f]{64}$`)
)

func (i Intent) Validate() error {
	if i.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schemaVersion %q", i.SchemaVersion)
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
		"power.sourceKind":             i.Power.SourceKind,
		"power.nodeName":               i.Power.NodeName,
		"power.endpoint":               i.Power.Endpoint,
		"power.metric":                 i.Power.Metric,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	for name, value := range map[string]string{
		"images.runtime":       i.Images.Runtime,
		"images.artifact":      i.Images.Artifact,
		"images.powerObserver": i.Images.PowerObserver,
	} {
		if !imagePattern.MatchString(value) {
			return fmt.Errorf("%s must be repository@sha256", name)
		}
	}
	if i.Power.NodeName != i.TargetNode.Name {
		return errors.New("power nodeName must match target Node")
	}
	if i.CPU.ExclusivityClaimed {
		return errors.New("first CPU policy must not claim Node exclusivity")
	}
	if strings.TrimSpace(i.CPU.Request) == "" {
		return errors.New("CPU request is required")
	}
	cpu, err := strconv.Atoi(i.TargetNode.AllocatableCPU)
	if err != nil || cpu < 1 {
		return errors.New("target allocatableCPU must be a positive integer")
	}
	if i.CPU.Limit != i.TargetNode.AllocatableCPU || i.CPU.ORTIntraOpThreads != cpu || i.CPU.ORTInterOpThreads != 1 {
		return errors.New("CPU contract does not match the AllocatableCPUQuota policy")
	}
	if i.Measurement.Batch < 1 || i.Measurement.WarmupIterations < 0 || i.Measurement.DurationSeconds < 1 ||
		i.Measurement.Repetitions < 1 {
		return errors.New("measurement batch, duration, and repetitions must be positive")
	}
	if i.Power.IntervalMilliseconds < 1 || i.Power.TimeoutMilliseconds < 1 ||
		i.Power.TimeoutMilliseconds >= i.Power.IntervalMilliseconds ||
		i.Power.DurationSeconds < i.Measurement.DurationSeconds {
		return errors.New("invalid or under-covering power observation request")
	}
	return nil
}

func (i Intent) CanonicalJSON() ([]byte, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(i)
}

func (i Intent) Digest() (string, error) {
	payload, err := i.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
