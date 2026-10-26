package kube

import (
	"cmp"
	"context"
	"reflect"
	"slices"
	"time"

	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const transitionRunKind = "transition"
const measurementLeaseKind = "measurement-lease"
const workloadDeletionKind = "workload-deletion"

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

// Callers hold mu. No partial acquisition; reset may inherit only terminal owners
// whose entire responsibility lies inside its admitted cleanup scope.
func (c *SchedulerController) reserveRun(id string, run *actionRun, operation Operation) bool {
	if existing := c.runs[id]; existing != nil && (existing.action.Status == nativeOperationRunning || existing.action.Status == nativeOperationCancel) {
		return false
	}
	scope := c.operationScope(operation)
	// Live owned physical resources may no longer appear in the last snapshot.
	scope.Pods = append(scope.Pods, run.scope.Pods...)
	scope.Instances = append(scope.Instances, run.scope.Instances...)
	if run.owner == "" {
		run.owner = id
	}
	var inherited []string
	if operation.Kind == operationReset {
		var err error
		inherited, err = c.resetReservationOwnersLocked(scope, operation.Nodes)
		if err != nil {
			return false
		}
	}
	for otherID, other := range c.runs {
		if otherID != id && other.reserved && other.owner != run.owner && !slices.Contains(inherited, other.owner) && scope.overlaps(other.scope) {
			return false
		}
	}
	for _, other := range c.runs {
		if other.reserved && slices.Contains(inherited, other.owner) {
			scope.Pods = append(scope.Pods, other.scope.Pods...)
			scope.Instances = append(scope.Instances, other.scope.Instances...)
			other.reserved = false
		}
	}
	run.inheritedReservation = len(inherited) > 0
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

// Fresh observation proves return when executors have disappeared, or a failed
// manual routing operation's exact executors acknowledge a known policy.
// A taint remains owned by the admission lifecycle.
func (c *SchedulerController) releaseObservedScopes(nodes []core.Node, pods []core.Pod, physical []serving.InstanceState, observedAt time.Time) {
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
				if pod.Spec.NodeName == ref.Name && (slices.Contains(run.scope.Pods, string(pod.UID)) || slices.Contains(run.scope.Workloads, workloadOwner(pod.OwnerReferences))) && !manualRoutingEffectConfirmed(run, &pod, physical) {
					returned = false
				}
			}
		}
		if returned {
			run.reserved = false
		}
	}
}

func manualRoutingEffectConfirmed(run *actionRun, pod *core.Pod, physical []serving.InstanceState) bool {
	operation := run.result.Operation
	if !isRoutingOperation(operation.Kind) || operation.WorkloadUID == "" ||
		(run.action.Status != nativeOperationFailed && run.action.Status != nativeOperationPartial) ||
		!slices.Contains(run.scope.Pods, string(pod.UID)) || !sameRunningContainer(pod, pod) {
		return false
	}
	id := physicalID(pod.UID, firstfloorStatus(pod).ContainerID)
	if !slices.Contains(run.scope.Instances, id) {
		return false
	}
	state := schedulerPhysicalState(id, physical)
	if state == nil || state.ID != id || state.PodUID != string(pod.UID) || state.Node.Name != pod.Spec.NodeName || !slices.Contains(run.scope.Nodes, state.Node) || !state.Ready || state.Stopped || state.Native == nil || !state.Native.Ready || !run.result.EndedAt.Before(state.ObservedAt) {
		return false
	}
	return manualRoutingPolicyConfirmed(run.result, state)
}

func manualRoutingPolicyConfirmed(result *OperationResult, state *serving.InstanceState) bool {
	operation := result.Operation
	id := state.ID
	actual := state.Native.Metadata
	if operation.Target != nil && actual.DecisionID == operation.Target.ID && actual.Policy.DecisionID == operation.Target.ID {
		for _, wanted := range operation.Target.Instances {
			if wanted.Profile != nil && wanted.WorkloadUID == operation.WorkloadUID && (wanted.ID == id || wanted.ID == state.TargetID) && wanted.Node == state.Node && schedulerPolicyAcknowledged(wanted, actual) {
				return true
			}
		}
	}
	// An unchanged previous policy is also a confirmed effect, not a successful
	// application of the requested declaration. Do not invent an ACK for it.
	before, known := result.Before.Native[id]
	if !known || actual.ProfileID != before.Metadata.ProfileID || actual.DecisionID != before.Metadata.DecisionID {
		return false
	}
	previous, current := before.Metadata.Policy, actual.Policy
	previous.Batches, current.Batches = nil, nil
	return reflect.DeepEqual(previous, current)
}

func (c *SchedulerController) releaseOwnerLocked(owner string) {
	for _, run := range c.runs {
		if run.owner == owner {
			run.reserved = false
			if run.action.Kind == measurementLeaseKind {
				run.action.Status = nativeOperationComplete
				run.result.Status, run.result.EndedAt = nativeOperationComplete, time.Now()
			}
		}
	}
}

func (c *SchedulerController) reservationsLocked() []serving.SchedulerReservation {
	byOwner := map[string]*serving.SchedulerReservation{}
	for _, run := range c.runs {
		if !run.reserved {
			continue
		}
		reservation := byOwner[run.owner]
		if reservation == nil {
			reservation = &serving.SchedulerReservation{Owner: run.owner}
			byOwner[run.owner] = reservation
		}
		reservation.Nodes = append(reservation.Nodes, run.scope.Nodes...)
		reservation.WorkloadUIDs = append(reservation.WorkloadUIDs, run.scope.Workloads...)
		reservation.PodUIDs = append(reservation.PodUIDs, run.scope.Pods...)
		reservation.Instances = append(reservation.Instances, run.scope.Instances...)
		if run.result != nil && (reservation.Target == nil || run.action.Kind == transitionRunKind) {
			reservation.Target = run.result.Operation.Target
		}
		if run.action.Reason != "" {
			reservation.Reason = run.action.Reason
		}
	}
	reservations := make([]serving.SchedulerReservation, 0, len(byOwner))
	for _, reservation := range byOwner {
		reservations = append(reservations, *reservation)
	}
	slices.SortFunc(reservations, func(a, b serving.SchedulerReservation) int {
		return cmp.Compare(a.Owner, b.Owner)
	})
	return reservations
}

func (c *SchedulerController) runReferencedLocked(id string) bool {
	for _, run := range c.runs {
		if (!run.reserved && run.action.Status != nativeOperationRunning) || run.result == nil || run.result.Operation.Target == nil {
			continue
		}
		for _, action := range run.result.Operation.Target.Actions {
			if action.ID == id || slices.Contains(action.DependsOn, id) {
				return true
			}
		}
	}
	return false
}

func (c *SchedulerController) nodeExecuting(node *core.Node) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, run := range c.runs {
		if run.action.Kind != measurementLeaseKind && run.action.Status == nativeOperationRunning && slices.Contains(run.scope.Nodes, serving.NodeReference{Name: node.Name, UID: string(node.UID)}) {
			return true
		}
	}
	return false
}

// reserveDeletion shares the operation ledger from acceptance through confirmed
// native deletion. Live owned Pods supplement snapshots after reset/removal.
func (c *SchedulerController) reserveDeletion(ctx context.Context, uid types.UID) error {
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	id := workloadDeletionKind + "/" + string(uid)
	c.mu.Lock()
	defer c.mu.Unlock()
	if run := c.runs[id]; run != nil && run.reserved {
		return nil
	}
	objects, err := c.observer.ownedResources(ctx, uid)
	if err != nil {
		return err
	}
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, meta.ListOptions{})
	if err != nil {
		return err
	}
	operation := Operation{ID: id, Kind: workloadDeletionKind, WorkloadUID: string(uid)}
	for _, object := range objects {
		if pod, ok := object.(*core.Pod); ok {
			for _, node := range nodes.Items {
				if node.Name == pod.Spec.NodeName {
					operation.Nodes = append(operation.Nodes, serving.NodeReference{Name: node.Name, UID: string(node.UID)})
				}
			}
		}
	}
	run := &actionRun{owner: id, action: serving.Action{ID: id, Kind: workloadDeletionKind, Status: nativeOperationRunning}}
	for _, object := range objects {
		if pod, ok := object.(*core.Pod); ok {
			run.scope.Pods = append(run.scope.Pods, string(pod.UID))
			if status := firstfloorStatus(pod); status != nil && status.ContainerID != "" {
				run.scope.Instances = append(run.scope.Instances, physicalID(pod.UID, status.ContainerID))
			}
		}
	}
	if !c.reserveRun(id, run, operation) {
		return refused("OperationConflict", "Workload deletion resources are reserved")
	}
	return nil
}

func (c *SchedulerController) releaseDeletion(uid types.UID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.runs, workloadDeletionKind+"/"+string(uid))
}
