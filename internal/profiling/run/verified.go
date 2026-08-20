package profilingrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
)

// VerifiedIntent is populated only after one intent has passed structural and
// semantic validation.
type VerifiedIntent struct {
	intent   Intent
	verified bool
}

func ValidateIntent(intent Intent) (VerifiedIntent, error) {
	if err := intent.Validate(); err != nil {
		return VerifiedIntent{}, err
	}
	return VerifiedIntent{intent: cloneIntent(intent), verified: true}, nil
}

func (i VerifiedIntent) Record() (Intent, error) {
	if !i.verified {
		return Intent{}, errors.New("verified Run intent is empty")
	}
	return cloneIntent(i.intent), nil
}

// VerifiedRuntimeResult is an active process result bound to one verified
// intent and Pod identity.
type VerifiedRuntimeResult struct {
	payload  []byte
	verified bool
}

func VerifyRuntimeResult(
	intent VerifiedIntent,
	podUID string,
	result RuntimeResult,
) (VerifiedRuntimeResult, error) {
	intentRecord, err := intent.Record()
	if err != nil {
		return VerifiedRuntimeResult{}, err
	}
	if err := ValidateRuntime(intentRecord, podUID, result); err != nil {
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
// verified intent and Pod identity.
type VerifiedIdleRuntimeResult struct {
	payload  []byte
	verified bool
}

func VerifyIdleRuntimeResult(
	intent VerifiedIntent,
	podUID string,
	result IdleRuntimeResult,
) (VerifiedIdleRuntimeResult, error) {
	intentRecord, err := intent.Record()
	if err != nil {
		return VerifiedIdleRuntimeResult{}, err
	}
	if err := ValidateIdleRuntime(intentRecord, podUID, result); err != nil {
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

func cloneIntent(source Intent) Intent {
	result := source
	result.State.HostContract = cloneCandidateHostContract(source.State.HostContract)
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
