package admission

import (
	"errors"
	"fmt"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

// ProjectCurvePlanSpec builds the plan the checker in this package later
// validates frozen intents against.
func ProjectCurvePlanSpec(
	name string,
	active, idle profilingrun.Intent,
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
		return profilederivation.CurvePlanSpec{}, fmt.Errorf("active and idle curve intents disagree: %w", err)
	}
	if err := validateCurvePlanIntent(idle, idle.TargetNode.UID, spec, spec.IdleProtocol); err != nil {
		return profilederivation.CurvePlanSpec{}, fmt.Errorf("active and idle curve intents disagree: %w", err)
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
	if err := validateCurvePlanIntents(
		activeExecution.Intent, idleExecution.Intent,
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

func validateCurvePlanIntents(
	active, idle profilingrun.Intent,
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
	if err := validateCurvePlanIntent(
		active, activeNodeUID, spec, spec.ActiveProtocol,
	); err != nil {
		return fmt.Errorf("active curve state: %w", err)
	}
	if idle.Measurement.Batch != spec.PlannedBatchSizes[0] {
		return errors.New("idle curve state does not use the first planned batch size")
	}
	if err := validateCurvePlanIntent(
		idle, idleNodeUID, spec, spec.IdleProtocol,
	); err != nil {
		return fmt.Errorf("idle curve state: %w", err)
	}
	return nil
}

func validateCurvePlanNodeFacts(active, idle profilingrun.Intent) error {
	if active.TargetNode.Architecture != idle.TargetNode.Architecture ||
		active.TargetNode.AllocatableCPU != idle.TargetNode.AllocatableCPU {
		return errors.New("active and idle frozen candidate or Node snapshots disagree")
	}
	return nil
}

func validateCurvePlanIntent(
	intent profilingrun.Intent,
	actualNodeUID string,
	spec profilederivation.CurvePlanSpec,
	protocol profilederivation.CurveProtocolContract,
) error {
	if intent.Candidate.ExecutionPath != spec.Artifact.ExecutionPath ||
		intent.State.DeviceClass != spec.DeviceClass || intent.State.Model != spec.LogicalModel ||
		intent.State.Artifact != spec.Artifact.Name ||
		intent.State.ArtifactPath != spec.Artifact.Path || intent.State.RuntimeFamily != spec.RuntimeFamily ||
		intent.State.Backend != spec.Backend || intent.State.PowerMode != spec.PowerMode ||
		intent.Images.Runtime != spec.RuntimeImage ||
		intent.Images.Artifact != spec.Artifact.Image || intent.TargetNode.UID != spec.NodeUID ||
		actualNodeUID != spec.NodeUID {
		return errors.New("frozen intent contradicts curve-plan state or artifact facts")
	}
	if projectCurveCPU(intent.CPU) != spec.CPU || projectCurveMetricSource(intent.Metrics) != spec.MetricSource {
		return errors.New("frozen intent contradicts curve-plan CPU or metric-source facts")
	}
	projectedProtocol, err := projectCurveProtocol(intent)
	if err != nil {
		return err
	}
	if projectedProtocol != protocol {
		return errors.New("frozen intent contradicts curve-plan measurement-protocol facts")
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

func projectCurveProtocol(intent profilingrun.Intent) (profilederivation.CurveProtocolContract, error) {
	if err := intent.Workload.Validate(); err != nil {
		return profilederivation.CurveProtocolContract{}, fmt.Errorf("validate frozen workload contract: %w", err)
	}
	return profilederivation.CurveProtocolContract{
		WorkloadMode:               intent.Measurement.WorkloadMode,
		InputGenerator:             intent.Measurement.InputGenerator,
		OutputValidation:           intent.Measurement.OutputValidation,
		WarmupIterations:           intent.Measurement.WarmupIterations,
		SettlingDurationSeconds:    intent.Measurement.SettlingDurationSeconds,
		DurationSeconds:            intent.Measurement.DurationSeconds,
		MetricIntervalMilliseconds: intent.Metrics.IntervalMilliseconds,
		MetricTimeoutMilliseconds:  intent.Metrics.TimeoutMilliseconds,
		MetricDurationSeconds:      intent.Metrics.DurationSeconds,
		MetricObserverImage:        intent.Images.MetricObserver,
		WorkloadObserverImage:      intent.Images.WorkloadObserver,
		WorkloadSchemaVersion:      intent.Workload.SchemaVersion,
	}, nil
}
