package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	evidences3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	consolekube "github.com/lab-paper-code/chill/internal/operatorconsole/kube"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	evidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	evidencekube "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence/kube"
	projection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	runconfig "github.com/lab-paper-code/chill/internal/profiling/run"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const statisticsEvidenceMissingReason = "RetainedEvidenceMissing"

func newRetainedEvidenceLoader(
	reader client.Reader,
	opts options,
) (*evidencekube.RetainedLoader, error) {
	return evidencekube.NewRetainedLoader(
		reader,
		func(binding storekube.Binding, credentials storekube.Credentials) (evidencestore.LocationReader, error) {
			return evidences3.New(evidences3.Config{
				LogicalStore: binding.Store, Bucket: binding.Bucket, Endpoint: binding.Endpoint, Region: binding.Region,
				AccessKeyID: credentials.AccessKeyID, SecretAccessKey: credentials.SecretAccessKey, TempDir: opts.tempDir,
			})
		},
	)
}

func newMeasurementStatistics(
	reader client.Reader,
	objects evidencestore.Reader,
	loader *evidencekube.RetainedLoader,
) consolekube.BatchEnergyOptimizationStatisticsReader {
	return func(
		ctx context.Context,
		cr *api.BatchEnergyOptimization,
		result campaign.ResultRecord,
	) (*operatorconsole.BatchEnergyOptimizationStatistics, *string, error) {
		statistics, err := readMeasurementStatistics(ctx, reader, objects, loader, cr, result)
		if errors.Is(err, evidencestore.ErrNotFound) || errors.Is(err, errStatisticsRunMissing) {
			reason := statisticsEvidenceMissingReason
			return nil, &reason, nil
		}
		if errors.Is(err, errStatisticsNotRecorded) {
			reason := "StatisticsNotRecorded"
			return nil, &reason, nil
		}
		return statistics, nil, err
	}
}

var errStatisticsNotRecorded = errors.New("statistics inputs were not recorded")
var errStatisticsRunMissing = errors.New("retained statistics Run is missing")

func readMeasurementStatistics(
	ctx context.Context,
	reader client.Reader,
	objects evidencestore.Reader,
	loader *evidencekube.RetainedLoader,
	cr *api.BatchEnergyOptimization,
	result campaign.ResultRecord,
) (*operatorconsole.BatchEnergyOptimizationStatistics, error) {
	spec := result.Spec()
	state := cr.Spec.State
	if spec.MeasurementState != (campaign.ResultMeasurementState{
		TargetNode: state.TargetNode, ModelSpec: state.Candidate.ModelSpec, DeviceClass: state.Candidate.DeviceClass,
		ExecutionPath: state.Candidate.ExecutionPath, PowerMode: state.Candidate.PowerMode,
	}) {
		return nil, errors.New("result contradicts campaign measurement state")
	}
	planIdentity := statisticsObjectIdentity(spec.CurvePlan)
	payload, err := readStatisticsObject(ctx, objects, planIdentity)
	if err != nil {
		return nil, err
	}
	plan, err := derivation.DecodeCurvePlan(payload)
	if err != nil {
		return nil, err
	}
	shared := plan.Shared()
	if spec.CurvePlan.SchemaVersion != plan.SchemaVersion() || shared.DeviceClass != state.Candidate.DeviceClass ||
		shared.Artifact.ExecutionPath != state.Candidate.ExecutionPath || shared.PowerMode != state.Candidate.PowerMode ||
		shared.MetricSource.NodeName != state.TargetNode ||
		shared.CPU.Request != state.CPURequest.String() {
		return nil, errors.New("curve plan contradicts campaign measurement state")
	}
	var active []evidence.VerifiedEvidence
	var idle []evidence.VerifiedIdleEvidence
	activeSources := make(map[int][]evidencestore.ObjectIdentity)
	idleSources := []evidencestore.ObjectIdentity{planIdentity}
	seenRaw := make(map[evidencestore.ObjectLocation]bool)
	seenPods := make(map[string]bool)
	for _, ref := range spec.CompletedTrials {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		run := &api.ProfilingRun{}
		if err := reader.Get(ctx, types.NamespacedName{Namespace: ref.Run.Namespace, Name: ref.Run.Name}, run); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("%w: %w", errStatisticsRunMissing, err)
			}
			return nil, err
		}
		if err := validateStatisticsRun(cr, run, ref); err != nil {
			return nil, err
		}
		identity := statisticsObjectIdentity(ref.Evidence)
		var execution evidence.ExecutionEvidence
		if ref.Measurement.WorkloadMode == campaign.WorkloadModeIdle {
			retained, err := loader.LoadIdle(ctx, run, identity)
			if err != nil {
				return nil, err
			}
			verified := retained.ForDerivation()
			execution, _, err = verified.Records()
			if err != nil {
				return nil, err
			}
			idle = append(idle, verified)
			idleSources = append(idleSources, identity, execution.RawMeasurement)
		} else {
			retained, err := loader.Load(ctx, run, identity)
			if err != nil {
				return nil, err
			}
			verified := retained.ForDerivation()
			execution, _, err = verified.Records()
			if err != nil {
				return nil, err
			}
			active = append(active, verified)
			batch := ref.Measurement.Batch
			activeSources[batch] = append(activeSources[batch], identity, execution.RawMeasurement)
		}
		if err := validateStatisticsRunConfig(run, execution.RunConfig); err != nil {
			return nil, err
		}
		if seenRaw[execution.RawMeasurement.Location()] || seenPods[execution.Terminal.Pod.UID] {
			return nil, errors.New("completed trials reuse raw evidence or execution Pod")
		}
		seenRaw[execution.RawMeasurement.Location()], seenPods[execution.Terminal.Pod.UID] = true, true
	}
	if len(active) == 0 || len(idle) == 0 {
		return nil, errStatisticsNotRecorded
	}
	rows, err := deriveMeasurementStatistics(cr, plan, active, idle)
	if err != nil {
		return nil, err
	}
	statistics := &operatorconsole.BatchEnergyOptimizationStatistics{
		Batches: []operatorconsole.BatchEnergyOptimizationBatchStatistics{},
	}
	for _, row := range rows {
		statistics.Batches = append(
			statistics.Batches,
			operatorconsole.BatchEnergyOptimizationBatchStatistics{
				Batch: row.Batch, IncrementalWorkJoulesPerItem: row.IncrementalWorkJoulesPerItem,
				ActiveTrialCount: row.ActiveTrialCount, IdleTrialCount: row.IdleTrialCount, Confidence: row.Confidence,
				LatencySampleCount: row.LatencySampleCount, LatencyP95Milliseconds: row.LatencyP95Milliseconds,
				Sources: append(slices.Clone(idleSources), activeSources[row.Batch]...),
			},
		)
	}
	return statistics, ctx.Err()
}

func statisticsObjectIdentity(ref campaign.StoredObjectReference) evidencestore.ObjectIdentity {
	return evidencestore.ObjectIdentity{Store: ref.Store, Key: ref.Key, Size: ref.Size}
}

func readStatisticsObject(
	ctx context.Context,
	objects evidencestore.Reader,
	identity evidencestore.ObjectIdentity,
) ([]byte, error) {
	const maximumStatisticsObjectBytes = 64 << 20
	if identity.Size > maximumStatisticsObjectBytes {
		return nil, errors.New("statistics object exceeds read limit")
	}
	stream, err := objects.Open(ctx, identity)
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, identity.Size+1))
	if err := errors.Join(readErr, stream.Close(), ctx.Err()); err != nil {
		return nil, err
	}
	if int64(len(payload)) != identity.Size {
		return nil, errors.New("statistics object size contradicts reference")
	}
	return payload, nil
}

func validateStatisticsRun(
	cr *api.BatchEnergyOptimization,
	run *api.ProfilingRun,
	ref campaign.CompletedTrialReference,
) error {
	owner := metav1.GetControllerOf(run)
	if run.Namespace != cr.Namespace || string(run.UID) != ref.Run.UID || owner == nil ||
		owner.UID != cr.UID || owner.Kind != "BatchEnergyOptimization" || owner.APIVersion != api.GroupVersion.String() ||
		run.DeletionTimestamp != nil {
		return errors.New("completed Run identity or campaign ownership mismatch")
	}
	if run.Spec.MeasurementStateSpec.Candidate != cr.Spec.State.Candidate ||
		run.Spec.TargetNode != cr.Spec.State.TargetNode ||
		run.Spec.CPURequest.Cmp(cr.Spec.State.CPURequest) != 0 ||
		int(run.Spec.Measurement.Batch) != ref.Measurement.Batch {
		return errors.New("completed Run measurement contradicts result")
	}
	mode := campaign.WorkloadModeActive
	if run.Spec.Measurement.WorkloadMode == "idle-model-loaded" {
		mode = campaign.WorkloadModeIdle
	} else if run.Spec.Measurement.WorkloadMode != "saturated-serial-fixed-batch" {
		return errors.New("completed Run workload mode is unsupported")
	}
	if mode != ref.Measurement.WorkloadMode {
		return errors.New("completed Run workload mode contradicts result")
	}
	for _, kind := range []string{api.ProfilingRunConditionExecutionComplete, api.ProfilingRunConditionEvidenceVerified} {
		condition := meta.FindStatusCondition(run.Status.Conditions, kind)
		if condition == nil || condition.Status != metav1.ConditionTrue ||
			condition.ObservedGeneration != run.Generation {
			return errors.New("completed Run lacks current verified completion")
		}
	}
	if run.Status.Evidence == nil {
		return errors.New("completed evidence reference is absent from Run")
	}
	retained := run.Status.Evidence
	identity := evidencestore.ObjectIdentity{Store: retained.Store, Key: retained.Key, Size: retained.Size}
	if identity != statisticsObjectIdentity(ref.Evidence) || retained.SchemaVersion != ref.Evidence.SchemaVersion {
		return errors.New("completed evidence reference contradicts Run")
	}
	return validateStatisticsRunProtocol(cr, run, mode)
}

func validateStatisticsRunConfig(run *api.ProfilingRun, config runconfig.RunConfig) error {
	measurement := run.Spec.Measurement
	if config.Candidate.Inputs.ModelSpec.Name != run.Spec.Candidate.ModelSpec ||
		config.State.DeviceClass != run.Spec.Candidate.DeviceClass ||
		config.Candidate.ExecutionPath != run.Spec.Candidate.ExecutionPath ||
		config.State.PowerMode != run.Spec.Candidate.PowerMode ||
		config.TargetNode.Name != run.Spec.TargetNode ||
		config.Measurement.Batch != int(measurement.Batch) ||
		config.Measurement.WorkloadMode != measurement.WorkloadMode ||
		config.CPU.Request != run.Spec.CPURequest.String() {
		return errors.New("stored RunConfig contradicts completed Run state")
	}
	if measurement.CPUQuota == nil {
		if config.CPU.Policy != runconfig.AllocatableCPUQuotaPolicy {
			return errors.New("stored CPU policy contradicts completed Run")
		}
	} else {
		quota, err := resource.ParseQuantity(config.CPU.Limit)
		if err != nil || config.CPU.Policy != runconfig.ConfiguredCPUQuotaPolicy || quota.Cmp(*measurement.CPUQuota) != 0 {
			return errors.New("stored CPU quota contradicts completed Run")
		}
	}
	if len(config.CPU.AffinityCPUs) != len(measurement.CPUAffinity) {
		return errors.New("stored CPU affinity contradicts completed Run")
	}
	for _, cpu := range measurement.CPUAffinity {
		if !slices.Contains(config.CPU.AffinityCPUs, int(cpu)) {
			return errors.New("stored CPU affinity contradicts completed Run")
		}
	}
	if config.Measurement.WarmupIterations != int(measurement.WarmupIterations) ||
		config.Measurement.DurationSeconds != int(measurement.DurationSeconds) ||
		config.Measurement.SettlingDurationSeconds != int(
			ptr.Deref(measurement.SettlingDurationSeconds, 0),
		) ||
		config.Measurement.DurationToleranceSeconds != 0 &&
			config.Measurement.DurationToleranceSeconds != ptr.Deref(
				measurement.DurationToleranceSeconds,
				.5,
			) ||
		(config.Measurement.Dataset == nil) != (measurement.Dataset == "") ||
		(config.Measurement.Dataset != nil && config.Measurement.Dataset.Name != measurement.Dataset) ||
		config.Metrics.IntervalMilliseconds != int(
			run.Spec.MetricObservation.IntervalMilliseconds,
		) ||
		config.Metrics.TimeoutMilliseconds != int(run.Spec.MetricObservation.TimeoutMilliseconds) ||
		config.Metrics.DurationSeconds != int(run.Spec.MetricObservation.DurationSeconds) {
		return errors.New("stored RunConfig contradicts completed Run protocol")
	}
	return nil
}

func deriveMeasurementStatistics(
	cr *api.BatchEnergyOptimization,
	plan derivation.CurvePlan,
	active []evidence.VerifiedEvidence,
	idle []evidence.VerifiedIdleEvidence,
) ([]derivation.BatchStatistics, error) {
	var err error
	var verifiedState derivation.VerifiedCurveState
	batches := make(map[int][]derivation.Trial)
	for _, verified := range active {
		projected, err := projection.ProjectVerifiedCurveState(plan, verified, idle[0])
		if err != nil {
			return nil, err
		}
		verifiedState = projected
		trial, err := projection.Trial(verified)
		if err != nil {
			return nil, err
		}
		if len(trial.LatencyMilliseconds) == 0 {
			return nil, errStatisticsNotRecorded
		}
		batches[trial.Batch] = append(batches[trial.Batch], trial)
	}
	idleTrials := make([]derivation.IdleTrial, len(idle))
	for i, verified := range idle {
		if _, err := projection.ProjectVerifiedCurveState(plan, active[0], verified); err != nil {
			return nil, err
		}
		idleTrials[i], err = projection.IdleTrial(verified, idle[0])
		if err != nil {
			return nil, err
		}
	}
	confidence, err := cr.Spec.PointPolicy.Confidence()
	if err != nil {
		return nil, err
	}
	policy := derivation.PointPolicy{
		Version:         "energy-objective-point-v1",
		MinimumTrials:   int(cr.Spec.TrialsPerBatch),
		ConfidenceLevel: confidence,
	}
	baseline, err := derivation.DeriveIdleBaseline(idleTrials, policy)
	if err != nil {
		return nil, err
	}
	verifiedPlan, err := derivation.VerifyCurvePlan(plan, verifiedState)
	if err != nil {
		return nil, err
	}
	pass := derivation.Pass{Role: derivation.PassSearch}
	for _, batch := range plan.PlannedBatchSizes() {
		if trials := batches[batch]; len(trials) != 0 {
			pass.Batches = append(pass.Batches, derivation.PassBatch{Batch: batch, Trials: trials})
		}
	}
	curve, err := derivation.DeriveCurve(
		[]derivation.Pass{pass},
		verifiedPlan,
		map[string]derivation.IdleBaseline{baseline.StateIdentity: baseline},
		policy,
	)
	if err != nil {
		return nil, err
	}
	return curve.Statistics(), nil
}

func validateStatisticsRunProtocol(
	cr *api.BatchEnergyOptimization,
	run *api.ProfilingRun,
	mode campaign.WorkloadMode,
) error {
	measurement := run.Spec.Measurement
	if (measurement.CPUQuota == nil) != (cr.Spec.CPUQuota == nil) ||
		(measurement.CPUQuota != nil && measurement.CPUQuota.Cmp(*cr.Spec.CPUQuota) != 0) ||
		!slices.Equal(measurement.CPUAffinity, cr.Spec.CPUAffinity) {
		return errors.New("completed Run CPU protocol contradicts campaign")
	}
	if mode == campaign.WorkloadModeIdle {
		protocol := cr.Spec.IdleProtocol
		if measurement.WarmupIterations != 0 || measurement.SettlingDurationSeconds == nil ||
			*measurement.SettlingDurationSeconds != protocol.SettlingDurationSeconds ||
			measurement.DurationSeconds != protocol.DurationSeconds || measurement.Dataset != "" ||
			ptr.Deref(
				measurement.DurationToleranceSeconds,
				.5,
			) != ptr.Deref(
				protocol.DurationToleranceSeconds,
				.5,
			) ||
			run.Spec.MetricObservation != protocol.MetricObservation {
			return errors.New("completed idle Run protocol contradicts campaign")
		}
	} else {
		protocol := cr.Spec.ActiveProtocol
		if measurement.SettlingDurationSeconds != nil || measurement.WarmupIterations != protocol.WarmupIterations ||
			measurement.DurationSeconds != protocol.DurationSeconds || measurement.Dataset != protocol.Dataset ||
			ptr.Deref(measurement.DurationToleranceSeconds, .5) != ptr.Deref(protocol.DurationToleranceSeconds, .5) ||
			run.Spec.MetricObservation != protocol.MetricObservation {
			return errors.New("completed active Run protocol contradicts campaign")
		}
	}
	return nil
}
