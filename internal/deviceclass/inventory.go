package deviceclass

import (
	"fmt"
	"sort"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

// InventoryClass is a read-only projection of one DeviceClass and the live
// Kubernetes Nodes selected by it. It is not persisted in DeviceClass.status.
type InventoryClass struct {
	Name            string
	UID             types.UID
	ResourceVersion string
	Spec            edgev1alpha1.DeviceClassSpec
	Nodes           []InventoryNode
}

// InventoryNode retains only Kubernetes-owned Node facts needed by the first
// inventory view. SchedulingDisabled is not CHILL execution eligibility.
type InventoryNode struct {
	Name               string
	UID                types.UID
	ResourceVersion    string
	Ready              corev1.ConditionStatus
	SchedulingDisabled bool
}

// ProjectInventory joins DeviceClasses with a caller-provided live Node list.
// Output is deterministic by class and Node name. A Node may appear in more
// than one class when selectors overlap; this function does not invent a
// uniqueness policy beyond Kubernetes label-selector semantics.
func ProjectInventory(classes []edgev1alpha1.DeviceClass, nodes []corev1.Node) ([]InventoryClass, error) {
	projected := make([]InventoryClass, 0, len(classes))
	for i := range classes {
		class := &classes[i]
		if len(class.Spec.NodeSelector) == 0 {
			return nil, fmt.Errorf("project DeviceClass %q inventory: node selector is required", class.Name)
		}

		selector, err := labels.ValidatedSelectorFromSet(class.Spec.NodeSelector)
		if err != nil {
			return nil, fmt.Errorf("project DeviceClass %q inventory: validate node selector: %w", class.Name, err)
		}

		inventoryClass := InventoryClass{
			Name:            class.Name,
			UID:             class.UID,
			ResourceVersion: class.ResourceVersion,
			Spec:            *class.Spec.DeepCopy(),
		}
		for j := range nodes {
			node := &nodes[j]
			if !selector.Matches(labels.Set(node.Labels)) {
				continue
			}
			inventoryClass.Nodes = append(inventoryClass.Nodes, InventoryNode{
				Name:               node.Name,
				UID:                node.UID,
				ResourceVersion:    node.ResourceVersion,
				Ready:              nodeReadyStatus(node),
				SchedulingDisabled: node.Spec.Unschedulable,
			})
		}
		sort.Slice(inventoryClass.Nodes, func(i, j int) bool {
			return inventoryClass.Nodes[i].Name < inventoryClass.Nodes[j].Name
		})
		projected = append(projected, inventoryClass)
	}

	sort.Slice(projected, func(i, j int) bool {
		return projected[i].Name < projected[j].Name
	})
	return projected, nil
}

func nodeReadyStatus(node *corev1.Node) corev1.ConditionStatus {
	for i := range node.Status.Conditions {
		condition := &node.Status.Conditions[i]
		if condition.Type == corev1.NodeReady {
			return condition.Status
		}
	}
	return corev1.ConditionUnknown
}
