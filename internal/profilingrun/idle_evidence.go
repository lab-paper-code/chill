package profilingrun

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	IdleRuntimeResultSchemaVersionV1Alpha1 = "chill.dacs.io/model-loaded-idle-runtime.v1alpha1"
	IdleRuntimeResultSchemaVersion         = "chill.dacs.io/model-loaded-idle-runtime.v1alpha2"
)

// IdleRuntimeResult contains runtime-owned facts for one model-loaded idle
// window. Observer results never determine these timestamps.
type IdleRuntimeResult struct {
	SchemaVersion                string               `json:"schemaVersion"`
	Status                       string               `json:"status"`
	IntentDigest                 string               `json:"intentDigest"`
	PodUID                       string               `json:"podUID"`
	NodeName                     string               `json:"nodeName"`
	Model                        string               `json:"model"`
	ArtifactDigest               string               `json:"artifactDigest"`
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
func ValidateIdleRuntime(intent Intent, podUID string, result IdleRuntimeResult) error {
	if result.SchemaVersion != IdleRuntimeResultSchemaVersion &&
		result.SchemaVersion != IdleRuntimeResultSchemaVersionV1Alpha1 {
		return fmt.Errorf("unsupported idle runtime result schemaVersion %q", result.SchemaVersion)
	}
	if err := intent.Validate(); err != nil {
		return fmt.Errorf("validate idle Run intent: %w", err)
	}
	if intent.Measurement.WorkloadMode != IdleModelLoadedWorkloadMode ||
		(!IsSupportedCPUORTIntent(intent) && !IsSupportedRKNNLiteIntent(intent)) {
		return errors.New("idle runtime requires a supported model-loaded idle intent")
	}
	if result.SchemaVersion == IdleRuntimeResultSchemaVersionV1Alpha1 && !IsSupportedCPUORTIntent(intent) {
		return errors.New("v1alpha1 idle runtime result is CPU ONNX Runtime only")
	}
	if result.SchemaVersion == IdleRuntimeResultSchemaVersionV1Alpha1 &&
		(result.Backend != "" || result.DerivedContract.Backend != "") {
		return errors.New("v1alpha1 idle runtime result cannot contain v1alpha2 backend fields")
	}
	digest, err := intent.Digest()
	if err != nil {
		return err
	}
	backend := result.Backend
	if result.SchemaVersion == IdleRuntimeResultSchemaVersionV1Alpha1 {
		backend = result.Provider
	}
	checks := []struct{ name, want, got string }{
		{"status", "Succeeded", result.Status},
		{"intent digest", digest, result.IntentDigest},
		{"Pod UID", podUID, result.PodUID},
		{"Node", intent.TargetNode.Name, result.NodeName},
		{"model", intent.State.Model, result.Model},
		{"artifact digest", intent.State.ArtifactDigest, result.ArtifactDigest},
		{"runtime family", intent.State.RuntimeFamily, result.Runtime},
		{"backend", intent.State.Backend, backend},
	}
	for _, check := range checks {
		if check.want != check.got {
			return fmt.Errorf("%s mismatch: wanted %q, got %q", check.name, check.want, check.got)
		}
	}
	if normalizeArchitecture(result.Architecture) != normalizeArchitecture(intent.TargetNode.Architecture) {
		return errors.New("idle runtime architecture does not match the frozen Node")
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
	minimumSettling := time.Duration(intent.Measurement.SettlingDurationSeconds) * time.Second
	if settlingCompleted.Sub(settlingStarted) < minimumSettling {
		return errors.New("idle settling duration is shorter than the frozen contract")
	}
	minimumElapsed := float64(intent.Measurement.DurationSeconds) - 0.5
	if result.MeasurementElapsedSeconds < minimumElapsed ||
		measurementEnded.Sub(measurementStarted) < time.Duration(minimumElapsed*float64(time.Second)) {
		return errors.New("idle measurement duration is shorter than the frozen contract")
	}
	if result.InferenceCountAfterModelLoad != 0 {
		return errors.New("idle runtime issued inference after model load")
	}
	if !result.ModelResidentAtEnd {
		return errors.New("idle runtime did not retain the model through the window")
	}
	if err := validateIdleRuntimePathEvidence(intent, result); err != nil {
		return err
	}
	if err := validateCPUQuota(intent.CPU.Limit, result.CPUContract.CgroupCPUMax); err != nil {
		return err
	}
	if result.DerivedContract.RunIntentDigest != digest ||
		result.DerivedContract.NodeSnapshot.UID != intent.TargetNode.UID {
		return errors.New("idle derived contract identity mismatch")
	}
	derivedBackend := result.DerivedContract.Backend
	if result.SchemaVersion == IdleRuntimeResultSchemaVersionV1Alpha1 {
		derivedBackend = result.DerivedContract.Provider
	}
	if derivedBackend != intent.State.Backend {
		return errors.New("idle derived contract backend mismatch")
	}
	return nil
}

func validateIdleRuntimePathEvidence(intent Intent, result IdleRuntimeResult) error {
	if strings.TrimSpace(result.CPUContract.CPUSetEffective) == "" || result.CPUContract.AffinityCPUCount < 1 {
		return errors.New("idle runtime CPU evidence is incomplete")
	}
	if IsSupportedCPUORTIntent(intent) {
		if result.RKNN != nil || result.Provider != intent.State.Backend ||
			!contains(result.AvailableProviders, intent.State.Backend) ||
			(result.SchemaVersion == IdleRuntimeResultSchemaVersion &&
				result.DerivedContract.Provider != intent.State.Backend) ||
			result.CPUContract.ORTIntraOpThreads != intent.CPU.ORTIntraOpThreads ||
			result.CPUContract.ORTInterOpThreads != intent.CPU.ORTInterOpThreads ||
			result.CPUContract.AffinityCPUCount < intent.CPU.ORTIntraOpThreads {
			return errors.New("idle CPU ONNX Runtime evidence mismatch")
		}
		return nil
	}
	if !IsSupportedRKNNLiteIntent(intent) || result.SchemaVersion != IdleRuntimeResultSchemaVersion ||
		result.Provider != "" || result.DerivedContract.Provider != "" || len(result.AvailableProviders) != 0 {
		return errors.New("idle runtime result does not match an explicit supported path")
	}
	return validateRKNNRuntimeEvidence(intent, result.RKNN)
}
