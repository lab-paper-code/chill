package projection

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

// ApplyFrozenActiveProtocol projects only the plan's authoritative measurement
// body. Public child specs carry no frozen Workload facts.
func ApplyFrozenActiveProtocol(
	measurement *profilingrun.MeasurementContract, protocol profilederivation.CurveProtocolContract,
) {
	measurement.WorkloadName = protocol.WorkloadName
	measurement.WorkloadUID = protocol.WorkloadUID
	measurement.WorkloadResourceVersion = protocol.WorkloadResourceVersion
	measurement.SLOPercentile = protocol.SLOPercentile
	measurement.SLOTargetLatencyNanos = protocol.SLOTargetLatencyNanos
	measurement.MinServiceSamples = protocol.MinServiceSamples
}

// ValidateRecordedIdleEvidence applies the existing curve-plan RunConfig
// boundary to a retained idle witness without requiring an active execution.
func ValidateRecordedIdleEvidence(plan profilederivation.CurvePlan, evidence profilerevidence.VerifiedIdleEvidence) error {
	execution, _, err := evidence.Records()
	if err != nil {
		return err
	}
	shared := plan.Shared()
	return validateCurvePlanRunConfig(execution.RunConfig, execution.Terminal.Node.UID, shared, shared.IdleProtocol)
}

// ProjectCurvePlanSpec builds the plan the checker in this package later
// validates RunConfigs against.
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

// ProjectMeasuredCurveCondition attaches observed semantic runtime facts without
// treating an exact image reference as a version or introducing a validity gate.
func ProjectMeasuredCurveCondition(
	condition profilederivation.CurvePlanSpec,
	active profilerevidence.VerifiedEvidence,
) (profilederivation.CurvePlanSpec, error) {
	_, raw, err := active.Records()
	if err != nil {
		return profilederivation.CurvePlanSpec{}, err
	}
	condition.RuntimeVersion = raw.Runtime.RuntimeVersion
	condition.RuntimeProvider = raw.Runtime.Provider
	return condition, nil
}

// ProjectVerifiedCurveState binds Loader-verified execution conditions to the
// frozen curve plan. Digest labels and external environments are provenance,
// not validation gates; runtime validity belongs to the Loader.
func ProjectVerifiedCurveState(
	plan profilederivation.CurvePlan,
	active profilerevidence.VerifiedEvidence,
	idle profilerevidence.VerifiedIdleEvidence,
) (profilederivation.VerifiedCurveState, error) {
	activeExecution, activeRaw, err := active.Records()
	if err != nil {
		return profilederivation.VerifiedCurveState{}, fmt.Errorf("read verified active evidence: %w", err)
	}
	idleExecution, _, err := idle.Records()
	if err != nil {
		return profilederivation.VerifiedCurveState{}, fmt.Errorf("read verified idle evidence: %w", err)
	}
	activeIdentities, err := ProjectRunIdentities(activeExecution.RunConfig, activeExecution.Terminal.Node.UID)
	if err != nil {
		return profilederivation.VerifiedCurveState{}, err
	}
	idleIdentities, err := ProjectRunIdentities(idleExecution.RunConfig, idleExecution.Terminal.Node.UID)
	if err != nil {
		return profilederivation.VerifiedCurveState{}, err
	}
	start, end, err := activeRaw.Runtime.MeasurementWindow()
	if err != nil {
		return profilederivation.VerifiedCurveState{}, err
	}
	workload, err := projectWorkloadWindow(activeRaw.Workload, start, end)
	if err != nil {
		return profilederivation.VerifiedCurveState{}, err
	}
	spec := plan.Shared()
	if err := validateCurvePlanRunConfigs(
		activeExecution.RunConfig, idleExecution.RunConfig,
		activeExecution.Terminal.Node.UID, idleExecution.Terminal.Node.UID,
		spec,
	); err != nil {
		return profilederivation.VerifiedCurveState{}, err
	}
	return profilederivation.VerifiedCurveState{
		Shared:                 spec,
		StateIdentity:          activeIdentities.StateIdentity,
		EnvironmentIdentity:    workload.EnvironmentIdentity,
		ActiveProtocolIdentity: activeIdentities.MeasurementProtocolIdentity,
		IdleProtocolIdentity:   idleIdentities.MeasurementProtocolIdentity,
	}, nil
}

func validateCurvePlanRunConfigs(
	active, idle profilingrun.RunConfig,
	activeNodeUID, idleNodeUID string,
	spec profilederivation.CurvePlanSpec,
) error {
	if err := validateCurvePlanSharedRunFacts(active, idle); err != nil {
		return err
	}
	if len(spec.PlannedBatchSizes) == 0 {
		return errors.New("curve plan has no planned batch sizes")
	}
	activeUsesPlannedBatchSize := false
	for _, plannedBatchSize := range spec.PlannedBatchSizes {
		if active.Measurement.Batch == plannedBatchSize {
			activeUsesPlannedBatchSize = true
			break
		}
	}
	if !activeUsesPlannedBatchSize {
		return fmt.Errorf("batch %d is not listed in the curve plan", active.Measurement.Batch)
	}
	if err := validateCurvePlanRunConfig(
		active, activeNodeUID, spec, spec.ActiveProtocol,
	); err != nil {
		return fmt.Errorf("active curve state: %w", err)
	}
	idleBatch := profilingrun.IdleExecutionBatch(idle.State.Backend, spec.PlannedBatchSizes[0])
	if idle.Measurement.Batch != idleBatch {
		return fmt.Errorf("idle curve state batch %d does not match required batch %d", idle.Measurement.Batch, idleBatch)
	}
	if err := validateCurvePlanRunConfig(
		idle, idleNodeUID, spec, spec.IdleProtocol,
	); err != nil {
		return fmt.Errorf("idle curve state: %w", err)
	}
	return nil
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
	}
}
