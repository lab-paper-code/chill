package systemcontroller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

const (
	DefaultSystemName      = "chill"
	DefaultRefreshInterval = 30 * time.Second
)

// Options configures the namespace-local CHILL status surface.
type Options struct {
	SystemName                 string
	Namespace                  string
	ControllerDeploymentName   string
	NodeDiscoveryDaemonSetName string
	NodeDiscoveryEnabled       bool
	RefreshInterval            time.Duration
}

// ChillSystemReconciler maintains the root CHILL status resource.
type ChillSystemReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Options Options
}

// +kubebuilder:rbac:groups=edge.dacs.io,resources=chillsystems,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=edge.dacs.io,resources=chillsystems/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=edge.dacs.io,resources=chillsystems/finalizers,verbs=update
// +kubebuilder:rbac:groups=edge.dacs.io,resources=deviceclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments;daemonsets,verbs=get;list;watch

// Reconcile updates the singleton ChillSystem status.
func (r *ChillSystemReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.NamespacedName != r.systemKey() {
		return ctrl.Result{}, nil
	}
	return r.reconcileSystem(ctx)
}

// SetupWithManager sets up event-driven and periodic status refreshes.
func (r *ChillSystemReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapToSystem := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: r.systemKey()}}
	})
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("chillsystem-status").
		For(&edgev1alpha1.ChillSystem{}).
		Watches(&appsv1.Deployment{}, mapToSystem, builder.WithPredicates(namedObjectPredicate(r.namespace(), r.controllerDeploymentName()))).
		Watches(&appsv1.DaemonSet{}, mapToSystem, builder.WithPredicates(namedObjectPredicate(r.namespace(), r.nodeDiscoveryDaemonSetName()))).
		Watches(&edgev1alpha1.DeviceClass{}, mapToSystem).
		Watches(&corev1.Node{}, mapToSystem).
		Complete(r); err != nil {
		return err
	}

	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		if synced := mgr.GetCache().WaitForCacheSync(ctx); !synced {
			return fmt.Errorf("wait for cache sync")
		}
		wait.UntilWithContext(ctx, func(ctx context.Context) {
			if _, err := r.reconcileSystem(ctx); err != nil {
				ctrl.LoggerFrom(ctx).Error(err, "refresh ChillSystem status")
			}
		}, r.refreshInterval())
		return nil
	}))
}

func (r *ChillSystemReconciler) reconcileSystem(ctx context.Context) (ctrl.Result, error) {
	key := r.systemKey()
	system := &edgev1alpha1.ChillSystem{}
	if err := r.Get(ctx, key, system); err != nil {
		if apierrors.IsNotFound(err) {
			system = &edgev1alpha1.ChillSystem{
				ObjectMeta: metav1.ObjectMeta{
					Name:      key.Name,
					Namespace: key.Namespace,
				},
			}
			if err := r.Create(ctx, system); err != nil && !apierrors.IsAlreadyExists(err) {
				return ctrl.Result{}, fmt.Errorf("create ChillSystem %s: %w", key.String(), err)
			}
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get ChillSystem %s: %w", key.String(), err)
	}

	next := buildStatus(r.observe(ctx, system), system.Status.Conditions, metav1Now())
	if statusesEqual(system.Status, next) {
		return ctrl.Result{RequeueAfter: r.refreshInterval()}, nil
	}
	system.Status = next
	if err := r.Status().Update(ctx, system); err != nil {
		return ctrl.Result{}, fmt.Errorf("update ChillSystem %s status: %w", key.String(), err)
	}
	return ctrl.Result{RequeueAfter: r.refreshInterval()}, nil
}

func (r *ChillSystemReconciler) observe(ctx context.Context, system *edgev1alpha1.ChillSystem) Observation {
	observed := Observation{
		ObservedGeneration: system.Generation,
		Namespace:          r.namespace(),

		ControllerDeploymentName: r.controllerDeploymentName(),

		NodeDiscoveryEnabled:       r.Options.NodeDiscoveryEnabled,
		NodeDiscoveryDaemonSetName: r.nodeDiscoveryDaemonSetName(),
	}

	controller := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: r.namespace(), Name: r.controllerDeploymentName()}, controller); err != nil {
		if !apierrors.IsNotFound(err) {
			observed.ControllerError = fmt.Errorf("observe controller Deployment: %w", err)
		}
	} else {
		observed.ControllerDeployment = controller
	}

	if r.Options.NodeDiscoveryEnabled {
		daemonSet := &appsv1.DaemonSet{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: r.namespace(), Name: r.nodeDiscoveryDaemonSetName()}, daemonSet); err != nil {
			if !apierrors.IsNotFound(err) {
				observed.NodeDiscoveryError = fmt.Errorf("observe node-discovery DaemonSet: %w", err)
			}
		} else {
			observed.NodeDiscoveryDaemonSet = daemonSet
		}
	}

	deviceClasses := &edgev1alpha1.DeviceClassList{}
	if err := r.List(ctx, deviceClasses); err != nil {
		observed.DeviceClassError = fmt.Errorf("observe DeviceClasses: %w", err)
	} else {
		observed.DeviceClassCount = int32(len(deviceClasses.Items))
	}

	nodes := &corev1.NodeList{}
	if err := r.List(ctx, nodes); err != nil {
		observed.NodeError = fmt.Errorf("observe Nodes: %w", err)
	} else {
		observed.ObservedNodeCount = int32(len(nodes.Items))
	}

	return observed
}

func (r *ChillSystemReconciler) systemKey() types.NamespacedName {
	return types.NamespacedName{Namespace: r.namespace(), Name: r.systemName()}
}

func (r *ChillSystemReconciler) systemName() string {
	if r.Options.SystemName != "" {
		return r.Options.SystemName
	}
	return DefaultSystemName
}

func (r *ChillSystemReconciler) namespace() string {
	return r.Options.Namespace
}

func (r *ChillSystemReconciler) controllerDeploymentName() string {
	if r.Options.ControllerDeploymentName != "" {
		return r.Options.ControllerDeploymentName
	}
	return "chill-controller-manager"
}

func (r *ChillSystemReconciler) nodeDiscoveryDaemonSetName() string {
	if r.Options.NodeDiscoveryDaemonSetName != "" {
		return r.Options.NodeDiscoveryDaemonSetName
	}
	return "chill-node-discovery"
}

func (r *ChillSystemReconciler) refreshInterval() time.Duration {
	if r.Options.RefreshInterval > 0 {
		return r.Options.RefreshInterval
	}
	return DefaultRefreshInterval
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
