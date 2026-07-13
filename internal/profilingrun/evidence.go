package profilingrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type RuntimeResult struct {
	SchemaVersion             string             `json:"schemaVersion"`
	Status                    string             `json:"status"`
	SweepID                   string             `json:"sweepId"`
	ExperimentID              string             `json:"experimentId"`
	NodeName                  string             `json:"nodeName"`
	Model                     string             `json:"model"`
	ArtifactDigest            string             `json:"artifactDigest"`
	Architecture              string             `json:"architecture"`
	Runtime                   string             `json:"runtime"`
	RuntimeVersion            string             `json:"runtimeVersion"`
	Provider                  string             `json:"provider"`
	AvailableProviders        []string           `json:"availableProviders"`
	BatchSize                 int                `json:"batchSize"`
	WarmupIterations          int                `json:"warmupIterations"`
	WarmupCompletedAt         string             `json:"warmupCompletedAt"`
	InferenceCount            int                `json:"inferenceCount"`
	MeasurementMode           string             `json:"measurementMode"`
	Repetitions               int                `json:"repetitions"`
	MeasurementStartedAt      string             `json:"measurementStartedAt"`
	MeasurementEndedAt        string             `json:"measurementEndedAt"`
	MeasurementElapsedSeconds float64            `json:"measurementElapsedSecondsMonotonic"`
	OutputValidation          OutputValidation   `json:"outputValidation"`
	CPUContract               RuntimeCPU         `json:"cpuContract"`
	DerivedContract           DerivedContract    `json:"derivedExecutionContract"`
	RepetitionResults         []Repetition       `json:"repetitionResults"`
	CompletionIndex           int                `json:"completionIndex"`
	InputShape                []int              `json:"inputShape"`
	MeasurementIterations     int                `json:"measurementIterations"`
	TargetDurationSeconds     *float64           `json:"targetDurationSecondsPerRepetition"`
	Latency                   map[string]float64 `json:"latencyMs"`
	ThroughputItemsPerSecond  float64            `json:"throughputItemsPerSecond"`
	Power                     json.RawMessage    `json:"power"`
	EnergyPerRequest          json.RawMessage    `json:"energyPerRequest"`
	BSat                      json.RawMessage    `json:"bSat"`
}

type OutputValidation struct {
	Method       string  `json:"method"`
	Passed       bool    `json:"passed"`
	OutputShapes [][]int `json:"outputShapes"`
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
	SchemaVersion   string          `json:"schemaVersion"`
	RunIntentDigest string          `json:"runIntentDigest"`
	Provider        string          `json:"provider"`
	NodeSnapshot    NodeSnapshot    `json:"nodeSnapshot"`
	CPU             json.RawMessage `json:"cpu"`
	RuntimeOptions  json.RawMessage `json:"runtimeOptions"`
	Scheduling      json.RawMessage `json:"scheduling"`
}
type Repetition struct {
	Repetition           int       `json:"repetition"`
	MeasurementStartedAt string    `json:"measurementStartedAt"`
	MeasurementEndedAt   string    `json:"measurementEndedAt"`
	LatencyMilliseconds  []float64 `json:"latencyMs"`
}

//nolint:gocyclo // Runtime validation is an explicit ordered checklist over one immutable evidence record.
func ValidateRuntime(intent Intent, podUID string, result RuntimeResult) error {
	digest, err := intent.Digest()
	if err != nil {
		return err
	}
	checks := []struct{ name, want, got string }{
		{
			"status",
			"Succeeded",
			result.Status,
		}, {"run intent digest", digest, result.SweepID}, {"Pod UID", podUID, result.ExperimentID},
		{
			"Node",
			intent.TargetNode.Name,
			result.NodeName,
		}, {"model", intent.State.Model, result.Model}, {"artifact digest", intent.State.ArtifactDigest, result.ArtifactDigest},
		{
			"runtime family",
			intent.State.RuntimeFamily,
			result.Runtime,
		}, {"backend", intent.State.Backend, result.Provider},
	}
	for _, check := range checks {
		if check.want != check.got {
			return fmt.Errorf("%s mismatch: wanted %q, got %q", check.name, check.want, check.got)
		}
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
	if !result.OutputValidation.Passed || result.OutputValidation.Method != intent.Measurement.OutputValidation ||
		len(result.OutputValidation.OutputShapes) == 0 {
		return errors.New("output validation evidence mismatch")
	}
	for _, shape := range result.OutputValidation.OutputShapes {
		if len(shape) == 0 || shape[0] != intent.Measurement.Batch {
			return errors.New("output shape batch mismatch")
		}
	}
	if result.CPUContract.ORTIntraOpThreads != intent.CPU.ORTIntraOpThreads ||
		result.CPUContract.ORTInterOpThreads != intent.CPU.ORTInterOpThreads {
		return errors.New("ORT thread evidence mismatch")
	}
	if result.CPUContract.AffinityCPUCount < intent.CPU.ORTIntraOpThreads ||
		strings.TrimSpace(result.CPUContract.CPUSetEffective) == "" {
		return errors.New("effective CPU evidence contradicts or omits the contract")
	}
	if result.DerivedContract.RunIntentDigest != digest ||
		result.DerivedContract.NodeSnapshot.UID != intent.TargetNode.UID {
		return errors.New("derived contract identity mismatch")
	}
	if result.MeasurementStartedAt == "" || result.MeasurementEndedAt == "" || result.RuntimeVersion == "" ||
		!contains(result.AvailableProviders, intent.State.Backend) {
		return errors.New("runtime identity or timestamp evidence is incomplete")
	}
	warmupCompleted, err := time.Parse(time.RFC3339Nano, result.WarmupCompletedAt)
	if err != nil {
		return errors.New("warm-up completion timestamp is invalid")
	}
	measurementStarted, err := time.Parse(time.RFC3339Nano, result.MeasurementStartedAt)
	if err != nil || warmupCompleted.After(measurementStarted) {
		return errors.New("warm-up does not precede the measurement window")
	}
	latencyCount := 0
	for _, repetition := range result.RepetitionResults {
		latencyCount += len(repetition.LatencyMilliseconds)
	}
	if latencyCount != result.InferenceCount {
		return errors.New("raw latency count does not match inference count")
	}
	for _, repetition := range result.RepetitionResults {
		if len(repetition.LatencyMilliseconds) == 0 {
			return errors.New("raw latency distribution is empty")
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
