package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

type CPURuntimeConfig struct {
	Command                 []string
	ObserverReadyPaths      []string
	WorkloadWindowStartPath string
	MeasurementStartPath    string
	WorkloadWindowReadyPath string
	MeasurementEndPath      string
	ResultPath              string
	ObserverReadyTimeout    time.Duration
	MaxLatencySamples       int
	MaxInputBytes           int64
	MaxResultBytes          int64
	Stdout                  io.Writer
	Stderr                  io.Writer
}

// CPURuntime invokes a trusted CPU runtime entrypoint and accepts evidence only
// from its dedicated JSON file. Stdout and stderr remain diagnostics.
type CPURuntime struct {
	config                               CPURuntimeConfig
	name, family, backend, requestSchema string
}

var _ Runtime = (*CPURuntime)(nil)

// Model files, the preprocessor directory, and the dataset profiling records
// are mounted at these fixed Job paths.
const (
	modelDirectory   = "/model"
	preprocessorPath = "/preprocessor"
	datasetPath      = "/dataset"
)

type cpuRuntimeRequest struct {
	SchemaVersion                    string                 `json:"schemaVersion"`
	RunConfig                        profilingrun.RunConfig `json:"runConfig"`
	PodUID                           string                 `json:"podUID"`
	ScheduledNodeName                string                 `json:"scheduledNodeName"`
	ModelDirectory                   string                 `json:"modelDirectory"`
	PreprocessorPath                 string                 `json:"preprocessorPath"`
	DatasetPath                      string                 `json:"datasetPath,omitempty"`
	ObserverReadyPaths               []string               `json:"observerReadyPaths"`
	WorkloadWindowStartPath          string                 `json:"workloadWindowStartPath"`
	MeasurementStartPath             string                 `json:"measurementStartPath"`
	WorkloadWindowReadyPath          string                 `json:"workloadWindowReadyPath"`
	MeasurementEndPath               string                 `json:"measurementEndPath"`
	ObserverReadyTimeoutMilliseconds int64                  `json:"observerReadyTimeoutMilliseconds"`
	MaxLatencySamples                int                    `json:"maxLatencySamples"`
	MaxInputBytes                    int64                  `json:"maxInputBytes"`
	MaxResultBytes                   int64                  `json:"maxResultBytes"`
}

func newCPURuntime(config CPURuntimeConfig, runtimeName, family, backend, requestSchema string) (*CPURuntime, error) {
	if len(config.Command) == 0 || strings.TrimSpace(config.Command[0]) == "" {
		return nil, errors.New(runtimeName + " command is required")
	}
	for _, value := range config.Command {
		if strings.TrimSpace(value) == "" {
			return nil, errors.New(runtimeName + " command contains an empty argument")
		}
	}
	for name, value := range map[string]string{
		"workload-window-start": config.WorkloadWindowStartPath,
		"measurement-start":     config.MeasurementStartPath, "result": config.ResultPath,
		"workload-window-ready": config.WorkloadWindowReadyPath,
		"measurement-end":       config.MeasurementEndPath,
	} {
		if !validAbsoluteFilePath(value) {
			return nil, fmt.Errorf("%s %s path must be a clean absolute file path", runtimeName, name)
		}
	}
	if len(config.ObserverReadyPaths) == 0 {
		return nil, errors.New(runtimeName + " observer-ready paths are required")
	}
	seenReadyPaths := map[string]struct{}{}
	for _, value := range config.ObserverReadyPaths {
		if !validAbsoluteFilePath(value) {
			return nil, errors.New(runtimeName + " observer-ready path must be a clean absolute file path")
		}
		if _, exists := seenReadyPaths[value]; exists {
			return nil, errors.New(runtimeName + " observer-ready paths must be unique")
		}
		seenReadyPaths[value] = struct{}{}
	}
	if config.MaxResultBytes < 1 {
		return nil, errors.New(runtimeName + " result maximum size must be positive")
	}
	if config.MaxLatencySamples < 1 || config.MaxInputBytes < 1 {
		return nil, errors.New(runtimeName + " resource budgets must be supplied by the Job execution contract")
	}
	if config.ObserverReadyTimeout < time.Millisecond {
		return nil, errors.New(runtimeName + " observer-ready timeout must be supplied by the Job execution contract")
	}
	if config.Stdout == nil || config.Stderr == nil {
		return nil, errors.New(runtimeName + " diagnostic writers are required")
	}
	config.Command = append([]string(nil), config.Command...)
	config.ObserverReadyPaths = append([]string(nil), config.ObserverReadyPaths...)
	return &CPURuntime{
		config: config, name: runtimeName, family: family,
		backend: backend, requestSchema: requestSchema,
	}, nil
}

func (r *CPURuntime) Measure(
	ctx context.Context,
	request RuntimeRequest,
) (RuntimeMeasurement, error) {
	runConfig, err := request.RunConfig.Record()
	if err != nil {
		return RuntimeMeasurement{}, err
	}
	if runConfig.State.RuntimeFamily != r.family || runConfig.State.Backend != r.backend {
		return RuntimeMeasurement{}, errors.New(r.name + " adapter received an unsupported runtime path")
	}
	payload, err := r.encodeRequest(request)
	if err != nil {
		return RuntimeMeasurement{}, err
	}
	if err := runRuntimeEntrypoint(
		ctx, r.name, r.config.Command, r.config.ResultPath, payload, r.config.Stdout, r.config.Stderr,
	); err != nil {
		return RuntimeMeasurement{}, err
	}
	if runConfig.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		var result profilingrun.IdleRuntimeResult
		if err := decodeJSONFile(r.config.ResultPath, r.config.MaxResultBytes, &result); err != nil {
			return RuntimeMeasurement{}, fmt.Errorf("read %s idle result: %w", r.name, err)
		}
		verified, err := profilingrun.VerifyIdleRuntimeResult(request.RunConfig, request.PodUID, result)
		if err != nil {
			return RuntimeMeasurement{}, fmt.Errorf("validate %s idle result: %w", r.name, err)
		}
		return idleRuntimeMeasurement(verified), nil
	}
	var result profilingrun.RuntimeResult
	if err := decodeJSONFile(r.config.ResultPath, r.config.MaxResultBytes, &result); err != nil {
		return RuntimeMeasurement{}, fmt.Errorf("read %s result: %w", r.name, err)
	}
	verified, err := profilingrun.VerifyRuntimeResult(request.RunConfig, request.PodUID, result)
	if err != nil {
		return RuntimeMeasurement{}, fmt.Errorf("validate %s result: %w", r.name, err)
	}
	return activeRuntimeMeasurement(verified), nil
}

func (r *CPURuntime) encodeRequest(runtimeRequest RuntimeRequest) ([]byte, error) {
	runConfig, err := runtimeRequest.RunConfig.Record()
	if err != nil {
		return nil, fmt.Errorf("read %s RunConfig: %w", r.name, err)
	}

	request := cpuRuntimeRequest{
		SchemaVersion: r.requestSchema, RunConfig: runConfig,
		PodUID: runtimeRequest.PodUID, ScheduledNodeName: runtimeRequest.ScheduledNodeName,
		ModelDirectory: modelDirectory, PreprocessorPath: preprocessorPath,
		ObserverReadyPaths:               r.config.ObserverReadyPaths,
		WorkloadWindowStartPath:          r.config.WorkloadWindowStartPath,
		MeasurementStartPath:             r.config.MeasurementStartPath,
		WorkloadWindowReadyPath:          r.config.WorkloadWindowReadyPath,
		MeasurementEndPath:               r.config.MeasurementEndPath,
		ObserverReadyTimeoutMilliseconds: r.config.ObserverReadyTimeout.Milliseconds(),
		MaxLatencySamples:                r.config.MaxLatencySamples,
		MaxInputBytes:                    r.config.MaxInputBytes,
		MaxResultBytes:                   r.config.MaxResultBytes,
	}
	if runConfig.Measurement.Dataset != nil {
		request.DatasetPath = datasetPath
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode %s request: %w", r.name, err)
	}
	return payload, nil
}
