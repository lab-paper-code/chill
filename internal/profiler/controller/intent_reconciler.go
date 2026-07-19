package controller

import (
	"context"
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiler"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RunIntentFreezer durably freezes one Run before execution is materialized.
type RunIntentFreezer interface {
	Freeze(context.Context, *edgev1alpha1.ProfilingRun) (FreezeIntentResult, error)
}

type RunExecutionJobs interface {
	Ensure(context.Context, *edgev1alpha1.ProfilingRun, profiler.PreparedRun) (*batchv1.Job, bool, error)
}

// RunReconciler advances one ProfilingRun through short, durable transitions.
// Runtime measurement remains entirely inside the owned Job.
type RunReconciler struct {
	client.Client
	freezer RunIntentFreezer
	jobs    RunExecutionJobs
}

func NewRunReconciler(
	kubeClient client.Client,
	freezer RunIntentFreezer,
	jobs RunExecutionJobs,
) (*RunReconciler, error) {
	if kubeClient == nil || freezer == nil || jobs == nil {
		return nil, errors.New("Kubernetes client, Run intent freezer, and execution Jobs are required")
	}
	return &RunReconciler{Client: kubeClient, freezer: freezer, jobs: jobs}, nil
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
	if frozen.Stopped || run.Status.Job != nil {
		return ctrl.Result{}, nil
	}
	job, _, err := r.jobs.Ensure(ctx, run, frozen.PreparedRun)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("materialize ProfilingRun %s Job: %w", req.NamespacedName, err)
	}
	if err := r.publishJobReference(ctx, run, job); err != nil {
		return ctrl.Result{}, fmt.Errorf("publish ProfilingRun %s Job reference: %w", req.NamespacedName, err)
	}
	return ctrl.Result{}, nil
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
