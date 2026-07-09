package system

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

const finalizerName = "edge.dacs.io/chillsystem-finalizer"

// ChillSystemReconciler maintains the root CHILL status resource.
type ChillSystemReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Options Options
}

// +kubebuilder:rbac:groups=edge.dacs.io,resources=chillsystems,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=edge.dacs.io,resources=chillsystems/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=edge.dacs.io,resources=chillsystems/finalizers,verbs=update
// +kubebuilder:rbac:groups=edge.dacs.io,resources=deviceclasses,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;delete

// Reconcile updates ChillSystem finalization and status.
func (r *ChillSystemReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Name == "" {
		req.Name = r.systemName()
	}
	return r.reconcileSystem(ctx, req.Name)
}

func (r *ChillSystemReconciler) reconcileSystem(ctx context.Context, name string) (ctrl.Result, error) {
	key := types.NamespacedName{Name: name}
	system := &edgev1alpha1.ChillSystem{}
	if err := r.Get(ctx, key, system); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get ChillSystem %s: %w", key.String(), err)
	}

	if !system.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(system, finalizerName) {
			return ctrl.Result{}, nil
		}
		if done, err := r.finalize(ctx, system); err != nil {
			return ctrl.Result{}, err
		} else if !done {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		original := system.DeepCopy()
		controllerutil.RemoveFinalizer(system, finalizerName)
		if err := r.Patch(ctx, system, client.MergeFrom(original)); err != nil {
			return ctrl.Result{}, fmt.Errorf("remove ChillSystem finalizer %s: %w", key.String(), err)
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(system, finalizerName) {
		original := system.DeepCopy()
		controllerutil.AddFinalizer(system, finalizerName)
		if err := r.Patch(ctx, system, client.MergeFrom(original)); err != nil {
			return ctrl.Result{}, fmt.Errorf("add ChillSystem finalizer %s: %w", key.String(), err)
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	next := buildStatus(r.observe(ctx, system), system.Status.Conditions, metav1Now())
	if statusesEqual(system.Status, next) {
		return ctrl.Result{}, nil
	}
	system.Status = next
	if err := r.Status().Update(ctx, system); err != nil {
		return ctrl.Result{}, fmt.Errorf("update ChillSystem %s status: %w", key.String(), err)
	}
	return ctrl.Result{}, nil
}
