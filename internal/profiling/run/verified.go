package profilingrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/lab-paper-code/chill/internal/modelcompat"
)

// VerifiedRunConfig is populated only after one RunConfig has passed
// structural and semantic validation.
type VerifiedRunConfig struct {
	runConfig RunConfig
	verified  bool
}

func ValidateRunConfig(runConfig RunConfig) (VerifiedRunConfig, error) {
	if err := runConfig.Validate(); err != nil {
		return VerifiedRunConfig{}, err
	}
	return VerifiedRunConfig{runConfig: cloneRunConfig(runConfig), verified: true}, nil
}

func (c VerifiedRunConfig) Record() (RunConfig, error) {
	if !c.verified {
		return RunConfig{}, errors.New("verified RunConfig is empty")
	}
	return cloneRunConfig(c.runConfig), nil
}

// VerifiedRuntimeResult is an active process result bound to one verified
// RunConfig and Pod identity.
type VerifiedRuntimeResult struct {
	payload  []byte
	verified bool
}

func VerifyRuntimeResult(
	runConfig VerifiedRunConfig,
	podUID string,
	result RuntimeResult,
) (VerifiedRuntimeResult, error) {
	runConfigRecord, err := runConfig.Record()
	if err != nil {
		return VerifiedRuntimeResult{}, err
	}
	if err := ValidateRuntime(runConfigRecord, podUID, result); err != nil {
		return VerifiedRuntimeResult{}, err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return VerifiedRuntimeResult{}, fmt.Errorf("encode verified runtime result: %w", err)
	}
	return VerifiedRuntimeResult{payload: payload, verified: true}, nil
}

func (r VerifiedRuntimeResult) Record() (RuntimeResult, error) {
	if !r.verified {
		return RuntimeResult{}, errors.New("verified runtime result is empty")
	}
	var result RuntimeResult
	if err := json.Unmarshal(r.payload, &result); err != nil {
		return RuntimeResult{}, fmt.Errorf("decode verified runtime result: %w", err)
	}
	return result, nil
}

// VerifiedIdleRuntimeResult is a model-loaded-idle process result bound to one
// verified RunConfig and Pod identity.
type VerifiedIdleRuntimeResult struct {
	payload  []byte
	verified bool
}

func VerifyIdleRuntimeResult(
	runConfig VerifiedRunConfig,
	podUID string,
	result IdleRuntimeResult,
) (VerifiedIdleRuntimeResult, error) {
	runConfigRecord, err := runConfig.Record()
	if err != nil {
		return VerifiedIdleRuntimeResult{}, err
	}
	if err := ValidateIdleRuntime(runConfigRecord, podUID, result); err != nil {
		return VerifiedIdleRuntimeResult{}, err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return VerifiedIdleRuntimeResult{}, fmt.Errorf("encode verified idle runtime result: %w", err)
	}
	return VerifiedIdleRuntimeResult{payload: payload, verified: true}, nil
}

func (r VerifiedIdleRuntimeResult) Record() (IdleRuntimeResult, error) {
	if !r.verified {
		return IdleRuntimeResult{}, errors.New("verified idle runtime result is empty")
	}
	var result IdleRuntimeResult
	if err := json.Unmarshal(r.payload, &result); err != nil {
		return IdleRuntimeResult{}, fmt.Errorf("decode verified idle runtime result: %w", err)
	}
	return result, nil
}

func cloneRunConfig(source RunConfig) RunConfig {
	result := source
	result.CPU.AffinityCPUs = slices.Clone(source.CPU.AffinityCPUs)
	result.State.Firstfloor = modelcompat.CloneFirstfloorRuntimeSettings(source.State.Firstfloor)
	result.State.HostContract = cloneCandidateHostContract(source.State.HostContract)
	result.Delivery = cloneRuntimeDelivery(source.Delivery)
	result.Metrics.MetricLabels = maps.Clone(source.Metrics.MetricLabels)
	if source.Actuation != nil {
		actuation := *source.Actuation
		if source.Actuation.Actuator.JetsonNVPModel != nil {
			target := *source.Actuation.Actuator.JetsonNVPModel
			actuation.Actuator.JetsonNVPModel = &target
		}
		if source.Actuation.Actuator.NvidiaLockedGPUClock != nil {
			target := *source.Actuation.Actuator.NvidiaLockedGPUClock
			if source.Actuation.Actuator.NvidiaLockedGPUClock.Mode != nil {
				mode := *source.Actuation.Actuator.NvidiaLockedGPUClock.Mode
				target.Mode = &mode
			}
			actuation.Actuator.NvidiaLockedGPUClock = &target
		}
		result.Actuation = &actuation
	}
	return result
}

func cloneRuntimeDelivery(source CandidateRuntimeDelivery) CandidateRuntimeDelivery {
	result := source
	if source.ArtifactCapability != nil {
		capability := *source.ArtifactCapability
		capability.DeclaredBatchSizes = append([]int32(nil), source.ArtifactCapability.DeclaredBatchSizes...)
		capability.Tensors = modelcompat.CloneInputTensors(source.ArtifactCapability.Tensors)
		result.ArtifactCapability = &capability
	}
	result.RuntimePackages = append([]CandidateReportRuntimePackage(nil), source.RuntimePackages...)
	result.HostContract = cloneCandidateHostContract(source.HostContract)
	if source.RuntimeAccess != nil {
		access := *source.RuntimeAccess
		access.SupplementalGroups = append([]int64(nil), source.RuntimeAccess.SupplementalGroups...)
		access.Resources = maps.Clone(source.RuntimeAccess.Resources)
		result.RuntimeAccess = &access
	}
	return result
}

func cloneCandidateHostContract(source *CandidateReportHostContract) *CandidateReportHostContract {
	if source == nil {
		return nil
	}
	result := *source
	result.Libraries = append([]CandidateReportHostLibrary(nil), source.Libraries...)
	result.Devices = append([]CandidateReportHostDevice(nil), source.Devices...)
	if source.Platform != nil {
		platform := *source.Platform
		result.Platform = &platform
	}
	return &result
}
