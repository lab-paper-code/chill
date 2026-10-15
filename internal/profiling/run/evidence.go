package profilingrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
)

const (
	RuntimeResultSchemaVersion = "chill.dacs.io/batch-characterization.v1alpha8"

	RKNNLiteCoreMask      = "NPU_CORE_0"
	RKNNLiteInputLayout   = "NHWC"
	runtimeAArch64        = "aarch64"
	cudaExecutionProvider = "CUDAExecutionProvider"
)

type RuntimeResult struct {
	SchemaVersion             string                     `json:"schemaVersion"`
	Status                    string                     `json:"status"`
	ExperimentID              string                     `json:"experimentId"`
	NodeName                  string                     `json:"nodeName"`
	Model                     string                     `json:"model"`
	Architecture              string                     `json:"architecture"`
	Runtime                   string                     `json:"runtime"`
	RuntimeVersion            string                     `json:"runtimeVersion"`
	Backend                   string                     `json:"backend,omitempty"`
	Provider                  string                     `json:"provider,omitempty"`
	AvailableProviders        []string                   `json:"availableProviders,omitempty"`
	RKNN                      *RKNNRuntimeEvidence       `json:"rknn,omitempty"`
	Firstfloor                *FirstfloorRuntimeEvidence `json:"firstfloor,omitempty"`
	BatchSize                 int                        `json:"batchSize"`
	WarmupIterations          int                        `json:"warmupIterations"`
	WarmupCompletedAt         string                     `json:"warmupCompletedAt"`
	InferenceCount            int                        `json:"inferenceCount"`
	MeasurementMode           string                     `json:"measurementMode"`
	MeasurementStartedAt      string                     `json:"measurementStartedAt"`
	MeasurementEndedAt        string                     `json:"measurementEndedAt"`
	MeasurementElapsedSeconds float64                    `json:"measurementElapsedSecondsMonotonic"`
	CPUContract               RuntimeCPU                 `json:"cpuContract"`
	DerivedContract           DerivedContract            `json:"derivedExecutionContract"`
	RepetitionResults         []Repetition               `json:"repetitionResults"`
	CompletionIndex           int                        `json:"completionIndex"`
	Inputs                    []string                   `json:"inputs"`
	// MemoryBytes is the kernel cgroup high-water mark (v2 memory.peak or v1
	// memory.max_usage_in_bytes) since runtime container creation, including
	// server, backend, worker, and page cache.
	// Zero is absent evidence, never artifact size or summed process peaks.
	// On a kernel without memory.peak, MemoryBytes is instead the maximum of the
	// retained whole-container memory.current samples inside the measurement window.
	MemoryBytes              uint64             `json:"memoryBytes,omitempty"`
	MemorySource             string             `json:"memorySource,omitempty"`
	MemorySamples            []MemorySample     `json:"memorySamples,omitempty"`
	MeasurementIterations    int                `json:"measurementIterations"`
	TargetDurationSeconds    *float64           `json:"targetDurationSecondsPerRepetition"`
	Latency                  map[string]float64 `json:"latencyMs"`
	ThroughputItemsPerSecond float64            `json:"throughputItemsPerSecond"`
	Power                    json.RawMessage    `json:"power"`
	EnergyPerRequest         json.RawMessage    `json:"energyPerRequest"`
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

// FirstfloorRuntimeEvidence binds the actual initialized actor and native runner.
// Counts cover measured inference only; warmup and idle have no executions.
type FirstfloorRuntimeEvidence struct {
	Runtime              firstfloor.RuntimeMetadata `json:"runtime"`
	Mode                 string                     `json:"mode"`
	ModelInstanceCount   int                        `json:"modelInstanceCount"`
	MaxBatchSize         int                        `json:"maxBatchSize"`
	MaxQueueSize         int                        `json:"maxQueueSize"`
	DeadlinePolicy       string                     `json:"deadlinePolicy"`
	ExecutionCount       uint64                     `json:"executionCount"`
	InferenceCount       uint64                     `json:"inferenceCount"`
	BatchExecutionCounts map[string]uint64          `json:"batchExecutionCounts"`
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
	Repetition           int                   `json:"repetition"`
	MeasurementStartedAt string                `json:"measurementStartedAt"`
	MeasurementEndedAt   string                `json:"measurementEndedAt"`
	LatencyMilliseconds  []float64             `json:"latencyMs"`
	NativeSamples        []NativeServiceSample `json:"nativeSamples,omitempty"`
}

// NativeServiceSample is one actual batch execution, aligned by slice index with
// the repetition's client latency sample. Spans overlap and must not be summed.
// RecordIDs and Units are per request of the batch; units are analysis
// observations, not the batching unit.
type NativeServiceSample struct {
	BatchID                   uint64   `json:"batchID"`
	BatchSize                 int      `json:"batchSize"`
	RecordIDs                 []string `json:"recordIDs"`
	Units                     []int    `json:"units"`
	RunnerServiceMilliseconds float64  `json:"runnerServiceMs"`
	PreprocessMilliseconds    float64  `json:"preprocessMs"`
	BackendCallMilliseconds   float64  `json:"backendCallMs"`
	PostprocessMilliseconds   float64  `json:"postprocessMs"`
	FirstTokenMilliseconds    float64  `json:"firstTokenMs,omitempty"`
	NativeRequestMilliseconds float64  `json:"nativeRequestMs"`
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
	if !slices.Equal(result.Inputs, config.State.Preprocessor.Inputs) {
		return errors.New("runtime input field evidence does not match the frozen preprocessor inputs")
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
	if len(config.CPU.AffinityCPUs) != 0 && (!slices.Equal(config.CPU.AffinityCPUs, result.CPUContract.AffinityCPUs) ||
		result.CPUContract.AffinityCPUCount != len(config.CPU.AffinityCPUs)) {
		return errors.New("runtime actual CPU affinity differs from requested frozen CPU IDs")
	}
	if !IsSupportedFirstfloorRunConfig(config) {
		return errors.New("runtime result requires a supported firstfloor RunConfig")
	}
	if err := validateRuntimeMemory(result.MemoryBytes, result.MemorySource, result.MemorySamples,
		result.MeasurementWindow); err != nil {
		return err
	}
	if err := validateFirstfloorPath(config, result.Firstfloor, result.RKNN, result.Provider,
		result.AvailableProviders, result.CPUContract, result.RuntimeVersion); err != nil {
		return err
	}
	if result.DerivedContract.Provider != result.Provider {
		return errors.New("firstfloor derived provider mismatch")
	}
	if err := validateFirstfloorRuntimeEvidence(config, result.Firstfloor, uint64(result.InferenceCount)); err != nil {
		return err
	}
	return validateNativeSamples(config, result.RepetitionResults)
}

// MemorySample is one actual whole-container memory.current reading.
type MemorySample struct {
	At    string `json:"at"`
	Bytes uint64 `json:"bytes"`
}

// validateRuntimeMemory accepts a kernel high-water mark as is, or sampled
// memory.current whose maximum, window and cadence are the frozen evidence.
// validateRuntimeMemory admits the sampled maximum only as the observed maximum of the retained
// timestamped samples. It can miss spikes between samples; actual cadence is the observed gaps in
// the raw timestamps, not the frozen interval, a kernel high-water mark, or an absolute bound.
func validateRuntimeMemory(bytes uint64, source string, samples []MemorySample,
	window func() (time.Time, time.Time, error)) error {
	switch source {
	case memoryPeakCgroupV1, memoryPeakCgroupV2:
		if bytes == 0 || len(samples) != 0 {
			return errors.New("firstfloor cgroup lifetime memory evidence is invalid")
		}
		return nil
	case memorySampledCgroupV2:
	default:
		return errors.New("firstfloor cgroup memory evidence is missing")
	}
	started, ended, err := window()
	if err != nil {
		return err
	}
	if len(samples) < 2 {
		return errors.New("firstfloor sampled memory needs at least two boundary samples")
	}
	var peak uint64
	var previous time.Time
	for i, sample := range samples {
		at, err := time.Parse(time.RFC3339Nano, sample.At)
		if err != nil || sample.Bytes == 0 || at.Before(started) || at.After(ended) || (i > 0 && !at.After(previous)) {
			return errors.New("firstfloor sampled memory is outside the measurement window or malformed")
		}
		previous = at
		if sample.Bytes > peak {
			peak = sample.Bytes
		}
	}
	if bytes != peak {
		return errors.New("firstfloor sampled memory maximum differs from retained samples")
	}
	return nil
}

func validateFirstfloorRuntimeEvidence(config RunConfig, evidence *FirstfloorRuntimeEvidence, executions uint64) error {
	if config.State.Firstfloor == nil || evidence == nil || evidence.Mode != "profiling" ||
		evidence.ModelInstanceCount != 1 || evidence.MaxBatchSize != int(config.State.Firstfloor.MaxBatchSize) ||
		evidence.MaxQueueSize != int(config.State.Firstfloor.MaxQueueSize) ||
		evidence.DeadlinePolicy != modelcompat.EffectiveDeadlinePolicy(config.State.Firstfloor.DeadlinePolicy) ||
		evidence.BatchExecutionCounts == nil || evidence.ExecutionCount != executions ||
		evidence.InferenceCount != executions*uint64(config.Measurement.Batch) {
		return errors.New("firstfloor actual actor settings or measured counts mismatch")
	}
	for batch, count := range evidence.BatchExecutionCounts {
		if count != 0 && batch != strconv.Itoa(config.Measurement.Batch) {
			return errors.New("firstfloor executed a different fixed batch")
		}
	}
	if evidence.BatchExecutionCounts[strconv.Itoa(config.Measurement.Batch)] != executions {
		return errors.New("firstfloor fixed-batch execution evidence is missing")
	}
	return nil
}

func validateFirstfloorPath(config RunConfig, evidence *FirstfloorRuntimeEvidence, rknn *RKNNRuntimeEvidence,
	provider string, providers []string, cpu RuntimeCPU, runtimeVersion string) error {
	if evidence == nil || evidence.Runtime.Synthetic || evidence.Runtime.Runtime != config.State.Backend ||
		evidence.Runtime.RuntimeVersion == "" || evidence.Runtime.RuntimeVersion != runtimeVersion ||
		!slices.Equal(evidence.Runtime.ModelFiles, config.State.ModelFiles()) ||
		len(evidence.Runtime.CPUAffinity) != cpu.AffinityCPUCount || !slices.Equal(evidence.Runtime.CPUAffinity, cpu.AffinityCPUs) ||
		evidence.Runtime.CgroupCPUMax != cpu.CgroupCPUMax || evidence.Runtime.CPUSet != cpu.CPUSetEffective ||
		len(evidence.Runtime.InitializedBatches) == 0 {
		return errors.New("firstfloor actual runner identity or affinity mismatch")
	}
	if !slices.Equal(evidence.Runtime.AvailableProviders, providers) {
		return errors.New("firstfloor available-provider observations contradict runner metadata")
	}
	for _, pkg := range config.Delivery.RuntimePackages {
		switch pkg.Name {
		case "onnxruntime", "onnxruntime-gpu", "rknn-toolkit-lite2":
			if pkg.Version != runtimeVersion {
				return errors.New("firstfloor actual backend package version differs from exact-image declaration")
			}
		case "numpy":
			if pkg.Version != evidence.Runtime.NumpyVersion {
				return errors.New("firstfloor actual NumPy version differs from exact-image declaration")
			}
		}
	}
	if err := validateFirstfloorInputsAndBatches(config, evidence); err != nil {
		return err
	}
	return validateFirstfloorBackend(config, evidence, rknn, provider, providers, cpu)
}

func validateFirstfloorInputsAndBatches(config RunConfig, evidence *FirstfloorRuntimeEvidence) error {
	if !slices.Equal(evidence.Runtime.Inputs, config.State.Preprocessor.Inputs) ||
		!slices.Equal(evidence.Runtime.Outputs, config.State.Preprocessor.Outputs) {
		return errors.New("firstfloor actual raw field contract mismatch")
	}
	seen := make(map[int]bool, len(evidence.Runtime.InitializedBatches))
	for _, batch := range evidence.Runtime.InitializedBatches {
		if batch < 1 || batch > evidence.MaxBatchSize || seen[batch] {
			return errors.New("firstfloor initialized batch bounds are invalid")
		}
		seen[batch] = true
	}
	if !seen[config.Measurement.Batch] {
		return errors.New("firstfloor selected batch was not initialized")
	}
	return nil
}

func validateFirstfloorBackend(config RunConfig, evidence *FirstfloorRuntimeEvidence, rknn *RKNNRuntimeEvidence,
	provider string, providers []string, cpu RuntimeCPU) error {
	if config.State.Backend == FirstfloorRKNNLiteBackend {
		if provider != "" || len(providers) != 0 || evidence.Runtime.Device != "npu" {
			return errors.New("firstfloor RKNN backend provider or device mismatch")
		}
		return validateRKNNRuntimeEvidence(config, rknn)
	}
	expected := cpuExecutionProvider
	device := "cpu"
	if config.State.Backend == FirstfloorORTCUDABackend {
		expected = cudaExecutionProvider
		device = "cuda"
	}
	if rknn != nil || provider != expected || !contains(providers, expected) ||
		len(evidence.Runtime.Providers) == 0 || evidence.Runtime.Providers[0] != expected ||
		evidence.Runtime.Device != device || evidence.Runtime.IntraOpThreads != config.CPU.ORTIntraOpThreads ||
		evidence.Runtime.InterOpThreads != config.CPU.ORTInterOpThreads ||
		cpu.ORTIntraOpThreads != config.CPU.ORTIntraOpThreads || cpu.ORTInterOpThreads != config.CPU.ORTInterOpThreads {
		return errors.New("firstfloor actual provider/device/threads mismatch")
	}
	if config.State.Backend == FirstfloorORTCUDABackend {
		access := config.Delivery.RuntimeAccess
		if access == nil || access.GPUUUID == "" || evidence.Runtime.GPUUUID != access.GPUUUID ||
			access.Resources["nvidia.com/gpu"] != "1" {
			return errors.New("firstfloor CUDA whole GPU identity mismatch")
		}
	}
	return nil
}

func validateNativeSamples(config RunConfig, repetitions []Repetition) error {
	seen := make(map[uint64]struct{})
	for _, repetition := range repetitions {
		if len(repetition.NativeSamples) != len(repetition.LatencyMilliseconds) {
			return errors.New("firstfloor native and client samples are not correlated")
		}
		for index, sample := range repetition.NativeSamples {
			if sample.BatchID == 0 || sample.BatchSize != config.Measurement.Batch {
				return errors.New("firstfloor native batch identity mismatch")
			}
			if _, duplicate := seen[sample.BatchID]; duplicate {
				return errors.New("firstfloor native batch ID is duplicated")
			}
			seen[sample.BatchID] = struct{}{}
			if err := validateNativeSampleRequests(config, sample); err != nil {
				return err
			}
			for _, span := range []float64{sample.RunnerServiceMilliseconds, sample.BackendCallMilliseconds, sample.NativeRequestMilliseconds} {
				if span <= 0 || math.IsNaN(span) || math.IsInf(span, 0) {
					return errors.New("firstfloor native span is invalid")
				}
			}
			if sample.BackendCallMilliseconds > sample.RunnerServiceMilliseconds ||
				sample.RunnerServiceMilliseconds > sample.NativeRequestMilliseconds ||
				sample.NativeRequestMilliseconds > repetition.LatencyMilliseconds[index] {
				return errors.New("firstfloor nested monotonic span order is invalid")
			}
		}
	}
	return nil
}

// validateNativeSampleRequests checks the per-request record IDs and unit
// counts and the preprocessing stage spans of one batch execution.
func validateNativeSampleRequests(config RunConfig, sample NativeServiceSample) error {
	if len(sample.RecordIDs) != sample.BatchSize || len(sample.Units) != sample.BatchSize {
		return errors.New("firstfloor native record IDs and units must cover the batch")
	}
	for index, recordID := range sample.RecordIDs {
		if recordID == "" || sample.Units[index] < 1 {
			return errors.New("firstfloor native record ID or unit count is invalid")
		}
	}
	for _, span := range []float64{sample.PreprocessMilliseconds, sample.PostprocessMilliseconds} {
		if span < 0 || math.IsNaN(span) || math.IsInf(span, 0) {
			return errors.New("firstfloor native preprocessing span is invalid")
		}
	}
	generative := config.State.Preprocessor.MaxNewTokens > 0
	firstToken := sample.FirstTokenMilliseconds
	if generative != (firstToken > 0) || math.IsNaN(firstToken) || math.IsInf(firstToken, 0) {
		return errors.New("firstfloor native first-token span must be present exactly for generative preprocessing")
	}
	return nil
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
