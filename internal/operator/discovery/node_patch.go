package discovery

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	k8slabels "k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/lab-paper-code/chill/internal/deviceclass"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
)

func labelKey(options DeviceDiscoveryOptions) string {
	if options.LabelKey != "" {
		return options.LabelKey
	}
	return defaultDeviceDiscoveryLabelKey
}

func nodeListOptions(nodeLabelSelector string) ([]client.ListOption, error) {
	if nodeLabelSelector == "" {
		return nil, nil
	}
	selector, err := k8slabels.Parse(nodeLabelSelector)
	if err != nil {
		return nil, fmt.Errorf("parse device discovery node label selector %q: %w", nodeLabelSelector, err)
	}
	return []client.ListOption{client.MatchingLabelsSelector{Selector: selector}}, nil
}

func (r *DeviceDiscoveryReconciler) ensureNodeClassLabel(ctx context.Context, node *corev1.Node, options DeviceDiscoveryOptions, className string) error {
	original := node.DeepCopy()
	labels := mutableNodeLabels(node)
	annotations := mutableNodeAnnotations(node)
	reason := applyDeviceClassLabelPolicy(labels, annotations, labelKey(options), className, options.OverwriteManualLabels)
	applyDeviceClassDiscoveryStatus(annotations, chillmeta.DiscoveryResultMatched, reason, className)
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
		return chillmeta.DiscoveryReasonManualLabelPreserved
	}
	return chillmeta.DiscoveryReasonCatalogMatched
}

func applyDeviceClassDiscoveryStatus(annotations map[string]string, result, reason, className string) {
	annotations[chillmeta.DeviceClassDiscoveryResult] = result
	annotations[chillmeta.DeviceClassDiscoveryReason] = reason
	if className == "" {
		delete(annotations, chillmeta.DeviceClassDiscoveryClass)
		return
	}
	annotations[chillmeta.DeviceClassDiscoveryClass] = className
}

func catalogMissReason(catalog deviceclass.Catalog) string {
	if len(catalog.Classes) == 0 {
		return chillmeta.DiscoveryReasonCatalogEmpty
	}
	return chillmeta.DiscoveryReasonNoCatalogMatch
}
