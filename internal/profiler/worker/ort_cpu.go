package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/profilingrun"
)

const ortCPURequestSchemaVersion = "chill.dacs.io/ort-cpu-runtime-request.v1alpha7"

type ORTCPURuntimeConfig struct {
	Command                 []string
	ModelPath               string
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

// ORTCPURuntime invokes the trusted CPU ONNX Runtime entrypoint and accepts its
// result only from a dedicated JSON file. Stdout and stderr remain diagnostics.
type ORTCPURuntime struct {
	config ORTCPURuntimeConfig
}

var _ Runtime = (*ORTCPURuntime)(nil)

type ortCPURequest struct {
	SchemaVersion                    string              `json:"schemaVersion"`
	Intent                           profilingrun.Intent `json:"intent"`
	IntentDigest                     string              `json:"intentDigest"`
	PodUID                           string              `json:"podUID"`
	ScheduledNodeName                string              `json:"scheduledNodeName"`
	ModelPath                        string              `json:"modelPath"`
	ObserverReadyPaths               []string            `json:"observerReadyPaths"`
	WorkloadWindowStartPath          string              `json:"workloadWindowStartPath"`
	MeasurementStartPath             string              `json:"measurementStartPath"`
	WorkloadWindowReadyPath          string              `json:"workloadWindowReadyPath"`
	MeasurementEndPath               string              `json:"measurementEndPath"`
	ObserverReadyTimeoutMilliseconds int64               `json:"observerReadyTimeoutMilliseconds"`
	MaxLatencySamples                int                 `json:"maxLatencySamples"`
	MaxInputBytes                    int64               `json:"maxInputBytes"`
	MaxResultBytes                   int64               `json:"maxResultBytes"`
}

func NewORTCPURuntime(config ORTCPURuntimeConfig) (*ORTCPURuntime, error) {
	if len(config.Command) == 0 || strings.TrimSpace(config.Command[0]) == "" {
		return nil, errors.New("CPU ORT command is required")
	}
	for _, value := range config.Command {
		if strings.TrimSpace(value) == "" {
			return nil, errors.New("CPU ORT command contains an empty argument")
		}
	}
	for name, value := range map[string]string{
		"model":                 config.ModelPath,
		"workload-window-start": config.WorkloadWindowStartPath,
		"measurement-start":     config.MeasurementStartPath, "result": config.ResultPath,
		"workload-window-ready": config.WorkloadWindowReadyPath,
		"measurement-end":       config.MeasurementEndPath,
	} {
		if !validAbsoluteFilePath(value) {
			return nil, fmt.Errorf("CPU ORT %s path must be a clean absolute file path", name)
		}
	}
	if len(config.ObserverReadyPaths) == 0 {
		return nil, errors.New("CPU ORT observer-ready paths are required")
	}
	seenReadyPaths := map[string]struct{}{}
	for _, value := range config.ObserverReadyPaths {
		if !validAbsoluteFilePath(value) {
			return nil, errors.New("CPU ORT observer-ready path must be a clean absolute file path")
		}
		if _, exists := seenReadyPaths[value]; exists {
			return nil, errors.New("CPU ORT observer-ready paths must be unique")
		}
		seenReadyPaths[value] = struct{}{}
	}
	if config.MaxResultBytes < 1 {
		return nil, errors.New("CPU ORT result maximum size must be positive")
	}
	if config.MaxLatencySamples < 1 || config.MaxInputBytes < 1 {
		return nil, errors.New("CPU ORT resource budgets must be supplied by the Job execution contract")
	}
	if config.ObserverReadyTimeout < time.Millisecond {
		return nil, errors.New("CPU ORT observer-ready timeout must be supplied by the Job execution contract")
	}
	if config.Stdout == nil || config.Stderr == nil {
		return nil, errors.New("CPU ORT diagnostic writers are required")
	}
	config.Command = append([]string(nil), config.Command...)
	config.ObserverReadyPaths = append([]string(nil), config.ObserverReadyPaths...)
	return &ORTCPURuntime{config: config}, nil
}

func (r *ORTCPURuntime) Measure(
	ctx context.Context,
	request RuntimeRequest,
) (profilingrun.RuntimeResult, error) {
	intent := request.Intent
	if intent.State.RuntimeFamily != "onnxruntime" || intent.State.Backend != "CPUExecutionProvider" {
		return profilingrun.RuntimeResult{}, errors.New("CPU ORT adapter received an unsupported runtime path")
	}
	if _, err := os.Lstat(r.config.ResultPath); err == nil {
		return profilingrun.RuntimeResult{}, errors.New("CPU ORT result path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return profilingrun.RuntimeResult{}, fmt.Errorf("inspect CPU ORT result path: %w", err)
	}
	payload, err := r.encodeRequest(request)
	if err != nil {
		return profilingrun.RuntimeResult{}, err
	}

	arguments := append([]string(nil), r.config.Command[1:]...)
	arguments = append(arguments, "--result-file", r.config.ResultPath)
	command := exec.CommandContext(ctx, r.config.Command[0], arguments...)
	command.Stdin = bytes.NewReader(payload)
	command.Stdout = r.config.Stdout
	command.Stderr = r.config.Stderr
	if err := command.Run(); err != nil {
		return profilingrun.RuntimeResult{}, fmt.Errorf("run CPU ORT entrypoint: %w", err)
	}

	var result profilingrun.RuntimeResult
	if err := decodeJSONFile(r.config.ResultPath, r.config.MaxResultBytes, &result); err != nil {
		return profilingrun.RuntimeResult{}, fmt.Errorf("read CPU ORT result: %w", err)
	}
	if err := profilingrun.ValidateRuntime(intent, request.PodUID, result); err != nil {
		return profilingrun.RuntimeResult{}, fmt.Errorf("validate CPU ORT result: %w", err)
	}
	return result, nil
}

func (r *ORTCPURuntime) encodeRequest(runtimeRequest RuntimeRequest) ([]byte, error) {
	intent := runtimeRequest.Intent
	intentDigest, err := intent.Digest()
	if err != nil {
		return nil, fmt.Errorf("validate CPU ORT intent: %w", err)
	}

	request := ortCPURequest{
		SchemaVersion: ortCPURequestSchemaVersion, Intent: intent, IntentDigest: intentDigest,
		PodUID: runtimeRequest.PodUID, ScheduledNodeName: runtimeRequest.ScheduledNodeName,
		ModelPath: r.config.ModelPath, ObserverReadyPaths: r.config.ObserverReadyPaths,
		WorkloadWindowStartPath:          r.config.WorkloadWindowStartPath,
		MeasurementStartPath:             r.config.MeasurementStartPath,
		WorkloadWindowReadyPath:          r.config.WorkloadWindowReadyPath,
		MeasurementEndPath:               r.config.MeasurementEndPath,
		ObserverReadyTimeoutMilliseconds: r.config.ObserverReadyTimeout.Milliseconds(),
		MaxLatencySamples:                r.config.MaxLatencySamples,
		MaxInputBytes:                    r.config.MaxInputBytes,
		MaxResultBytes:                   r.config.MaxResultBytes,
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode CPU ORT request: %w", err)
	}
	return payload, nil
}
