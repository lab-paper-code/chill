package evidencestore

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

// Reconciler manages the Evidence Store operand from ChillSystem desired state.
type Reconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Options Options
}

// +kubebuilder:rbac:groups=edge.dacs.io,resources=chillsystems,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=create;get;list;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=create;get;list;patch;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=create;delete;get;list;patch;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=create;delete;get;list;patch;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=create;delete;get;list;watch

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	options := r.Options
	if err := options.DefaultAndValidate(); err != nil {
		return ctrl.Result{}, err
	}
	if req.Name == "" {
		req.Name = options.SystemName
	}
	return r.reconcile(ctx, req.Name, options)
}

func (r *Reconciler) reconcile(ctx context.Context, systemName string, options Options) (ctrl.Result, error) {
	system := &edgev1alpha1.ChillSystem{}
	if err := r.Get(ctx, types.NamespacedName{Name: systemName}, system); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get ChillSystem %s for Evidence Store: %w", systemName, err)
	}

	instance := resolveInstance(system, options)
	if !system.DeletionTimestamp.IsZero() || !system.Spec.EvidenceStore.Enabled {
		return ctrl.Result{}, errors.Join(
			deleteOwnedBootstrapJob(ctx, r.Client, system, instance),
			deleteRuntimeResources(ctx, r.Client, system, instance),
		)
	}

	config, configDigest, err := loadConfig(ctx, r.Client, instance)
	if err != nil {
		return ctrl.Result{}, err
	}
	if _, _, err := ensureCredentialsSecret(ctx, r.Client, instance); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile Evidence Store credentials: %w", err)
	}
	if _, _, err := ensurePersistentVolumeClaim(ctx, r.Client, instance, config); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile Evidence Store storage: %w", err)
	}
	requeue, err := applyRuntimeResources(ctx, r.Client, r.Scheme, system, instance, config)
	if err != nil {
		return ctrl.Result{}, err
	}
	if requeue {
		return ctrl.Result{Requeue: true}, nil
	}
	_, requeue, err = reconcileBootstrapJob(ctx, r.Client, r.Scheme, system, instance, config, configDigest)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile Evidence Store bootstrap: %w", err)
	}
	return ctrl.Result{Requeue: requeue}, nil
}
