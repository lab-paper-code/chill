package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiler"
	profileradmission "github.com/lab-paper-code/chill/internal/profiler/admission"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RunIntentFreezer durably freezes one Run before execution is materialized.
type RunIntentFreezer interface {
	Freeze(context.Context, *edgev1alpha1.ProfilingRun) (FreezeIntentResult, error)
}

type RunExecutionJobs interface {
	Ensure(context.Context, *edgev1alpha1.ProfilingRun, FrozenRun) (*batchv1.Job, bool, error)
}

// RunReconciler advances one ProfilingRun through short, durable transitions.
// Runtime measurement remains entirely inside the owned Job.
type RunReconciler struct {
	client.Client
	freezer  RunIntentFreezer
	jobs     RunExecutionJobs
	finalize RunEvidenceFinalizer
	admit    RunEvidenceAdmitter
}

func NewRunReconciler(
	kubeClient client.Client,
	freezer RunIntentFreezer,
	jobs RunExecutionJobs,
	finalize RunEvidenceFinalizer,
	admit RunEvidenceAdmitter,
) (*RunReconciler, error) {
	if kubeClient == nil || freezer == nil || jobs == nil || finalize == nil || admit == nil {
		return nil, errors.New("Kubernetes client, Run intent freezer, execution Jobs, evidence finalizer, and evidence admitter are required")
	}
	return &RunReconciler{Client: kubeClient, freezer: freezer, jobs: jobs, finalize: finalize, admit: admit}, nil
}

func (r *RunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	run := &edgev1alpha1.ProfilingRun{}
	if err := r.Get(ctx, req.NamespacedName, run); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get ProfilingRun %s: %w", req.NamespacedName, err)
	}
	frozen, err := r.freezer.Freeze(ctx, run)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("freeze ProfilingRun %s intent: %w", req.NamespacedName, err)
	}
	if frozen.Stopped {
		return ctrl.Result{}, nil
	}
	if run.Status.Job == nil {
		job, _, err := r.jobs.Ensure(ctx, run, frozen.Run)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("materialize ProfilingRun %s Job: %w", req.NamespacedName, err)
		}
		if err := r.publishJobReference(ctx, run, job); err != nil {
			return ctrl.Result{}, fmt.Errorf("publish ProfilingRun %s Job reference: %w", req.NamespacedName, err)
		}
		return ctrl.Result{}, nil
	}

	job := &batchv1.Job{}
	jobKey := types.NamespacedName{Namespace: run.Namespace, Name: run.Status.Job.Name}
	if err := r.Get(ctx, jobKey, job); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.publishExecutionOutcome(ctx, run, metav1.ConditionFalse, "ExecutionLost", "recorded measurement Job no longer exists")
		}
		return ctrl.Result{}, fmt.Errorf("get recorded measurement Job %s: %w", jobKey, err)
	}
	snapshot, err := ProjectSnapshot(run, &frozen.Run.Prepared.Intent, job)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("project ProfilingRun %s lifecycle: %w", req.NamespacedName, err)
	}
	decision, err := profiler.Decide(snapshot)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("decide ProfilingRun %s lifecycle: %w", req.NamespacedName, err)
	}
	switch decision.Action {
	case profiler.ActionWaitForJob:
		return ctrl.Result{}, r.publishExecutionOutcome(ctx, run, metav1.ConditionUnknown, "JobRunning", "measurement Job has not completed")
	case profiler.ActionReportExecutionFailure:
		return ctrl.Result{}, r.publishExecutionOutcome(ctx, run, metav1.ConditionFalse, "JobFailed", "measurement Job failed")
	case profiler.ActionFinalizeEvidence:
		finalized, finalizeErr := r.finalize.Finalize(ctx, run, frozen.Run.Prepared, job)
		if finalizeErr != nil {
			statusErr := r.publishEvidenceFailure(ctx, run)
			if statusErr != nil {
				return ctrl.Result{}, errors.Join(finalizeErr, statusErr)
			}
			return ctrl.Result{}, fmt.Errorf("finalize ProfilingRun %s evidence: %w", req.NamespacedName, finalizeErr)
		}
		if err := r.publishFinalizedEvidence(ctx, run, finalized); err != nil {
			return ctrl.Result{}, fmt.Errorf("publish ProfilingRun %s evidence: %w", req.NamespacedName, err)
		}
		return ctrl.Result{}, nil
	case profiler.ActionAdmitEvidence:
		if decision.EvidenceReference == nil {
			return ctrl.Result{}, errors.New("admission decision omitted evidence reference")
		}
		admission, admitErr := r.admit.Admit(ctx, run, frozen.Run, job, *decision.EvidenceReference)
		if admitErr != nil {
			statusErr := r.publishAdmissionFailure(ctx, run)
			if statusErr != nil {
				return ctrl.Result{}, errors.Join(admitErr, statusErr)
			}
			return ctrl.Result{}, fmt.Errorf("admit ProfilingRun %s evidence: %w", req.NamespacedName, admitErr)
		}
		if err := r.publishAdmission(ctx, run, admission); err != nil {
			return ctrl.Result{}, fmt.Errorf("publish ProfilingRun %s admission: %w", req.NamespacedName, err)
		}
		return ctrl.Result{}, nil
	case profiler.ActionComplete:
		return ctrl.Result{}, nil
	default:
		return ctrl.Result{}, fmt.Errorf("unexpected ProfilingRun lifecycle action %q", decision.Action)
	}
}

func (r *RunReconciler) publishAdmissionFailure(ctx context.Context, run *edgev1alpha1.ProfilingRun) error {
	return r.patchStatus(ctx, run, func(desired *edgev1alpha1.ProfilingRunStatus) {
		meta.SetStatusCondition(&desired.Conditions, metav1.Condition{
			Type:   edgev1alpha1.ProfilingRunConditionEvidenceAdmitted,
			Status: metav1.ConditionUnknown, ObservedGeneration: run.Generation,
			Reason: "AdmissionEvaluationFailed", Message: "verified evidence could not be evaluated",
		})
	})
}

func (r *RunReconciler) publishAdmission(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	result profileradmission.Result,
) error {
	conditionStatus, reason, message, err := admissionCondition(result.Verdict)
	if err != nil {
		return err
	}
	return r.patchStatus(ctx, run, func(desired *edgev1alpha1.ProfilingRunStatus) {
		desired.Admission = &edgev1alpha1.ProfilingRunAdmissionStatus{
			Verdict: string(result.Verdict), PolicyVersion: result.PolicyVersion,
			ReasonCodes: append([]string(nil), result.ReasonCodes...),
		}
		meta.SetStatusCondition(&desired.Conditions, metav1.Condition{
			Type:   edgev1alpha1.ProfilingRunConditionEvidenceAdmitted,
			Status: conditionStatus, ObservedGeneration: run.Generation,
			Reason: reason, Message: message,
		})
	})
}

func admissionCondition(verdict profileradmission.Verdict) (
	metav1.ConditionStatus,
	string,
	string,
	error,
) {
	switch verdict {
	case profileradmission.VerdictAccepted:
		return metav1.ConditionTrue, "EvidenceAccepted", "verified evidence passed single-Run scientific admission", nil
	case profileradmission.VerdictRejected:
		return metav1.ConditionFalse, "EvidenceRejected", "verified evidence failed single-Run scientific admission", nil
	case profileradmission.VerdictInsufficient:
		return metav1.ConditionUnknown, "EvidenceInsufficient", "verified evidence is insufficient for scientific admission", nil
	default:
		return metav1.ConditionUnknown, "", "", fmt.Errorf("unsupported admission verdict %q", verdict)
	}
}

func (r *RunReconciler) publishJobReference(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	job *batchv1.Job,
) error {
	if job == nil || job.Name == "" || job.UID == "" || !metav1.IsControlledBy(job, run) {
		return errors.New("created measurement Job has incomplete identity or ownership")
	}
	desiredReference := edgev1alpha1.ProfilingRunObjectReference{Name: job.Name, UID: string(job.UID)}
	if run.Status.Job != nil {
		if *run.Status.Job != desiredReference {
			return errors.New("refusing to replace recorded measurement Job identity")
		}
		return nil
	}
	if run.Status.IntentDigest == "" {
		return errors.New("cannot publish measurement Job before frozen intent")
	}
	base := run.DeepCopy()
	desired := run.DeepCopy()
	desired.Status.Job = &desiredReference
	if err := r.Status().Patch(ctx, desired, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patch Job reference status: %w", err)
	}
	run.Status = desired.Status
	return nil
}

func (r *RunReconciler) publishExecutionOutcome(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	status metav1.ConditionStatus,
	reason, message string,
) error {
	return r.patchStatus(ctx, run, func(desired *edgev1alpha1.ProfilingRunStatus) {
		meta.SetStatusCondition(&desired.Conditions, metav1.Condition{
			Type:   edgev1alpha1.ProfilingRunConditionExecutionComplete,
			Status: status, ObservedGeneration: run.Generation,
			Reason: reason, Message: message,
		})
	})
}

func (r *RunReconciler) publishEvidenceFailure(ctx context.Context, run *edgev1alpha1.ProfilingRun) error {
	return r.patchStatus(ctx, run, func(desired *edgev1alpha1.ProfilingRunStatus) {
		meta.SetStatusCondition(&desired.Conditions, metav1.Condition{
			Type:   edgev1alpha1.ProfilingRunConditionExecutionComplete,
			Status: metav1.ConditionTrue, ObservedGeneration: run.Generation,
			Reason: "JobSucceeded", Message: "measurement Job completed successfully",
		})
		meta.SetStatusCondition(&desired.Conditions, metav1.Condition{
			Type:   edgev1alpha1.ProfilingRunConditionEvidenceVerified,
			Status: metav1.ConditionFalse, ObservedGeneration: run.Generation,
			Reason: "EvidenceVerificationFailed", Message: "retained execution evidence could not be verified",
		})
	})
}

func (r *RunReconciler) publishFinalizedEvidence(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	finalized FinalizedRunEvidence,
) error {
	if err := finalized.Reference.Validate(); err != nil {
		return fmt.Errorf("validate finalized evidence reference: %w", err)
	}
	if finalized.Pod.Name == "" || finalized.Pod.UID == "" || finalized.Node.Name == "" || finalized.Node.UID == "" {
		return errors.New("finalized execution identities are incomplete")
	}
	return r.patchStatus(ctx, run, func(desired *edgev1alpha1.ProfilingRunStatus) {
		desired.ActualExecution = &edgev1alpha1.ProfilingRunActualExecutionStatus{
			Pod: finalized.Pod, Node: finalized.Node,
		}
		desired.Evidence = &edgev1alpha1.ProfilingRunEvidenceReference{
			Store:         finalized.Reference.Store,
			Key:           finalized.Reference.Key,
			Digest:        finalized.Reference.Digest,
			SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
			Size:          finalized.Reference.Size,
		}
		meta.SetStatusCondition(&desired.Conditions, metav1.Condition{
			Type:   edgev1alpha1.ProfilingRunConditionExecutionComplete,
			Status: metav1.ConditionTrue, ObservedGeneration: run.Generation,
			Reason: "JobSucceeded", Message: "measurement Job completed successfully",
		})
		meta.SetStatusCondition(&desired.Conditions, metav1.Condition{
			Type:   edgev1alpha1.ProfilingRunConditionEvidenceVerified,
			Status: metav1.ConditionTrue, ObservedGeneration: run.Generation,
			Reason: "EvidenceVerified", Message: "immutable execution evidence was independently verified",
		})
	})
}

func (r *RunReconciler) patchStatus(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	mutate func(*edgev1alpha1.ProfilingRunStatus),
) error {
	base := run.DeepCopy()
	desired := run.DeepCopy()
	mutate(&desired.Status)
	if reflect.DeepEqual(base.Status, desired.Status) {
		return nil
	}
	if err := r.Status().Patch(ctx, desired, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patch ProfilingRun status: %w", err)
	}
	run.Status = desired.Status
	return nil
}
