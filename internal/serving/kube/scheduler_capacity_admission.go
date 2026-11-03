package kube

import (
	"errors"
	"fmt"
	"strings"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
)

// ValidatePreparationCapacity admits coexistence using fresh physical Pod
// requests and the owner's configured inventory/reservations, not CPU isolation.
// Only serving-namespace Pods establish serving ownership; all Pod requests count.
// It never retires an existing serving instance.
func ValidatePreparationCapacity(
	servingNamespace string,
	node *core.Node, pods *core.PodList, inventory serving.SchedulerNode, wanted serving.SchedulerInstance,
) error {
	if inventory.Node != wanted.Node || string(node.UID) != wanted.Node.UID || node.Name != wanted.Node.Name {
		return errors.New("preparation inventory differs from the current physical Node")
	}
	ready := false
	for _, condition := range node.Status.Conditions {
		if condition.Type == core.NodeMemoryPressure && condition.Status == core.ConditionTrue {
			return errors.New("preparation target has actual Node memory pressure")
		}
		ready = ready || condition.Type == core.NodeReady && condition.Status == core.ConditionTrue
	}
	memory := node.Status.Allocatable.Memory().Value()
	gpus := node.Status.Allocatable["nvidia.com/gpu"]
	if !ready || memory <= 0 || int64(len(inventory.GPUUUIDs)) > gpus.Value() {
		return errors.New("preparation requires current Node readiness and bounded memory/device inventory")
	}
	for _, pod := range pods.Items {
		if pod.Namespace == servingNamespace && pod.Spec.NodeName == node.Name &&
			pod.Status.Phase != core.PodSucceeded && pod.Status.Phase != core.PodFailed &&
			pod.Labels["app.kubernetes.io/component"] == servingstate.ServingInstanceComponent {
			return errors.New("preparation target already has an active serving Pod")
		}
	}
	// Discard the older aggregate; the same capture path recomputes all current
	// foreign/system Pod requests, init/sidecar maxima and overhead exactly once.
	inventory.NonServing = nil
	inventory.Reasons = nil
	inventory.MemoryBytes = uint64(memory)
	var snapshot serving.SchedulerSnapshot
	servingstate.CaptureNodePods(&snapshot, &inventory, node, pods, nil, nil)
	if len(inventory.Reasons) != 0 {
		return fmt.Errorf("preparation non-serving admission: %s", strings.Join(inventory.Reasons, "; "))
	}
	allocations := append([]serving.SchedulerAllocation{inventory.Reserved}, inventory.NonServing...)
	allocations = append(allocations, wanted.Allocation)
	if reasons := serving.ValidateNodeBudget(inventory, allocations, nil); len(reasons) != 0 {
		return fmt.Errorf("preparation declared Node budget: %s", strings.Join(reasons, "; "))
	}
	return nil
}
