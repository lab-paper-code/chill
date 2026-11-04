package state

import (
	"slices"

	"github.com/lab-paper-code/chill/internal/serving"
)

// OperationResources is the value footprint admitted by the reservation ledger.
// Owners identify local physical ownership; only Workloads is UID-exclusive.
type OperationResources struct {
	Nodes                                       []serving.NodeReference
	Workloads, Routing, Owners, Pods, Instances []string
}

func (scope *OperationResources) includeOwner(uid string) {
	if uid != "" && !slices.Contains(scope.Owners, uid) {
		scope.Owners = append(scope.Owners, uid)
	}
}

// ResourcesForOperation computes policy from explicit operation/observation
// values. It neither reads a ledger nor acquires a lock.
func ResourcesForOperation(operation Operation, snapshot serving.SchedulerSnapshot) OperationResources {
	scope := OperationResources{Nodes: slices.Clone(operation.Nodes), Owners: slices.Clone(operation.WorkloadUIDs)}
	if operation.Kind == OperationTransition && operation.Target != nil {
		scope.Owners = PositiveWorkloadScope(*operation.Target, operation.WorkloadUIDs)
	}
	scope.includeOwner(operation.WorkloadUID)
	if operation.Prepare != nil && operation.Prepare.Owner != nil {
		scope.includeOwner(operation.Prepare.Owner.UID)
	}
	if !localPhysicalOperation(operation.Kind) {
		scope.Workloads = slices.Clone(operation.WorkloadUIDs)
		if operation.WorkloadUID != "" {
			scope.Workloads = append(scope.Workloads, operation.WorkloadUID)
		}
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
	scope.addPlannedEffects(operation)
	for _, instance := range snapshot.Instances {
		affected := slices.Contains(scope.Nodes, instance.Node) || slices.Contains(scope.Workloads, instance.WorkloadUID) || slices.Contains(scope.Workloads, instance.OwnerWorkloadUID) || slices.Contains(scope.Instances, instance.ID) || slices.Contains(scope.Pods, instance.PodUID)
		if !affected {
			for _, uid := range scope.Workloads {
				if snapshot.Routing[uid][instance.ID] > 0 {
					affected = true
					break
				}
			}
		}
		if affected {
			scope.includeOwner(instance.WorkloadUID)
			scope.includeOwner(instance.OwnerWorkloadUID)
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
			if slices.Contains(scope.Nodes, instance.Node) {
				scope.includeOwner(instance.WorkloadUID)
			}
		}
	}
	return scope
}

func localPhysicalOperation(kind string) bool {
	switch kind {
	case OperationTransition, OperationPrepare, OperationStop, OperationDrain, OperationRemove, OperationPower, OperationResize:
		return true
	default:
		return false
	}
}

func (scope *OperationResources) addPlannedEffects(operation Operation) {
	if operation.Kind == OperationTransition && operation.Target != nil && len(scope.Nodes) == 0 && slices.ContainsFunc(operation.Target.Actions, func(action serving.Action) bool { return action.Kind == OperationRoute }) {
		scope.Routing = slices.Clone(operation.WorkloadUIDs)
	}
	if !IsRoutingOperation(operation.Kind) {
		return
	}
	scope.Routing = slices.Clone(scope.Workloads)
	if operation.PolicyInstances != nil {
		scope.Workloads = nil
	}
	if operation.Target == nil {
		return
	}
	for uid := range operation.Target.Routing {
		if !operation.ControlsWorkload(uid) {
			continue
		}
		if !slices.Contains(scope.Routing, uid) {
			scope.Routing = append(scope.Routing, uid)
		}
		scope.includeOwner(uid)
	}
	for _, wanted := range operation.Target.Instances {
		if operation.ControlsWorkload(wanted.WorkloadUID) && (operation.PolicyInstances == nil || slices.Contains(operation.PolicyInstances, wanted.ID)) {
			scope.Nodes = append(scope.Nodes, wanted.Node)
			scope.Instances = append(scope.Instances, wanted.ID)
		}
	}
}

// HasPhysicalWorkloadScope classifies each UID independently, including an
// unrouted owner's retirement that no longer appears in the target.
func HasPhysicalWorkloadScope(target []serving.SchedulerInstance, before OperationState, nodes []serving.NodeReference, uid string) bool {
	return slices.ContainsFunc(target, func(instance serving.SchedulerInstance) bool {
		return instance.WorkloadUID == uid && slices.Contains(nodes, instance.Node)
	}) || slices.ContainsFunc(before.Instances, func(instance serving.InstanceState) bool {
		return slices.Contains(nodes, instance.Node) && (instance.OwnerWorkloadUID == uid || before.Routing[uid][instance.ID] > 0)
	})
}

// FailedStopCleanup finishes only duties of an already completed stop. Original
// dependencies remain intact; failed routes, new stops and prepares are not retried.
func FailedStopCleanup(target serving.SchedulerCandidate, completed []serving.Action) serving.SchedulerCandidate {
	actions := target.Actions
	target.Actions = nil
	for _, action := range actions {
		if action.Kind != OperationDrain && action.Kind != OperationRemove {
			continue
		}
		if slices.ContainsFunc(completed, func(stop serving.Action) bool {
			return stop.Kind == OperationStop && stop.Status == NativeOperationComplete &&
				stop.Instance == action.Instance && stop.Node == action.Node
		}) {
			target.Actions = append(target.Actions, action)
		}
	}
	return target
}
