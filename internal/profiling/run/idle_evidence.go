package profilingrun

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	IdleRuntimeResultSchemaVersion = "chill.dacs.io/model-loaded-idle-runtime.v1alpha3"
)

// IdleRuntimeResult contains runtime-owned facts for one model-loaded idle
// window. Observer results never determine these timestamps.
type IdleRuntimeResult struct {
	SchemaVersion                string               `json:"schemaVersion"`
	Status                       string               `json:"status"`
	PodUID                       string               `json:"podUID"`
	NodeName                     string               `json:"nodeName"`
	Model                        string               `json:"model"`
	Architecture                 string               `json:"architecture"`
	Runtime                      string               `json:"runtime"`
	RuntimeVersion               string               `json:"runtimeVersion"`
	Backend                      string               `json:"backend,omitempty"`
	Provider                     string               `json:"provider,omitempty"`
	AvailableProviders           []string             `json:"availableProviders,omitempty"`
	RKNN                         *RKNNRuntimeEvidence `json:"rknn,omitempty"`
	ModelLoadCompletedAt         string               `json:"modelLoadCompletedAt"`
	SettlingStartedAt            string               `json:"settlingStartedAt"`
	SettlingCompletedAt          string               `json:"settlingCompletedAt"`
	MeasurementStartedAt         string               `json:"measurementStartedAt"`
	MeasurementEndedAt           string               `json:"measurementEndedAt"`
	MeasurementElapsedSeconds    float64              `json:"measurementElapsedSecondsMonotonic"`
	InferenceCountAfterModelLoad int                  `json:"inferenceCountAfterModelLoad"`
	ModelResidentAtEnd           bool                 `json:"modelResidentAtEnd"`
	CPUContract                  RuntimeCPU           `json:"cpuContract"`
	DerivedContract              DerivedContract      `json:"derivedExecutionContract"`
}

func (r IdleRuntimeResult) MeasurementWindow() (time.Time, time.Time, error) {
	started, err := time.Parse(time.RFC3339Nano, r.MeasurementStartedAt)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("idle measurement start timestamp is invalid")
	}
	ended, err := time.Parse(time.RFC3339Nano, r.MeasurementEndedAt)
	if err != nil || !ended.After(started) {
		return time.Time{}, time.Time{}, errors.New("idle measurement end timestamp is invalid")
	}
	return started, ended, nil
}

//nolint:gocyclo // Idle validation deliberately exposes every model-residency and timing gate.
func ValidateIdleRuntime(config RunConfig, podUID string, result IdleRuntimeResult) error {
	if result.SchemaVersion != IdleRuntimeResultSchemaVersion {
		return fmt.Errorf("unsupported idle runtime result schemaVersion %q", result.SchemaVersion)
	}
	return validateBoundIdleRuntime(config, podUID, result)
}

//nolint:gocyclo // Idle validation deliberately exposes every model-residency and timing gate.
func validateBoundIdleRuntime(config RunConfig, podUID string, result IdleRuntimeResult) error {
	if config.Measurement.WorkloadMode != IdleModelLoadedWorkloadMode ||
		(!IsSupportedCPUORTRunConfig(config) && !IsSupportedRKNNLiteRunConfig(config)) {
		return errors.New("idle runtime requires a supported model-loaded idle RunConfig")
	}
	checks := []struct{ name, want, got string }{
		{"status", "Succeeded", result.Status},
		{"Pod UID", podUID, result.PodUID},
		{"Node", config.TargetNode.Name, result.NodeName},
		{"model", config.State.Model, result.Model},
		{"runtime family", config.State.RuntimeFamily, result.Runtime},
		{"backend", config.State.Backend, result.Backend},
	}
	for _, check := range checks {
		if check.want != check.got {
			return fmt.Errorf("%s mismatch: wanted %q, got %q", check.name, check.want, check.got)
		}
	}
	if normalizeArchitecture(result.Architecture) != normalizeArchitecture(config.TargetNode.Architecture) {
		return errors.New("idle runtime architecture does not match the RunConfig Node")
	}
	if strings.TrimSpace(result.RuntimeVersion) == "" {
		return errors.New("idle runtime identity is incomplete")
	}
	loaded, loadErr := time.Parse(time.RFC3339Nano, result.ModelLoadCompletedAt)
	settlingStarted, settleStartErr := time.Parse(time.RFC3339Nano, result.SettlingStartedAt)
	settlingCompleted, settleEndErr := time.Parse(time.RFC3339Nano, result.SettlingCompletedAt)
	measurementStarted, measurementEnded, windowErr := result.MeasurementWindow()
	if loadErr != nil || settleStartErr != nil || settleEndErr != nil || windowErr != nil ||
		loaded.After(settlingStarted) || !settlingCompleted.After(settlingStarted) ||
		settlingCompleted.After(measurementStarted) {
		return errors.New("idle model-load, settling, or measurement timestamp order is invalid")
	}
	minimumSettling := time.Duration(config.Measurement.SettlingDurationSeconds) * time.Second
	if settlingCompleted.Sub(settlingStarted) < minimumSettling {
		return errors.New("idle settling duration is shorter than the RunConfig contract")
	}
	minimumElapsed := float64(config.Measurement.DurationSeconds) - 0.5
	if result.MeasurementElapsedSeconds < minimumElapsed ||
		measurementEnded.Sub(measurementStarted) < time.Duration(minimumElapsed*float64(time.Second)) {
		return errors.New("idle measurement duration is shorter than the RunConfig contract")
	}
	if result.InferenceCountAfterModelLoad != 0 {
		return errors.New("idle runtime issued inference after model load")
	}
	if !result.ModelResidentAtEnd {
		return errors.New("idle runtime did not retain the model through the window")
	}
	if err := validateIdleRuntimePathEvidence(config, result); err != nil {
		return err
	}
	if err := validateCPUQuota(config.CPU.Limit, result.CPUContract.CgroupCPUMax); err != nil {
		return err
	}
	if result.DerivedContract.NodeSnapshot.UID != config.TargetNode.UID {
		return errors.New("idle derived contract identity mismatch")
	}
	if result.DerivedContract.Backend != config.State.Backend {
		return errors.New("idle derived contract backend mismatch")
	}
	return nil
}

func validateIdleRuntimePathEvidence(config RunConfig, result IdleRuntimeResult) error {
	if strings.TrimSpace(result.CPUContract.CPUSetEffective) == "" || result.CPUContract.AffinityCPUCount < 1 {
		return errors.New("idle runtime CPU evidence is incomplete")
	}
	if IsSupportedCPUORTRunConfig(config) {
		if result.RKNN != nil || result.Provider != config.State.Backend ||
			!contains(result.AvailableProviders, config.State.Backend) ||
			(result.SchemaVersion == IdleRuntimeResultSchemaVersion &&
				result.DerivedContract.Provider != config.State.Backend) ||
			result.CPUContract.ORTIntraOpThreads != config.CPU.ORTIntraOpThreads ||
			result.CPUContract.ORTInterOpThreads != config.CPU.ORTInterOpThreads ||
			result.CPUContract.AffinityCPUCount < config.CPU.ORTIntraOpThreads {
			return errors.New("idle CPU ONNX Runtime evidence mismatch")
		}
		return nil
	}
	if !IsSupportedRKNNLiteRunConfig(config) || result.SchemaVersion != IdleRuntimeResultSchemaVersion ||
		result.Provider != "" || result.DerivedContract.Provider != "" || len(result.AvailableProviders) != 0 {
		return errors.New("idle runtime result does not match an explicit supported path")
	}
	return validateRKNNRuntimeEvidence(config, result.RKNN)
}
