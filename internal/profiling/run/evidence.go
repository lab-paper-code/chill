package profilingrun

import (
	"bytes"
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
	RuntimeResultSchemaVersion = "chill.dacs.io/batch-characterization.v1alpha14"

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
	Phases                    []Phase                    `json:"phases"`
	SupplySizing              SupplySizing               `json:"supplySizing"`
	PostLoad                  *PostLoadObservation       `json:"postLoad"`
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

// PostLoadObservation proves the same actor stayed loaded without serving
// requests while metrics.samples retained its post-load stabilization series.
type PostLoadObservation struct {
	StartedAt                string        `json:"startedAt"`
	EndedAt                  string        `json:"endedAt"`
	DurationSecondsMonotonic float64       `json:"durationSecondsMonotonic"`
	ActorPID                 int           `json:"actorPID"`
	ModelResidentAtEnd       bool          `json:"modelResidentAtEnd"`
	CountersBefore           ActorCounters `json:"countersBefore"`
	CountersAfter            ActorCounters `json:"countersAfter"`
}

type ActorCounters struct {
	AcceptedRequests  uint64 `json:"acceptedRequests"`
	CompletedRequests uint64 `json:"completedRequests"`
	Executions        uint64 `json:"executions"`
}

type RuntimeCPU struct {
	OSCPUCount         int            `json:"osCPUCount"`
	CgroupCPUMax       string         `json:"cgroupCPUMax"`
	ORTIntraOpThreads  int            `json:"ortIntraOpThreads"`
	ORTInterOpThreads  int            `json:"ortInterOpThreads"`
	AffinityCPUCount   int            `json:"affinityCPUCount"`
	AffinityCPUs       []int          `json:"affinityCPUs"`
	CPUSetEffective    string         `json:"cpusetCPUsEffective"`
	ProcessCPUSeconds  float64        `json:"processCPUSeconds"`
	CgroupCPUStatDelta map[string]int `json:"cgroupCPUStatDelta"`
}
type DerivedContract struct {
	SchemaVersion string       `json:"schemaVersion"`
	Backend       string       `json:"backend,omitempty"`
	Provider      string       `json:"provider,omitempty"`
	NodeSnapshot  NodeSnapshot `json:"nodeSnapshot"`
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
type Phase struct {
	ID                           string  `json:"id"`
	Kind                         string  `json:"kind"`
	Utilization                  float64 `json:"utilization,omitempty"`
	ArrivalRatePerSecond         float64 `json:"arrivalRatePerSecond,omitempty"`
	AchievedArrivalRatePerSecond float64 `json:"achievedArrivalRatePerSecond"`
	// IssueElapsedSeconds ends at the last scheduled arrival and excludes response drain.
	IssueElapsedSeconds       float64               `json:"issueElapsedSecondsMonotonic"`
	OfferingStartedAt         time.Time             `json:"offeringStartedAt"`
	OfferingEndedAt           time.Time             `json:"offeringEndedAt"`
	DrainEndedAt              time.Time             `json:"drainEndedAt"`
	IssueLag                  *IssueLag             `json:"issueLagMs,omitempty"`
	ScheduledInputs           int                   `json:"scheduledInputs"`
	SentInputs                int                   `json:"sentInputs"`
	ArrivalSeed               string                `json:"arrivalSeed,omitempty"`
	DriverGC                  *DriverGC             `json:"driverGC"`
	Workers                   int                   `json:"workers,omitempty"`
	MeasurementStartedAt      string                `json:"measurementStartedAt"`
	MeasurementEndedAt        string                `json:"measurementEndedAt"`
	MeasurementElapsedSeconds float64               `json:"measurementElapsedSecondsMonotonic"`
	CompletedInputs           int                   `json:"completedInputs"`
	Requests                  []PhaseRequest        `json:"requests"`
	LatencyMilliseconds       []float64             `json:"latencyMs"`
	NativeSamples             []NativeServiceSample `json:"nativeSamples"`
}

// UnmarshalJSON rejects retired fields and Poisson-only fields on saturation.
func (p *Phase) UnmarshalJSON(payload []byte) error {
	type wire Phase
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return err
	}
	for _, name := range []string{"scheduledInputs", "sentInputs", "completedInputs", "offeringStartedAt",
		"offeringEndedAt", "drainEndedAt", "issueElapsedSecondsMonotonic", "achievedArrivalRatePerSecond"} {
		if len(fields[name]) == 0 || string(fields[name]) == "null" {
			return fmt.Errorf("phase.%s is required", name)
		}
	}
	if decoded.Kind == phaseKindSaturation {
		for _, name := range []string{"arrivalRatePerSecond", "utilization", "arrivalSeed", "issueLagMs"} {
			if _, present := fields[name]; present {
				return fmt.Errorf("saturation must omit %s", name)
			}
		}
	}
	*p = Phase(decoded)
	return nil
}

type IssueLag struct {
	Count int     `json:"count"`
	Min   float64 `json:"min"`
	Mean  float64 `json:"mean"`
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	P99   float64 `json:"p99"`
	Max   float64 `json:"max"`
}

// DriverGC observes collections from issuance start through the last send, excluding response drain.
type DriverGC struct {
	Cycles                 uint32  `json:"cycles"`
	PauseTotalMilliseconds float64 `json:"pauseTotalMilliseconds"`
	MaxPauseMilliseconds   float64 `json:"maxPauseMilliseconds"`
}

// UnmarshalJSON admits only the complete current driver observation shape.
func (g *DriverGC) UnmarshalJSON(payload []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return err
	}
	if len(fields) != 3 {
		return errors.New("driverGC requires cycles, pauseTotalMilliseconds and maxPauseMilliseconds")
	}
	for _, name := range []string{"cycles", "pauseTotalMilliseconds", "maxPauseMilliseconds"} {
		if len(fields[name]) == 0 || string(fields[name]) == "null" {
			return fmt.Errorf("driverGC.%s is required", name)
		}
	}
	type wire DriverGC
	var decoded wire
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return err
	}
	*g = DriverGC(decoded)
	return nil
}

type PhaseRequest struct {
	RequestID           string        `json:"requestID"`
	RecordID            string        `json:"recordID"`
	ScheduledAt         time.Time     `json:"scheduledAt"`
	DispatchedAt        time.Time     `json:"dispatchedAt"`
	CompletedAt         time.Time     `json:"completedAt"`
	LatencyMilliseconds float64       `json:"latencyMs"`
	Response            PhaseResponse `json:"response"`
}

// PhaseResponse retains request evidence without repeating physical batch proof.
// The native serving response remains firstfloor.Response.
type PhaseResponse struct {
	ModelName         string             `json:"model_name"`
	ID                string             `json:"id"`
	Outputs           []firstfloor.Field `json:"outputs"`
	Progress          PhaseProgress      `json:"progress"`
	ExecutionComplete bool               `json:"executionComplete"`
}

type PhaseProgress struct {
	RequestID         string                        `json:"requestID"`
	WorkloadUID       string                        `json:"workloadUID"`
	Definition        string                        `json:"definition"`
	Phase             string                        `json:"phase"`
	At                time.Time                     `json:"at"`
	BatchID           uint64                        `json:"batchID,omitempty"`
	BatchSize         int                           `json:"batchSize,omitempty"`
	ProfileID         string                        `json:"profileID,omitempty"`
	DecisionID        string                        `json:"decisionID,omitempty"`
	QueueMS           float64                       `json:"queueMS"`
	ExecutorWaitNanos *int64                        `json:"executorWaitNanos,omitempty"`
	ServiceMS         float64                       `json:"serviceMS"`
	Units             int                           `json:"units,omitempty"`
	NativeElapsedMS   float64                       `json:"nativeElapsedMS"`
	ExecutionComplete bool                          `json:"executionComplete"`
	Outcome           string                        `json:"outcome,omitempty"`
	NativeHTTP        *firstfloor.HTTPRequestTiming `json:"nativeHTTP,omitempty"`
	NativeDeferredAt  *time.Time                    `json:"nativeDeferredAt,omitempty"`
}

// NativeServiceSample is one actual batch execution, aligned by slice index with
// the phase's maximum member client latency. Spans overlap and must not be summed.
// RecordIDs and Units are per request; units are analysis observations.
type NativeServiceSample struct {
	BatchID    uint64   `json:"batchID"`
	BatchSize  int      `json:"batchSize"`
	RequestIDs []string `json:"requestIDs"`
	RecordIDs  []string `json:"recordIDs"`
	Units      []int    `json:"units"`
	firstfloor.ServiceSample
	RunnerServiceMilliseconds float64   `json:"runnerServiceMs"`
	PreprocessMilliseconds    float64   `json:"preprocessMs"`
	BackendCallMilliseconds   float64   `json:"backendCallMs"`
	PostprocessMilliseconds   float64   `json:"postprocessMs"`
	FirstTokenMilliseconds    float64   `json:"firstTokenMs,omitempty"`
	NativeRequestMilliseconds float64   `json:"nativeRequestMs"`
	StartedAt                 time.Time `json:"startedAt"`
	FinishedAt                time.Time `json:"finishedAt"`
	LatestEnqueuedAt          time.Time `json:"latestEnqueuedAt"`
	EarliestEnqueuedAt        time.Time `json:"earliestEnqueuedAt"`
}

// Clone preserves pair/provenance ownership along with aligned request metadata.
func (s NativeServiceSample) Clone() NativeServiceSample {
	s.ServiceSample = s.ServiceSample.Clone()
	s.RequestIDs = slices.Clone(s.RequestIDs)
	s.RecordIDs, s.Units = slices.Clone(s.RecordIDs), slices.Clone(s.Units)
	return s
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
	minimumElapsed := float64(config.Measurement.DurationSeconds)
	if result.MeasurementMode != "duration" || result.InferenceCount < config.Measurement.MinServiceSamples ||
		math.IsNaN(result.MeasurementElapsedSeconds) || math.IsInf(result.MeasurementElapsedSeconds, 0) ||
		result.MeasurementElapsedSeconds < minimumElapsed {
		return errors.New("runtime measurement evidence is incomplete")
	}
	if result.TargetDurationSeconds == nil ||
		*result.TargetDurationSeconds != float64(config.Measurement.DurationSeconds) {
		return errors.New("runtime target duration does not match the measurement contract")
	}
	if err := validateRuntimePathEvidence(config, result); err != nil {
		return err
	}
	if err := validateCPUObservation(result.CPUContract.CgroupCPUMax); err != nil {
		return err
	}
	if result.MeasurementStartedAt == "" || result.MeasurementEndedAt == "" {
		return errors.New("runtime timestamp evidence is incomplete")
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
	latencyCount, err := validatePhases(config, result.Phases, result.SupplySizing, measurementStarted, measurementEnded)
	if err != nil {
		return err
	}
	if latencyCount != result.InferenceCount {
		return errors.New("raw latency count does not match inference count")
	}
	if err := validatePostLoad(config, result); err != nil {
		return err
	}
	return nil
}

func validatePostLoad(config RunConfig, result RuntimeResult) error {
	post := result.PostLoad
	if post == nil || post.ActorPID <= 0 || !post.ModelResidentAtEnd ||
		!finitePositive(post.DurationSecondsMonotonic) ||
		post.DurationSecondsMonotonic < float64(config.Measurement.OperatingPoints.PostLoadDurationSeconds) {
		return errors.New("post-load requires loaded actor identity, residency and the frozen observation duration")
	}
	start, err := time.Parse(time.RFC3339Nano, post.StartedAt)
	if err != nil {
		return errors.New("post-load start is invalid")
	}
	end, err := time.Parse(time.RFC3339Nano, post.EndedAt)
	if err != nil || !end.After(start) || post.EndedAt != result.MeasurementEndedAt {
		return errors.New("post-load end must close the runtime measurement window")
	}
	lastEnd, err := time.Parse(time.RFC3339Nano, result.Phases[len(result.Phases)-1].MeasurementEndedAt)
	if err != nil || !start.Equal(lastEnd) {
		return errors.New("post-load must begin at the final drained load-phase boundary")
	}
	before := post.CountersBefore
	if before != post.CountersAfter || before.Executions != uint64(result.InferenceCount) ||
		before.AcceptedRequests != uint64(result.InferenceCount)*uint64(config.Measurement.Batch) ||
		before.CompletedRequests != before.AcceptedRequests {
		return errors.New("post-load actor counters require zero requests and zero inference")
	}
	return nil
}

func validateRuntimePathEvidence(config RunConfig, result RuntimeResult) error {
	if strings.TrimSpace(result.CPUContract.CPUSetEffective) == "" || result.CPUContract.AffinityCPUCount < 1 {
		return errors.New("effective CPU evidence is incomplete")
	}
	if !IsSupportedFirstfloorRunConfig(config) {
		return errors.New("runtime result requires a supported firstfloor RunConfig")
	}
	if err := validateRuntimeMemory(result.MemoryBytes, result.MemorySource, result.MemorySamples,
		result.MeasurementWindow); err != nil {
		return err
	}
	if err := validateFirstfloorPath(config, result.Firstfloor, result.RKNN, result.Provider,
		result.AvailableProviders, result.CPUContract); err != nil {
		return err
	}
	if err := validateFirstfloorRuntimeEvidence(config, result.Firstfloor, uint64(result.InferenceCount)); err != nil {
		return err
	}
	return validateNativeSamples(config, result.ExperimentID, result.Phases)
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
	provider string, providers []string, cpu RuntimeCPU) error {
	if evidence == nil || evidence.Runtime.Synthetic || evidence.Runtime.Runtime != config.State.Backend ||
		!slices.Equal(evidence.Runtime.ModelFiles, config.State.ModelFiles()) ||
		len(evidence.Runtime.CPUAffinity) != cpu.AffinityCPUCount || !slices.Equal(evidence.Runtime.CPUAffinity, cpu.AffinityCPUs) ||
		evidence.Runtime.CgroupCPUMax != cpu.CgroupCPUMax || evidence.Runtime.CPUSet != cpu.CPUSetEffective ||
		len(evidence.Runtime.InitializedBatches) == 0 {
		return errors.New("firstfloor actual runner identity or affinity mismatch")
	}
	if !slices.Equal(evidence.Runtime.AvailableProviders, providers) {
		return errors.New("firstfloor available-provider observations contradict runner metadata")
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
		evidence.Runtime.Device != device || evidence.Runtime.IntraOpThreads < 0 || evidence.Runtime.InterOpThreads < 0 ||
		cpu.ORTIntraOpThreads != evidence.Runtime.IntraOpThreads || cpu.ORTInterOpThreads != evidence.Runtime.InterOpThreads {
		return errors.New("firstfloor actual provider/device/thread observations mismatch")
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

func validateNativeSamples(config RunConfig, trialID string, phases []Phase) error {
	seen := make(map[uint64]struct{})
	var callIndex uint64
	for _, phase := range phases {
		if phase.Kind != phaseKindSaturation && phase.ArrivalSeed != trialID+"/"+phase.ID {
			return errors.New("Poisson arrival seed differs from Run and phase identity")
		}
		knownGapCalls := 0
		if len(phase.NativeSamples) != len(phase.LatencyMilliseconds) {
			return errors.New("firstfloor native and client samples are not correlated")
		}
		for index, sample := range phase.NativeSamples {
			if err := validateServicePair(trialID, sample, callIndex); err != nil {
				return err
			}
			callIndex++
			if sample.PrecedingIdleNanos != nil {
				knownGapCalls++
			}
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
			serviceMS := float64(sample.ServiceDurationNanos) / 1e6
			if sample.BackendCallMilliseconds > serviceMS ||
				serviceMS > sample.NativeRequestMilliseconds ||
				sample.NativeRequestMilliseconds > phase.LatencyMilliseconds[index] {
				return errors.New("firstfloor nested monotonic span order is invalid")
			}
		}
		if knownGapCalls < config.Measurement.MinServiceSamples {
			return errors.New("firstfloor per-phase known-gap service calls are incomplete")
		}
	}
	return nil
}

// Pair integrity is admitted at the raw/retained reader boundary. Only the
// initial exchange may lack a preceding native end anchor.
func validateServicePair(trialID string, sample NativeServiceSample, callIndex uint64) error {
	if sample.ServiceDurationNanos <= 0 || trialID == "" || sample.TrialID != trialID ||
		(sample.PrecedingIdleNanos != nil && *sample.PrecedingIdleNanos < 0) {
		return errors.New("firstfloor canonical service pair or physical trial identity is invalid")
	}
	if callIndex > 0 && sample.PrecedingIdleNanos == nil {
		return errors.New("firstfloor post-start exchange is missing its actual gap")
	}
	return nil
}

// validateNativeSampleRequests checks the per-request record IDs and unit
// counts and the preprocessing stage spans of one batch execution.
func validateNativeSampleRequests(config RunConfig, sample NativeServiceSample) error {
	if len(sample.RequestIDs) != sample.BatchSize || len(sample.RecordIDs) != sample.BatchSize || len(sample.Units) != sample.BatchSize {
		return errors.New("firstfloor native request IDs, record IDs and units must cover the batch")
	}
	for index, recordID := range sample.RecordIDs {
		if sample.RequestIDs[index] == "" || recordID == "" || sample.Units[index] < 1 {
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
		!validArtifactPath(evidence.Driver.VersionPath) {
		return errors.New("RKNN driver evidence is invalid")
	}
	if !validArtifactPath(evidence.Platform.CompatiblePath) ||
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

// validateCPUObservation checks the recorded controller value, not a CPU allocation.
// Both cgroup v2 "max" and cgroup v1 "-1" are normal unlimited observations.
func validateCPUObservation(cpuMax string) error {
	fields := strings.Fields(cpuMax)
	if len(fields) != 2 {
		return errors.New("runtime cgroup CPU quota/period is malformed")
	}
	period, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || period < 1 {
		return errors.New("runtime cgroup CPU period must be positive")
	}
	if fields[0] != "max" && fields[0] != "-1" {
		quota, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || quota < 1 {
			return errors.New("runtime cgroup CPU quota is malformed")
		}
	}
	return nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
