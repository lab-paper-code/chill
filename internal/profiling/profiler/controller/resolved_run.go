package controller

import (
	"errors"
	"fmt"
	"reflect"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	// Owned requests are authenticated once by Resolve; recovered Jobs carry
	// their executable record, not an authoritative child request body.
	if metav1.GetControllerOf(run) != nil {
		return nil
	}
	request, err := ProjectRequest(run.Spec)
	if err != nil {
		return err
	}
	actual := preparationFromRunConfig(prepared.RunConfig)
	// ResourceVersion remains in raw RunConfig, but never gates admission.
	request.Preparation.Measurement.WorkloadResourceVersion = ""
	if !reflect.DeepEqual(request.Preparation, actual) {
		return errors.New("RunConfig does not match ProfilingRun spec")
	}
	return nil
}

func preparationFromRunConfig(config profilingrun.RunConfig) profiler.PreparationRequest {
	measurement := config.Measurement
	request := profiler.PreparationRequest{
		Candidate: profiler.CandidateSelection{
			ModelSpec: config.State.Model, DeviceClass: config.State.DeviceClass, Node: config.TargetNode.Name,
			ExecutionPath: config.Candidate.ExecutionPath, PowerMode: config.State.PowerMode,
			Preprocessor: config.State.Preprocessor.Name,
		},
		TargetNode: config.TargetNode.Name,
		Workload:   measurement.WorkloadName,
		// Standalone source facts are resolved, not selected. Campaign source
		// facts are added to the comparison above only when the spec freezes them.
		Measurement: profilingrun.MeasurementContract{
			Batch:                    measurement.Batch,
			WarmupIterations:         measurement.WarmupIterations,
			StartupWarmupIterations:  measurement.StartupWarmupIterations,
			SettlingDurationSeconds:  measurement.SettlingDurationSeconds,
			DurationSeconds:          measurement.DurationSeconds,
			DurationToleranceSeconds: measurement.DurationToleranceSeconds,
			WarmupBatchSizes:         measurement.WarmupBatchSizes,
			WorkloadMode:             measurement.WorkloadMode,
			OperatingPoints:          measurement.OperatingPoints,
		},
		Metrics: profiler.MetricObservationPolicy{
			IntervalMilliseconds: config.Metrics.IntervalMilliseconds,
			TimeoutMilliseconds:  config.Metrics.TimeoutMilliseconds,
			DurationSeconds:      config.Metrics.DurationSeconds,
		},
	}
	if measurement.OperatingPoints != nil {
		points := *measurement.OperatingPoints
		points.QueueBytesCap = 0
		request.Measurement.OperatingPoints = &points
	}
	if config.Measurement.Dataset != nil {
		request.Candidate.Dataset = config.Measurement.Dataset.Name
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
