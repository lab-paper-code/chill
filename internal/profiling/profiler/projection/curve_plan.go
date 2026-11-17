package projection

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

// ProjectCurvePlanSpec builds the curve plan spec for one active and idle
// RunConfig pair and checks both RunConfigs against it.
func ProjectCurvePlanSpec(
	name string,
	active, idle profilingrun.RunConfig,
	conversion profilederivation.CurveConversionContract,
	plannedBatchSizes []int,
) (profilederivation.CurvePlanSpec, error) {
	activeProtocol := projectCurveProtocol(active)
	idleProtocol := projectCurveProtocol(idle)

	spec := profilederivation.CurvePlanSpec{
		Name:           name,
		LogicalModel:   active.State.Model,
		DeviceClass:    active.State.DeviceClass,
		NodeUID:        active.TargetNode.UID,
		RuntimeFamily:  active.State.RuntimeFamily,
		Backend:        active.State.Backend,
		Firstfloor:     modelcompat.CloneFirstfloorRuntimeSettings(active.State.Firstfloor),
		RuntimeAccess:  projectCurveRuntimeAccess(active.Delivery.RuntimeAccess),
		RuntimeImage:   active.Images.Runtime,
		Actor:          active.Actor,
		PowerMode:      active.State.PowerMode,
		MetricSource:   projectCurveMetricSource(active.Metrics),
		ActiveProtocol: activeProtocol,
		IdleProtocol:   idleProtocol,
		Conversion:     conversion,
		Artifact: profilederivation.CurveArtifactContract{
			ExecutionPath: active.Candidate.ExecutionPath,
			Name:          active.State.Artifact,
			Files:         slices.Clone(active.State.ArtifactFiles),
			Image:         active.Images.Artifact,
		},
		Preprocessor:      projectCurvePreprocessor(active.State.Preprocessor),
		PlannedBatchSizes: append([]int(nil), plannedBatchSizes...),
	}
	if err := validateCurvePlanRunConfig(active, active.TargetNode.UID, spec, spec.ActiveProtocol); err != nil {
		return profilederivation.CurvePlanSpec{}, fmt.Errorf("active curve RunConfig disagrees: %w", err)
	}
	if err := validateCurvePlanSharedRunFacts(active, idle); err != nil {
		return profilederivation.CurvePlanSpec{}, fmt.Errorf("active and idle curve RunConfigs disagree: %w", err)
	}
	if err := validateCurvePlanRunConfig(idle, idle.TargetNode.UID, spec, spec.IdleProtocol); err != nil {
		return profilederivation.CurvePlanSpec{}, fmt.Errorf("active and idle curve RunConfigs disagree: %w", err)
	}
	return spec, nil
}

func validateCurvePlanSharedRunFacts(active, idle profilingrun.RunConfig) error {
	if active.TargetNode.Architecture != idle.TargetNode.Architecture {
		return errors.New("active and idle recorded candidate or Node snapshots disagree")
	}
	if (active.Actuation == nil) != (idle.Actuation == nil) ||
		active.Actuation != nil && (active.Actuation.PowerMode != idle.Actuation.PowerMode ||
			!reflect.DeepEqual(active.Actuation.Actuator, idle.Actuation.Actuator)) {
		return errors.New("active and idle power actuation conditions disagree")
	}
	return nil
}

func validateCurvePlanRunConfig(
	config profilingrun.RunConfig,
	actualNodeUID string,
	spec profilederivation.CurvePlanSpec,
	protocol profilederivation.CurveProtocolContract,
) error {
	if config.Candidate.ExecutionPath != spec.Artifact.ExecutionPath ||
		config.State.DeviceClass != spec.DeviceClass || config.State.Model != spec.LogicalModel ||
		config.State.Artifact != spec.Artifact.Name ||
		!slices.Equal(config.State.ArtifactFiles, spec.Artifact.Files) || config.State.RuntimeFamily != spec.RuntimeFamily ||
		config.State.Backend != spec.Backend || config.State.PowerMode != spec.PowerMode ||
		config.Images.Runtime != spec.RuntimeImage ||
		config.Images.Artifact != spec.Artifact.Image || config.TargetNode.UID != spec.NodeUID ||
		actualNodeUID != spec.NodeUID {
		return errors.New("RunConfig contradicts curve-plan state or artifact facts")
	}
	actualActor := profilederivation.PerformanceCondition(profilederivation.CurvePlanSpec{Actor: config.Actor}).Actor
	expectedActor := profilederivation.PerformanceCondition(profilederivation.CurvePlanSpec{Actor: spec.Actor}).Actor
	if !reflect.DeepEqual(actualActor, expectedActor) {
		return errors.New("RunConfig contradicts curve-plan actor facts")
	}
	if !reflect.DeepEqual(projectCurveRuntimeAccess(config.Delivery.RuntimeAccess), spec.RuntimeAccess) {
		return errors.New("RunConfig contradicts curve-plan accelerator delivery")
	}
	preprocessor := config.State.Preprocessor
	if preprocessor.Image != spec.Preprocessor.Image || preprocessor.Architecture != spec.Preprocessor.Architecture ||
		preprocessor.Path != spec.Preprocessor.Path || preprocessor.MaxNewTokens != spec.Preprocessor.MaxNewTokens ||
		!slices.Equal(preprocessor.Inputs, spec.Preprocessor.Inputs) || !slices.Equal(preprocessor.Outputs, spec.Preprocessor.Outputs) {
		return errors.New("RunConfig contradicts curve-plan preprocessor facts")
	}
	if config.Measurement.WorkloadMode != protocol.WorkloadMode {
		return errors.New("RunConfig contradicts curve-plan workload mode")
	}
	if (config.Measurement.Dataset == nil) != (protocol.Dataset == nil) ||
		config.Measurement.Dataset != nil && config.Measurement.Dataset.Name != protocol.Dataset.Name {
		return errors.New("RunConfig contradicts curve-plan dataset selection")
	}
	if !reflect.DeepEqual((*profilederivation.OperatingPointsContract)(config.Measurement.OperatingPoints), protocol.OperatingPoints) {
		return errors.New("RunConfig contradicts curve-plan operatingPoints")
	}
	return nil
}

func projectCurvePreprocessor(state profilingrun.PreprocessorState) profilederivation.CurvePreprocessorContract {
	return profilederivation.CurvePreprocessorContract{
		Name: state.Name, Image: state.Image, Architecture: state.Architecture, Path: state.Path,
		MaxNewTokens: state.MaxNewTokens, Inputs: slices.Clone(state.Inputs), Outputs: slices.Clone(state.Outputs),
	}
}

func projectCurveRuntimeAccess(access *profilingrun.CandidateReportRuntimeAccess) *profilederivation.CurveRuntimeAccessContract {
	if access == nil {
		return nil
	}
	return &profilederivation.CurveRuntimeAccessContract{
		Privileged: access.Privileged, SupplementalGroups: append([]int64(nil), access.SupplementalGroups...),
		Resources: maps.Clone(access.Resources), GPUUUID: access.GPUUUID,
	}
}

func projectCurveMetricSource(metrics profilingrun.MetricContract) profilederivation.CurveMetricSourceContract {
	return profilederivation.CurveMetricSourceContract{
		SourceKind: metrics.SourceKind, NodeName: metrics.NodeName, Endpoint: metrics.Endpoint,
		PowerMetric: metrics.PowerMetric,
	}
}

func projectCurveProtocol(config profilingrun.RunConfig) profilederivation.CurveProtocolContract {
	var dataset *profilederivation.CurveDatasetContract
	var points *profilederivation.OperatingPointsContract
	if config.Measurement.OperatingPoints != nil {
		frozen := profilederivation.OperatingPointsContract(*config.Measurement.OperatingPoints)
		frozen.Utilizations = slices.Clone(frozen.Utilizations)
		points = &frozen
	}
	if config.Measurement.Dataset != nil {
		dataset = &profilederivation.CurveDatasetContract{
			Name: config.Measurement.Dataset.Name, UID: config.Measurement.Dataset.UID,
		}
	}
	return profilederivation.CurveProtocolContract{
		WorkloadMode:               config.Measurement.WorkloadMode,
		Dataset:                    dataset,
		WarmupIterations:           config.Measurement.WarmupIterations,
		StartupWarmupIterations:    config.Measurement.StartupWarmupIterations,
		SettlingDurationSeconds:    config.Measurement.SettlingDurationSeconds,
		DurationSeconds:            config.Measurement.DurationSeconds,
		DurationToleranceSeconds:   config.Measurement.DurationToleranceSeconds,
		SLOPercentile:              config.Measurement.SLOPercentile,
		MinServiceSamples:          config.Measurement.MinServiceSamples,
		SLOTargetLatencyNanos:      config.Measurement.SLOTargetLatencyNanos,
		WorkloadName:               config.Measurement.WorkloadName,
		WorkloadUID:                config.Measurement.WorkloadUID,
		WorkloadResourceVersion:    config.Measurement.WorkloadResourceVersion,
		MetricIntervalMilliseconds: config.Metrics.IntervalMilliseconds,
		MetricTimeoutMilliseconds:  config.Metrics.TimeoutMilliseconds,
		MetricDurationSeconds:      config.Metrics.DurationSeconds,
		MetricObserverImage:        config.Images.MetricObserver,
		WorkloadObserverImage:      config.Images.WorkloadObserver,
		WorkloadSchemaVersion:      config.Workload.SchemaVersion,
		OperatingPoints:            points,
	}
}
