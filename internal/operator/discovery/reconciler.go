package discovery

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/lab-paper-code/chill/internal/deviceclass"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
)

const (
	defaultDeviceDiscoveryLabelKey = chillmeta.DeviceClass
	deviceDiscoveryManagedBy       = chillmeta.ManagedByDeviceDiscovery
	deviceDiscoveryManagedByKey    = chillmeta.ManagedBy
	deviceDiscoverySourceKey       = chillmeta.DiscoverySource
	deviceDiscoverySourceNode      = chillmeta.SourceNode
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
// +kubebuilder:rbac:groups=edge.dacs.io,resources=deviceclasses,verbs=get;list;watch;create;update;patch;delete

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

	discoveredClasses := map[string]struct{}{}
	for i := range nodes.Items {
		node := &nodes.Items[i]
		discovered, ok, err := deviceclass.Discover(node, catalog, deviceclass.Options{
			LabelKey:            r.labelKey(),
			RequireCatalogMatch: r.Options.RequireCatalogMatch,
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("discover device class for node %q: %w", node.Name, err)
		}
		if !ok {
			if err := r.ensureNodeClassDiscoveryAnnotations(ctx, node, chillmeta.DiscoveryResultUnmatched, catalogMissReason(catalog), ""); err != nil {
				return ctrl.Result{}, err
			}
			continue
		}

		if err := r.ensureDeviceClass(ctx, discovered); err != nil {
			return ctrl.Result{}, err
		}
		discoveredClasses[discovered.Name] = struct{}{}
		if err := r.ensureNodeClassLabel(ctx, node, discovered.Name); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.pruneDeviceClasses(ctx, discoveredClasses); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}
