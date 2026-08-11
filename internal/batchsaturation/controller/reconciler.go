package controller

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/batchsaturation"
	"github.com/lab-paper-code/chill/internal/profilederivation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	sequentialPolicyVersion = "sequential-v1"
	waitBackstop            = 30 * time.Second

	batchSaturationPhaseMeasuring = "Measuring"
	batchSaturationPhaseComplete  = "Complete"
	batchSaturationPhaseFailed    = "Failed"
	batchSaturationConditionDone  = "MeasurementComplete"
)

// ReconcileCurvePlanSource resolves and records the immutable plan used by one
// BatchSaturation campaign.
type ReconcileCurvePlanSource interface {
	Resolve(
		context.Context,
		*edgev1alpha1.BatchSaturation,
	) (profilederivation.CurvePlan, batchsaturation.StoredObjectReference, error)
}

// ReconcileCurveDeriver derives the method result from accepted owned Runs.
type ReconcileCurveDeriver interface {
	Derive(
		context.Context,
		profilederivation.CurvePlan,
		[]edgev1alpha1.ProfilingRun,
		[]edgev1alpha1.ProfilingRun,
		[]int,
		profilederivation.SequentialPolicy,
	) (batchsaturation.CurveResult, error)
}

// ReconcileResultWriter records the terminal result and returns its exact
// stored-object reference.
type ReconcileResultWriter interface {
	Write(
		context.Context,
		*edgev1alpha1.BatchSaturation,
		batchsaturation.StoredObjectReference,
		[]edgev1alpha1.ProfilingRun,
		batchsaturation.Result,
	) (batchsaturation.StoredObjectReference, error)
}

// BatchSaturationReconciler reconstructs campaign progress from the immutable
// parent spec and the currently owned ProfilingRuns on every call.
type BatchSaturationReconciler struct {
	client       client.Client
	curvePlans   ReconcileCurvePlanSource
	curves       ReconcileCurveDeriver
	resultWriter ReconcileResultWriter
}

func NewBatchSaturationReconciler(
	kubeClient client.Client,
	curvePlans ReconcileCurvePlanSource,
	curves ReconcileCurveDeriver,
	resultWriter ReconcileResultWriter,
) (*BatchSaturationReconciler, error) {
	if kubeClient == nil || curvePlans == nil || curves == nil || resultWriter == nil {
		return nil, errors.New("Kubernetes client, curve-plan source, curve deriver, and result writer are required")
	}
	return &BatchSaturationReconciler{
		client: kubeClient, curvePlans: curvePlans, curves: curves, resultWriter: resultWriter,
	}, nil
}

func (r *BatchSaturationReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	parent := &edgev1alpha1.BatchSaturation{}
	if err := r.client.Get(ctx, request.NamespacedName, parent); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !parent.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	curvePlan, curvePlanReference, err := r.curvePlans.Resolve(ctx, parent)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("resolve BatchSaturation curve plan: %w", err)
	}
	runs, err := r.listRuns(ctx, parent)
	if err != nil {
		return ctrl.Result{}, err
	}
	snapshot, err := ProjectSnapshot(parent, runs, curvePlan.PlannedBatchSizes())
	if err != nil {
		return ctrl.Result{}, err
	}

	config, policy, err := projectSequentialPolicy(parent)
	if err != nil {
		return ctrl.Result{}, err
	}
	activeRuns, idleRuns, err := acceptedRuns(parent.Name, snapshot, runs)
	if err != nil {
		return ctrl.Result{}, err
	}
	curveSource := func(batches []int) (batchsaturation.CurveResult, error) {
		return r.curves.Derive(ctx, curvePlan, activeRuns, idleRuns, batches, policy)
	}
	method, err := batchsaturation.NewSequentialEarlyStop(config, curveSource)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("construct sequential early-stop method: %w", err)
	}
	decision, err := batchsaturation.Decide(snapshot, method)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("decide BatchSaturation progress: %w", err)
	}

	var resultReference *batchsaturation.StoredObjectReference
	alreadyExists := false
	switch decision.Action {
	case batchsaturation.ActionCreateRun:
		if decision.Run == nil {
			return ctrl.Result{}, errors.New("CreateRun decision omitted its measurement")
		}
		attempt := nextAttempt(*decision.Run, snapshot.Trials)
		run, buildErr := BuildRun(parent, *decision.Run, attempt)
		if buildErr != nil {
			return ctrl.Result{}, fmt.Errorf("build ProfilingRun: %w", buildErr)
		}
		if createErr := r.client.Create(ctx, run); createErr != nil {
			if !apierrors.IsAlreadyExists(createErr) {
				return ctrl.Result{}, fmt.Errorf("create ProfilingRun %q: %w", run.Name, createErr)
			}
			alreadyExists = true
		}
	case batchsaturation.ActionWait:
	case batchsaturation.ActionFinish:
		if decision.Result == nil {
			return ctrl.Result{}, errors.New("Finish decision omitted its result")
		}
		stored, writeErr := r.resultWriter.Write(
			ctx, parent, curvePlanReference, runs, *decision.Result,
		)
		if writeErr != nil {
			return ctrl.Result{}, fmt.Errorf("write BatchSaturation result: %w", writeErr)
		}
		resultReference = &stored
	case batchsaturation.ActionFail:
	default:
		return ctrl.Result{}, fmt.Errorf("unsupported BatchSaturation action %q", decision.Action)
	}

	status := deriveStatus(parent.Generation, snapshot, decision, resultReference, parent.Status.Conditions)
	if err := r.patchStatus(ctx, parent, status); err != nil {
		return ctrl.Result{}, err
	}
	if alreadyExists {
		return ctrl.Result{Requeue: true}, nil
	}
	if decision.Action == batchsaturation.ActionWait {
		return ctrl.Result{RequeueAfter: waitBackstop}, nil
	}
	return ctrl.Result{}, nil
}

func (r *BatchSaturationReconciler) listRuns(
	ctx context.Context,
	parent *edgev1alpha1.BatchSaturation,
) ([]edgev1alpha1.ProfilingRun, error) {
	list := &edgev1alpha1.ProfilingRunList{}
	if err := r.client.List(
		ctx,
		list,
		client.InNamespace(parent.Namespace),
		client.MatchingLabels{labelBatchSaturation: parent.Name},
	); err != nil {
		return nil, fmt.Errorf("list BatchSaturation ProfilingRuns: %w", err)
	}
	sort.Slice(list.Items, func(left, right int) bool {
		return list.Items[left].Name < list.Items[right].Name
	})
	return list.Items, nil
}

func projectSequentialPolicy(
	parent *edgev1alpha1.BatchSaturation,
) (batchsaturation.SequentialConfig, profilederivation.SequentialPolicy, error) {
	if parent.Spec.Method.Name != edgev1alpha1.MethodSequentialEarlyStop ||
		parent.Spec.Method.SequentialEarlyStop == nil {
		return batchsaturation.SequentialConfig{}, profilederivation.SequentialPolicy{},
			errors.New("BatchSaturation requires sequential-early-stop method parameters")
	}
	spec := parent.Spec.Method.SequentialEarlyStop
	minDelta, err := strconv.ParseFloat(spec.MinDelta, 64)
	if err != nil || math.IsNaN(minDelta) || math.IsInf(minDelta, 0) {
		return batchsaturation.SequentialConfig{}, profilederivation.SequentialPolicy{},
			fmt.Errorf("parse sequential minDelta %q", spec.MinDelta)
	}
	divergenceFactor, err := strconv.ParseFloat(spec.DivergenceFactor, 64)
	if err != nil || math.IsNaN(divergenceFactor) || math.IsInf(divergenceFactor, 0) {
		return batchsaturation.SequentialConfig{}, profilederivation.SequentialPolicy{},
			fmt.Errorf("parse sequential divergenceFactor %q", spec.DivergenceFactor)
	}
	config := batchsaturation.SequentialConfig{
		MinDelta: minDelta, Patience: int(spec.Patience),
		TrialsPerBatch: int(spec.TrialsPerBatch), DivergenceFactor: divergenceFactor,
	}
	policy := profilederivation.SequentialPolicy{
		Version: sequentialPolicyVersion, MinDelta: minDelta, Patience: int(spec.Patience),
		TrialsPerBatch: int(spec.TrialsPerBatch), DivergenceFactor: divergenceFactor,
	}
	return config, policy, nil
}

func acceptedRuns(
	parentName string,
	snapshot batchsaturation.Snapshot,
	runs []edgev1alpha1.ProfilingRun,
) ([]edgev1alpha1.ProfilingRun, []edgev1alpha1.ProfilingRun, error) {
	if len(snapshot.Trials) != len(runs) {
		return nil, nil, fmt.Errorf(
			"projected trial/Run count invariant failed: %d trials for %d Runs",
			len(snapshot.Trials), len(runs),
		)
	}
	active := make([]edgev1alpha1.ProfilingRun, 0, len(runs))
	idle := make([]edgev1alpha1.ProfilingRun, 0, len(runs))
	for index, trial := range snapshot.Trials {
		if runs[index].Name != RunName(parentName, trial.Measurement, trial.Attempt) {
			return nil, nil, fmt.Errorf(
				"projected trial/Run order invariant failed at index %d", index,
			)
		}
		if trial.Verdict != batchsaturation.VerdictAccepted {
			continue
		}
		if trial.Measurement.WorkloadMode == batchsaturation.WorkloadModeIdle {
			idle = append(idle, runs[index])
			continue
		}
		active = append(active, runs[index])
	}
	return active, idle, nil
}

func nextAttempt(measurement batchsaturation.Measurement, trials []batchsaturation.Trial) int {
	highest := 0
	for _, trial := range trials {
		if trial.Measurement == measurement && trial.Attempt > highest {
			highest = trial.Attempt
		}
	}
	return highest + 1
}

func deriveStatus(
	generation int64,
	snapshot batchsaturation.Snapshot,
	decision batchsaturation.Decision,
	resultReference *batchsaturation.StoredObjectReference,
	previousConditions []metav1.Condition,
) edgev1alpha1.BatchSaturationStatus {
	accepted := int32(0)
	for _, trial := range snapshot.Trials {
		if trial.Verdict == batchsaturation.VerdictAccepted {
			accepted++
		}
	}
	status := edgev1alpha1.BatchSaturationStatus{
		ObservedGeneration: generation,
		AcceptedTrials:     accepted,
	}
	condition := metav1.Condition{
		Type: batchSaturationConditionDone, ObservedGeneration: generation,
	}
	switch decision.Action {
	case batchsaturation.ActionCreateRun, batchsaturation.ActionWait:
		status.Phase = batchSaturationPhaseMeasuring
		condition.Status = metav1.ConditionUnknown
		condition.Reason = decision.Reason
		condition.Message = "batch-saturation measurements are in progress"
	case batchsaturation.ActionFinish:
		status.Phase = batchSaturationPhaseComplete
		status.Outcome = decision.Result.Outcome
		status.Result = projectResultReference(*resultReference)
		condition.Status = metav1.ConditionTrue
		condition.Reason = "ResultWritten"
		condition.Message = "batch-saturation result is complete"
	case batchsaturation.ActionFail:
		status.Phase = batchSaturationPhaseFailed
		status.Outcome = decision.Reason
		condition.Status = metav1.ConditionFalse
		condition.Reason = decision.Reason
		condition.Message = "batch-saturation measurement failed"
	}
	condition.LastTransitionTime = conditionTransitionTime(previousConditions, condition)
	status.Conditions = []metav1.Condition{condition}
	return status
}

func conditionTransitionTime(previous []metav1.Condition, desired metav1.Condition) metav1.Time {
	for _, condition := range previous {
		if condition.Type == desired.Type && condition.Status == desired.Status {
			return condition.LastTransitionTime
		}
	}
	return metav1.Now()
}

func projectResultReference(
	reference batchsaturation.StoredObjectReference,
) *edgev1alpha1.ProfilingRunEvidenceReference {
	return &edgev1alpha1.ProfilingRunEvidenceReference{
		Store: reference.Store, Key: reference.Key,
		SchemaVersion: reference.SchemaVersion, Size: reference.Size,
	}
}

func (r *BatchSaturationReconciler) patchStatus(
	ctx context.Context,
	parent *edgev1alpha1.BatchSaturation,
	status edgev1alpha1.BatchSaturationStatus,
) error {
	if batchSaturationStatusesEqual(parent.Status, status) {
		return nil
	}
	base := parent.DeepCopy()
	desired := parent.DeepCopy()
	desired.Status = status
	if err := r.client.Status().Patch(ctx, desired, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patch BatchSaturation status: %w", err)
	}
	return nil
}

func batchSaturationStatusesEqual(
	left edgev1alpha1.BatchSaturationStatus,
	right edgev1alpha1.BatchSaturationStatus,
) bool {
	left.Conditions = append([]metav1.Condition(nil), left.Conditions...)
	right.Conditions = append([]metav1.Condition(nil), right.Conditions...)
	for index := range left.Conditions {
		left.Conditions[index].LastTransitionTime = metav1.Time{}
	}
	for index := range right.Conditions {
		right.Conditions[index].LastTransitionTime = metav1.Time{}
	}
	return reflect.DeepEqual(left, right)
}
