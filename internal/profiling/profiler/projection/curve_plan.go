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

// ProjectCurvePlanSpec builds the plan the checker in this package later
// validates RunConfigs against.
func ProjectCurvePlanSpec(
	name string,
	active, idle profilingrun.RunConfig,
	conversion profilederivation.CurveConversionContract,
	plannedBatchSizes []int,
) (profilederivation.CurvePlanSpec, error) {
	activeProtocol, err := projectCurveProtocol(active)
	if err != nil {
		return profilederivation.CurvePlanSpec{}, fmt.Errorf("project active curve protocol: %w", err)
	}
	idleProtocol, err := projectCurveProtocol(idle)
	if err != nil {
		return profilederivation.CurvePlanSpec{}, fmt.Errorf("project idle curve protocol: %w", err)
	}

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
		PowerMode:      active.State.PowerMode,
		CPU:            projectCurveCPU(active.CPU),
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
	if idle.Measurement.Batch != spec.PlannedBatchSizes[0] {
		return errors.New("idle curve state does not use the first planned batch size")
	}
	if err := validateCurvePlanRunConfig(
		idle, idleNodeUID, spec, spec.IdleProtocol,
	); err != nil {
		return fmt.Errorf("idle curve state: %w", err)
	}
	return nil
}

func validateCurvePlanSharedRunFacts(active, idle profilingrun.RunConfig) error {
	if active.TargetNode.Architecture != idle.TargetNode.Architecture ||
		active.TargetNode.AllocatableCPU != idle.TargetNode.AllocatableCPU {
		return errors.New("active and idle recorded candidate or Node snapshots disagree")
	}
	if active.Candidate.Inputs.ModelSpec.UID != idle.Candidate.Inputs.ModelSpec.UID ||
		active.Candidate.Inputs.DeviceClass.UID != idle.Candidate.Inputs.DeviceClass.UID {
		return errors.New("active and idle model or device-class identities disagree")
	}
	if !reflect.DeepEqual(active.State.HostContract, idle.State.HostContract) ||
		!reflect.DeepEqual(active.Delivery.HostContract, idle.Delivery.HostContract) ||
		!reflect.DeepEqual(active.Delivery.RuntimePackages, idle.Delivery.RuntimePackages) {
		return errors.New("active and idle runtime delivery conditions disagree")
	}
	if !reflect.DeepEqual(active.Actuation, idle.Actuation) {
		return errors.New("active and idle power actuation conditions disagree")
	}
	if !maps.Equal(active.Metrics.MetricLabels, idle.Metrics.MetricLabels) {
		return errors.New("active and idle wall-power metric labels disagree")
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
	if !sameFirstfloorSettings(config.State.Firstfloor, spec.Firstfloor) {
		return errors.New("RunConfig contradicts curve-plan firstfloor settings")
	}
	if !reflect.DeepEqual(projectCurveRuntimeAccess(config.Delivery.RuntimeAccess), spec.RuntimeAccess) {
		return errors.New("RunConfig contradicts curve-plan accelerator delivery")
	}
	if !reflect.DeepEqual(projectCurvePreprocessor(config.State.Preprocessor), spec.Preprocessor) {
		return errors.New("RunConfig contradicts curve-plan preprocessor facts")
	}
	cpu := config.CPU
	if cpu.Policy != spec.CPU.Policy || cpu.PolicyVersion != spec.CPU.PolicyVersion ||
		cpu.Request != spec.CPU.Request || cpu.Limit != spec.CPU.Limit ||
		cpu.ORTIntraOpThreads != spec.CPU.ORTIntraOpThreads || cpu.ORTInterOpThreads != spec.CPU.ORTInterOpThreads ||
		cpu.ExclusivityClaimed != spec.CPU.ExclusivityClaimed || !slices.Equal(cpu.AffinityCPUs, spec.CPU.AffinityCPUs) ||
		projectCurveMetricSource(config.Metrics) != spec.MetricSource {
		return errors.New("RunConfig contradicts curve-plan CPU or metric-source facts")
	}
	projectedProtocol, err := projectCurveProtocol(config)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(projectedProtocol, protocol) {
		return errors.New("RunConfig contradicts curve-plan measurement-protocol facts")
	}
	return nil
}
func sameFirstfloorSettings(actual, expected *modelcompat.FirstfloorSettings) bool {
	if actual == nil || expected == nil {
		return actual == expected
	}
	return actual.MaxBatchSize == expected.MaxBatchSize && actual.MaxQueueSize == expected.MaxQueueSize &&
		modelcompat.EffectiveDeadlinePolicy(actual.DeadlinePolicy) ==
			modelcompat.EffectiveDeadlinePolicy(expected.DeadlinePolicy)
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

func projectCurveCPU(cpu profilingrun.CPUContract) profilederivation.CurveCPUContract {
	return profilederivation.CurveCPUContract{
		Policy: cpu.Policy, PolicyVersion: cpu.PolicyVersion, Request: cpu.Request, Limit: cpu.Limit,
		ORTIntraOpThreads: cpu.ORTIntraOpThreads, ORTInterOpThreads: cpu.ORTInterOpThreads,
		AffinityCPUs:       append([]int(nil), cpu.AffinityCPUs...),
		ExclusivityClaimed: cpu.ExclusivityClaimed,
	}
}

func projectCurveMetricSource(metrics profilingrun.MetricContract) profilederivation.CurveMetricSourceContract {
	return profilederivation.CurveMetricSourceContract{
		SourceKind: metrics.SourceKind, NodeName: metrics.NodeName, Endpoint: metrics.Endpoint,
		PowerMetric: metrics.PowerMetric,
	}
}

func projectCurveProtocol(config profilingrun.RunConfig) (profilederivation.CurveProtocolContract, error) {
	if err := config.Workload.Validate(); err != nil {
		return profilederivation.CurveProtocolContract{}, fmt.Errorf("validate recorded workload contract: %w", err)
	}
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
		SettlingDurationSeconds:    config.Measurement.SettlingDurationSeconds,
		DurationSeconds:            config.Measurement.DurationSeconds,
		MetricIntervalMilliseconds: config.Metrics.IntervalMilliseconds,
		MetricTimeoutMilliseconds:  config.Metrics.TimeoutMilliseconds,
		MetricDurationSeconds:      config.Metrics.DurationSeconds,
		MetricObserverImage:        config.Images.MetricObserver,
		WorkloadObserverImage:      config.Images.WorkloadObserver,
		WorkloadSchemaVersion:      config.Workload.SchemaVersion,
	}, nil
}
