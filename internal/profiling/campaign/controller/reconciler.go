package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	energyObjectivePolicyVersion = "quick-energy-v1"
	waitBackstop                 = 30 * time.Second

	batchEnergyOptimizationPhaseMeasuring = "Measuring"
	batchEnergyOptimizationPhaseComplete  = "Complete"
	batchEnergyOptimizationPhaseFailed    = "Failed"
	batchEnergyOptimizationConditionDone  = "MeasurementComplete"
)

// ReconcileCurvePlanSource resolves and records the immutable plan used by one
// BatchEnergyOptimization campaign.
type ReconcileCurvePlanSource interface {
	Resolve(
		context.Context,
		*edgev1alpha1.BatchEnergyOptimization,
	) (profilederivation.CurvePlan, batchenergyoptimization.StoredObjectReference, *TransitionMaterial, error)
}

// ReconcileCurveDeriver derives the method result from accepted owned Runs.
type ReconcileCurveDeriver interface {
	Derive(
		context.Context,
		profilederivation.CurvePlan,
		[]edgev1alpha1.ProfilingRun,
		[]edgev1alpha1.ProfilingRun,
		[]int,
		profilederivation.EnergyObjectivePolicy,
	) (batchenergyoptimization.CurveResult, error)
}

// ReconcileResultWriter records the terminal result and returns its exact
// stored-object reference.
type ReconcileResultWriter interface {
	Write(
		context.Context,
		*edgev1alpha1.BatchEnergyOptimization,
		batchenergyoptimization.StoredObjectReference,
		[]edgev1alpha1.ProfilingRun,
		batchenergyoptimization.Result,
	) (batchenergyoptimization.StoredObjectReference, error)
}

// BatchEnergyOptimizationReconciler reconstructs campaign progress from the immutable
// parent spec and the currently owned ProfilingRuns on every call.
type BatchEnergyOptimizationReconciler struct {
	client       client.Client
	curvePlans   ReconcileCurvePlanSource
	curves       ReconcileCurveDeriver
	resultWriter ReconcileResultWriter
	now          func() time.Time
}

func NewBatchEnergyOptimizationReconciler(
	kubeClient client.Client,
	curvePlans ReconcileCurvePlanSource,
	curves ReconcileCurveDeriver,
	resultWriter ReconcileResultWriter,
) (*BatchEnergyOptimizationReconciler, error) {
	if kubeClient == nil || curvePlans == nil || curves == nil || resultWriter == nil {
		return nil, errors.New("Kubernetes client, curve-plan source, curve deriver, and result writer are required")
	}
	return &BatchEnergyOptimizationReconciler{
		client: kubeClient, curvePlans: curvePlans, curves: curves, resultWriter: resultWriter,
		now: time.Now,
	}, nil
}

func (r *BatchEnergyOptimizationReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	parent := &edgev1alpha1.BatchEnergyOptimization{}
	if err := r.client.Get(ctx, request.NamespacedName, parent); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !parent.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	curvePlan, curvePlanReference, transitionMaterial, err := r.curvePlans.Resolve(ctx, parent)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("resolve BatchEnergyOptimization curve plan: %w", err)
	}
	runs, err := r.listRuns(ctx, parent)
	if err != nil {
		return ctrl.Result{}, err
	}
	transitions, err := r.listTransitions(ctx, parent)
	if err != nil {
		return ctrl.Result{}, err
	}
	snapshot, err := ProjectSnapshot(
		parent, runs, transitions, curvePlan.PlannedBatchSizes(), transitionMaterial != nil,
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	config, policy := projectEnergyObjectivePolicy(parent)
	activeRuns, idleRuns, err := acceptedRuns(parent.Name, snapshot, runs)
	if err != nil {
		return ctrl.Result{}, err
	}
	curveSource := func(batches []int) (batchenergyoptimization.CurveResult, error) {
		return r.curves.Derive(ctx, curvePlan, activeRuns, idleRuns, batches, policy)
	}
	method, err := batchenergyoptimization.NewQuickEnergyMinimization(config, curveSource)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("construct quick-energy method: %w", err)
	}
	decision, err := batchenergyoptimization.Decide(snapshot, method)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("decide BatchEnergyOptimization progress: %w", err)
	}

	var resultReference *batchenergyoptimization.StoredObjectReference
	alreadyExists := false
	switch decision.Action {
	case batchenergyoptimization.ActionCreateTransition:
		transition, buildErr := BuildTransition(parent, transitionMaterial, r.now())
		if buildErr != nil {
			return ctrl.Result{}, fmt.Errorf("build NodeActuation: %w", buildErr)
		}
		if createErr := r.client.Create(ctx, transition); createErr != nil {
			if !apierrors.IsAlreadyExists(createErr) {
				return ctrl.Result{}, fmt.Errorf("create NodeActuation %q: %w", transition.Name, createErr)
			}
			alreadyExists = true
		}
	case batchenergyoptimization.ActionCreateRun:
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
	case batchenergyoptimization.ActionWait:
	case batchenergyoptimization.ActionFinish:
		if decision.Result == nil {
			return ctrl.Result{}, errors.New("Finish decision omitted its result")
		}
		stored, writeErr := r.resultWriter.Write(
			ctx, parent, curvePlanReference, runs, *decision.Result,
		)
		if writeErr != nil {
			return ctrl.Result{}, fmt.Errorf("write BatchEnergyOptimization result: %w", writeErr)
		}
		resultReference = &stored
	case batchenergyoptimization.ActionFail:
	default:
		return ctrl.Result{}, fmt.Errorf("unsupported BatchEnergyOptimization action %q", decision.Action)
	}

	status := deriveStatus(parent.Generation, snapshot, decision, resultReference, parent.Status.Conditions)
	if err := r.patchStatus(ctx, parent, status); err != nil {
		return ctrl.Result{}, err
	}
	if alreadyExists {
		return ctrl.Result{Requeue: true}, nil
	}
	if decision.Action == batchenergyoptimization.ActionWait {
		return ctrl.Result{RequeueAfter: waitBackstop}, nil
	}
	return ctrl.Result{}, nil
}

func (r *BatchEnergyOptimizationReconciler) listTransitions(
	ctx context.Context,
	parent *edgev1alpha1.BatchEnergyOptimization,
) ([]edgev1alpha1.NodeActuation, error) {
	list := &edgev1alpha1.NodeActuationList{}
	if err := r.client.List(
		ctx,
		list,
		client.InNamespace(parent.Namespace),
		client.MatchingLabels{labelBatchEnergyOptimization: parent.Name},
	); err != nil {
		return nil, fmt.Errorf("list BatchEnergyOptimization NodeActuations: %w", err)
	}
	return list.Items, nil
}

func (r *BatchEnergyOptimizationReconciler) listRuns(
	ctx context.Context,
	parent *edgev1alpha1.BatchEnergyOptimization,
) ([]edgev1alpha1.ProfilingRun, error) {
	list := &edgev1alpha1.ProfilingRunList{}
	if err := r.client.List(
		ctx,
		list,
		client.InNamespace(parent.Namespace),
		client.MatchingLabels{labelBatchEnergyOptimization: parent.Name},
	); err != nil {
		return nil, fmt.Errorf("list BatchEnergyOptimization ProfilingRuns: %w", err)
	}
	sort.Slice(list.Items, func(left, right int) bool {
		return list.Items[left].Name < list.Items[right].Name
	})
	return list.Items, nil
}

func projectEnergyObjectivePolicy(
	parent *edgev1alpha1.BatchEnergyOptimization,
) (batchenergyoptimization.QuickEnergyConfig, profilederivation.EnergyObjectivePolicy) {
	trialsPerBatch := int(parent.Spec.TrialsPerBatch)
	return batchenergyoptimization.QuickEnergyConfig{TrialsPerBatch: trialsPerBatch},
		profilederivation.EnergyObjectivePolicy{
			Version: energyObjectivePolicyVersion, TrialsPerBatch: trialsPerBatch,
		}
}

func acceptedRuns(
	parentName string,
	snapshot batchenergyoptimization.Snapshot,
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
		if trial.Verdict != batchenergyoptimization.VerdictAccepted {
			continue
		}
		if trial.Measurement.WorkloadMode == batchenergyoptimization.WorkloadModeIdle {
			idle = append(idle, runs[index])
			continue
		}
		active = append(active, runs[index])
	}
	return active, idle, nil
}

func nextAttempt(measurement batchenergyoptimization.Measurement, trials []batchenergyoptimization.Trial) int {
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
	snapshot batchenergyoptimization.Snapshot,
	decision batchenergyoptimization.Decision,
	resultReference *batchenergyoptimization.StoredObjectReference,
	previousConditions []metav1.Condition,
) edgev1alpha1.BatchEnergyOptimizationStatus {
	accepted := int32(0)
	for _, trial := range snapshot.Trials {
		if trial.Verdict == batchenergyoptimization.VerdictAccepted {
			accepted++
		}
	}
	status := edgev1alpha1.BatchEnergyOptimizationStatus{
		ObservedGeneration: generation,
		AcceptedTrials:     accepted,
	}
	condition := metav1.Condition{
		Type: batchEnergyOptimizationConditionDone, ObservedGeneration: generation,
	}
	switch decision.Action {
	case batchenergyoptimization.ActionCreateTransition, batchenergyoptimization.ActionCreateRun, batchenergyoptimization.ActionWait:
		status.Phase = batchEnergyOptimizationPhaseMeasuring
		condition.Status = metav1.ConditionUnknown
		condition.Reason = decision.Reason
		condition.Message = "batch-energy-optimization measurements are in progress"
	case batchenergyoptimization.ActionFinish:
		status.Phase = batchEnergyOptimizationPhaseComplete
		status.Outcome = decision.Result.Outcome
		status.Result = projectResultReference(*resultReference)
		condition.Status = metav1.ConditionTrue
		condition.Reason = "ResultWritten"
		condition.Message = "batch-energy-optimization result is complete"
	case batchenergyoptimization.ActionFail:
		status.Phase = batchEnergyOptimizationPhaseFailed
		status.Outcome = decision.Reason
		condition.Status = metav1.ConditionFalse
		condition.Reason = decision.Reason
		condition.Message = "batch-energy-optimization measurement failed"
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
	reference batchenergyoptimization.StoredObjectReference,
) *edgev1alpha1.ProfilingRunEvidenceReference {
	return &edgev1alpha1.ProfilingRunEvidenceReference{
		Store: reference.Store, Key: reference.Key,
		SchemaVersion: reference.SchemaVersion, Size: reference.Size,
	}
}

func (r *BatchEnergyOptimizationReconciler) patchStatus(
	ctx context.Context,
	parent *edgev1alpha1.BatchEnergyOptimization,
	status edgev1alpha1.BatchEnergyOptimizationStatus,
) error {
	if batchEnergyOptimizationStatusesEqual(parent.Status, status) {
		return nil
	}
	base := parent.DeepCopy()
	desired := parent.DeepCopy()
	desired.Status = status
	if err := r.client.Status().Patch(ctx, desired, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patch BatchEnergyOptimization status: %w", err)
	}
	return nil
}

func batchEnergyOptimizationStatusesEqual(
	left edgev1alpha1.BatchEnergyOptimizationStatus,
	right edgev1alpha1.BatchEnergyOptimizationStatus,
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
