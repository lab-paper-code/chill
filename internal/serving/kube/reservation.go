package kube

import (
	"cmp"
	"context"
	"reflect"
	"slices"
	"strings"
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
	var cleanupOwner *actionRun
	if operation.Kind == operationRoute {
		var allowed bool
		cleanupOwner, allowed = c.reserveRouteOwnerLocked(run, operation, scope)
		if !allowed {
			return false
		}
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
	if cleanupOwner != nil {
		if cleanupOwner.cleanupRoutes == nil {
			cleanupOwner.cleanupRoutes = map[string]*actionRun{}
		}
		for uid := range operation.Target.Routing {
			if operation.controlsWorkload(uid) {
				cleanupOwner.cleanupRoutes[uid] = run
			}
		}
	}
	return true
}

func (c *SchedulerController) reserveRouteOwnerLocked(run *actionRun, operation Operation, scope resourceScope) (*actionRun, bool) {
	var cleanupOwner *actionRun
	for _, parent := range c.runs {
		if parent.owner != run.owner && parent.action.Kind == transitionRunKind && parent.reserved && scope.overlaps(parent.scope) {
			if cleanupOwner != nil || !c.cleanupRouteAllowedLocked(parent, operation) {
				return nil, false
			}
			cleanupOwner = parent
		}
	}
	if cleanupOwner != nil {
		run.owner = cleanupOwner.owner
	}
	// Policy ACKs for one owner must never overlap, including refreshes.
	for _, other := range c.runs {
		if other.owner == run.owner && other.action.Kind == operationRoute &&
			(other.action.Status == nativeOperationRunning || other.action.Status == nativeOperationCancel) {
			return nil, false
		}
	}
	return cleanupOwner, true
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

// Fresh observation proves physical return or an exact executor's known effect.
// Terminal transitions consume this proof in reconcileSnapshot without changing
// failed receipts. A taint remains owned by the admission lifecycle.
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
				if pod.Spec.NodeName == ref.Name && (slices.Contains(run.scope.Pods, string(pod.UID)) || slices.Contains(run.scope.Workloads, workloadOwner(pod.OwnerReferences))) && !c.knownOwnerEffectConfirmed(run, &pod, physical) {
					returned = false
				}
			}
		}
		if run.action.Kind == transitionRunKind {
			run.returnObserved = returned
		} else if returned {
			run.reserved = false
		}
	}
}

// Terminal preparations share only their frozen parent's terminal receipts.
// Owner strings alone do not establish DAG membership or physical identity.
func (c *SchedulerController) knownOwnerEffectConfirmed(run *actionRun, pod *core.Pod, physical []serving.InstanceState) bool {
	if run.owner == "" || (run.action.Kind != transitionRunKind && run.action.Kind != operationPrepare) {
		return knownEffectConfirmed(run, pod, physical, run.result.EndedAt)
	}
	parent := run
	if run.action.Kind == operationPrepare {
		parent = nil
		for _, candidate := range c.runs {
			if candidate.owner == run.owner && candidate.action.Kind == transitionRunKind && candidate.result != nil &&
				candidate.result.Operation.Target != nil && slices.ContainsFunc(candidate.result.Operation.Target.Actions, func(a serving.Action) bool {
				return a.ID == run.action.ID && actionEquivalent(a, run.action)
			}) {
				parent = candidate
				break
			}
		}
	}
	if parent == nil {
		return knownEffectConfirmed(run, pod, physical, run.result.EndedAt)
	}
	if parent.result == nil || parent.result.Operation.Target == nil ||
		parent.action.Status == nativeOperationRunning || parent.action.Status == nativeOperationCancel {
		return false
	}
	for _, sibling := range c.runs {
		if sibling.owner == parent.owner && (sibling.action.Status == nativeOperationRunning || sibling.action.Status == nativeOperationCancel) {
			return false
		}
	}
	after := run.result.EndedAt
	if parent.result.EndedAt.After(after) {
		after = parent.result.EndedAt
	}
	if knownEffectConfirmed(run, pod, physical, after) {
		return true
	}
	for _, action := range parent.result.Operation.Target.Actions {
		child := c.runs[action.ID]
		if child != nil && child.owner == parent.owner && actionEquivalent(action, child.action) && knownEffectConfirmed(child, pod, physical, after) {
			return true
		}
	}
	// Cleanup refreshes are explicitly adopted by reserveRun after handoff;
	// they are not part of the original frozen goal's action list.
	for _, refresh := range parent.cleanupRoutes {
		if refresh != nil && refresh.owner == parent.owner && knownEffectConfirmed(refresh, pod, physical, after) {
			return true
		}
	}
	return false
}

//nolint:gocyclo // Keep exact-executor and known-policy proof together at the reservation release boundary.
func knownEffectConfirmed(run *actionRun, pod *core.Pod, physical []serving.InstanceState, after time.Time) bool {
	if run.result == nil || run.action.Status == nativeOperationRunning || run.action.Status == nativeOperationCancel || !sameRunningContainer(pod, pod) {
		return false
	}
	operation := run.result.Operation
	if !isRoutingOperation(operation.Kind) && operation.Kind != operationPrepare {
		return false
	}
	id := physicalID(pod.UID, firstfloorStatus(pod).ContainerID)
	_, beforeKnown := run.result.Before.Native[id]
	_, actualKnown := run.result.Actual.Native[id]
	identityKnown := slices.Contains(run.scope.Pods, string(pod.UID)) && slices.Contains(run.scope.Instances, id)
	identityKnown = identityKnown || beforeKnown || actualKnown || slices.ContainsFunc(run.result.Actual.Instances, func(state serving.InstanceState) bool {
		return state.PodUID == string(pod.UID) && state.ID == id
	})
	state := schedulerPhysicalState(id, physical)
	if !identityKnown || state == nil || state.ID != id || state.PodUID != string(pod.UID) || state.Node.Name != pod.Spec.NodeName || !slices.Contains(run.scope.Nodes, state.Node) || !state.Ready || state.Stopped || state.Native == nil || !state.Native.Ready || !run.result.EndedAt.Before(state.ObservedAt) || !after.Before(state.ObservedAt) {
		return false
	}
	actual := state.Native.Metadata
	if operation.Kind == operationPrepare && operation.Prepare != nil {
		wanted := operation.Prepare.Instance
		decision := operation.Prepare.Config.Policy.DecisionID
		if wanted.Profile != nil && state.TargetID == wanted.ID && wanted.Node == state.Node &&
			operation.controlsWorkload(wanted.WorkloadUID) && operation.Prepare.Owner != nil &&
			operation.Prepare.Owner.UID == wanted.WorkloadUID && workloadOwner(pod.OwnerReferences) == wanted.WorkloadUID &&
			actual.ProfileID == wanted.Profile.ID && decision != "" && actual.DecisionID == decision &&
			actual.Policy.DecisionID == decision && schedulerPolicyAcknowledged(wanted, actual) {
			return true
		}
	}
	if isRoutingOperation(operation.Kind) && operation.Target != nil && actual.DecisionID == operation.Target.ID && actual.Policy.DecisionID == operation.Target.ID {
		for _, wanted := range operation.Target.Instances {
			if wanted.Profile != nil && operation.controlsWorkload(wanted.WorkloadUID) &&
				(wanted.ID == id && (wanted.TargetID == "" || wanted.TargetID == state.TargetID) || wanted.ID == state.TargetID) &&
				wanted.Node == state.Node && actual.ProfileID == wanted.Profile.ID && schedulerPolicyAcknowledged(wanted, actual) {
				return true
			}
		}
	}
	// An unchanged previous policy is a confirmed state, not a successful
	// application of the requested declaration. Do not invent an ACK for it.
	before, known := run.result.Before.Native[id]
	if !known || actual.ProfileID != before.Metadata.ProfileID || actual.DecisionID != before.Metadata.DecisionID {
		return false
	}
	previous, current := before.Metadata.Policy, actual.Policy
	previous.Batches, current.Batches = nil, nil
	return reflect.DeepEqual(previous, current)
}

// SCH-AC-38 / P2 §3 R4: compensate only a failed owner's own, unclaimed
// preparation. remove already performs stop/drain/source retirement; its
// completion is not a return proof until releaseObservedScopes sees Pod absence.
func (c *SchedulerController) launchFailedPreparationCleanupLocked(config SchedulerControllerConfig) {
	for _, parent := range c.runs {
		if parent.action.Kind != transitionRunKind || parent.action.Status != nativeOperationFailed || !parent.reserved || parent.result == nil {
			continue
		}
		// A canceled/finishing sibling can still publish a policy or route.
		active := false
		for _, run := range c.runs {
			if run.owner == parent.owner && (run.action.Status == nativeOperationRunning || run.action.Status == nativeOperationCancel) {
				active = true
				break
			}
		}
		if active {
			continue
		}
		physical := c.runtime.Instances()
		for _, child := range c.runs {
			if child.owner != parent.owner || !child.reserved || child.action.Kind != operationPrepare || child.result == nil ||
				(child.action.Status != nativeOperationFailed && child.action.Status != nativeOperationPartial) || child.result.Operation.Prepare == nil {
				continue
			}
			for _, actual := range physical {
				if !c.failedPreparationUnclaimed(parent, child, actual) {
					continue
				}
				c.launchTargetLocked(serving.SchedulerCandidate{ID: parent.owner, Actions: []serving.Action{{
					ID: child.action.ID + "/compensate/" + actual.ID, Kind: operationRemove, Node: actual.Node, Instance: actual.ID,
				}}}, config)
			}
		}
	}
}

func (c *SchedulerController) failedPreparationUnclaimed(parent, child *actionRun, actual serving.InstanceState) bool {
	if !actual.Ready || actual.Stopped || actual.Native == nil || !actual.Native.Ready ||
		!child.result.EndedAt.Before(actual.ObservedAt) || !parent.result.EndedAt.Before(actual.ObservedAt) ||
		!failedPreparationIdentityMatches(child, actual) || !preparationCreatedSource(child, actual) {
		return false
	}
	wanted := child.result.Operation.Prepare.Instance
	metadata := actual.Native.Metadata
	decision := child.result.Operation.Prepare.Config.Policy.DecisionID
	if decision != "" && metadata.ProfileID == wanted.Profile.ID && metadata.DecisionID == decision &&
		metadata.Policy.DecisionID == decision && schedulerPolicyAcknowledged(wanted, metadata) {
		return false
	}
	return c.preparationSourceUnclaimed(actual) && slices.ContainsFunc(c.state.Snapshot.Nodes, func(node serving.SchedulerNode) bool {
		return node.Node == actual.Node && node.Ready && node.PowerMode != ""
	})
}

func failedPreparationIdentityMatches(child *actionRun, actual serving.InstanceState) bool {
	wanted := child.result.Operation.Prepare.Instance
	podUID, containerID, exact := strings.Cut(actual.ID, "/")
	return wanted.Profile != nil && child.result.Operation.Prepare.Owner != nil && child.result.Operation.Prepare.Owner.UID == wanted.WorkloadUID &&
		actual.OwnerWorkloadUID == wanted.WorkloadUID && actual.Node == wanted.Node && actual.TargetID == wanted.ID && actual.Profile == wanted.Profile.ID &&
		child.result.Operation.controlsWorkload(wanted.WorkloadUID) && exact && containerID != "" && podUID == actual.PodUID &&
		actual.PodUID != "" && slices.Contains(child.scope.Pods, actual.PodUID) && slices.Contains(child.scope.Instances, actual.ID)
}

func preparationCreatedSource(child *actionRun, actual serving.InstanceState) bool {
	// Never infer creation from a slot/owner alone, or retire a pre-existing
	// source. A timeout may have no final capture: the live frozen UID suffices.
	identityKnown := child.prepareIdentity == actual.ID || slices.ContainsFunc(child.result.Actual.Instances, func(state serving.InstanceState) bool {
		return state.ID == actual.ID && state.PodUID == actual.PodUID && state.Node == actual.Node
	})
	if !identityKnown || slices.ContainsFunc(child.result.Before.Instances, func(state serving.InstanceState) bool {
		return state.PodUID == actual.PodUID || state.ID == actual.ID
	}) {
		return false
	}
	if _, existed := child.result.Before.Native[actual.ID]; existed {
		return false
	}
	for _, pod := range child.result.Before.Pods {
		if pod.UID == actual.PodUID {
			return false
		}
	}
	return true
}

func (c *SchedulerController) preparationSourceUnclaimed(actual serving.InstanceState) bool {
	if len(actual.Workloads) != 0 {
		return false
	}
	for _, routable := range actual.Routable {
		if routable {
			return false
		}
	}
	for _, route := range c.state.Snapshot.Routing {
		if route[actual.ID] > 0 || route[actual.TargetID] > 0 {
			return false
		}
	}
	for _, instance := range c.state.Snapshot.Instances {
		if instance.ID == actual.ID && instance.WorkloadUID != "" {
			return false
		}
	}
	return true
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

func (c *SchedulerController) reservationsLocked(snapshot serving.SchedulerSnapshot) []serving.SchedulerReservation {
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
		if run.action.Kind == transitionRunKind && run.cleanup && run.action.Status == nativeOperationRunning {
			reservation.RouteOnly = c.cleanupDestinationsLocked(run, snapshot)
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

// A successful final handoff opens only routing; the original DAG and its
// physical retirement scope stay owned until every cleanup obligation finishes.
func (c *SchedulerController) cleanupReadyLocked(parent *actionRun, snapshot serving.SchedulerSnapshot) bool {
	final := false
	for _, action := range parent.result.Operation.Target.Actions {
		if action.Kind != operationRoute && action.Kind != operationPrepare {
			continue
		}
		run := c.runs[action.ID]
		if run == nil || run.action.Status != nativeOperationComplete || run.result == nil || run.result.Status != nativeOperationComplete {
			return false
		}
		if action.Kind == operationRoute {
			final = action.From != readySubsetRoute && c.actionEffectPresent(snapshot, run)
		}
	}
	return final
}

func (c *SchedulerController) cleanupDestinationsLocked(parent *actionRun, snapshot serving.SchedulerSnapshot) map[string][]string {
	ready := map[string][]string{}
	if !parent.cleanup || !parent.reserved || parent.action.Status != nativeOperationRunning {
		return ready
	}
	for _, child := range c.runs {
		if child.owner == parent.owner && child.action.Kind == operationRoute &&
			(child.action.Status == nativeOperationRunning || child.action.Status == nativeOperationCancel) {
			return ready
		}
	}
	target := parent.result.Operation.Target
	physical := c.runtime.Instances()
	for _, wanted := range target.Instances {
		if target.Routing[wanted.WorkloadUID][wanted.ID] <= 0 || !parent.result.Operation.controlsWorkload(wanted.WorkloadUID) {
			continue
		}
		for _, actual := range snapshot.Instances {
			if (actual.ID == wanted.ID || actual.TargetID == wanted.ID) && actual.Ready && !actual.Stopped &&
				actual.Node == wanted.Node && actual.Profile != nil && wanted.Profile != nil && actual.Profile.ID == wanted.Profile.ID &&
				schedulerTargetReady(actual, snapshot, physical, true) {
				ready[wanted.WorkloadUID] = append(ready[wanted.WorkloadUID], actual.ID)
			}
		}
	}
	return ready
}

func (c *SchedulerController) cleanupRouteAllowedLocked(parent *actionRun, operation Operation) bool {
	if operation.Target == nil || len(operation.Target.Actions) != 1 || operation.Target.Actions[0].Kind != operationRoute {
		return false
	}
	ready := c.cleanupDestinationsLocked(parent, c.state.Snapshot)
	target := controlledTarget(operation)
	if len(target.Routing) == 0 {
		return false
	}
	for uid, weights := range target.Routing {
		if len(ready[uid]) == 0 || len(weights) == 0 {
			return false
		}
		for id, weight := range weights {
			if weight <= 0 || !slices.Contains(ready[uid], id) {
				return false
			}
			if !slices.ContainsFunc(target.Instances, func(wanted serving.SchedulerInstance) bool {
				return wanted.ID == id && wanted.WorkloadUID == uid
			}) {
				return false
			}
		}
	}
	for _, wanted := range target.Instances {
		if target.Routing[wanted.WorkloadUID][wanted.ID] <= 0 {
			continue
		}
		if !slices.ContainsFunc(c.state.Snapshot.Instances, func(actual serving.SchedulerInstance) bool {
			return actual.ID == wanted.ID && actual.Node == wanted.Node && actual.Ready && !actual.Stopped &&
				serving.SchedulerInstanceState(actual) == serving.SchedulerInstanceState(wanted) &&
				actual.MaxBatch == wanted.MaxBatch && actual.MaxWait == wanted.MaxWait && actual.Capacity == wanted.Capacity &&
				actual.FixedBatchSize == wanted.FixedBatchSize && actual.Profile != nil && wanted.Profile != nil &&
				reflect.DeepEqual(actual.Profile.Condition.Firstfloor, wanted.Profile.Condition.Firstfloor)
		}) {
			return false
		}
	}
	return true
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
