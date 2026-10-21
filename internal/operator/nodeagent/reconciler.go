package nodeagent

import (
	"context"
	"fmt"
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
)

var daemonSetResource = schema.GroupResource{Group: "apps", Resource: "daemonsets"}

// Reconciler manages the node agent DaemonSet on every Node and, when enabled,
// the actuation DaemonSet on Nodes of the DeviceClasses its policies name.
type Reconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Options Options
}

// +kubebuilder:rbac:groups=edge.dacs.io,resources=chillsystems,verbs=get;list;watch
// +kubebuilder:rbac:groups=edge.dacs.io,resources=nodeactuations,verbs=get;list;watch
// +kubebuilder:rbac:groups=edge.dacs.io,resources=deviceclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=list;patch
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
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("get ChillSystem %s for node agent: %w", systemName, err)
		}
		options := r.Options.Defaulted()
		options.SystemName = systemName
		return r.stop(ctx, options)
	}
	options := runtimeOptions(system, r.Options)
	spec := system.Spec.NodeAgent
	if !system.DeletionTimestamp.IsZero() || !spec.Enabled {
		return r.stop(ctx, options)
	}
	if err := r.deleteLegacy(ctx, options); err != nil {
		return ctrl.Result{}, err
	}
	config, err := r.loadConfig(ctx, options, spec.Actuation)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.applyDaemonSet(ctx, system, buildAgentDaemonSet(options, config)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile node agent DaemonSet %s/%s: %w", options.Namespace, options.agentDaemonSetName(), err)
	}
	if !spec.Actuation {
		return ctrl.Result{RequeueAfter: options.ReconcileInterval}, r.stopActuation(ctx, options)
	}
	classes := &edgev1alpha1.DeviceClassList{}
	if err := r.List(ctx, classes); err != nil {
		return ctrl.Result{}, fmt.Errorf("list DeviceClasses for node agent actuation: %w", err)
	}
	actuation, err := buildActuationDaemonSet(options, config, classes.Items)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("build node agent actuation DaemonSet: %w", err)
	}
	if err := r.applyDaemonSet(ctx, system, actuation); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile node agent actuation DaemonSet %s/%s: %w", options.Namespace, actuation.Name, err)
	}
	return ctrl.Result{RequeueAfter: options.ReconcileInterval}, nil
}

func (r *Reconciler) stop(ctx context.Context, options Options) (ctrl.Result, error) {
	if err := r.deleteLegacy(ctx, options); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.deleteDaemonSet(ctx, options.Namespace, options.agentDaemonSetName()); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: options.ReconcileInterval}, r.stopActuation(ctx, options)
}

// deleteLegacy removes the DaemonSets the node agent replaced. The legacy
// actuator goes only once no retained transition depends on it.
func (r *Reconciler) deleteLegacy(ctx context.Context, options Options) error {
	discovery, actuator := component.LegacyNodeDaemonSetNames(options.SystemName)
	if err := r.deleteDaemonSet(ctx, options.Namespace, discovery); err != nil {
		return err
	}
	safe, err := SafeToStop(ctx, r.Client, options.Namespace)
	if err != nil || !safe {
		return err
	}
	return r.deleteDaemonSet(ctx, options.Namespace, actuator)
}

// stopActuation deletes the actuation DaemonSet only once no retained
// transition depends on it; otherwise the periodic requeue retries. Without
// an actuation agent no read-back is current, so it is removed from Nodes.
func (r *Reconciler) stopActuation(ctx context.Context, options Options) error {
	safe, err := SafeToStop(ctx, r.Client, options.Namespace)
	if err != nil || !safe {
		return err
	}
	if err := r.deleteDaemonSet(ctx, options.Namespace, options.actuationDaemonSetName()); err != nil {
		return err
	}
	return StripReadBack(ctx, r.Client)
}

// StripReadBack removes the actuation agent's power read-back from every Node.
func StripReadBack(ctx context.Context, c client.Client) error {
	nodes := &corev1.NodeList{}
	if err := c.List(ctx, nodes); err != nil {
		return fmt.Errorf("list Nodes to remove power read-back: %w", err)
	}
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if _, ok := node.Annotations[chillmeta.PowerActuator]; !ok {
			continue
		}
		original := node.DeepCopy()
		for _, key := range chillmeta.NodeActuationAnnotationKeys() {
			delete(node.Annotations, key)
		}
		if err := c.Patch(ctx, node, client.MergeFrom(original)); err != nil {
			return fmt.Errorf("remove power read-back from Node %q: %w", node.Name, err)
		}
	}
	return nil
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

func (r *Reconciler) deleteDaemonSet(ctx context.Context, namespace, name string) error {
	key := types.NamespacedName{Namespace: namespace, Name: name}
	daemonSet := &appsv1.DaemonSet{}
	if err := r.Get(ctx, key, daemonSet); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get DaemonSet %s: %w", key, err)
	}
	if err := r.Delete(ctx, daemonSet); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete DaemonSet %s: %w", key, err)
	}
	return nil
}
