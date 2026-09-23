package projection

import (
	"errors"
	"fmt"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

// ProjectCurvePlanSpec builds the plan the checker in this package later
// validates RunConfigs against.
func ProjectCurvePlanSpec(
	name string,
	active, idle profilingrun.RunConfig,
	capability profilingrun.CandidateArtifactCapability,
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

	inputSampleShape := make([]int, len(capability.InputSampleShape))
	for index, dimension := range capability.InputSampleShape {
		inputSampleShape[index] = int(dimension)
	}
	spec := profilederivation.CurvePlanSpec{
		Name:           name,
		LogicalModel:   active.State.Model,
		DeviceClass:    active.State.DeviceClass,
		NodeUID:        active.TargetNode.UID,
		RuntimeFamily:  active.State.RuntimeFamily,
		Backend:        active.State.Backend,
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
			Path:          active.State.ArtifactPath,
			Image:         active.Images.Artifact,
		},
		PlannedBatchSizes: append([]int(nil), plannedBatchSizes...),
		Tensor: profilederivation.CurveTensorContract{
			InputSampleShape: inputSampleShape,
		},
	}
	if err := validateCurvePlanNodeFacts(active, idle); err != nil {
		return profilederivation.CurvePlanSpec{}, fmt.Errorf("active and idle curve RunConfigs disagree: %w", err)
	}
	if err := validateCurvePlanRunConfig(idle, idle.TargetNode.UID, spec, spec.IdleProtocol); err != nil {
		return profilederivation.CurvePlanSpec{}, fmt.Errorf("active and idle curve RunConfigs disagree: %w", err)
	}
	return spec, nil
}

// ProjectVerifiedCurveState consumes only evidence already sealed by the
// active and idle Loader boundaries. Catalog-only conversion, batch capability,
// and tensor facts remain bound through the curve plan.
func ProjectVerifiedCurveState(
	plan profilederivation.CurvePlan,
	active profilerevidence.VerifiedEvidence,
	idle profilerevidence.VerifiedIdleEvidence,
) (profilederivation.VerifiedCurveState, error) {
	activeExecution, activeRaw, err := active.Records()
	if err != nil {
		return profilederivation.VerifiedCurveState{}, fmt.Errorf("read verified active evidence: %w", err)
	}
	idleExecution, idleRaw, err := idle.Records()
	if err != nil {
		return profilederivation.VerifiedCurveState{}, fmt.Errorf("read verified idle evidence: %w", err)
	}
	activeTrial, err := projectTrial(activeExecution, activeRaw)
	if err != nil {
		return profilederivation.VerifiedCurveState{}, fmt.Errorf("project verified active trial: %w", err)
	}
	idleObservation, err := projectIdleObservation(idleExecution, idleRaw)
	if err != nil {
		return profilederivation.VerifiedCurveState{}, fmt.Errorf("project verified idle observation: %w", err)
	}
	spec := plan.Shared()
	if err := validateCurvePlanRunConfigs(
		activeExecution.RunConfig, idleExecution.RunConfig,
		activeExecution.Terminal.Node.UID, idleExecution.Terminal.Node.UID,
		spec,
	); err != nil {
		return profilederivation.VerifiedCurveState{}, err
	}
	if activeTrial.StateIdentity != idleObservation.StateIdentity ||
		activeTrial.EnvironmentIdentity != idleObservation.EnvironmentIdentity {
		return profilederivation.VerifiedCurveState{}, errors.New("active and idle evidence do not share exact state and environment identities")
	}
	return profilederivation.VerifiedCurveState{
		Shared:                 spec,
		StateIdentity:          activeTrial.StateIdentity,
		EnvironmentIdentity:    activeTrial.EnvironmentIdentity,
		ActiveProtocolIdentity: activeTrial.MeasurementProtocolIdentity,
		IdleProtocolIdentity:   idleObservation.MeasurementProtocolIdentity,
	}, nil
}

func validateCurvePlanRunConfigs(
	active, idle profilingrun.RunConfig,
	activeNodeUID, idleNodeUID string,
	spec profilederivation.CurvePlanSpec,
) error {
	if err := validateCurvePlanNodeFacts(active, idle); err != nil {
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

func validateCurvePlanNodeFacts(active, idle profilingrun.RunConfig) error {
	if active.TargetNode.Architecture != idle.TargetNode.Architecture ||
		active.TargetNode.AllocatableCPU != idle.TargetNode.AllocatableCPU {
		return errors.New("active and idle recorded candidate or Node snapshots disagree")
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
		config.State.ArtifactPath != spec.Artifact.Path || config.State.RuntimeFamily != spec.RuntimeFamily ||
		config.State.Backend != spec.Backend || config.State.PowerMode != spec.PowerMode ||
		config.Images.Runtime != spec.RuntimeImage ||
		config.Images.Artifact != spec.Artifact.Image || config.TargetNode.UID != spec.NodeUID ||
		actualNodeUID != spec.NodeUID {
		return errors.New("RunConfig contradicts curve-plan state or artifact facts")
	}
	if projectCurveCPU(config.CPU) != spec.CPU || projectCurveMetricSource(config.Metrics) != spec.MetricSource {
		return errors.New("RunConfig contradicts curve-plan CPU or metric-source facts")
	}
	projectedProtocol, err := projectCurveProtocol(config)
	if err != nil {
		return err
	}
	if projectedProtocol != protocol {
		return errors.New("RunConfig contradicts curve-plan measurement-protocol facts")
	}
	return nil
}

func projectCurveCPU(cpu profilingrun.CPUContract) profilederivation.CurveCPUContract {
	return profilederivation.CurveCPUContract{
		Policy: cpu.Policy, PolicyVersion: cpu.PolicyVersion, Request: cpu.Request, Limit: cpu.Limit,
		ORTIntraOpThreads: cpu.ORTIntraOpThreads, ORTInterOpThreads: cpu.ORTInterOpThreads,
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
	return profilederivation.CurveProtocolContract{
		WorkloadMode:               config.Measurement.WorkloadMode,
		InputGenerator:             config.Measurement.InputGenerator,
		OutputValidation:           config.Measurement.OutputValidation,
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
