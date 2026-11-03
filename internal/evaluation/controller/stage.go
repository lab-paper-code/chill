package controller

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
)

var errParentPending = errors.New("parent has not recorded active child")
var errParentRequired = errors.New(api.EvaluationReasonParentRequired)

type StageReconciler struct{ Runtime }

func (r *Runtime) activeParent(ctx context.Context, stage *api.EvaluationStage) (*api.EvaluationRun, error) {
	owner := metav1.GetControllerOf(stage)
	if owner == nil || owner.APIVersion != api.GroupVersion.String() || owner.Kind != "EvaluationRun" {
		return nil, errParentRequired
	}
	parent := &api.EvaluationRun{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: stage.Namespace, Name: owner.Name}, parent); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, errParentRequired
		}
		return nil, err
	}
	if parent.UID != owner.UID {
		return nil, errParentRequired
	}
	for _, child := range parent.Status.ChildRefs {
		if child.StageID == stage.Spec.StageID {
			if child.UID != stage.UID || child.Name != stage.Name {
				return nil, errParentRequired
			}
			if !occupied(parent) || parent.Status.CurrentStage != stage.Spec.StageID {
				return nil, errParentPending
			}
			return parent, nil
		}
	}
	return nil, errParentPending
}
func (r *StageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	stage := &api.EvaluationStage{}
	if err := r.APIReader.Get(ctx, req.NamespacedName, stage); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if stage.Status.FinishedAt != nil {
		if stage.DeletionTimestamp.IsZero() {
			return ctrl.Result{}, nil
		}
		owner := metav1.GetControllerOf(stage)
		if owner == nil {
			return r.finalizeStage(ctx, stage, nil)
		}
		parent := &api.EvaluationRun{}
		if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: stage.Namespace, Name: owner.Name}, parent); err != nil {
			if apierrors.IsNotFound(err) {
				return r.finalizeStage(ctx, stage, nil)
			}
			return ctrl.Result{}, err
		}
		if parent.UID != owner.UID {
			return r.finalizeStage(ctx, stage, nil)
		}
		return r.finalizeStage(ctx, stage, parent)
	}
	parent, err := r.activeParent(ctx, stage)
	if errors.Is(err, errParentPending) {
		return pending, nil
	}
	if errors.Is(err, errParentRequired) {
		before := stage.DeepCopy()
		stage.Status.ObservedGeneration = stage.Generation
		condition(&stage.Status.Conditions, stage.Generation, api.EvaluationConditionExecutionComplete, metav1.ConditionFalse, api.EvaluationReasonParentRequired, "")
		now := r.now()
		stage.Status.FinishedAt = &now
		if e := r.patchStatus(ctx, stage, before); e != nil {
			return ctrl.Result{}, e
		}
		if !stage.DeletionTimestamp.IsZero() {
			return r.finalizeStage(ctx, stage, nil)
		}
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if !controllerutil.ContainsFinalizer(stage, api.EvaluationStageFinalizer) {
		if !stage.DeletionTimestamp.IsZero() {
			return ctrl.Result{}, nil
		}
		before := stage.DeepCopy()
		controllerutil.AddFinalizer(stage, api.EvaluationStageFinalizer)
		return pending, r.Client.Patch(ctx, stage, client.MergeFrom(before))
	}
	before := stage.DeepCopy()
	if stage.Status.StartedAt == nil {
		now := r.now()
		stage.Status.StartedAt = &now
		stage.Status.ObservedGeneration = stage.Generation
		for _, kind := range []string{api.EvaluationConditionExecutionComplete, api.EvaluationConditionMeasurementPassed, api.EvaluationConditionStageCleanupComplete} {
			condition(&stage.Status.Conditions, stage.Generation, kind, metav1.ConditionUnknown, api.EvaluationReasonNotStarted, "")
		}
		return pending, r.patchStatus(ctx, stage, before)
	}
	return r.reconcileSteps(ctx, parent, stage)
}

func (r *StageReconciler) reconcileSteps(ctx context.Context, parent *api.EvaluationRun, stage *api.EvaluationStage) (ctrl.Result, error) {
	before := stage.DeepCopy()
	failed := meta.IsStatusConditionFalse(stage.Status.Conditions, api.EvaluationConditionExecutionComplete)
	deleting := !stage.DeletionTimestamp.IsZero()
	for _, step := range []string{api.EvaluationStepFactor, api.EvaluationStepStage, api.EvaluationStepJudge, api.EvaluationStepCleanupJobs} {
		p := findProcedure(stage.Status.Procedures, step)
		if p != nil && p.FinishedAt != nil {
			continue
		}
		// Do not launch more measurement effects after failure/deletion; a running Job
		// is still observed through its evidence boundary before producer cleanup.
		if (failed || deleting) && step != api.EvaluationStepCleanupJobs && (p == nil || p.JobRef == nil) {
			continue
		}
		p = procedure(&stage.Status.Procedures, step)
		stage.Status.CurrentProcedure = step
		hadJob := p.JobRef != nil
		result, done, err := r.advance(ctx, parent, stage, step, p)
		stage.Status.JobRef = p.JobRef
		stage.Status.PodRef = p.PodRef
		if errors.Is(err, errParentPending) {
			return pending, nil
		}
		if err != nil {
			if !permanent(err) {
				return ctrl.Result{}, err
			}
			r.failProcedure(p, err)
			condition(&stage.Status.Conditions, stage.Generation, api.EvaluationConditionExecutionComplete, metav1.ConditionFalse, reason(err), err.Error())
			if step == api.EvaluationStepCleanupJobs {
				condition(&stage.Status.Conditions, stage.Generation, api.EvaluationConditionStageCleanupComplete, metav1.ConditionFalse, reason(err), err.Error())
			}
		}
		// Completion evidence wins over the wall clock: never inject after completion.
		if err == nil && !done && step == api.EvaluationStepStage && hadJob && !failed && !deleting {
			// Commit newly observed Job/Pod status before injection's own status patches.
			if !equality.Semantic.DeepEqual(before.Status, stage.Status) {
				return pending, r.patchStatus(ctx, stage, before)
			}
			if err := r.injections(ctx, parent, stage); err != nil {
				if errors.Is(err, errParentPending) || errors.Is(err, errInjectionUpdated) {
					return pending, nil
				}
				return ctrl.Result{}, err
			}
		}
		if done && result != nil {
			if step == api.EvaluationStepStage {
				if err := r.finishInjections(ctx, parent, stage, result); err != nil {
					return ctrl.Result{}, err
				}
			}
			r.recordStageResult(stage, step, p, result)
		}
		if step == api.EvaluationStepCleanupJobs && p.FinishedAt != nil {
			if err := r.completeStage(ctx, parent, stage, deleting); err != nil {
				return ctrl.Result{}, err
			}
		}
		return pending, r.patchStatus(ctx, stage, before)
	}
	return ctrl.Result{}, fmt.Errorf("stage has no cleanup boundary")
}

// Persist the final stage boundary while the parent/config still exist: background
// GC may remove both before the child's deletion reconcile is delivered.
func (r *StageReconciler) completeStage(ctx context.Context, parent *api.EvaluationRun, stage *api.EvaluationStage, deleting bool) error {
	if !terminal(stage.Status.Conditions, api.EvaluationConditionExecutionComplete) {
		status := metav1.ConditionTrue
		why := complete
		if deleting {
			status = metav1.ConditionFalse
			why = "Deleted"
		}
		condition(&stage.Status.Conditions, stage.Generation, api.EvaluationConditionExecutionComplete, status, why, "")
	}
	now := r.now()
	stage.Status.FinishedAt = &now
	evidence, err := r.audit(ctx, parent, stage.UID, scope(stage.Spec.StageID)+"/finalizer.json", "", stage.Status)
	if err != nil {
		return err
	}
	if !isTrue(stage.Status.Conditions, api.EvaluationConditionStageCleanupComplete) || uncertainStage(stage) {
		stage.Status.ResidualStateRef = evidence
	}
	return nil
}

func (r *StageReconciler) recordStageResult(stage *api.EvaluationStage, step string, p *api.EvaluationProcedure, result *stepResult) {
	if result.Execution.Status != complete {
		condition(&stage.Status.Conditions, stage.Generation, api.EvaluationConditionExecutionComplete, metav1.ConditionFalse, result.Execution.Reason, result.Execution.Message)
	}
	if result.Measurement != nil {
		verdict := result.Measurement.Verdict
		why := result.Measurement.Reason
		if stage.Status.MeasurementVerdict == api.MeasurementINVALID {
			verdict = api.MeasurementINVALID
			if c := meta.FindStatusCondition(stage.Status.Conditions, api.EvaluationConditionMeasurementPassed); c != nil {
				why = c.Reason
			}
		}
		stage.Status.MeasurementVerdict = verdict
		s, _ := api.AggregateMeasurement(verdict)
		if why == "" {
			why = string(verdict)
		}
		condition(&stage.Status.Conditions, stage.Generation, api.EvaluationConditionMeasurementPassed, s, why, "")
	}
	if result.Outputs != nil {
		if stage.Status.Outputs == nil {
			stage.Status.Outputs = map[string]api.EvaluationEvidenceReference{}
		}
		for k, v := range result.Outputs {
			stage.Status.Outputs[k] = v
		}
	}
	if step == api.EvaluationStepJudge {
		stage.Status.ResultRef = p.EvidenceRef
	}
	if step == api.EvaluationStepCleanupJobs {
		s := metav1.ConditionFalse
		why := api.EvaluationReasonResidualState
		if result.Cleanup != nil {
			why = result.Cleanup.Reason
			if result.Cleanup.Status == complete && result.Execution.Status == complete {
				s = metav1.ConditionTrue
			}
		}
		if s == metav1.ConditionFalse && why == "" {
			why = api.EvaluationReasonResidualState
		}
		condition(&stage.Status.Conditions, stage.Generation, api.EvaluationConditionStageCleanupComplete, s, why, result.Execution.Message)
		if s != metav1.ConditionTrue {
			stage.Status.ResidualStateRef = p.EvidenceRef
		}
	}
}
func (r *StageReconciler) finalizeStage(ctx context.Context, stage *api.EvaluationStage, parent *api.EvaluationRun) (ctrl.Result, error) {
	key := scope(stage.Spec.StageID) + "/finalizer.json"
	if parent != nil && (stage.Status.ResidualStateRef == nil || stage.Status.ResidualStateRef.Key != evidenceKey(parent.UID, key)) {
		evidence, err := r.audit(ctx, parent, stage.UID, key, "", stage.Status)
		if err != nil {
			return ctrl.Result{}, err
		}
		before := stage.DeepCopy()
		stage.Status.ResidualStateRef = evidence
		return pending, r.patchStatus(ctx, stage, before)
	}
	before := stage.DeepCopy()
	controllerutil.RemoveFinalizer(stage, api.EvaluationStageFinalizer)
	return ctrl.Result{}, r.Client.Patch(ctx, stage, client.MergeFrom(before))
}
