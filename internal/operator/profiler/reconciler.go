package profiler

import (
	"context"
	"fmt"
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

const deploymentFieldOwner = "chill-operator-profiler"

// Reconciler manages the Profiler Deployment from ChillSystem desired state.
type Reconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Options Options
}

// +kubebuilder:rbac:groups=edge.dacs.io,resources=chillsystems,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=create;delete;get;list;patch;watch

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

func (r *Reconciler) reconcile(ctx context.Context, systemName string, baseOptions Options) (ctrl.Result, error) {
	system := &edgev1alpha1.ChillSystem{}
	if err := r.Get(ctx, types.NamespacedName{Name: systemName}, system); err != nil {
		if apierrors.IsNotFound(err) {
			// Namespaced operands are garbage-collected through their controller
			// reference. Do not delete a same-named foreign Deployment blindly.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get ChillSystem %s: %w", systemName, err)
	}

	options := runtimeOptions(system, baseOptions)
	if !system.DeletionTimestamp.IsZero() || !system.Spec.Profiler.Enabled {
		return ctrl.Result{}, r.deleteDeployment(ctx, system, options)
	}

	config, err := r.loadConfig(ctx, options)
	if err != nil {
		return ctrl.Result{}, err
	}
	desired := buildDeployment(options, config)
	requeue, err := r.applyDeployment(ctx, system, desired)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile Profiler Deployment %s: %w", deploymentKey(options), err)
	}
	if requeue {
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) applyDeployment(ctx context.Context, system *edgev1alpha1.ChillSystem, desired *appsv1.Deployment) (bool, error) {
	key := types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}
	current := &appsv1.Deployment{}
	err := r.Get(ctx, key, current)
	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	if err == nil && !metav1.IsControlledBy(current, system) {
		if owner := metav1.GetControllerOf(current); owner != nil {
			return false, &controllerutil.AlreadyOwnedError{Object: current, Owner: *owner}
		}
		return false, fmt.Errorf("Deployment %s exists without a controller reference", key.String())
	}
	if err == nil && !reflect.DeepEqual(current.Spec.Selector, desired.Spec.Selector) {
		if err := r.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		return true, nil
	}

	desired.TypeMeta = metav1.TypeMeta{APIVersion: appsv1.SchemeGroupVersion.String(), Kind: "Deployment"}
	if err := controllerutil.SetControllerReference(system, desired, r.Scheme); err != nil {
		return false, err
	}
	if apierrors.IsNotFound(err) {
		return false, r.Create(ctx, desired)
	}
	return false, r.Patch(ctx, desired, client.Apply, client.FieldOwner(deploymentFieldOwner), client.ForceOwnership)
}

func (r *Reconciler) deleteDeployment(ctx context.Context, system *edgev1alpha1.ChillSystem, options Options) error {
	deployment := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: options.Namespace, Name: options.DeploymentName}
	if err := r.Get(ctx, key, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get Profiler Deployment %s: %w", key.String(), err)
	}
	if !metav1.IsControlledBy(deployment, system) {
		return nil
	}
	if err := r.Delete(ctx, deployment); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete Profiler Deployment %s: %w", key.String(), err)
	}
	return nil
}
