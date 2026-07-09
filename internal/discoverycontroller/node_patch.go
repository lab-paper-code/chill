package discoverycontroller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	k8slabels "k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/lab-paper-code/chill/internal/deviceclasscatalog"
	chilllabels "github.com/lab-paper-code/chill/internal/labels"
)

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
	selector, err := k8slabels.Parse(r.Options.NodeLabelSelector)
	if err != nil {
		return nil, fmt.Errorf("parse device discovery node label selector %q: %w", r.Options.NodeLabelSelector, err)
	}
	return []client.ListOption{client.MatchingLabelsSelector{Selector: selector}}, nil
}

func (r *DeviceDiscoveryReconciler) ensureNodeClassLabel(ctx context.Context, node *corev1.Node, className string) error {
	original := node.DeepCopy()
	labels := mutableNodeLabels(node)
	annotations := mutableNodeAnnotations(node)
	reason := applyDeviceClassLabelPolicy(labels, annotations, r.labelKey(), className, r.Options.OverwriteManualLabels)
	applyDeviceClassDiscoveryStatus(annotations, chilllabels.DiscoveryResultMatched, reason, className)
	return r.patchNodeMetadataIfChanged(ctx, node, original, "device class label")
}

func (r *DeviceDiscoveryReconciler) ensureNodeClassDiscoveryAnnotations(ctx context.Context, node *corev1.Node, result, reason, className string) error {
	original := node.DeepCopy()
	annotations := mutableNodeAnnotations(node)
	applyDeviceClassDiscoveryStatus(annotations, result, reason, className)
	return r.patchNodeMetadataIfChanged(ctx, node, original, "device class discovery annotations")
}

func (r *DeviceDiscoveryReconciler) patchNodeMetadataIfChanged(ctx context.Context, node, original *corev1.Node, field string) error {
	if apiequality.Semantic.DeepEqual(original.GetLabels(), node.GetLabels()) &&
		apiequality.Semantic.DeepEqual(original.GetAnnotations(), node.GetAnnotations()) {
		return nil
	}
	if err := r.Patch(ctx, node, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("patch node %q %s: %w", node.Name, field, err)
	}
	return nil
}

func mutableNodeLabels(node *corev1.Node) map[string]string {
	labels := node.GetLabels()
	if labels == nil {
		labels = map[string]string{}
		node.SetLabels(labels)
	}
	return labels
}

func mutableNodeAnnotations(node *corev1.Node) map[string]string {
	annotations := node.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
		node.SetAnnotations(annotations)
	}
	return annotations
}

func applyDeviceClassLabelPolicy(labels, annotations map[string]string, labelKey, className string, overwriteManualLabels bool) string {
	current := labels[labelKey]
	switch {
	case current == "":
		labels[labelKey] = className
		annotations[deviceDiscoveryManagedByKey] = deviceDiscoveryManagedBy
	case current == className:
	case annotations[deviceDiscoveryManagedByKey] == deviceDiscoveryManagedBy:
		labels[labelKey] = className
	case overwriteManualLabels:
		labels[labelKey] = className
		annotations[deviceDiscoveryManagedByKey] = deviceDiscoveryManagedBy
	default:
		return chilllabels.DiscoveryReasonManualLabelPreserved
	}
	return chilllabels.DiscoveryReasonCatalogMatched
}

func applyDeviceClassDiscoveryStatus(annotations map[string]string, result, reason, className string) {
	annotations[chilllabels.DeviceClassDiscoveryResult] = result
	annotations[chilllabels.DeviceClassDiscoveryReason] = reason
	if className == "" {
		delete(annotations, chilllabels.DeviceClassDiscoveryClass)
		return
	}
	annotations[chilllabels.DeviceClassDiscoveryClass] = className
}

func catalogMissReason(catalog deviceclasscatalog.Catalog) string {
	if len(catalog.Classes) == 0 {
		return chilllabels.DiscoveryReasonCatalogEmpty
	}
	return chilllabels.DiscoveryReasonNoCatalogMatch
}
