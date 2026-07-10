package nodediscovery

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

// Reconciler manages the node-discovery DaemonSet from operator configuration.
type Reconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Options Options
}

// +kubebuilder:rbac:groups=edge.dacs.io,resources=chillsystems,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=create;delete;get;list;patch;update;watch

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
			return ctrl.Result{}, r.deleteDaemonSet(ctx, r.Options.Defaulted())
		}
		return ctrl.Result{}, fmt.Errorf("get ChillSystem %s: %w", systemName, err)
	}

	options := r.runtimeOptions(system)
	if !system.DeletionTimestamp.IsZero() || !system.Spec.NodeDiscovery.Enabled {
		return ctrl.Result{}, r.deleteDaemonSet(ctx, options)
	}

	config, err := r.loadConfig(ctx, options)
	if err != nil {
		return ctrl.Result{}, err
	}

	desired := buildDaemonSet(options, config)
	if err := r.applyDaemonSet(ctx, system, desired); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile node-discovery DaemonSet %s/%s: %w", options.Namespace, options.DaemonSetName, err)
	}

	return ctrl.Result{RequeueAfter: options.ReconcileInterval}, nil
}

func (r *Reconciler) applyDaemonSet(ctx context.Context, system *edgev1alpha1.ChillSystem, desired *appsv1.DaemonSet) error {
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

		original := current.DeepCopy()
		current.Labels = desired.Labels
		current.Annotations = desired.Annotations
		current.Spec = desired.Spec
		if err := controllerutil.SetControllerReference(system, current, r.Scheme); err != nil {
			return err
		}
		if reflect.DeepEqual(original.Labels, current.Labels) &&
			reflect.DeepEqual(original.Annotations, current.Annotations) &&
			reflect.DeepEqual(original.OwnerReferences, current.OwnerReferences) &&
			reflect.DeepEqual(original.Spec, current.Spec) {
			return nil
		}
		return r.Patch(ctx, current, client.MergeFrom(original))
	})
}

func (r *Reconciler) deleteDaemonSet(ctx context.Context, options Options) error {
	daemonSet := &appsv1.DaemonSet{}
	key := types.NamespacedName{Namespace: options.Namespace, Name: options.DaemonSetName}
	if err := r.Get(ctx, key, daemonSet); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get node-discovery DaemonSet %s: %w", key.String(), err)
	}
	if err := r.Delete(ctx, daemonSet); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete node-discovery DaemonSet %s: %w", key.String(), err)
	}
	return nil
}
