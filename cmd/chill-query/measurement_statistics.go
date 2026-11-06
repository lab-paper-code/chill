package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	evidences3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	campaignevidence "github.com/lab-paper-code/chill/internal/profiling/campaign/evidence"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	evidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	evidencekube "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence/kube"
	projection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	"github.com/lab-paper-code/chill/internal/queryapi"
	querykube "github.com/lab-paper-code/chill/internal/queryapi/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const statisticsEvidenceMissingReason = "RetainedEvidenceMissing"
const statisticsNotRecordedReason = "StatisticsNotRecorded"

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
) querykube.BatchEnergyOptimizationStatisticsReader {
	return func(
		ctx context.Context,
		cr *api.BatchEnergyOptimization,
		result campaign.ResultRecord,
	) (*queryapi.BatchEnergyOptimizationStatistics, *string, error) {
		statistics, err := readMeasurementStatistics(ctx, reader, objects, loader, cr, result)
		if errors.Is(err, evidencestore.ErrNotFound) || errors.Is(err, errStatisticsRunMissing) {
			reason := statisticsEvidenceMissingReason
			return nil, &reason, nil
		}
		if errors.Is(err, errStatisticsNotRecorded) {
			reason := statisticsNotRecordedReason
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
) (*queryapi.BatchEnergyOptimizationStatistics, error) {
	retainedReader := campaignevidence.Reader{
		Runs: statisticsRuns{Reader: reader},
		ReadObject: func(ctx context.Context, id evidencestore.ObjectIdentity) ([]byte, error) {
			return readStatisticsObject(ctx, objects, id)
		},
		LoadActive: func(
			ctx context.Context, run *api.ProfilingRun, id evidencestore.ObjectIdentity,
		) (evidence.VerifiedEvidence, error) {
			retained, err := loader.Load(ctx, run, id)
			return retained.ForDerivation(), err
		},
		LoadIdle: func(
			ctx context.Context, run *api.ProfilingRun, id evidencestore.ObjectIdentity,
		) (evidence.VerifiedIdleEvidence, error) {
			retained, err := loader.LoadIdle(ctx, run, id)
			return retained.ForDerivation(), err
		},
	}
	retained, err := retainedReader.LoadPlan(ctx, cr, result)
	if err != nil {
		return nil, statisticsCampaignError(err)
	}
	discovery, sequential, err := campaign.DecodeDiscoveryResult(result, retained.Plan.PlannedBatchSizes())
	if err != nil {
		return nil, err
	}
	trials, err := retainedReader.ReadTrials(ctx, cr, retained)
	if err != nil {
		return nil, statisticsCampaignError(err)
	}
	if sequential && len(discovery.MeasuredBatchSizes) == 0 {
		// Preserve this absence reason through Query's assessment: a sealed
		// discovery result alone does not establish a usable active batch.
		return nil, errStatisticsNotRecorded
	}
	plan, shared := retained.Plan, retained.Plan.Shared()
	active, idle := trials.Active, trials.Idle
	activeSources, idleSources := trials.ActiveSources, trials.IdleSources
	if len(active) == 0 || len(idle) == 0 {
		return nil, errStatisticsNotRecorded
	}
	curve, err := deriveMeasurementCurve(cr, plan, active, idle, discovery.MeasuredBatchSizes)
	if err != nil {
		return nil, err
	}
	// Match Serving's condition projection from the same Loader-verified evidence.
	shared, err = projection.ProjectMeasuredCurveCondition(shared, active[0])
	if err != nil {
		return nil, err
	}
	factBatches := curve.MeasurementBatches()
	statistics := &queryapi.BatchEnergyOptimizationStatistics{
		Batches: []queryapi.BatchEnergyOptimizationBatchStatistics{},
		MeasurementFacts: &derivation.MeasurementFacts{
			Request: derivation.MeasurementRequestReference{
				Namespace: cr.Namespace,
				Name:      cr.Name,
				UID:       string(cr.UID),
			},
			ResultReference: derivation.RawEvidenceIdentity{
				Store: retained.ResultIdentity.Store, Key: retained.ResultIdentity.Key, Size: retained.ResultIdentity.Size,
			},
			Condition: shared,
			PointPolicy: derivation.PointPolicy{
				Version: "energy-objective-point-v1", MinimumTrials: int(cr.Spec.TrialsPerBatch),
				ConfidenceLevel: factBatches[0].Confidence.Level,
			},
			Batches: factBatches,
		},
	}
	for _, row := range factBatches {
		statistics.Batches = append(
			statistics.Batches,
			queryapi.BatchEnergyOptimizationBatchStatistics{
				Batch: row.Batch, IncrementalWorkJoulesPerItem: *row.IncrementalJoulesPerItem,
				ActiveTrialCount: row.ActiveTrialCount, IdleTrialCount: row.IdleTrialCount, Confidence: *row.Confidence,
				LatencySampleCount: row.LatencySampleCount, LatencyP95Milliseconds: *row.P95Milliseconds,
				Sources: append(slices.Clone(idleSources), activeSources[row.Batch]...),
			},
		)
	}
	if err := addRetainedMeasurementCaptureFacts(
		statistics.MeasurementFacts, active, idle, activeSources, idleSources,
	); err != nil {
		return nil, err
	}
	return statistics, ctx.Err()
}

func addRetainedMeasurementCaptureFacts(
	facts *derivation.MeasurementFacts,
	active []evidence.VerifiedEvidence,
	idle []evidence.VerifiedIdleEvidence,
	activeSources map[int][]evidencestore.ObjectIdentity,
	idleSources []evidencestore.ObjectIdentity,
) error {
	for i := range facts.Batches {
		row := &facts.Batches[i]
		for _, source := range append(slices.Clone(idleSources), activeSources[row.Batch]...) {
			row.Sources = append(row.Sources, derivation.RawEvidenceIdentity{
				Store: source.Store, Key: source.Key, Size: source.Size,
			})
		}
		for _, verified := range active {
			execution, raw, err := verified.Records()
			if err != nil {
				return err
			}
			if execution.RunConfig.Measurement.Batch != row.Batch {
				continue
			}
			start, end, err := raw.Runtime.MeasurementWindow()
			if err != nil {
				return err
			}
			row.PowerSource = projection.MeasurementPowerSource(raw.Metrics.Source)
			quality := projection.MeasurementQuality(
				execution.RunConfig, raw.Metrics, start, end, facts.Condition.ActiveProtocol,
			)
			quality.TrialID = execution.Terminal.Pod.UID
			row.Quality = append(row.Quality, quality)
		}
		for _, verified := range idle {
			execution, raw, err := verified.Records()
			if err != nil {
				return err
			}
			start, end, err := raw.Runtime.MeasurementWindow()
			if err != nil {
				return err
			}
			quality := projection.MeasurementQuality(
				execution.RunConfig, raw.Metrics, start, end, facts.Condition.IdleProtocol,
			)
			quality.TrialID = execution.Terminal.Pod.UID
			row.Quality = append(row.Quality, quality)
		}
	}
	return nil
}

func readStatisticsObject(
	ctx context.Context,
	objects evidencestore.Reader,
	identity evidencestore.ObjectIdentity,
) ([]byte, error) {
	const maximumStatisticsObjectBytes = 64 << 20
	stream, err := objects.Open(ctx, identity)
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, maximumStatisticsObjectBytes+1))
	if err := errors.Join(readErr, stream.Close(), ctx.Err()); err != nil {
		return nil, err
	}
	if int64(len(payload)) > maximumStatisticsObjectBytes {
		return nil, errors.New("statistics object exceeds read limit")
	}
	return payload, nil
}

// statisticsRuns distinguishes missing Runs. Retained Job/Secret lookup still
// uses the original reader.
type statisticsRuns struct{ client.Reader }

func (r statisticsRuns) Get(
	ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption,
) error {
	if err := r.Reader.Get(ctx, key, obj, opts...); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: %w", errStatisticsRunMissing, err)
		}
		return err
	}
	return nil
}

// statisticsCampaignError keeps Query's public reasons while the common reader
// owns reconstruction and validation. Dependency errors retain their identities.
func statisticsCampaignError(err error) error {
	message := err.Error()
	cause := errors.Unwrap(err)
	if cause != nil {
		switch {
		case strings.HasPrefix(message, "read frozen curve plan: "),
			strings.HasPrefix(message, "get completed Run "),
			strings.HasPrefix(message, "load active Run "),
			strings.HasPrefix(message, "load idle Run "):
			return cause
		case strings.HasPrefix(message, "completed Run "):
			err = cause
		}
	}
	var reason string
	switch {
	case errors.Is(err, campaignevidence.ErrUnsupportedWorkloadMode):
		reason = "completed Run workload mode is unsupported"
	default:
		switch err.Error() {
		case "Run identity or campaign ownership mismatch":
			reason = "completed Run identity or campaign ownership mismatch"
		case "Run measurement state or batch contradicts completed trial reference":
			reason = "completed Run measurement contradicts result"
		case "Run workload mode contradicts completed trial reference":
			reason = "completed Run workload mode contradicts result"
		case "Run has no current completed execution and verified evidence":
			reason = "completed Run lacks current verified completion"
		case "completed trials reuse one raw measurement object", "completed trials reuse one execution Pod":
			reason = "completed trials reuse raw evidence or execution Pod"
		default:
			if strings.HasPrefix(message, "completed Run ") && strings.HasSuffix(message, " appears more than once") {
				reason = "completed trials reuse raw evidence or execution Pod"
			}
		}
	}
	if reason == "" {
		return err
	}
	return statisticsEvidenceError{error: err, reason: reason}
}

type statisticsEvidenceError struct {
	error
	reason string
}

func (e statisticsEvidenceError) Error() string { return e.reason }
func (e statisticsEvidenceError) Unwrap() error { return e.error }

func deriveMeasurementCurve(
	cr *api.BatchEnergyOptimization,
	plan derivation.CurvePlan,
	active []evidence.VerifiedEvidence,
	idle []evidence.VerifiedIdleEvidence,
	measured []int,
) (derivation.Curve, error) {
	var err error
	var verifiedState derivation.VerifiedCurveState
	verifiedState.Shared = plan.Shared()
	if len(active) == 0 || len(idle) == 0 {
		return derivation.Curve{}, errStatisticsNotRecorded
	}
	idleExecution, _, err := idle[0].Records()
	if err != nil {
		return derivation.Curve{}, err
	}
	batches := make(map[int][]derivation.Trial)
	for _, verified := range active {
		execution, _, err := verified.Records()
		if err != nil {
			return derivation.Curve{}, err
		}
		if err := projection.ValidateCurvePlanRunConfigs(
			execution.RunConfig, idleExecution.RunConfig,
			execution.Terminal.Node.UID, idleExecution.Terminal.Node.UID, verifiedState.Shared,
		); err != nil {
			return derivation.Curve{}, err
		}
		trial, err := projection.Trial(verified)
		if err != nil {
			return derivation.Curve{}, err
		}
		verifiedState.StateIdentity = trial.StateIdentity
		verifiedState.EnvironmentIdentity = trial.EnvironmentIdentity
		verifiedState.ActiveProtocolIdentity = trial.MeasurementProtocolIdentity
		if measured != nil && !slices.Contains(measured, trial.Batch) {
			continue
		}
		if len(trial.LatencyMilliseconds) == 0 {
			return derivation.Curve{}, errStatisticsNotRecorded
		}
		batches[trial.Batch] = append(batches[trial.Batch], trial)
	}
	idleTrials := make([]derivation.IdleTrial, len(idle))
	for i, verified := range idle {
		idleTrials[i], err = projection.IdleTrial(verified, idle[0])
		if err != nil {
			return derivation.Curve{}, err
		}
	}
	verifiedState.IdleProtocolIdentity = idleTrials[0].MeasurementProtocolIdentity
	confidence, err := cr.Spec.PointPolicy.Confidence()
	if err != nil {
		return derivation.Curve{}, err
	}
	policy := derivation.PointPolicy{
		Version:         "energy-objective-point-v1",
		MinimumTrials:   int(cr.Spec.TrialsPerBatch),
		ConfidenceLevel: confidence,
	}
	baseline, err := derivation.DeriveIdleBaseline(idleTrials, policy)
	if err != nil {
		return derivation.Curve{}, err
	}
	verifiedPlan, err := derivation.VerifyCurvePlan(plan, verifiedState)
	if err != nil {
		return derivation.Curve{}, err
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
		return derivation.Curve{}, err
	}
	return curve, nil
}
