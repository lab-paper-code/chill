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

const rknnLiteRequestSchemaVersion = "chill.dacs.io/rknnlite-runtime-request.v1alpha2"

type RKNNLiteRuntimeConfig struct {
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
	Delivery                profilingrun.CandidateRuntimeDelivery
	Stdout                  io.Writer
	Stderr                  io.Writer
}

// RKNNLiteRuntime invokes the trusted RKNNLite entrypoint. The generic result
// validator checks the frozen intent; this adapter additionally compares the
// observed mutable host/runtime facts with the verified candidate delivery.
type RKNNLiteRuntime struct {
	config RKNNLiteRuntimeConfig
}

var _ Runtime = (*RKNNLiteRuntime)(nil)

type rknnLiteRequest struct {
	SchemaVersion                    string                                `json:"schemaVersion"`
	Intent                           profilingrun.Intent                   `json:"intent"`
	IntentDigest                     string                                `json:"intentDigest"`
	PodUID                           string                                `json:"podUID"`
	ScheduledNodeName                string                                `json:"scheduledNodeName"`
	ModelPath                        string                                `json:"modelPath"`
	Candidate                        profilingrun.CandidateRuntimeDelivery `json:"candidate"`
	ObserverReadyPaths               []string                              `json:"observerReadyPaths"`
	WorkloadWindowStartPath          string                                `json:"workloadWindowStartPath"`
	MeasurementStartPath             string                                `json:"measurementStartPath"`
	WorkloadWindowReadyPath          string                                `json:"workloadWindowReadyPath"`
	MeasurementEndPath               string                                `json:"measurementEndPath"`
	ObserverReadyTimeoutMilliseconds int64                                 `json:"observerReadyTimeoutMilliseconds"`
	MaxLatencySamples                int                                   `json:"maxLatencySamples"`
	MaxInputBytes                    int64                                 `json:"maxInputBytes"`
	MaxResultBytes                   int64                                 `json:"maxResultBytes"`
}

func NewRKNNLiteRuntime(config RKNNLiteRuntimeConfig) (*RKNNLiteRuntime, error) {
	if len(config.Command) == 0 || strings.TrimSpace(config.Command[0]) == "" {
		return nil, errors.New("RKNNLite command is required")
	}
	for _, value := range config.Command {
		if strings.TrimSpace(value) == "" {
			return nil, errors.New("RKNNLite command contains an empty argument")
		}
	}
	for name, value := range map[string]string{
		"model":                 config.ModelPath,
		"workload-window-start": config.WorkloadWindowStartPath,
		"measurement-start":     config.MeasurementStartPath,
		"workload-window-ready": config.WorkloadWindowReadyPath,
		"measurement-end":       config.MeasurementEndPath,
		"result":                config.ResultPath,
	} {
		if !validAbsoluteFilePath(value) {
			return nil, fmt.Errorf("RKNNLite %s path must be a clean absolute file path", name)
		}
	}
	if len(config.ObserverReadyPaths) == 0 {
		return nil, errors.New("RKNNLite observer-ready paths are required")
	}
	seenReadyPaths := map[string]struct{}{}
	for _, value := range config.ObserverReadyPaths {
		if !validAbsoluteFilePath(value) {
			return nil, errors.New("RKNNLite observer-ready path must be a clean absolute file path")
		}
		if _, exists := seenReadyPaths[value]; exists {
			return nil, errors.New("RKNNLite observer-ready paths must be unique")
		}
		seenReadyPaths[value] = struct{}{}
	}
	if config.MaxResultBytes < 1 || config.MaxLatencySamples < 1 || config.MaxInputBytes < 1 {
		return nil, errors.New("RKNNLite resource budgets must be supplied by the Job execution contract")
	}
	if config.ObserverReadyTimeout < time.Millisecond {
		return nil, errors.New("RKNNLite observer-ready timeout must be supplied by the Job execution contract")
	}
	if config.Stdout == nil || config.Stderr == nil {
		return nil, errors.New("RKNNLite diagnostic writers are required")
	}
	if err := validateRKNNLiteDelivery(config.Delivery); err != nil {
		return nil, err
	}
	config.Command = append([]string(nil), config.Command...)
	config.ObserverReadyPaths = append([]string(nil), config.ObserverReadyPaths...)
	config.Delivery = copyRuntimeDelivery(config.Delivery)
	return &RKNNLiteRuntime{config: config}, nil
}

func (r *RKNNLiteRuntime) Measure(
	ctx context.Context,
	request RuntimeRequest,
) (RuntimeMeasurement, error) {
	intent, err := request.Intent.snapshot()
	if err != nil {
		return RuntimeMeasurement{}, err
	}
	if !profilingrun.IsSupportedRKNNLiteIntent(intent) {
		return RuntimeMeasurement{}, errors.New("RKNNLite adapter received an unsupported runtime path")
	}
	if _, err := os.Lstat(r.config.ResultPath); err == nil {
		return RuntimeMeasurement{}, errors.New("RKNNLite result path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return RuntimeMeasurement{}, fmt.Errorf("inspect RKNNLite result path: %w", err)
	}
	payload, err := r.encodeRequest(request)
	if err != nil {
		return RuntimeMeasurement{}, err
	}

	arguments := append([]string(nil), r.config.Command[1:]...)
	arguments = append(arguments, "--result-file", r.config.ResultPath)
	command := exec.CommandContext(ctx, r.config.Command[0], arguments...)
	command.Stdin = bytes.NewReader(payload)
	command.Stdout = r.config.Stdout
	command.Stderr = r.config.Stderr
	if err := command.Run(); err != nil {
		return RuntimeMeasurement{}, fmt.Errorf("run RKNNLite entrypoint: %w", err)
	}
	if intent.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		var result profilingrun.IdleRuntimeResult
		if err := decodeJSONFile(r.config.ResultPath, r.config.MaxResultBytes, &result); err != nil {
			return RuntimeMeasurement{}, fmt.Errorf("read RKNNLite idle result: %w", err)
		}
		verified, err := profilingrun.VerifyIdleRuntimeResult(request.Intent.verifiedIntent(), request.PodUID, result)
		if err != nil {
			return RuntimeMeasurement{}, fmt.Errorf("validate RKNNLite idle result: %w", err)
		}
		if err := r.validateObservedContract(result.RuntimeVersion, result.RKNN, nil); err != nil {
			return RuntimeMeasurement{}, err
		}
		return idleRuntimeMeasurement(verified), nil
	}
	var result profilingrun.RuntimeResult
	if err := decodeJSONFile(r.config.ResultPath, r.config.MaxResultBytes, &result); err != nil {
		return RuntimeMeasurement{}, fmt.Errorf("read RKNNLite result: %w", err)
	}
	verified, err := profilingrun.VerifyRuntimeResult(request.Intent.verifiedIntent(), request.PodUID, result)
	if err != nil {
		return RuntimeMeasurement{}, fmt.Errorf("validate RKNNLite result: %w", err)
	}
	if err := r.validateObservedContract(result.RuntimeVersion, result.RKNN, result.InputShape); err != nil {
		return RuntimeMeasurement{}, err
	}
	return activeRuntimeMeasurement(verified), nil
}

func (r *RKNNLiteRuntime) encodeRequest(runtimeRequest RuntimeRequest) ([]byte, error) {
	intent, err := runtimeRequest.Intent.snapshot()
	if err != nil {
		return nil, fmt.Errorf("read RKNNLite intent: %w", err)
	}
	request := rknnLiteRequest{
		SchemaVersion: rknnLiteRequestSchemaVersion, Intent: intent, IntentDigest: runtimeRequest.Intent.Digest(),
		PodUID: runtimeRequest.PodUID, ScheduledNodeName: runtimeRequest.ScheduledNodeName,
		ModelPath: r.config.ModelPath, Candidate: copyRuntimeDelivery(r.config.Delivery),
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
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode RKNNLite request: %w", err)
	}
	return payload, nil
}

func validateRKNNLiteDelivery(delivery profilingrun.CandidateRuntimeDelivery) error {
	if delivery.ArtifactCapability == nil || delivery.HostContract == nil || delivery.HostContract.Platform == nil ||
		delivery.RuntimeAccess == nil {
		return errors.New("verified RKNNLite delivery contract is incomplete")
	}
	if len(delivery.RuntimePackages) != 1 {
		return errors.New("RKNNLite adapter requires exactly one image-owned runtime package")
	}
	capability := delivery.ArtifactCapability
	if capability.FixedBatch < 1 || len(capability.InputShape) == 0 ||
		capability.InputShape[0] != capability.FixedBatch ||
		capability.InputLayout != profilingrun.RKNNLiteInputLayout ||
		capability.InputDataType != profilingrun.RKNNLiteInputDataType {
		return errors.New("verified RKNNLite artifact input contract is unsupported")
	}
	if len(delivery.HostContract.Libraries) == 0 || len(delivery.HostContract.Devices) == 0 {
		return errors.New("verified RKNNLite host contract is incomplete")
	}
	return nil
}

func (r *RKNNLiteRuntime) validateObservedContract(
	runtimeVersion string,
	evidence *profilingrun.RKNNRuntimeEvidence,
	inputShape []int,
) error {
	delivery := r.config.Delivery
	expectedPackage := delivery.RuntimePackages[0]
	if evidence == nil || runtimeVersion != expectedPackage.Version ||
		evidence.Package.Name != expectedPackage.Name || evidence.Package.Version != expectedPackage.Version {
		return errors.New("observed RKNNLite package does not match the verified candidate")
	}
	if inputShape != nil {
		expectedShape := delivery.ArtifactCapability.InputShape
		if len(inputShape) != len(expectedShape) {
			return errors.New("observed RKNNLite input shape does not match the verified artifact")
		}
		for index := range inputShape {
			if inputShape[index] != int(expectedShape[index]) {
				return errors.New("observed RKNNLite input shape does not match the verified artifact")
			}
		}
	}
	expectedLibraries := make(map[string]profilingrun.CandidateReportHostLibrary, len(delivery.HostContract.Libraries))
	for _, library := range delivery.HostContract.Libraries {
		expectedLibraries[library.Name] = library
	}
	if len(evidence.HostLibraries) != len(expectedLibraries) {
		return errors.New("observed RKNNLite host libraries do not match the verified candidate")
	}
	for _, observed := range evidence.HostLibraries {
		expected, exists := expectedLibraries[observed.Name]
		if !exists || observed.Path != expected.ContainerPath || observed.Digest != expected.Digest {
			return errors.New("observed RKNNLite host libraries do not match the verified candidate")
		}
	}
	expectedDriver := delivery.HostContract.Driver
	if evidence.Driver.Name != expectedDriver.Name || evidence.Driver.VersionPath != expectedDriver.VersionPath ||
		evidence.Driver.Version != expectedDriver.Version {
		return errors.New("observed RKNNLite driver does not match the verified candidate")
	}
	expectedPlatform := delivery.HostContract.Platform
	if evidence.Platform.CompatiblePath != expectedPlatform.CompatiblePath ||
		evidence.Platform.CompatibleToken != expectedPlatform.CompatibleToken || !evidence.Platform.Matched {
		return errors.New("observed RKNNLite platform does not match the verified candidate")
	}
	expectedDevices := make(map[string]struct{}, len(delivery.HostContract.Devices))
	for _, device := range delivery.HostContract.Devices {
		expectedDevices[device.Path] = struct{}{}
	}
	if len(evidence.Devices) != len(expectedDevices) {
		return errors.New("observed RKNNLite devices do not match the verified candidate")
	}
	for _, observed := range evidence.Devices {
		if _, exists := expectedDevices[observed.Path]; !exists || !observed.CharacterDevice || !observed.Opened {
			return errors.New("observed RKNNLite devices do not match the verified candidate")
		}
	}
	return nil
}

func copyRuntimeDelivery(delivery profilingrun.CandidateRuntimeDelivery) profilingrun.CandidateRuntimeDelivery {
	copy := delivery
	if delivery.ArtifactCapability != nil {
		capability := *delivery.ArtifactCapability
		capability.InputShape = append([]int32(nil), capability.InputShape...)
		copy.ArtifactCapability = &capability
	}
	copy.RuntimePackages = append([]profilingrun.CandidateReportRuntimePackage(nil), delivery.RuntimePackages...)
	if delivery.HostContract != nil {
		contract := *delivery.HostContract
		contract.Libraries = append([]profilingrun.CandidateReportHostLibrary(nil), contract.Libraries...)
		contract.Devices = append([]profilingrun.CandidateReportHostDevice(nil), contract.Devices...)
		if contract.Platform != nil {
			platform := *contract.Platform
			contract.Platform = &platform
		}
		copy.HostContract = &contract
	}
	if delivery.RuntimeAccess != nil {
		access := *delivery.RuntimeAccess
		access.SupplementalGroups = append([]int64(nil), access.SupplementalGroups...)
		copy.RuntimeAccess = &access
	}
	return copy
}
