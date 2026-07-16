package controller

import (
	"context"
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RunIntentFreezer is the one transition owned by IntentReconciler. Keeping
// this port narrow prevents the first controller path from growing execution
// or evidence responsibilities.
type RunIntentFreezer interface {
	Freeze(context.Context, *edgev1alpha1.ProfilingRun) (FreezeIntentResult, error)
}

// IntentReconciler freezes the resolved inputs of one ProfilingRun. It does
// not create Jobs, observe execution, or admit evidence.
type IntentReconciler struct {
	client.Client
	freezer RunIntentFreezer
}

func NewIntentReconciler(
	kubeClient client.Client,
	freezer RunIntentFreezer,
) (*IntentReconciler, error) {
	if kubeClient == nil || freezer == nil {
		return nil, errors.New("Kubernetes client and Run intent freezer are required")
	}
	return &IntentReconciler{Client: kubeClient, freezer: freezer}, nil
}

func (r *IntentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	run := &edgev1alpha1.ProfilingRun{}
	if err := r.Get(ctx, req.NamespacedName, run); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get ProfilingRun %s: %w", req.NamespacedName, err)
	}
	if _, err := r.freezer.Freeze(ctx, run); err != nil {
		return ctrl.Result{}, fmt.Errorf("freeze ProfilingRun %s intent: %w", req.NamespacedName, err)
	}
	return ctrl.Result{}, nil
}
