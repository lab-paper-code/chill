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
	RuntimeResultSchemaVersion = "chill.dacs.io/batch-characterization.v1alpha5"

	RKNNLiteCoreMask    = "NPU_CORE_0"
	RKNNLiteInputLayout = "NHWC"
	runtimeAArch64      = "aarch64"
)

type RuntimeResult struct {
	SchemaVersion             string               `json:"schemaVersion"`
	Status                    string               `json:"status"`
	ExperimentID              string               `json:"experimentId"`
	NodeName                  string               `json:"nodeName"`
	Model                     string               `json:"model"`
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
	SchemaVersion  string          `json:"schemaVersion"`
	Backend        string          `json:"backend,omitempty"`
	Provider       string          `json:"provider,omitempty"`
	NodeSnapshot   NodeSnapshot    `json:"nodeSnapshot"`
	CPU            json.RawMessage `json:"cpu"`
	RuntimeOptions json.RawMessage `json:"runtimeOptions"`
	Scheduling     json.RawMessage `json:"scheduling"`
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
	Name string `json:"name"`
	Path string `json:"path"`
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
func ValidateRuntime(config RunConfig, podUID string, result RuntimeResult) error {
	if result.SchemaVersion != RuntimeResultSchemaVersion {
		return fmt.Errorf("unsupported runtime result schemaVersion %q", result.SchemaVersion)
	}
	return validateBoundRuntime(config, podUID, result)
}

//nolint:gocyclo // Runtime validation is an explicit ordered checklist over one immutable evidence record.
func validateBoundRuntime(config RunConfig, podUID string, result RuntimeResult) error {
	checks := []struct{ name, want, got string }{
		{"status", "Succeeded", result.Status},
		{"Pod UID", podUID, result.ExperimentID},
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
		return errors.New("runtime architecture does not match the RunConfig Node")
	}
	if result.BatchSize != config.Measurement.Batch || result.WarmupIterations != config.Measurement.WarmupIterations {
		return errors.New("measurement protocol mismatch")
	}
	minimumElapsed := float64(config.Measurement.DurationSeconds) - 0.5
	if result.MeasurementMode != "duration" || result.InferenceCount < 1 ||
		result.MeasurementElapsedSeconds < minimumElapsed {
		return errors.New("runtime measurement evidence is incomplete")
	}
	if len(result.RepetitionResults) < 1 {
		return errors.New("runtime result must contain at least one repetition result")
	}
	if result.TargetDurationSeconds == nil ||
		*result.TargetDurationSeconds != float64(config.Measurement.DurationSeconds) {
		return errors.New("runtime target duration does not match the measurement contract")
	}
	if !result.OutputValidation.Passed || result.OutputValidation.Method != config.Measurement.OutputValidation ||
		len(result.OutputValidation.OutputShapes) == 0 {
		return errors.New("output validation evidence mismatch")
	}
	for _, shape := range result.OutputValidation.OutputShapes {
		if len(shape) == 0 || shape[0] != config.Measurement.Batch {
			return errors.New("output shape batch mismatch")
		}
	}
	if err := validateRuntimePathEvidence(config, result); err != nil {
		return err
	}
	if err := validateCPUQuota(config.CPU.Limit, result.CPUContract.CgroupCPUMax); err != nil {
		return err
	}
	if result.DerivedContract.NodeSnapshot.UID != config.TargetNode.UID {
		return errors.New("derived contract identity mismatch")
	}
	if result.DerivedContract.Backend != config.State.Backend {
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

func validateRuntimePathEvidence(config RunConfig, result RuntimeResult) error {
	if strings.TrimSpace(result.CPUContract.CPUSetEffective) == "" || result.CPUContract.AffinityCPUCount < 1 {
		return errors.New("effective CPU evidence is incomplete")
	}
	if IsSupportedCPUORTRunConfig(config) {
		if result.RKNN != nil || result.Provider != config.State.Backend ||
			!contains(result.AvailableProviders, config.State.Backend) ||
			(result.SchemaVersion == RuntimeResultSchemaVersion &&
				result.DerivedContract.Provider != config.State.Backend) {
			return errors.New("CPU ONNX Runtime backend evidence mismatch")
		}
		if result.CPUContract.ORTIntraOpThreads != config.CPU.ORTIntraOpThreads ||
			result.CPUContract.ORTInterOpThreads != config.CPU.ORTInterOpThreads ||
			result.CPUContract.AffinityCPUCount < config.CPU.ORTIntraOpThreads {
			return errors.New("ORT thread evidence mismatch")
		}
		return nil
	}
	if !IsSupportedRKNNLiteRunConfig(config) || result.SchemaVersion != RuntimeResultSchemaVersion ||
		result.Provider != "" || result.DerivedContract.Provider != "" || len(result.AvailableProviders) != 0 {
		return errors.New("runtime result does not match an explicit supported path")
	}
	return validateRKNNRuntimeEvidence(config, result.RKNN)
}

func validateRKNNRuntimeEvidence(config RunConfig, evidence *RKNNRuntimeEvidence) error {
	if evidence == nil || !evidence.InitSucceeded || !evidence.LoadSucceeded ||
		evidence.SelectedBatch != config.Measurement.Batch ||
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
		if strings.TrimSpace(library.Name) == "" || !validArtifactPath(library.Path) {
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
		return errors.New("RunConfig CPU limit is not a positive integer core count")
	}
	fields := strings.Fields(cpuMax)
	if len(fields) != 2 || fields[0] == "max" {
		return errors.New("runtime cgroup CPU quota is missing or unlimited")
	}
	quota, quotaErr := strconv.ParseInt(fields[0], 10, 64)
	period, periodErr := strconv.ParseInt(fields[1], 10, 64)
	if quotaErr != nil || periodErr != nil || quota < 1 || period < 1 ||
		quota/period != cores || quota%period != 0 {
		return errors.New("runtime cgroup CPU quota does not match the RunConfig limit")
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
