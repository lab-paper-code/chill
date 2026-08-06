package admission

import (
	"errors"
	"fmt"

	"github.com/lab-paper-code/chill/internal/profilederivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

// ProjectVerifiedCurveFamilyMember consumes only evidence already sealed by
// the active and idle Loader boundaries. Catalog-only conversion and tensor
// facts remain bound through the material fields frozen in both intents and in
// the family contract.
func ProjectVerifiedCurveFamilyMember(
	contract profilederivation.CurveFamilyContract,
	batch int,
	active profilerevidence.VerifiedEvidence,
	idle profilerevidence.VerifiedIdleEvidence,
) (profilederivation.VerifiedCurveFamilyMember, error) {
	var expected *profilederivation.CurveFamilyMemberContract
	for _, member := range contract.Members() {
		if member.Batch == batch {
			copy := member
			expected = &copy
			break
		}
	}
	if expected == nil {
		return profilederivation.VerifiedCurveFamilyMember{}, fmt.Errorf("batch %d is outside the frozen curve-family contract", batch)
	}
	activeExecution, activeRaw, err := active.Records()
	if err != nil {
		return profilederivation.VerifiedCurveFamilyMember{}, fmt.Errorf("read verified active evidence: %w", err)
	}
	idleExecution, idleRaw, err := idle.Records()
	if err != nil {
		return profilederivation.VerifiedCurveFamilyMember{}, fmt.Errorf("read verified idle evidence: %w", err)
	}
	activeTrial, err := projectTrial(activeExecution, activeRaw)
	if err != nil {
		return profilederivation.VerifiedCurveFamilyMember{}, fmt.Errorf("project verified active trial: %w", err)
	}
	idleObservation, err := projectIdleObservation(idleExecution, idleRaw)
	if err != nil {
		return profilederivation.VerifiedCurveFamilyMember{}, fmt.Errorf("project verified idle observation: %w", err)
	}
	shared := contract.Shared()
	if err := validateCurveMemberIntents(
		activeExecution.Intent, idleExecution.Intent,
		activeExecution.Terminal.Node.UID, idleExecution.Terminal.Node.UID,
		shared, *expected,
	); err != nil {
		return profilederivation.VerifiedCurveFamilyMember{}, err
	}
	if activeTrial.StateIdentity != idleObservation.StateIdentity ||
		activeTrial.EnvironmentIdentity != idleObservation.EnvironmentIdentity {
		return profilederivation.VerifiedCurveFamilyMember{}, errors.New("active and idle evidence do not share exact state and environment identities")
	}
	return profilederivation.VerifiedCurveFamilyMember{
		Shared: shared, Member: *expected,
		StateIdentity:          activeTrial.StateIdentity,
		EnvironmentIdentity:    activeTrial.EnvironmentIdentity,
		ActiveProtocolIdentity: activeTrial.MeasurementProtocolIdentity,
		IdleProtocolIdentity:   idleObservation.MeasurementProtocolIdentity,
	}, nil
}

func validateCurveMemberIntents(
	active, idle profilingrun.Intent,
	activeNodeUID, idleNodeUID string,
	shared profilederivation.CurveFamilySharedContract,
	member profilederivation.CurveFamilyMemberContract,
) error {
	if active.TargetNode.Architecture != idle.TargetNode.Architecture ||
		active.TargetNode.AllocatableCPU != idle.TargetNode.AllocatableCPU {
		return errors.New("active and idle frozen candidate or Node snapshots disagree")
	}
	if err := validateCurveMemberIntent(
		active, activeNodeUID, shared, member, shared.ActiveProtocol, member.Batch,
	); err != nil {
		return fmt.Errorf("active curve-family member: %w", err)
	}
	idleBatch := member.Batch
	if member.FixedBatchCapability == 0 {
		idleBatch = 1
	}
	if err := validateCurveMemberIntent(
		idle, idleNodeUID, shared, member, shared.IdleProtocol, idleBatch,
	); err != nil {
		return fmt.Errorf("idle curve-family member: %w", err)
	}
	return nil
}

func validateCurveMemberIntent(
	intent profilingrun.Intent,
	actualNodeUID string,
	shared profilederivation.CurveFamilySharedContract,
	member profilederivation.CurveFamilyMemberContract,
	protocol profilederivation.CurveProtocolContract,
	expectedBatch int,
) error {
	if intent.Candidate.ExecutionPath != member.ExecutionPath ||
		intent.State.DeviceClass != shared.DeviceClass || intent.State.Model != shared.LogicalModel ||
		intent.State.Artifact != member.ArtifactName ||
		intent.State.ArtifactPath != member.ArtifactPath || intent.State.RuntimeFamily != shared.RuntimeFamily ||
		intent.State.Backend != shared.Backend || intent.State.PowerMode != shared.PowerMode ||
		intent.Images.Runtime != shared.RuntimeImage ||
		intent.Images.Artifact != member.ArtifactImage || intent.TargetNode.UID != shared.NodeUID ||
		actualNodeUID != shared.NodeUID || intent.Measurement.Batch != expectedBatch {
		return errors.New("frozen intent contradicts curve-family state or member facts")
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
		PowerMetric: metrics.PowerMetric, ConfigurationContentDigest: metrics.ConfigurationContentDigest,
	}
}

func projectCurveProtocol(intent profilingrun.Intent) (profilederivation.CurveProtocolContract, error) {
	workloadDigest, err := intent.Workload.Digest()
	if err != nil {
		return profilederivation.CurveProtocolContract{}, fmt.Errorf("digest frozen workload contract: %w", err)
	}
	return profilederivation.CurveProtocolContract{
		WorkloadMode:               intent.Measurement.WorkloadMode,
		InputGenerator:             intent.Measurement.InputGenerator,
		OutputValidation:           intent.Measurement.OutputValidation,
		WarmupIterations:           intent.Measurement.WarmupIterations,
		SettlingDurationSeconds:    intent.Measurement.SettlingDurationSeconds,
		DurationSeconds:            intent.Measurement.DurationSeconds,
		Repetitions:                intent.Measurement.Repetitions,
		MetricIntervalMilliseconds: intent.Metrics.IntervalMilliseconds,
		MetricTimeoutMilliseconds:  intent.Metrics.TimeoutMilliseconds,
		MetricDurationSeconds:      intent.Metrics.DurationSeconds,
		MetricObserverImage:        intent.Images.MetricObserver,
		WorkloadObserverImage:      intent.Images.WorkloadObserver,
		WorkloadContractDigest:     workloadDigest,
	}, nil
}
