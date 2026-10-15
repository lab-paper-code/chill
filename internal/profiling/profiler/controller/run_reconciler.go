package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	evidenceReasonInvalid            = "EvidenceInvalid"
	evidenceReasonVerificationFailed = "EvidenceVerificationFailed"
)

type RunConfigurationResolver interface {
	Resolve(context.Context, *edgev1alpha1.ProfilingRun) (ResolvedRun, error)
}

type RunExecutionJobs interface {
	Find(context.Context, *edgev1alpha1.ProfilingRun) (*batchv1.Job, *ResolvedRun, error)
	Ensure(context.Context, *edgev1alpha1.ProfilingRun, ResolvedRun) (*batchv1.Job, bool, error)
}

// RunReconciler advances one ProfilingRun through short, durable transitions;
// the owned Job is both the executable projection and its recoverable record.
type RunReconciler struct {
	client.Client
	resolver RunConfigurationResolver
	jobs     RunExecutionJobs
	finalize RunEvidenceFinalizer
}

func NewRunReconciler(
	kubeClient client.Client,
	resolver RunConfigurationResolver,
	jobs RunExecutionJobs,
	finalize RunEvidenceFinalizer,
) (*RunReconciler, error) {
	if kubeClient == nil || resolver == nil || jobs == nil || finalize == nil {
		return nil, errors.New("Kubernetes client, RunConfig resolver, execution Jobs, evidence finalizer are required")
	}
	return &RunReconciler{
		Client: kubeClient, resolver: resolver,
		jobs: jobs, finalize: finalize,
	}, nil
}

func (r *RunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	run := &edgev1alpha1.ProfilingRun{}
	if err := r.Get(ctx, req.NamespacedName, run); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get ProfilingRun %s: %w", req.NamespacedName, err)
	}
	if !run.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if terminalPreJobFailureCurrent(run) {
		return ctrl.Result{}, nil
	}

	var resolved ResolvedRun
	var job *batchv1.Job
	var err error
	if run.Status.Job != nil {
		job, err = r.loadRecordedJob(ctx, run)
		if err != nil {
			return ctrl.Result{}, err
		}
		if job == nil {
			return ctrl.Result{}, r.publishExecutionOutcome(
				ctx, run, metav1.ConditionFalse, "ExecutionLost", "recorded measurement Job no longer exists",
			)
		}
		resolved, err = resolvedRunFromJob(run, job)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("recover ProfilingRun %s configuration from Job: %w", req.NamespacedName, err)
		}
	} else {
		var recorded *ResolvedRun
		job, recorded, err = r.jobs.Find(ctx, run)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("recover ProfilingRun %s Job: %w", req.NamespacedName, err)
		}
		if job != nil {
			resolved = *recorded
			if err := r.publishJobReference(ctx, run, job); err != nil {
				return ctrl.Result{}, fmt.Errorf("publish recovered ProfilingRun %s Job reference: %w", req.NamespacedName, err)
			}
		} else {
			resolved, err = r.resolver.Resolve(ctx, run)
			if err != nil {
				if reason, terminal := profiler.TerminalResolutionReason(err); terminal {
					return ctrl.Result{}, r.publishRunConfigFailure(ctx, run, reason, err.Error())
				}
				return ctrl.Result{}, fmt.Errorf("resolve ProfilingRun %s configuration: %w", req.NamespacedName, err)
			}
		}
	}
	if resolved.Prepared.RunConfig.Actuation != nil {
		handled, err := r.reconcileRequiredState(ctx, run, resolved)
		if err != nil || handled {
			return ctrl.Result{}, err
		}
	}
	if run.Status.Job == nil {
		return ctrl.Result{}, r.ensureMeasurementJob(ctx, req, run, resolved)
	}
	snapshot, err := ProjectSnapshot(run, &resolved.Prepared.RunConfig, job)
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
		finalized, finalizeErr := r.finalize.Finalize(ctx, run, resolved.Prepared, job)
		if finalizeErr != nil {
			return ctrl.Result{}, r.handleEvidenceFinalizationFailure(
				ctx, req.NamespacedName, run, finalizeErr,
			)
		}
		if err := r.publishFinalizedEvidence(ctx, run, finalized); err != nil {
			return ctrl.Result{}, fmt.Errorf("publish ProfilingRun %s evidence: %w", req.NamespacedName, err)
		}
		return ctrl.Result{}, nil
	case profiler.ActionComplete:
		return ctrl.Result{}, nil
	default:
		return ctrl.Result{}, fmt.Errorf("unexpected ProfilingRun lifecycle action %q", decision.Action)
	}
}

func (r *RunReconciler) handleEvidenceFinalizationFailure(
	ctx context.Context,
	key types.NamespacedName,
	run *edgev1alpha1.ProfilingRun,
	failure error,
) error {
	permanent := errors.Is(failure, profilerevidence.ErrPermanentValidation)
	reason := evidenceReasonVerificationFailed
	message := "retained execution evidence could not be verified"
	if permanent {
		reason = evidenceReasonInvalid
		message = "retained execution evidence is permanently invalid: " + failure.Error()
	}

	if statusErr := r.publishEvidenceFailure(ctx, run, reason, message); statusErr != nil {
		if permanent {
			return fmt.Errorf("publish permanent evidence failure: %w", statusErr)
		}
		return errors.Join(failure, statusErr)
	}
	if !permanent {
		return fmt.Errorf("finalize ProfilingRun %s evidence: %w", key, failure)
	}
	return nil
}

func (r *RunReconciler) loadRecordedJob(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
) (*batchv1.Job, error) {
	if run.Status.Job == nil {
		return nil, nil
	}
	if run.Status.Job.Name == "" || run.Status.Job.UID == "" {
		return nil, errors.New("recorded measurement Job reference is incomplete")
	}
	job := &batchv1.Job{}
	key := types.NamespacedName{Namespace: run.Namespace, Name: run.Status.Job.Name}
	if err := r.Get(ctx, key, job); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get recorded measurement Job %s: %w", key, err)
	}
	if string(job.UID) != run.Status.Job.UID {
		return nil, errors.New("observed Job identity does not match recorded reference")
	}
	if !metav1.IsControlledBy(job, run) {
		return nil, errors.New("observed Job is not controlled by ProfilingRun")
	}
	return job, nil
}

func (r *RunReconciler) ensureMeasurementJob(
	ctx context.Context,
	request ctrl.Request,
	run *edgev1alpha1.ProfilingRun,
	resolved ResolvedRun,
) error {
	job, _, err := r.jobs.Ensure(ctx, run, resolved)
	if err != nil {
		return fmt.Errorf("materialize ProfilingRun %s Job: %w", request.NamespacedName, err)
	}
	if err := r.publishJobReference(ctx, run, job); err != nil {
		return fmt.Errorf("publish ProfilingRun %s Job reference: %w", request.NamespacedName, err)
	}
	return nil
}

func (r *RunReconciler) publishActuationReference(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	handoff *edgev1alpha1.NodeActuation,
) error {
	if handoff == nil || handoff.Namespace != run.Namespace || handoff.Name == "" || handoff.UID == "" {
		return errors.New("confirming NodeActuation has incomplete identity")
	}
	desiredReference := edgev1alpha1.ProfilingRunObjectReference{Name: handoff.Name, UID: string(handoff.UID)}
	if run.Status.Actuation != nil {
		if *run.Status.Actuation != desiredReference {
			return errors.New("refusing to replace recorded confirming NodeActuation identity")
		}
		return nil
	}
	return r.patchStatus(ctx, run, func(desired *edgev1alpha1.ProfilingRunStatus) {
		desired.Actuation = &desiredReference
	})
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
	base := run.DeepCopy()
	desired := run.DeepCopy()
	desired.Status.Job = &desiredReference
	desired.Status.ObservedGeneration = run.Generation
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
		setRunCondition(desired, run, edgev1alpha1.ProfilingRunConditionExecutionComplete,
			status, reason, message)
	})
}

func (r *RunReconciler) publishRunConfigFailure(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	reason, message string,
) error {
	return r.patchStatus(ctx, run, func(desired *edgev1alpha1.ProfilingRunStatus) {
		desired.ObservedGeneration = run.Generation
		setRunCondition(desired, run, edgev1alpha1.ProfilingRunConditionExecutionComplete,
			metav1.ConditionFalse, reason, "RunConfig could not be resolved: "+message)
	})
}

func (r *RunReconciler) publishEvidenceFailure(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	reason string,
	message string,
) error {
	return r.patchStatus(ctx, run, func(desired *edgev1alpha1.ProfilingRunStatus) {
		setRunCondition(desired, run, edgev1alpha1.ProfilingRunConditionExecutionComplete,
			metav1.ConditionTrue, "JobSucceeded", "measurement Job completed successfully")
		setRunCondition(desired, run, edgev1alpha1.ProfilingRunConditionEvidenceVerified,
			metav1.ConditionFalse, reason, message)
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
	if !profilerevidence.IsSupportedExecutionEvidenceSchemaVersion(finalized.SchemaVersion) {
		return fmt.Errorf("unsupported finalized evidence schemaVersion %q", finalized.SchemaVersion)
	}
	if finalized.Pod.Name == "" || finalized.Pod.UID == "" || finalized.Node.Name == "" || finalized.Node.UID == "" {
		return errors.New("finalized execution identities are incomplete")
	}
	return r.patchStatus(ctx, run, func(desired *edgev1alpha1.ProfilingRunStatus) {
		desired.ObservedExecution = &edgev1alpha1.ProfilingRunObservedExecutionStatus{
			Pod: finalized.Pod, Node: finalized.Node,
		}
		desired.Evidence = &edgev1alpha1.ProfilingRunEvidenceReference{
			Store:         finalized.Reference.Store,
			Key:           finalized.Reference.Key,
			SchemaVersion: finalized.SchemaVersion,
			Size:          finalized.Reference.Size,
		}
		setRunCondition(desired, run, edgev1alpha1.ProfilingRunConditionExecutionComplete,
			metav1.ConditionTrue, "JobSucceeded", "measurement Job completed successfully")
		setRunCondition(desired, run, edgev1alpha1.ProfilingRunConditionEvidenceVerified,
			metav1.ConditionTrue, "EvidenceVerified", "immutable execution evidence was independently verified")
	})
}

func setRunCondition(
	desired *edgev1alpha1.ProfilingRunStatus,
	run *edgev1alpha1.ProfilingRun,
	conditionType string,
	status metav1.ConditionStatus,
	reason, message string,
) {
	meta.SetStatusCondition(&desired.Conditions, metav1.Condition{
		Type: conditionType, Status: status, ObservedGeneration: run.Generation,
		Reason: reason, Message: message,
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

func terminalPreJobFailureCurrent(run *edgev1alpha1.ProfilingRun) bool {
	if run.Generation < 1 || run.Status.Job != nil {
		return false
	}
	for _, condition := range run.Status.Conditions {
		if condition.Type != edgev1alpha1.ProfilingRunConditionExecutionComplete ||
			condition.Status != metav1.ConditionFalse || condition.ObservedGeneration != run.Generation {
			continue
		}
		switch condition.Reason {
		case profiler.ResolutionReasonInvalidRunSpec, profiler.ResolutionReasonExecutionPolicyInvalid,
			profiler.ResolutionReasonActuationPolicyInvalid, requiredStateNotConfirmedReason:
			return true
		}
	}
	return false
}
