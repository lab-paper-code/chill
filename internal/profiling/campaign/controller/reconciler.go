package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metadata"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
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
	terminalReasonRetryLimitExhausted     = "RetryLimitExhausted"
	terminalReasonTransitionFailed        = "TransitionFailed"
	admissionWait                         = 5 * time.Second
)

const terminalReasonEnergyObjectivePolicyMismatch = "EnergyObjectivePolicyMismatch"

// ReconcileCurvePlanSource resolves and records the immutable plan used by one
// BatchEnergyOptimization campaign.
type ReconcileCurvePlanSource interface {
	Resolve(
		context.Context,
		*edgev1alpha1.BatchEnergyOptimization,
	) (profilederivation.CurvePlan, batchenergyoptimization.StoredObjectReference, *TransitionMaterial, error)
}

// ReconcileCurveDeriver derives the method result from completed owned Runs.
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
	// A terminal current-generation campaign is immutable. Replaying its
	// derivation after explicit evidence cleanup must not recreate stored data.
	if terminalCampaignStatusCurrent(parent) {
		return ctrl.Result{}, nil
	}

	runs, err := r.listRuns(ctx, parent)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result, wait, err := r.awaitAdmission(ctx, parent, len(runs) > 0); wait {
		return result, err
	}

	curvePlan, curvePlanReference, transitionMaterial, err := r.curvePlans.Resolve(ctx, parent)
	if err != nil {
		status := failedStatus(parent, "CurvePlanResolutionFailed", err.Error())
		if patchErr := r.patchStatus(ctx, parent, status); patchErr != nil {
			return ctrl.Result{}, errors.Join(err, patchErr)
		}
		return ctrl.Result{}, fmt.Errorf("resolve BatchEnergyOptimization curve plan: %w", err)
	}
	node, transition, err := r.observeNode(ctx, parent, transitionMaterial)
	if err != nil {
		return ctrl.Result{}, err
	}
	snapshot, err := ProjectSnapshot(
		parent, runs, transition, curvePlan.PlannedBatchSizes(), transitionMaterial != nil,
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	config, policy := projectEnergyObjectivePolicy(parent)
	activeRuns, idleRuns, err := completedRuns(parent.Name, snapshot, runs)
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
		if errors.Is(err, profilederivation.ErrEnergyObjectivePolicyMismatch) {
			status := failedStatus(parent, terminalReasonEnergyObjectivePolicyMismatch, err.Error())
			status.CompletedTrials = int32(len(activeRuns) + len(idleRuns))
			return ctrl.Result{}, r.patchStatus(ctx, parent, status)
		}
		return ctrl.Result{}, fmt.Errorf("decide BatchEnergyOptimization progress: %w", err)
	}

	var resultReference *batchenergyoptimization.StoredObjectReference
	alreadyExists := false
	switch decision.Action {
	case batchenergyoptimization.ActionCreateTransition:
		if err := metadata.RequestPowerMode(ctx, r.client, node, transitionMaterial.PowerMode, r.now()); err != nil {
			return ctrl.Result{}, err
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
	return requeueFor(decision, transition), nil
}

// requeueFor polls the Node while its power mode settles and backs off other
// waits.
// ponytail: polls the Node; a Node watch needs a campaign-by-Node index and
// fires on every Node heartbeat.
func requeueFor(decision batchenergyoptimization.Decision, transition *batchenergyoptimization.Transition) ctrl.Result {
	if decision.Action == batchenergyoptimization.ActionCreateTransition ||
		decision.Action == batchenergyoptimization.ActionWait && transition != nil &&
			transition.Outcome == batchenergyoptimization.TransitionInFlight {
		return ctrl.Result{RequeueAfter: admissionWait}
	}
	if decision.Action == batchenergyoptimization.ActionWait {
		return ctrl.Result{RequeueAfter: waitBackstop}
	}
	return ctrl.Result{}
}

// observeNode reads the campaign's Node and projects its power mode; the
// campaign writes the mode only on a read whose measurement taint grants the
// Node to this campaign. Both are nil when the campaign needs no transition.
func (r *BatchEnergyOptimizationReconciler) observeNode(
	ctx context.Context,
	parent *edgev1alpha1.BatchEnergyOptimization,
	material *TransitionMaterial,
) (*corev1.Node, *batchenergyoptimization.Transition, error) {
	if material == nil {
		return nil, nil, nil
	}
	node, err := profilingrun.GrantedNode(ctx, r.client, parent.UID, material.Node.Name, material.Node.UID)
	if err != nil {
		return nil, nil, err
	}
	return node, projectNodeTransition(node, material, r.now()), nil
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

func completedRuns(
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
		if trial.Verdict != batchenergyoptimization.VerdictCompleted {
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
	completed := int32(0)
	for _, trial := range snapshot.Trials {
		if trial.Verdict == batchenergyoptimization.VerdictCompleted {
			completed++
		}
	}
	status := edgev1alpha1.BatchEnergyOptimizationStatus{
		ObservedGeneration: generation,
		CompletedTrials:    completed,
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

// awaitAdmission stops a campaign that has not started until ChillServer
// admits it, before it resolves its plan or creates its first child; a refusal
// ends the campaign Failed.
func (r *BatchEnergyOptimizationReconciler) awaitAdmission(
	ctx context.Context, parent *edgev1alpha1.BatchEnergyOptimization, children bool,
) (ctrl.Result, bool, error) {
	if parent.Status.Phase == batchEnergyOptimizationPhaseMeasuring || children {
		return ctrl.Result{}, false, nil
	}
	granted, _, refused := profilingrun.Admission(parent.Annotations)
	if refused != "" {
		return ctrl.Result{}, true, r.patchStatus(ctx, parent, failedStatus(parent, refused, "measurement admission refused"))
	}
	return ctrl.Result{RequeueAfter: admissionWait}, !granted, nil
}

// failedStatus is the Failed status of a campaign stopped for reason.
func failedStatus(parent *edgev1alpha1.BatchEnergyOptimization, reason, message string) edgev1alpha1.BatchEnergyOptimizationStatus {
	status := parent.Status
	status.ObservedGeneration = parent.Generation
	status.Phase = batchEnergyOptimizationPhaseFailed
	status.Outcome = reason
	condition := metav1.Condition{
		Type: batchEnergyOptimizationConditionDone, Status: metav1.ConditionFalse,
		ObservedGeneration: parent.Generation, Reason: reason, Message: message,
	}
	condition.LastTransitionTime = conditionTransitionTime(parent.Status.Conditions, condition)
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

func terminalCampaignStatusCurrent(parent *edgev1alpha1.BatchEnergyOptimization) bool {
	if parent.Generation < 1 || parent.Status.ObservedGeneration != parent.Generation {
		return false
	}
	for _, condition := range parent.Status.Conditions {
		if condition.Type != batchEnergyOptimizationConditionDone || condition.ObservedGeneration != parent.Generation {
			continue
		}
		if parent.Status.Phase == batchEnergyOptimizationPhaseComplete {
			return parent.Status.Result != nil && condition.Status == metav1.ConditionTrue && condition.Reason == "ResultWritten"
		}
		if parent.Status.Phase == batchEnergyOptimizationPhaseFailed && condition.Status == metav1.ConditionFalse && condition.Reason == parent.Status.Outcome {
			// CurvePlanResolutionFailed is deliberately not terminal: its
			// resolver error still owns the existing retry/recovery behavior.
			return condition.Reason == terminalReasonTransitionFailed ||
				condition.Reason == terminalReasonRetryLimitExhausted ||
				condition.Reason == terminalReasonEnergyObjectivePolicyMismatch
		}
	}
	return false
}
