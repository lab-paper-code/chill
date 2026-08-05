package profilingrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	RuntimeResultSchemaVersionV1Alpha1 = "chill.dacs.io/batch-characterization.v1alpha1"
	RuntimeResultSchemaVersion         = "chill.dacs.io/batch-characterization.v1alpha2"

	RKNNLiteCoreMask      = "NPU_CORE_0"
	RKNNLiteInputLayout   = "NHWC"
	RKNNLiteInputDataType = "float32"
	runtimeAArch64        = "aarch64"
)

type RuntimeResult struct {
	SchemaVersion             string               `json:"schemaVersion"`
	Status                    string               `json:"status"`
	SweepID                   string               `json:"sweepId"`
	ExperimentID              string               `json:"experimentId"`
	NodeName                  string               `json:"nodeName"`
	Model                     string               `json:"model"`
	ArtifactDigest            string               `json:"artifactDigest"`
	Architecture              string               `json:"architecture"`
	Runtime                   string               `json:"runtime"`
	RuntimeVersion            string               `json:"runtimeVersion"`
	Backend                   string               `json:"backend,omitempty"`
	Provider                  string               `json:"provider,omitempty"`
	AvailableProviders        []string             `json:"availableProviders,omitempty"`
	RKNN                      *RKNNRuntimeEvidence `json:"rknn,omitempty"`
	BatchSize                 int                  `json:"batchSize"`
	WarmupIterations          int                  `json:"warmupIterations"`
	WarmupCompletedAt         string               `json:"warmupCompletedAt"`
	InferenceCount            int                  `json:"inferenceCount"`
	MeasurementMode           string               `json:"measurementMode"`
	Repetitions               int                  `json:"repetitions"`
	MeasurementStartedAt      string               `json:"measurementStartedAt"`
	MeasurementEndedAt        string               `json:"measurementEndedAt"`
	MeasurementElapsedSeconds float64              `json:"measurementElapsedSecondsMonotonic"`
	OutputValidation          OutputValidation     `json:"outputValidation"`
	CPUContract               RuntimeCPU           `json:"cpuContract"`
	DerivedContract           DerivedContract      `json:"derivedExecutionContract"`
	RepetitionResults         []Repetition         `json:"repetitionResults"`
	CompletionIndex           int                  `json:"completionIndex"`
	InputShape                []int                `json:"inputShape"`
	MeasurementIterations     int                  `json:"measurementIterations"`
	TargetDurationSeconds     *float64             `json:"targetDurationSecondsPerRepetition"`
	Latency                   map[string]float64   `json:"latencyMs"`
	ThroughputItemsPerSecond  float64              `json:"throughputItemsPerSecond"`
	Power                     json.RawMessage      `json:"power"`
	EnergyPerRequest          json.RawMessage      `json:"energyPerRequest"`
	BSat                      json.RawMessage      `json:"bSat"`
}

type OutputValidation struct {
	Method       string  `json:"method"`
	Passed       bool    `json:"passed"`
	OutputShapes [][]int `json:"outputShapes"`
}
type RuntimeCPU struct {
	OSCPUCount         int            `json:"osCPUCount"`
	CgroupCPUMax       string         `json:"cgroupCPUMax"`
	ORTIntraOpThreads  int            `json:"ortIntraOpThreads,omitempty"`
	ORTInterOpThreads  int            `json:"ortInterOpThreads,omitempty"`
	AffinityCPUCount   int            `json:"affinityCPUCount"`
	AffinityCPUs       []int          `json:"affinityCPUs"`
	CPUSetEffective    string         `json:"cpusetCPUsEffective"`
	ProcessCPUSeconds  float64        `json:"processCPUSeconds"`
	CgroupCPUStatDelta map[string]int `json:"cgroupCPUStatDelta"`
}
type DerivedContract struct {
	SchemaVersion   string          `json:"schemaVersion"`
	RunIntentDigest string          `json:"runIntentDigest"`
	Backend         string          `json:"backend,omitempty"`
	Provider        string          `json:"provider,omitempty"`
	NodeSnapshot    NodeSnapshot    `json:"nodeSnapshot"`
	CPU             json.RawMessage `json:"cpu"`
	RuntimeOptions  json.RawMessage `json:"runtimeOptions"`
	Scheduling      json.RawMessage `json:"scheduling"`
}

type RKNNRuntimeEvidence struct {
	Package       RKNNPackageObservation       `json:"package"`
	HostLibraries []RKNNHostLibraryObservation `json:"hostLibraries"`
	Driver        RKNNDriverObservation        `json:"driver"`
	Platform      RKNNPlatformObservation      `json:"platform"`
	Devices       []RKNNDeviceObservation      `json:"devices"`
	CoreMask      string                       `json:"coreMask"`
	InputLayout   string                       `json:"inputLayout"`
	InitSucceeded bool                         `json:"initSucceeded"`
	LoadSucceeded bool                         `json:"loadSucceeded"`
	SelectedBatch int                          `json:"selectedBatch"`
}

type RKNNPackageObservation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type RKNNHostLibraryObservation struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type RKNNDriverObservation struct {
	Name        string `json:"name"`
	VersionPath string `json:"versionPath"`
	Version     string `json:"version"`
}

type RKNNPlatformObservation struct {
	CompatiblePath  string `json:"compatiblePath"`
	CompatibleToken string `json:"compatibleToken"`
	Matched         bool   `json:"matched"`
}

type RKNNDeviceObservation struct {
	Path            string `json:"path"`
	CharacterDevice bool   `json:"characterDevice"`
	Opened          bool   `json:"opened"`
}
type Repetition struct {
	Repetition           int       `json:"repetition"`
	MeasurementStartedAt string    `json:"measurementStartedAt"`
	MeasurementEndedAt   string    `json:"measurementEndedAt"`
	LatencyMilliseconds  []float64 `json:"latencyMs"`
}

// MeasurementWindow returns the runtime's wall-clock steady-measurement
// interval. Scientific coverage policy remains outside RuntimeResult.
func (r RuntimeResult) MeasurementWindow() (time.Time, time.Time, error) {
	started, err := time.Parse(time.RFC3339Nano, r.MeasurementStartedAt)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("measurement start timestamp is invalid")
	}
	ended, err := time.Parse(time.RFC3339Nano, r.MeasurementEndedAt)
	if err != nil || !ended.After(started) {
		return time.Time{}, time.Time{}, errors.New("measurement end timestamp is invalid")
	}
	return started, ended, nil
}

//nolint:gocyclo // Runtime validation is an explicit ordered checklist over one immutable evidence record.
func ValidateRuntime(intent Intent, podUID string, result RuntimeResult) error {
	if result.SchemaVersion != RuntimeResultSchemaVersion &&
		result.SchemaVersion != RuntimeResultSchemaVersionV1Alpha1 {
		return fmt.Errorf("unsupported runtime result schemaVersion %q", result.SchemaVersion)
	}
	if result.SchemaVersion == RuntimeResultSchemaVersionV1Alpha1 && !IsSupportedCPUORTIntent(intent) {
		return errors.New("v1alpha1 runtime result is CPU ONNX Runtime only")
	}
	if result.SchemaVersion == RuntimeResultSchemaVersionV1Alpha1 &&
		(result.Backend != "" || result.DerivedContract.Backend != "") {
		return errors.New("v1alpha1 runtime result cannot contain v1alpha2 backend fields")
	}
	digest, err := intent.Digest()
	if err != nil {
		return err
	}
	backend := result.Backend
	if result.SchemaVersion == RuntimeResultSchemaVersionV1Alpha1 {
		backend = result.Provider
	}
	checks := []struct{ name, want, got string }{
		{"status", "Succeeded", result.Status},
		{"run intent digest", digest, result.SweepID},
		{"Pod UID", podUID, result.ExperimentID},
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
		return errors.New("runtime architecture does not match the frozen Node")
	}
	if result.BatchSize != intent.Measurement.Batch || result.WarmupIterations != intent.Measurement.WarmupIterations ||
		result.Repetitions != intent.Measurement.Repetitions {
		return errors.New("measurement protocol mismatch")
	}
	minimumElapsed := float64(intent.Measurement.DurationSeconds*intent.Measurement.Repetitions) - 0.5
	if result.MeasurementMode != "duration" || result.InferenceCount < 1 ||
		result.MeasurementElapsedSeconds < minimumElapsed ||
		len(result.RepetitionResults) != intent.Measurement.Repetitions {
		return errors.New("runtime measurement evidence is incomplete")
	}
	if result.TargetDurationSeconds == nil ||
		*result.TargetDurationSeconds != float64(intent.Measurement.DurationSeconds) {
		return errors.New("runtime target duration does not match the measurement contract")
	}
	if !result.OutputValidation.Passed || result.OutputValidation.Method != intent.Measurement.OutputValidation ||
		len(result.OutputValidation.OutputShapes) == 0 {
		return errors.New("output validation evidence mismatch")
	}
	for _, shape := range result.OutputValidation.OutputShapes {
		if len(shape) == 0 || shape[0] != intent.Measurement.Batch {
			return errors.New("output shape batch mismatch")
		}
	}
	if err := validateRuntimePathEvidence(intent, result); err != nil {
		return err
	}
	if err := validateCPUQuota(intent.CPU.Limit, result.CPUContract.CgroupCPUMax); err != nil {
		return err
	}
	if result.DerivedContract.RunIntentDigest != digest ||
		result.DerivedContract.NodeSnapshot.UID != intent.TargetNode.UID {
		return errors.New("derived contract identity mismatch")
	}
	derivedBackend := result.DerivedContract.Backend
	if result.SchemaVersion == RuntimeResultSchemaVersionV1Alpha1 {
		derivedBackend = result.DerivedContract.Provider
	}
	if derivedBackend != intent.State.Backend {
		return errors.New("derived contract backend mismatch")
	}
	if result.MeasurementStartedAt == "" || result.MeasurementEndedAt == "" || result.RuntimeVersion == "" {
		return errors.New("runtime identity or timestamp evidence is incomplete")
	}
	warmupCompleted, err := time.Parse(time.RFC3339Nano, result.WarmupCompletedAt)
	if err != nil {
		return errors.New("warm-up completion timestamp is invalid")
	}
	measurementStarted, measurementEnded, err := result.MeasurementWindow()
	if err != nil {
		return err
	}
	if warmupCompleted.After(measurementStarted) {
		return errors.New("warm-up does not precede the measurement window")
	}
	latencyCount, err := validateRepetitions(result.RepetitionResults, measurementStarted, measurementEnded)
	if err != nil {
		return err
	}
	if latencyCount != result.InferenceCount {
		return errors.New("raw latency count does not match inference count")
	}
	return nil
}

func validateRuntimePathEvidence(intent Intent, result RuntimeResult) error {
	if strings.TrimSpace(result.CPUContract.CPUSetEffective) == "" || result.CPUContract.AffinityCPUCount < 1 {
		return errors.New("effective CPU evidence is incomplete")
	}
	if IsSupportedCPUORTIntent(intent) {
		if result.RKNN != nil || result.Provider != intent.State.Backend ||
			!contains(result.AvailableProviders, intent.State.Backend) ||
			(result.SchemaVersion == RuntimeResultSchemaVersion &&
				result.DerivedContract.Provider != intent.State.Backend) {
			return errors.New("CPU ONNX Runtime backend evidence mismatch")
		}
		if result.CPUContract.ORTIntraOpThreads != intent.CPU.ORTIntraOpThreads ||
			result.CPUContract.ORTInterOpThreads != intent.CPU.ORTInterOpThreads ||
			result.CPUContract.AffinityCPUCount < intent.CPU.ORTIntraOpThreads {
			return errors.New("ORT thread evidence mismatch")
		}
		return nil
	}
	if !IsSupportedRKNNLiteIntent(intent) || result.SchemaVersion != RuntimeResultSchemaVersion ||
		result.Provider != "" || result.DerivedContract.Provider != "" || len(result.AvailableProviders) != 0 {
		return errors.New("runtime result does not match an explicit supported path")
	}
	return validateRKNNRuntimeEvidence(intent, result.RKNN)
}

func validateRKNNRuntimeEvidence(intent Intent, evidence *RKNNRuntimeEvidence) error {
	if evidence == nil || !evidence.InitSucceeded || !evidence.LoadSucceeded ||
		evidence.SelectedBatch != intent.Measurement.Batch ||
		evidence.CoreMask != RKNNLiteCoreMask || evidence.InputLayout != RKNNLiteInputLayout {
		return errors.New("RKNN initialization, load, batch, core-mask, or input-layout evidence mismatch")
	}
	if strings.TrimSpace(evidence.Package.Name) == "" ||
		strings.TrimSpace(evidence.Package.Version) == "" ||
		len(evidence.HostLibraries) == 0 || len(evidence.Devices) == 0 {
		return errors.New("RKNN package or host evidence is incomplete")
	}
	seenLibraries := make(map[string]struct{}, len(evidence.HostLibraries))
	for _, library := range evidence.HostLibraries {
		if strings.TrimSpace(library.Name) == "" || !validArtifactPath(library.Path) ||
			!digestPattern.MatchString(library.Digest) {
			return errors.New("RKNN host library evidence is invalid")
		}
		if _, duplicate := seenLibraries[library.Name]; duplicate {
			return errors.New("RKNN host library evidence contains a duplicate name")
		}
		seenLibraries[library.Name] = struct{}{}
	}
	if strings.TrimSpace(evidence.Driver.Name) == "" ||
		strings.TrimSpace(evidence.Driver.Version) == "" ||
		!validArtifactPath(evidence.Driver.VersionPath) {
		return errors.New("RKNN driver evidence is invalid")
	}
	if !evidence.Platform.Matched || !validArtifactPath(evidence.Platform.CompatiblePath) ||
		strings.TrimSpace(evidence.Platform.CompatibleToken) == "" {
		return errors.New("RKNN platform evidence is invalid")
	}
	seenDevices := make(map[string]struct{}, len(evidence.Devices))
	for _, device := range evidence.Devices {
		if !validArtifactPath(device.Path) || !device.CharacterDevice || !device.Opened {
			return errors.New("RKNN device evidence is invalid or inaccessible")
		}
		if _, duplicate := seenDevices[device.Path]; duplicate {
			return errors.New("RKNN device evidence contains a duplicate path")
		}
		seenDevices[device.Path] = struct{}{}
	}
	return nil
}

func normalizeArchitecture(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "x86_64":
		return "amd64"
	case runtimeAArch64:
		return "arm64"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func validateCPUQuota(limit, cpuMax string) error {
	cores, err := strconv.ParseInt(limit, 10, 64)
	if err != nil || cores < 1 {
		return errors.New("frozen CPU limit is not a positive integer core count")
	}
	fields := strings.Fields(cpuMax)
	if len(fields) != 2 || fields[0] == "max" {
		return errors.New("runtime cgroup CPU quota is missing or unlimited")
	}
	quota, quotaErr := strconv.ParseInt(fields[0], 10, 64)
	period, periodErr := strconv.ParseInt(fields[1], 10, 64)
	if quotaErr != nil || periodErr != nil || quota < 1 || period < 1 ||
		quota/period != cores || quota%period != 0 {
		return errors.New("runtime cgroup CPU quota does not match the frozen limit")
	}
	return nil
}

func validateRepetitions(repetitions []Repetition, measurementStarted, measurementEnded time.Time) (int, error) {
	latencyCount := 0
	for index, repetition := range repetitions {
		if repetition.Repetition != index+1 {
			return 0, errors.New("runtime repetition sequence is invalid")
		}
		started, err := time.Parse(time.RFC3339Nano, repetition.MeasurementStartedAt)
		if err != nil {
			return 0, errors.New("runtime repetition start timestamp is invalid")
		}
		ended, err := time.Parse(time.RFC3339Nano, repetition.MeasurementEndedAt)
		if err != nil || !ended.After(started) || started.Before(measurementStarted) || ended.After(measurementEnded) {
			return 0, errors.New("runtime repetition window is invalid")
		}
		if len(repetition.LatencyMilliseconds) == 0 {
			return 0, errors.New("raw latency distribution is empty")
		}
		for _, latency := range repetition.LatencyMilliseconds {
			if latency <= 0 || math.IsNaN(latency) || math.IsInf(latency, 0) {
				return 0, errors.New("raw latency sample is invalid")
			}
		}
		latencyCount += len(repetition.LatencyMilliseconds)
	}
	return latencyCount, nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
