package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/chilllabels"
	"github.com/lab-paper-code/chill/internal/deviceclassdiscovery"
)

const (
	defaultDeviceDiscoveryLabelKey = chilllabels.DeviceClass
	deviceDiscoveryManagedBy       = chilllabels.ManagedByDeviceDiscovery
	deviceDiscoveryManagedByKey    = chilllabels.ManagedBy
	deviceDiscoverySourceKey       = chilllabels.DiscoverySource
	deviceDiscoverySourceNode      = chilllabels.SourceNode
)

// DeviceDiscoveryOptions configures node-based DeviceClass discovery.
type DeviceDiscoveryOptions struct {
	LabelKey              string
	OverwriteManualLabels bool
	NodeLabelSelector     string
	RequireCatalogMatch   bool
	CatalogNamespace      string
	CatalogName           string
	CatalogKey            string
}

// DeviceDiscoveryReconciler discovers DeviceClass objects from Kubernetes Nodes.
type DeviceDiscoveryReconciler struct {
	client.Client
	Options DeviceDiscoveryOptions
}

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=edge.dacs.io,resources=deviceclasses,verbs=get;list;watch;create;update;patch

// Reconcile syncs discovered DeviceClasses and node labels from the current node set.
func (r *DeviceDiscoveryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	catalog, err := r.loadCatalog(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	listOptions, err := r.nodeListOptions()
	if err != nil {
		return ctrl.Result{}, err
	}

	nodes := &corev1.NodeList{}
	if err := r.List(ctx, nodes, listOptions...); err != nil {
		return ctrl.Result{}, fmt.Errorf("list nodes: %w", err)
	}

	for i := range nodes.Items {
		node := &nodes.Items[i]
		discovered, ok, err := deviceclassdiscovery.Discover(node, catalog, deviceclassdiscovery.Options{
			LabelKey:            r.labelKey(),
			RequireCatalogMatch: r.Options.RequireCatalogMatch,
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("discover device class for node %q: %w", node.Name, err)
		}
		if !ok {
			continue
		}

		if err := r.ensureDeviceClass(ctx, discovered); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.ensureNodeClassLabel(ctx, node, discovered.Name); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up node and catalog watches for device discovery.
func (r *DeviceDiscoveryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapToSingleton := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "cluster"}}}
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named("deviceclass-discovery").
		Watches(&corev1.Node{}, mapToSingleton).
		Watches(&corev1.ConfigMap{}, mapToSingleton, builder.WithPredicates(catalogConfigMapPredicate(r.Options.CatalogNamespace, r.Options.CatalogName))).
		Complete(r)
}

func catalogConfigMapPredicate(namespace, name string) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return isCatalogObject(e.Object, namespace, name) },
		DeleteFunc: func(e event.DeleteEvent) bool { return isCatalogObject(e.Object, namespace, name) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return isCatalogObject(e.ObjectNew, namespace, name) || isCatalogObject(e.ObjectOld, namespace, name)
		},
		GenericFunc: func(e event.GenericEvent) bool { return isCatalogObject(e.Object, namespace, name) },
	}
}

func isCatalogObject(obj client.Object, namespace, name string) bool {
	if name == "" {
		return false
	}
	if namespace != "" && obj.GetNamespace() != namespace {
		return false
	}
	return obj.GetName() == name
}

func (r *DeviceDiscoveryReconciler) labelKey() string {
	if r.Options.LabelKey != "" {
		return r.Options.LabelKey
	}
	return defaultDeviceDiscoveryLabelKey
}

func (r *DeviceDiscoveryReconciler) nodeListOptions() ([]client.ListOption, error) {
	if r.Options.NodeLabelSelector == "" {
		return nil, nil
	}
	selector, err := labels.Parse(r.Options.NodeLabelSelector)
	if err != nil {
		return nil, fmt.Errorf("parse device discovery node label selector %q: %w", r.Options.NodeLabelSelector, err)
	}
	return []client.ListOption{client.MatchingLabelsSelector{Selector: selector}}, nil
}

func (r *DeviceDiscoveryReconciler) catalogKey() string {
	if r.Options.CatalogKey != "" {
		return r.Options.CatalogKey
	}
	return deviceclassdiscovery.CatalogDataKey
}

func (r *DeviceDiscoveryReconciler) loadCatalog(ctx context.Context) (deviceclassdiscovery.Catalog, error) {
	if r.Options.CatalogName == "" {
		return deviceclassdiscovery.Catalog{}, nil
	}

	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{
		Namespace: r.Options.CatalogNamespace,
		Name:      r.Options.CatalogName,
	}
	if err := r.Get(ctx, key, configMap); err != nil {
		if apierrors.IsNotFound(err) {
			return deviceclassdiscovery.Catalog{}, nil
		}
		return deviceclassdiscovery.Catalog{}, fmt.Errorf("get discovery catalog configmap %s/%s: %w", key.Namespace, key.Name, err)
	}

	raw := configMap.Data[r.catalogKey()]
	if raw == "" {
		return deviceclassdiscovery.Catalog{}, nil
	}

	var catalog deviceclassdiscovery.Catalog
	if err := yaml.Unmarshal([]byte(raw), &catalog); err != nil {
		return deviceclassdiscovery.Catalog{}, fmt.Errorf("parse discovery catalog %s/%s: %w", key.Namespace, key.Name, err)
	}
	if err := deviceclassdiscovery.ValidateCatalog(catalog); err != nil {
		return deviceclassdiscovery.Catalog{}, fmt.Errorf("validate discovery catalog %s/%s: %w", key.Namespace, key.Name, err)
	}
	return catalog, nil
}

func (r *DeviceDiscoveryReconciler) ensureDeviceClass(ctx context.Context, discovered deviceclassdiscovery.DiscoveredClass) error {
	existing := &edgev1alpha1.DeviceClass{}
	if err := r.Get(ctx, types.NamespacedName{Name: discovered.Name}, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get DeviceClass %q: %w", discovered.Name, err)
		}

		deviceClass := &edgev1alpha1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: discovered.Name,
				Annotations: map[string]string{
					deviceDiscoveryManagedByKey: deviceDiscoveryManagedBy,
					deviceDiscoverySourceKey:    deviceDiscoverySourceNode,
				},
			},
			Spec: discovered.Spec,
		}
		if err := r.Create(ctx, deviceClass); err != nil {
			return fmt.Errorf("create DeviceClass %q: %w", discovered.Name, err)
		}
		return nil
	}

	if existing.Annotations[deviceDiscoveryManagedByKey] != deviceDiscoveryManagedBy {
		return nil
	}
	if deviceclassdiscovery.SpecEqual(existing.Spec, discovered.Spec) {
		return nil
	}

	original := existing.DeepCopy()
	existing.Spec = discovered.Spec
	if err := r.Patch(ctx, existing, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("patch DeviceClass %q: %w", discovered.Name, err)
	}
	return nil
}

func (r *DeviceDiscoveryReconciler) ensureNodeClassLabel(ctx context.Context, node *corev1.Node, className string) error {
	labelKey := r.labelKey()
	labels := node.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	if current := labels[labelKey]; current != "" {
		if current == className || !r.Options.OverwriteManualLabels {
			return nil
		}
	}

	original := node.DeepCopy()
	labels[labelKey] = className
	node.SetLabels(labels)
	annotations := node.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[deviceDiscoveryManagedByKey] = deviceDiscoveryManagedBy
	node.SetAnnotations(annotations)

	if err := r.Patch(ctx, node, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("patch node %q device class label: %w", node.Name, err)
	}
	return nil
}
