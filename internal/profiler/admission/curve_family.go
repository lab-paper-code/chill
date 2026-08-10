package admission

import (
	"errors"
	"fmt"

	"github.com/lab-paper-code/chill/internal/profilederivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

// ProjectVerifiedCurveFamilyState consumes only evidence already sealed by the
// active and idle Loader boundaries. Catalog-only conversion, batch capability,
// and tensor facts remain bound through the family contract.
func ProjectVerifiedCurveFamilyState(
	contract profilederivation.CurveFamilyContract,
	active profilerevidence.VerifiedEvidence,
	idle profilerevidence.VerifiedIdleEvidence,
) (profilederivation.VerifiedCurveFamilyState, error) {
	activeExecution, activeRaw, err := active.Records()
	if err != nil {
		return profilederivation.VerifiedCurveFamilyState{}, fmt.Errorf("read verified active evidence: %w", err)
	}
	idleExecution, idleRaw, err := idle.Records()
	if err != nil {
		return profilederivation.VerifiedCurveFamilyState{}, fmt.Errorf("read verified idle evidence: %w", err)
	}
	activeTrial, err := projectTrial(activeExecution, activeRaw)
	if err != nil {
		return profilederivation.VerifiedCurveFamilyState{}, fmt.Errorf("project verified active trial: %w", err)
	}
	idleObservation, err := projectIdleObservation(idleExecution, idleRaw)
	if err != nil {
		return profilederivation.VerifiedCurveFamilyState{}, fmt.Errorf("project verified idle observation: %w", err)
	}
	shared := contract.Shared()
	if err := validateCurveFamilyIntents(
		activeExecution.Intent, idleExecution.Intent,
		activeExecution.Terminal.Node.UID, idleExecution.Terminal.Node.UID,
		shared,
	); err != nil {
		return profilederivation.VerifiedCurveFamilyState{}, err
	}
	if activeTrial.StateIdentity != idleObservation.StateIdentity ||
		activeTrial.EnvironmentIdentity != idleObservation.EnvironmentIdentity {
		return profilederivation.VerifiedCurveFamilyState{}, errors.New("active and idle evidence do not share exact state and environment identities")
	}
	return profilederivation.VerifiedCurveFamilyState{
		Shared:                 shared,
		StateIdentity:          activeTrial.StateIdentity,
		EnvironmentIdentity:    activeTrial.EnvironmentIdentity,
		ActiveProtocolIdentity: activeTrial.MeasurementProtocolIdentity,
		IdleProtocolIdentity:   idleObservation.MeasurementProtocolIdentity,
	}, nil
}

func validateCurveFamilyIntents(
	active, idle profilingrun.Intent,
	activeNodeUID, idleNodeUID string,
	shared profilederivation.CurveFamilySharedContract,
) error {
	if active.TargetNode.Architecture != idle.TargetNode.Architecture ||
		active.TargetNode.AllocatableCPU != idle.TargetNode.AllocatableCPU {
		return errors.New("active and idle frozen candidate or Node snapshots disagree")
	}
	activeBatchEligible := false
	for _, eligibleBatch := range shared.EligibleBatches {
		if active.Measurement.Batch == eligibleBatch {
			activeBatchEligible = true
			break
		}
	}
	if !activeBatchEligible {
		return fmt.Errorf("batch %d is outside the frozen curve-family contract", active.Measurement.Batch)
	}
	if err := validateCurveFamilyIntent(
		active, activeNodeUID, shared, shared.ActiveProtocol, active.Measurement.Batch,
	); err != nil {
		return fmt.Errorf("active curve-family state: %w", err)
	}
	if err := validateCurveFamilyIntent(
		idle, idleNodeUID, shared, shared.IdleProtocol, 1,
	); err != nil {
		return fmt.Errorf("idle curve-family state: %w", err)
	}
	return nil
}

func validateCurveFamilyIntent(
	intent profilingrun.Intent,
	actualNodeUID string,
	shared profilederivation.CurveFamilySharedContract,
	protocol profilederivation.CurveProtocolContract,
	expectedBatch int,
) error {
	if intent.Candidate.ExecutionPath != shared.Artifact.ExecutionPath ||
		intent.State.DeviceClass != shared.DeviceClass || intent.State.Model != shared.LogicalModel ||
		intent.State.Artifact != shared.Artifact.Name ||
		intent.State.ArtifactPath != shared.Artifact.Path || intent.State.RuntimeFamily != shared.RuntimeFamily ||
		intent.State.Backend != shared.Backend || intent.State.PowerMode != shared.PowerMode ||
		intent.Images.Runtime != shared.RuntimeImage ||
		intent.Images.Artifact != shared.Artifact.Image || intent.TargetNode.UID != shared.NodeUID ||
		actualNodeUID != shared.NodeUID || intent.Measurement.Batch != expectedBatch {
		return errors.New("frozen intent contradicts curve-family state or artifact facts")
	}
	if projectCurveCPU(intent.CPU) != shared.CPU || projectCurveMetricSource(intent.Metrics) != shared.MetricSource {
		return errors.New("frozen intent contradicts curve-family CPU or metric-source facts")
	}
	projectedProtocol, err := projectCurveProtocol(intent)
	if err != nil {
		return err
	}
	if projectedProtocol != protocol {
		return errors.New("frozen intent contradicts curve-family measurement-protocol facts")
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
