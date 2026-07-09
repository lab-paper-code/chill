package nodediscoverycontroller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

// Reconciler manages the node-discovery DaemonSet from operator configuration.
type Reconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Options Options
}

// +kubebuilder:rbac:groups=edge.dacs.io,resources=chillsystems,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=create;delete;get;list;patch;update;watch

func (r *Reconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	if err := r.Options.DefaultAndValidate(); err != nil {
		return ctrl.Result{}, err
	}
	return r.reconcile(ctx)
}

func (r *Reconciler) reconcile(ctx context.Context) (ctrl.Result, error) {
	system := &edgev1alpha1.ChillSystem{}
	if err := r.Get(ctx, r.systemKey(), system); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get ChillSystem %s: %w", r.systemKey().String(), err)
	}

	if !r.Options.Enabled {
		return ctrl.Result{}, r.deleteDaemonSet(ctx)
	}

	config, configMap, err := r.loadConfig(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	daemonSet := &appsv1.DaemonSet{
		ObjectMeta: buildDaemonSet(r.Options, config).ObjectMeta,
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, daemonSet, func() error {
		desired := buildDaemonSet(r.Options, config)
		daemonSet.Labels = desired.Labels
		daemonSet.Spec = desired.Spec
		return controllerutil.SetControllerReference(configMap, daemonSet, r.Scheme)
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile node-discovery DaemonSet %s/%s: %w", r.Options.Namespace, r.Options.DaemonSetName, err)
	}

	return ctrl.Result{}, nil
}

func (r *Reconciler) deleteDaemonSet(ctx context.Context) error {
	daemonSet := &appsv1.DaemonSet{}
	key := types.NamespacedName{Namespace: r.Options.Namespace, Name: r.Options.DaemonSetName}
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

func (r *Reconciler) loadConfig(ctx context.Context) (Config, *corev1.ConfigMap, error) {
	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: r.Options.Namespace, Name: r.Options.ConfigMapName}
	if err := r.Get(ctx, key, configMap); err != nil {
		return Config{}, nil, fmt.Errorf("get node-discovery config ConfigMap %s: %w", key.String(), err)
	}

	raw := configMap.Data[r.Options.ConfigMapKey]
	if raw == "" {
		return Config{}, nil, fmt.Errorf("node-discovery config ConfigMap %s missing key %q", key.String(), r.Options.ConfigMapKey)
	}

	var config Config
	if err := yaml.Unmarshal([]byte(raw), &config); err != nil {
		return Config{}, nil, fmt.Errorf("parse node-discovery config ConfigMap %s: %w", key.String(), err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, nil, fmt.Errorf("validate node-discovery config ConfigMap %s: %w", key.String(), err)
	}
	return config, configMap, nil
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.Options.DefaultAndValidate(); err != nil {
		return fmt.Errorf("validate node-discovery operator options: %w", err)
	}

	mapToSystem := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: r.systemKey()}}
	})
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("node-discovery").
		For(&corev1.ConfigMap{}, builder.WithPredicates(namedObjectPredicate(r.Options.Namespace, r.Options.ConfigMapName))).
		Owns(&appsv1.DaemonSet{}).
		Watches(&edgev1alpha1.ChillSystem{}, mapToSystem, builder.WithPredicates(namedObjectPredicate(r.Options.Namespace, r.Options.SystemName))).
		Complete(r); err != nil {
		return err
	}

	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		if synced := mgr.GetCache().WaitForCacheSync(ctx); !synced {
			return fmt.Errorf("wait for cache sync")
		}
		log := ctrl.LoggerFrom(ctx).WithName("node-discovery")
		wait.UntilWithContext(ctx, func(ctx context.Context) {
			if _, err := r.reconcile(ctx); err != nil {
				log.Error(err, "refresh node-discovery")
			}
		}, r.Options.ReconcileInterval)
		return nil
	}))
}

func (r *Reconciler) systemKey() types.NamespacedName {
	return types.NamespacedName{Namespace: r.Options.Namespace, Name: r.Options.SystemName}
}

func namedObjectPredicate(namespace, name string) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return isNamedObject(e.Object, namespace, name) },
		DeleteFunc: func(e event.DeleteEvent) bool { return isNamedObject(e.Object, namespace, name) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return isNamedObject(e.ObjectNew, namespace, name) || isNamedObject(e.ObjectOld, namespace, name)
		},
		GenericFunc: func(e event.GenericEvent) bool { return isNamedObject(e.Object, namespace, name) },
	}
}

func isNamedObject(obj client.Object, namespace, name string) bool {
	if obj == nil {
		return false
	}
	if namespace != "" && obj.GetNamespace() != namespace {
		return false
	}
	return obj.GetName() == name
}
