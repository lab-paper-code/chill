package controller

import (
	"errors"
	"fmt"
	"reflect"
	"slices"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

// ResolvedRun is the controller-side form of one executable Run. It exists in
// memory while a Job is created and is reconstructed from that Job afterwards.
type ResolvedRun struct {
	Prepared        profiler.PreparedRun
	ExecutionPolicy profiler.FrozenExecutionPolicy
}

func (r ResolvedRun) Validate() error {
	if err := r.Prepared.RunConfig.Validate(); err != nil {
		return fmt.Errorf("validate RunConfig: %w", err)
	}
	if err := r.ExecutionPolicy.Validate(); err != nil {
		return err
	}
	if err := r.Prepared.ValidateActuationBinding(); err != nil {
		return err
	}
	return nil
}

func validateResolvedRunForRun(run *edgev1alpha1.ProfilingRun, resolved ResolvedRun) error {
	if run == nil {
		return errors.New("ProfilingRun is required")
	}
	if err := resolved.Validate(); err != nil {
		return err
	}
	if err := validatePreparedForRun(run, resolved.Prepared); err != nil {
		return err
	}
	return validateSelectedExecutionPolicy(resolved.Prepared, resolved.ExecutionPolicy)
}

func validatePreparedForRun(run *edgev1alpha1.ProfilingRun, prepared profiler.PreparedRun) error {
	request, err := ProjectRequest(run.Spec)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(request.Preparation, preparationFromRunConfig(prepared.RunConfig)) {
		return errors.New("RunConfig does not match ProfilingRun spec")
	}
	return nil
}

func preparationFromRunConfig(config profilingrun.RunConfig) profiler.PreparationRequest {
	request := profiler.PreparationRequest{
		Candidate: profiler.CandidateSelection{
			ModelSpec: config.State.Model, DeviceClass: config.State.DeviceClass,
			ExecutionPath: config.Candidate.ExecutionPath, PowerMode: config.State.PowerMode,
		},
		TargetNode:  config.TargetNode.Name,
		CPURequest:  config.CPU.Request,
		CPUAffinity: slices.Clone(config.CPU.AffinityCPUs),
		Measurement: config.Measurement,
		Metrics: profiler.MetricObservationPolicy{
			IntervalMilliseconds: config.Metrics.IntervalMilliseconds,
			TimeoutMilliseconds:  config.Metrics.TimeoutMilliseconds,
			DurationSeconds:      config.Metrics.DurationSeconds,
		},
	}
	if config.CPU.Policy == profilingrun.ConfiguredCPUQuotaPolicy {
		request.CPUQuota = config.CPU.Limit
	}
	return request
}

func validateSelectedExecutionPolicy(
	prepared profiler.PreparedRun,
	policy profiler.FrozenExecutionPolicy,
) error {
	if (policy.Policy.DeviceClass != "*" && policy.Policy.DeviceClass != prepared.RunConfig.State.DeviceClass) ||
		policy.Policy.ExecutionPath != prepared.RunConfig.Candidate.ExecutionPath ||
		policy.Policy.WorkloadMode != prepared.RunConfig.Measurement.WorkloadMode {
		return profiler.InvalidExecutionPolicy("selected execution policy does not match RunConfig")
	}
	return nil
}
