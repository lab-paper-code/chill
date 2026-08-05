package nodeactuator

import (
	"context"
	"fmt"
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

var daemonSetResource = schema.GroupResource{Group: "apps", Resource: "daemonsets"}

type Reconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Options Options
}

// +kubebuilder:rbac:groups=edge.dacs.io,resources=chillsystems,verbs=get;list;watch
// +kubebuilder:rbac:groups=edge.dacs.io,resources=nodeactuations,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=create;delete;get;list;patch;update;watch

func (r *Reconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	if err := r.Options.DefaultAndValidate(); err != nil {
		return ctrl.Result{}, err
	}
	if request.Name == "" {
		request.Name = r.Options.SystemName
	}
	return r.reconcile(ctx, request.Name)
}

func (r *Reconciler) reconcile(ctx context.Context, systemName string) (ctrl.Result, error) {
	system := &edgev1alpha1.ChillSystem{}
	if err := r.Get(ctx, types.NamespacedName{Name: systemName}, system); err != nil {
		if apierrors.IsNotFound(err) {
			return r.stop(ctx, r.Options.Defaulted())
		}
		return ctrl.Result{}, fmt.Errorf("get ChillSystem %s for NodeActuator: %w", systemName, err)
	}
	options := runtimeOptions(system, r.Options)
	if !system.DeletionTimestamp.IsZero() || !system.Spec.NodeActuator.Enabled {
		return r.stop(ctx, options)
	}
	config, err := r.loadConfig(ctx, options)
	if err != nil {
		return ctrl.Result{}, err
	}
	desired, err := buildDaemonSet(options, config)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("build NodeActuator DaemonSet: %w", err)
	}
	if err := r.applyDaemonSet(ctx, system, desired); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile NodeActuator DaemonSet %s/%s: %w", options.Namespace, options.DaemonSetName, err)
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) stop(ctx context.Context, options Options) (ctrl.Result, error) {
	safe, err := SafeToStop(ctx, r.Client, options.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !safe {
		return ctrl.Result{RequeueAfter: options.ReconcileInterval}, nil
	}
	if err := r.deleteDaemonSet(ctx, options); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) applyDaemonSet(
	ctx context.Context,
	system *edgev1alpha1.ChillSystem,
	desired *appsv1.DaemonSet,
) error {
	key := types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &appsv1.DaemonSet{}
		if err := r.Get(ctx, key, current); err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
			toCreate := desired.DeepCopy()
			if err := controllerutil.SetControllerReference(system, toCreate, r.Scheme); err != nil {
				return err
			}
			if err := r.Create(ctx, toCreate); err != nil {
				if apierrors.IsAlreadyExists(err) {
					return apierrors.NewConflict(daemonSetResource, desired.Name, err)
				}
				return err
			}
			return nil
		}
		if !reflect.DeepEqual(current.Spec.Selector, desired.Spec.Selector) {
			if err := r.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			return apierrors.NewConflict(daemonSetResource, desired.Name, fmt.Errorf("deleted DaemonSet with immutable selector drift"))
		}
		base := current.DeepCopy()
		current.Labels, current.Annotations, current.Spec = desired.Labels, desired.Annotations, desired.Spec
		if err := controllerutil.SetControllerReference(system, current, r.Scheme); err != nil {
			return err
		}
		if reflect.DeepEqual(base.Labels, current.Labels) && reflect.DeepEqual(base.Annotations, current.Annotations) &&
			reflect.DeepEqual(base.OwnerReferences, current.OwnerReferences) && reflect.DeepEqual(base.Spec, current.Spec) {
			return nil
		}
		return r.Patch(ctx, current, client.MergeFrom(base))
	})
}

func (r *Reconciler) deleteDaemonSet(ctx context.Context, options Options) error {
	key := types.NamespacedName{Namespace: options.Namespace, Name: options.DaemonSetName}
	daemonSet := &appsv1.DaemonSet{}
	if err := r.Get(ctx, key, daemonSet); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get NodeActuator DaemonSet %s: %w", key, err)
	}
	if err := r.Delete(ctx, daemonSet); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete NodeActuator DaemonSet %s: %w", key, err)
	}
	return nil
}
