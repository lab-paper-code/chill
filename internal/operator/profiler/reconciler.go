package profiler

import (
	"context"
	"fmt"
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

var deploymentResource = schema.GroupResource{Group: "apps", Resource: "deployments"}

// Reconciler manages the Profiler Deployment from ChillSystem desired state.
type Reconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Options Options
}

// +kubebuilder:rbac:groups=edge.dacs.io,resources=chillsystems,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=create;delete;get;list;patch;update;watch

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if err := r.Options.DefaultAndValidate(); err != nil {
		return ctrl.Result{}, err
	}
	if req.Name == "" {
		req.Name = r.Options.SystemName
	}
	return r.reconcile(ctx, req.Name)
}

func (r *Reconciler) reconcile(ctx context.Context, systemName string) (ctrl.Result, error) {
	system := &edgev1alpha1.ChillSystem{}
	if err := r.Get(ctx, types.NamespacedName{Name: systemName}, system); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.deleteDeployment(ctx, r.Options.Defaulted())
		}
		return ctrl.Result{}, fmt.Errorf("get ChillSystem %s: %w", systemName, err)
	}

	options := r.runtimeOptions(system)
	if !system.DeletionTimestamp.IsZero() || !system.Spec.Profiler.Enabled {
		return ctrl.Result{}, r.deleteDeployment(ctx, options)
	}

	config, err := r.loadConfig(ctx, options)
	if err != nil {
		return ctrl.Result{}, err
	}
	desired := buildDeployment(options, config)
	if err := r.applyDeployment(ctx, system, desired); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile Profiler Deployment %s: %w", deploymentKey(options), err)
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) applyDeployment(ctx context.Context, system *edgev1alpha1.ChillSystem, desired *appsv1.Deployment) error {
	key := types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}
	current := &appsv1.Deployment{}
	err := r.Get(ctx, key, current)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && !reflect.DeepEqual(current.Spec.Selector, desired.Spec.Selector) {
		if err := r.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return apierrors.NewConflict(deploymentResource, desired.Name, fmt.Errorf("deleted Deployment with immutable selector drift"))
	}

	desired.TypeMeta = metav1.TypeMeta{APIVersion: appsv1.SchemeGroupVersion.String(), Kind: "Deployment"}
	if err := controllerutil.SetControllerReference(system, desired, r.Scheme); err != nil {
		return err
	}
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	return r.Patch(ctx, desired, client.Apply, client.FieldOwner("chill-operator-profiler"), client.ForceOwnership)
}

func (r *Reconciler) deleteDeployment(ctx context.Context, options Options) error {
	deployment := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: options.Namespace, Name: options.DeploymentName}
	if err := r.Get(ctx, key, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get Profiler Deployment %s: %w", key.String(), err)
	}
	if err := r.Delete(ctx, deployment); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete Profiler Deployment %s: %w", key.String(), err)
	}
	return nil
}
