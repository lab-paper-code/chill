package kube

import (
	"slices"
	"time"

	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
)

// resourceScope is acquired atomically before an operation can have effects.
// A completed execution can still own its scope until observation proves return.
type resourceScope struct {
	Nodes     []serving.NodeReference
	Workloads []string
	Pods      []string
	Instances []string
}

func (s resourceScope) overlaps(other resourceScope) bool {
	return slices.ContainsFunc(s.Nodes, func(n serving.NodeReference) bool { return slices.Contains(other.Nodes, n) }) ||
		slices.ContainsFunc(s.Workloads, func(uid string) bool { return slices.Contains(other.Workloads, uid) }) ||
		slices.ContainsFunc(s.Pods, func(uid string) bool { return slices.Contains(other.Pods, uid) }) ||
		slices.ContainsFunc(s.Instances, func(id string) bool { return slices.Contains(other.Instances, id) })
}

func (c *SchedulerController) operationScope(operation Operation) resourceScope {
	scope := resourceScope{Workloads: slices.Clone(operation.WorkloadUIDs), Nodes: slices.Clone(operation.Nodes)}
	if operation.WorkloadUID != "" {
		scope.Workloads = append(scope.Workloads, operation.WorkloadUID)
	}
	if operation.Node.UID != "" {
		scope.Nodes = append(scope.Nodes, operation.Node)
	}
	if operation.PodUID != "" {
		scope.Pods = append(scope.Pods, operation.PodUID)
	}
	if operation.Instance != "" {
		scope.Instances = append(scope.Instances, operation.Instance)
	}
	if operation.Prepare != nil && operation.Prepare.Owner != nil {
		scope.Workloads = append(scope.Workloads, operation.Prepare.Owner.UID)
	}
	if operation.Target != nil {
		for uid := range operation.Target.Routing {
			if isRoutingOperation(operation.Kind) && operation.controlsWorkload(uid) {
				scope.Workloads = append(scope.Workloads, uid)
			}
		}
		for _, instance := range operation.Target.Instances {
			if !isRoutingOperation(operation.Kind) && (instance.ID == operation.Instance || instance.Node == operation.Node) && instance.WorkloadUID != "" {
				scope.Workloads = append(scope.Workloads, instance.WorkloadUID)
			}
		}
	}
	for _, instance := range c.state.Snapshot.Instances {
		affected := slices.Contains(scope.Workloads, instance.WorkloadUID) || slices.Contains(scope.Workloads, instance.OwnerWorkloadUID) || slices.Contains(scope.Instances, instance.ID) || slices.Contains(scope.Pods, instance.PodUID)
		if !affected {
			for _, uid := range scope.Workloads {
				if c.state.Snapshot.Routing[uid][instance.ID] > 0 {
					affected = true
					break
				}
			}
		}
		if affected {
			scope.Nodes = append(scope.Nodes, instance.Node)
			scope.Instances = append(scope.Instances, instance.ID)
			if instance.PodUID != "" {
				scope.Pods = append(scope.Pods, instance.PodUID)
			}
		}
	}
	if operation.Target != nil {
		for _, instance := range operation.Target.Instances {
			if slices.Contains(scope.Workloads, instance.WorkloadUID) {
				scope.Nodes = append(scope.Nodes, instance.Node)
				scope.Instances = append(scope.Instances, instance.ID)
			}
		}
	}
	return scope
}

// Callers hold mu. No partial acquisition and no kind-specific exemptions.
func (c *SchedulerController) reserveRun(id string, run *actionRun, operation Operation) bool {
	if existing := c.runs[id]; existing != nil && (existing.action.Status == nativeOperationRunning || existing.action.Status == nativeOperationCancel) {
		return false
	}
	scope := c.operationScope(operation)
	if run.owner == "" {
		run.owner = id
	}
	for otherID, other := range c.runs {
		if otherID != id && other.reserved && other.owner != run.owner && scope.overlaps(other.scope) {
			return false
		}
	}
	run.scope, run.reserved = scope, true
	c.runs[id] = run
	return true
}

func (c *SchedulerController) authorize(operation Operation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	scope := c.operationScope(operation)
	for _, run := range c.runs {
		if run.reserved && scope.overlaps(run.scope) {
			return refused("OperationConflict", "resources are reserved by "+run.owner)
		}
	}
	return nil
}

// Empty, settled Nodes prove that a terminal run has no remaining physical
// executor or power actuation. A taint remains owned by the admission lifecycle.
func (c *SchedulerController) releaseEmptyScopes(nodes []core.Node, pods []core.Pod, observedAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, run := range c.runs {
		if !run.reserved || run.action.Status == nativeOperationRunning || run.action.Status == nativeOperationCancel || run.result == nil || !run.result.EndedAt.Before(observedAt) || len(run.scope.Nodes) == 0 {
			continue
		}
		returned := true
		for _, ref := range run.scope.Nodes {
			for _, node := range nodes {
				if node.Name == ref.Name && string(node.UID) == ref.UID {
					if node.Labels[chillmeta.PowerModeState] == chillmeta.PowerModePending {
						returned = false
					}
				}
			}
			for _, pod := range pods {
				if pod.Spec.NodeName == ref.Name && (slices.Contains(run.scope.Pods, string(pod.UID)) || slices.Contains(run.scope.Workloads, workloadOwner(pod.OwnerReferences))) {
					returned = false
				}
			}
		}
		if returned {
			run.reserved = false
		}
	}
}

func (c *SchedulerController) releaseOwnerLocked(owner string) {
	for _, run := range c.runs {
		if run.owner == owner {
			run.reserved = false
		}
	}
}
