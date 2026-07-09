package system

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
)

func (r *ChillSystemReconciler) finalize(ctx context.Context, system *edgev1alpha1.ChillSystem) (bool, error) {
	if done, err := r.deleteNodeDiscoveryDaemonSet(ctx, system); err != nil || !done {
		return done, err
	}
	if err := r.cleanupNodes(ctx); err != nil {
		return false, err
	}
	if err := r.deleteDeviceClasses(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (r *ChillSystemReconciler) deleteNodeDiscoveryDaemonSet(ctx context.Context, system *edgev1alpha1.ChillSystem) (bool, error) {
	key := types.NamespacedName{
		Namespace: r.managementNamespace(system),
		Name:      nodeDiscoveryDaemonSetName(system),
	}
	daemonSet := &appsv1.DaemonSet{}
	if err := r.Get(ctx, key, daemonSet); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("get node-discovery DaemonSet %s during finalization: %w", key.String(), err)
	}
	if err := r.Delete(ctx, daemonSet); err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("delete node-discovery DaemonSet %s during finalization: %w", key.String(), err)
	}
	return false, nil
}

func (r *ChillSystemReconciler) cleanupNodes(ctx context.Context) error {
	nodes := &corev1.NodeList{}
	if err := r.List(ctx, nodes); err != nil {
		return fmt.Errorf("list Nodes during finalization: %w", err)
	}
	for i := range nodes.Items {
		node := &nodes.Items[i]
		original := node.DeepCopy()
		if !removeManagedNodeMetadata(node) {
			continue
		}
		if err := r.Patch(ctx, node, client.MergeFrom(original)); err != nil {
			return fmt.Errorf("cleanup Node %q metadata during finalization: %w", node.Name, err)
		}
	}
	return nil
}

func removeManagedNodeMetadata(node *corev1.Node) bool {
	labels := node.GetLabels()
	annotations := node.GetAnnotations()
	changed := false

	if annotations[chillmeta.DiscoverySource] == chillmeta.SourceNodeDiscovery {
		changed = deleteMapKeys(labels,
			chillmeta.DeviceVendor,
			chillmeta.DeviceFamily,
			chillmeta.DeviceModel,
			chillmeta.Accelerator,
		) || changed
		changed = deleteMapKeys(annotations,
			chillmeta.DeviceModelRaw,
			chillmeta.DiscoverySource,
			chillmeta.NodeDiscoveryResult,
			chillmeta.NodeDiscoveryReason,
		) || changed
	}

	if annotations[chillmeta.ManagedBy] == chillmeta.ManagedByDeviceDiscovery {
		changed = deleteMapKeys(labels, chillmeta.DeviceClass) || changed
		changed = deleteMapKeys(annotations, chillmeta.ManagedBy) || changed
	}

	changed = deleteMapKeys(annotations,
		chillmeta.DeviceClassDiscoveryResult,
		chillmeta.DeviceClassDiscoveryReason,
		chillmeta.DeviceClassDiscoveryClass,
	) || changed

	if len(labels) == 0 && node.Labels != nil {
		node.Labels = nil
	}
	if len(annotations) == 0 && node.Annotations != nil {
		node.Annotations = nil
	}
	return changed
}

func deleteMapKeys(values map[string]string, keys ...string) bool {
	changed := false
	for _, key := range keys {
		if _, ok := values[key]; ok {
			delete(values, key)
			changed = true
		}
	}
	return changed
}

func (r *ChillSystemReconciler) deleteDeviceClasses(ctx context.Context) error {
	deviceClasses := &edgev1alpha1.DeviceClassList{}
	if err := r.List(ctx, deviceClasses); err != nil {
		return fmt.Errorf("list DeviceClasses during finalization: %w", err)
	}
	for i := range deviceClasses.Items {
		deviceClass := &deviceClasses.Items[i]
		if err := r.Delete(ctx, deviceClass); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete DeviceClass %q during finalization: %w", deviceClass.Name, err)
		}
	}
	return nil
}
