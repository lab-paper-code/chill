package discovery

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/defaults"
	"github.com/lab-paper-code/chill/internal/deviceclass"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
)

// DeviceDiscoveryOptions configures node-based DeviceClass discovery.
type DeviceDiscoveryOptions struct {
	SystemName            string
	Namespace             string
	LabelKey              string
	OverwriteManualLabels bool
	NodeLabelSelector     string
	RequireCatalogMatch   bool
	FallbackPowerModes    []edgev1alpha1.PowerMode
	CatalogNamespace      string
	CatalogName           string
	CatalogKey            string
}

// DeviceDiscoveryReconciler discovers DeviceClass objects from Kubernetes Nodes.
type DeviceDiscoveryReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Options DeviceDiscoveryOptions
}

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=edge.dacs.io,resources=deviceclasses,verbs=get;list;watch;create;patch;delete

// Reconcile syncs discovered DeviceClasses and node labels from the current node set.
func (r *DeviceDiscoveryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Name == "" {
		req.Name = r.systemName()
	}

	system := &edgev1alpha1.ChillSystem{}
	if err := r.Get(ctx, types.NamespacedName{Name: req.Name}, system); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get ChillSystem %s for DeviceClass discovery: %w", req.Name, err)
	}
	if !system.DeletionTimestamp.IsZero() || !system.Spec.DeviceDiscovery.Enabled {
		return ctrl.Result{}, nil
	}

	options := r.runtimeOptions(system)
	catalog, err := r.loadCatalog(ctx, options)
	if err != nil {
		return ctrl.Result{}, err
	}

	listOptions, err := nodeListOptions(options.NodeLabelSelector)
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
			LabelKey:            labelKey(options),
			RequireCatalogMatch: options.RequireCatalogMatch,
			FallbackPowerModes:  options.FallbackPowerModes,
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("discover device class for node %q: %w", node.Name, err)
		}
		if !ok {
			if err := r.ensureNodeClassDiscoveryAnnotations(ctx, node, options, chillmeta.DiscoveryResultUnmatched, catalogMissReason(catalog), ""); err != nil {
				return ctrl.Result{}, err
			}
			continue
		}

		if err := r.ensureDeviceClass(ctx, system, discovered); err != nil {
			return ctrl.Result{}, err
		}
		discoveredClasses[discovered.Name] = struct{}{}
		if err := r.ensureNodeClassLabel(ctx, node, options, discovered.Name); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.pruneDeviceClasses(ctx, system, discoveredClasses); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *DeviceDiscoveryReconciler) systemName() string {
	return defaults.String(strings.TrimSpace(r.Options.SystemName), component.DefaultSystemName)
}

func (r *DeviceDiscoveryReconciler) runtimeOptions(system *edgev1alpha1.ChillSystem) DeviceDiscoveryOptions {
	spec := system.Spec.DeviceDiscovery
	catalog := spec.Catalog
	requireCatalogMatch := r.Options.RequireCatalogMatch
	if spec.RequireCatalogMatch != nil {
		requireCatalogMatch = *spec.RequireCatalogMatch
	}
	overwriteManualLabels := r.Options.OverwriteManualLabels
	if spec.OverwriteManualLabels != nil {
		overwriteManualLabels = *spec.OverwriteManualLabels
	}
	fallbackPowerModes := append([]edgev1alpha1.PowerMode(nil), r.Options.FallbackPowerModes...)
	if len(spec.FallbackPowerModes) > 0 {
		fallbackPowerModes = append([]edgev1alpha1.PowerMode(nil), spec.FallbackPowerModes...)
	}
	namespace := defaults.String(strings.TrimSpace(system.Spec.ManagementNamespace), strings.TrimSpace(r.Options.Namespace))
	return DeviceDiscoveryOptions{
		SystemName:            system.Name,
		Namespace:             namespace,
		LabelKey:              defaults.String(strings.TrimSpace(spec.LabelKey), r.Options.LabelKey),
		OverwriteManualLabels: overwriteManualLabels,
		NodeLabelSelector:     defaults.String(strings.TrimSpace(spec.NodeLabelSelector), r.Options.NodeLabelSelector),
		RequireCatalogMatch:   requireCatalogMatch,
		FallbackPowerModes:    fallbackPowerModes,
		CatalogNamespace:      defaults.String(strings.TrimSpace(catalog.Namespace), namespace),
		CatalogName:           defaults.String(strings.TrimSpace(catalog.Name), r.Options.CatalogName),
		CatalogKey:            defaults.String(strings.TrimSpace(catalog.Key), r.Options.CatalogKey),
	}
}
