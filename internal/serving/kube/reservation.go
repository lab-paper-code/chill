package kube

import (
	"cmp"
	"context"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
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

func (c *reservationState) operationScope(operation servingstate.Operation) resourceScope {
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
			if servingstate.IsRoutingOperation(operation.Kind) && operation.ControlsWorkload(uid) {
				scope.Workloads = append(scope.Workloads, uid)
			}
		}
		for _, instance := range operation.Target.Instances {
			if !servingstate.IsRoutingOperation(operation.Kind) && (instance.ID == operation.Instance || instance.Node == operation.Node) && instance.WorkloadUID != "" {
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
func (c *reservationState) reserveRun(id string, run *actionRun, operation servingstate.Operation) bool {
	if existing := c.runs[id]; existing != nil && (existing.action.Status == servingstate.NativeOperationRunning || existing.action.Status == servingstate.NativeOperationCancel) {
		return false
	}
	scope := c.operationScope(operation)
	// Live owned physical resources may no longer appear in the last snapshot.
	scope.Pods = append(scope.Pods, run.scope.Pods...)
	scope.Instances = append(scope.Instances, run.scope.Instances...)
	if run.owner == "" {
		run.owner = id
	}
	// Policy ACKs within one owner remain serialized.
	if operation.Kind == servingstate.OperationRoute {
		for _, other := range c.runs {
			if other.owner == run.owner && other.action.Kind == servingstate.OperationRoute &&
				(other.action.Status == servingstate.NativeOperationRunning || other.action.Status == servingstate.NativeOperationCancel) {
				return false
			}
		}
	}
	var inherited []string
	if operation.Kind == servingstate.OperationReset {
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
	if operation.Kind == servingstate.OperationRoute {
		if parent := c.runs[transitionRunKind+"/"+run.owner]; parent != nil && parent.owner == run.owner {
			if parent.routeReceipts == nil {
				parent.routeReceipts = map[string]*actionRun{}
			}
			parent.routeReceipts[id] = run
			run.proofParent = parent
		}
	}
	c.registerMayTouchLocked(run, operation, scope)
	return true
}

// recordPreparationLocked precedes all terminal publication paths. The result
// evidence and scope share the owner's mutex and lifetime, not Capture's.
func (c *reservationState) recordPreparationLocked(run *actionRun, evidence *servingstate.PreparationEvidence) {
	if evidence == nil || evidence.PodUID == "" {
		return
	}
	run.result.Preparation = evidence
	run.scope.Pods = append(run.scope.Pods, evidence.PodUID)
	if parent := c.returnProofParent(run); parent != nil {
		parent.returnObserved = false
		parent.scope.Pods = append(parent.scope.Pods, evidence.PodUID)
	}
}

func (c *reservationState) registerMayTouchLocked(run *actionRun, operation servingstate.Operation, scope resourceScope) {
	if operation.Kind == transitionRunKind {
		return
	}
	parent := c.returnProofParent(run)
	if parent == nil {
		return
	}
	parent.returnObserved = false
	if parent.mayTouch == nil {
		parent.mayTouch = map[string]bool{}
	}
	for _, state := range parent.result.Before.Instances {
		if operationMayTouch(operation, scope, state) {
			parent.mayTouch[state.ID] = true
		}
	}
}

// Effect scope follows executeOperation: local actions write only their Node;
// routing (including resize) can write every controlled workload's executor.
func operationMayTouch(operation servingstate.Operation, scope resourceScope, state serving.InstanceState) bool {
	switch operation.Kind {
	case servingstate.OperationPrepare, servingstate.OperationStop, servingstate.OperationDrain, servingstate.OperationRemove, servingstate.OperationPower:
		return state.Node == operation.Node
	case servingstate.OperationRoute, servingstate.OperationUnroute, servingstate.OperationRelease:
		return slices.Contains(scope.Nodes, state.Node) || slices.Contains(scope.Instances, state.ID)
	case servingstate.OperationResize:
		return state.Node == operation.Node || slices.Contains(scope.Nodes, state.Node) || slices.Contains(scope.Instances, state.ID)
	default:
		return true
	}
}

func (c *reservationState) returnProofParent(run *actionRun) *actionRun {
	if run.action.Kind == transitionRunKind {
		return run
	}
	return run.proofParent
}

// This check also runs when there are no Pods: absence is not permission to
// release while an authorized sibling can still publish a remote write.
func (c *reservationState) ownerReturnTerminal(run *actionRun) bool {
	if parent := c.returnProofParent(run); parent != nil &&
		(parent.action.Status == servingstate.NativeOperationRunning || parent.action.Status == servingstate.NativeOperationCancel) {
		return false
	}
	for _, sibling := range c.runs {
		if sibling.owner == run.owner && (sibling.action.Status == servingstate.NativeOperationRunning || sibling.action.Status == servingstate.NativeOperationCancel) {
			return false
		}
	}
	return true
}

func (c *reservationState) authorize(operation servingstate.Operation) error {
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
func (c *reservationState) releaseObservedScopes(nodes []core.Node, pods []core.Pod, physical []serving.InstanceState, observedAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, run := range c.runs {
		if !run.reserved || run.action.Status == servingstate.NativeOperationRunning || run.action.Status == servingstate.NativeOperationCancel || run.result == nil || !run.result.EndedAt.Before(observedAt) || len(run.scope.Nodes) == 0 {
			continue
		}
		if !c.ownerReturnTerminal(run) {
			continue
		}
		returned := true
		for _, sibling := range c.runs {
			if sibling.owner == run.owner && sibling.result != nil && !sibling.result.EndedAt.Before(observedAt) {
				returned = false
			}
		}
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

// Terminal owner members share receipts through their authorization-time parent
// link, never an owner's name alone. Its baseline bounds previous-policy proof.
func (c *reservationState) knownOwnerEffectConfirmed(run *actionRun, pod *core.Pod, physical []serving.InstanceState) bool {
	parent := c.returnProofParent(run)
	if parent == nil {
		return knownEffectConfirmed(run, pod, physical, run.result.EndedAt, true)
	}
	if parent.result == nil || parent.result.Operation.Target == nil {
		return false
	}
	after := run.result.EndedAt
	if parent.result.EndedAt.After(after) {
		after = parent.result.EndedAt
	}
	for _, sibling := range c.runs {
		if sibling.owner == parent.owner && sibling.result != nil && sibling.result.EndedAt.After(after) {
			after = sibling.result.EndedAt
		}
	}
	preexisting, untouched := untouchedExecutorProof(parent, pod, physical, after)
	if untouched {
		return true
	}
	if knownEffectConfirmed(run, pod, physical, after, preexisting) {
		return true
	}
	for _, action := range parent.result.Operation.Target.Actions {
		child := c.runs[action.ID]
		if child != nil && child.owner == parent.owner && actionEquivalent(action, child.action) && knownEffectConfirmed(child, pod, physical, after, preexisting) {
			return true
		}
	}
	// Every route is registered at authorization, independent of its planner
	// phase. A sibling's unchanged previous policy proves only preexisting
	// executors, never a newly prepared source that still needs compensation.
	for _, route := range parent.routeReceipts {
		if route != nil && route.owner == parent.owner && knownEffectConfirmed(route, pod, physical, after, preexisting) {
			return true
		}
	}
	return false
}

// Return policy-receipt exemption separately from admission identity: a dirty
// executor may still use an authorized receipt's unchanged previous policy.
func untouchedExecutorProof(parent *actionRun, pod *core.Pod, physical []serving.InstanceState, after time.Time) (preexisting, untouched bool) {
	status := firstfloorStatus(pod)
	if status == nil {
		return false, false
	}
	id := physicalID(pod.UID, status.ContainerID)
	current := schedulerPhysicalState(id, physical)
	if current == nil {
		return false, false
	}
	preexisting = slices.ContainsFunc(parent.result.Before.Instances, func(state serving.InstanceState) bool {
		return state.ID == id && state.PodUID == string(pod.UID) && state.Node == current.Node
	})
	untouched = preexisting && !parent.mayTouch[id] && sameRunningContainer(pod, pod) &&
		current.Node.Name == pod.Spec.NodeName && current.Ready && !current.Stopped &&
		current.Native != nil && current.Native.Ready && after.Before(current.ObservedAt)
	return preexisting, untouched
}

//nolint:gocyclo // Keep exact-executor and known-policy proof together at the reservation release boundary.
func knownEffectConfirmed(run *actionRun, pod *core.Pod, physical []serving.InstanceState, after time.Time, allowPrevious bool) bool {
	if run.result == nil || run.action.Status == servingstate.NativeOperationRunning || run.action.Status == servingstate.NativeOperationCancel || !sameRunningContainer(pod, pod) {
		return false
	}
	operation := run.result.Operation
	if !servingstate.IsRoutingOperation(operation.Kind) && operation.Kind != servingstate.OperationPrepare && operation.Kind != servingstate.OperationResize {
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
	if (operation.Kind == servingstate.OperationPrepare || operation.Kind == servingstate.OperationResize) && operation.Prepare != nil {
		wanted := operation.Prepare.Instance
		decision := operation.Prepare.Config.Policy.DecisionID
		if wanted.Profile != nil && state.TargetID == wanted.ID && wanted.Node == state.Node &&
			operation.ControlsWorkload(wanted.WorkloadUID) && operation.Prepare.Owner != nil &&
			operation.Prepare.Owner.UID == wanted.WorkloadUID && workloadOwner(pod.OwnerReferences) == wanted.WorkloadUID &&
			actual.ProfileID == wanted.Profile.ID && decision != "" && actual.DecisionID == decision &&
			actual.Policy.DecisionID == decision && schedulerPolicyAcknowledged(wanted, actual) {
			return true
		}
	}
	if servingstate.IsRoutingOperation(operation.Kind) && operation.Target != nil && actual.DecisionID == operation.Target.ID && actual.Policy.DecisionID == operation.Target.ID {
		for _, wanted := range operation.Target.Instances {
			if wanted.Profile != nil && operation.ControlsWorkload(wanted.WorkloadUID) &&
				(wanted.ID == id && (wanted.TargetID == "" || wanted.TargetID == state.TargetID) || wanted.ID == state.TargetID) &&
				wanted.Node == state.Node && actual.ProfileID == wanted.Profile.ID && schedulerPolicyAcknowledged(wanted, actual) {
				return true
			}
		}
	}
	// An unchanged previous policy is a confirmed state, not a successful
	// application of the requested declaration. Do not invent an ACK for it.
	before, known := run.result.Before.Native[id]
	if !allowPrevious || !known || actual.ProfileID != before.Metadata.ProfileID || actual.DecisionID != before.Metadata.DecisionID {
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
		if parent.action.Kind != transitionRunKind || parent.action.Status != servingstate.NativeOperationFailed || !parent.reserved || parent.result == nil {
			continue
		}
		// A canceled/finishing sibling can still publish a policy or route.
		active := false
		for _, run := range c.runs {
			if run.owner == parent.owner && (run.action.Status == servingstate.NativeOperationRunning || run.action.Status == servingstate.NativeOperationCancel) {
				active = true
				break
			}
		}
		if active {
			continue
		}
		physical := c.runtime.Instances()
		for _, child := range c.runs {
			if child.owner != parent.owner || !child.reserved || child.action.Kind != servingstate.OperationPrepare || child.result == nil ||
				(child.action.Status != servingstate.NativeOperationFailed && child.action.Status != servingstate.NativeOperationPartial) || child.result.Operation.Prepare == nil {
				continue
			}
			for _, actual := range physical {
				if !c.failedPreparationUnclaimed(parent, child, actual) {
					continue
				}
				c.launchTargetLocked(serving.SchedulerCandidate{ID: parent.owner, Actions: []serving.Action{{
					ID: child.action.ID + "/compensate/" + actual.ID, Kind: servingstate.OperationRemove, Node: actual.Node, Instance: actual.ID,
				}}}, config)
			}
		}
	}
}

func (c *reservationState) failedPreparationUnclaimed(parent, child *actionRun, actual serving.InstanceState) bool {
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
		child.result.Operation.ControlsWorkload(wanted.WorkloadUID) && exact && containerID != "" && podUID == actual.PodUID &&
		actual.PodUID != "" && slices.Contains(child.scope.Pods, actual.PodUID) && slices.Contains(child.scope.Instances, actual.ID)
}

func preparationCreatedSource(child *actionRun, actual serving.InstanceState) bool {
	if evidence := child.result.Preparation; evidence == nil ||
		!evidence.Created || evidence.PodUID != actual.PodUID || evidence.NodeUID != actual.Node.UID {
		return false
	}
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

func (c *reservationState) preparationSourceUnclaimed(actual serving.InstanceState) bool {
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

func (c *reservationState) releaseOwnerLocked(owner string) {
	for _, run := range c.runs {
		if run.owner == owner {
			run.reserved = false
			if run.action.Kind == measurementLeaseKind {
				run.action.Status = servingstate.NativeOperationComplete
				run.result.Status, run.result.EndedAt = servingstate.NativeOperationComplete, time.Now()
			}
		}
	}
}

func (c *reservationState) reservationsLocked() []serving.SchedulerReservation {
	byOwner := map[string]*serving.SchedulerReservation{}
	reasons := map[string][]string{}
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
		reason := run.action.Reason
		if run.result != nil && run.result.Operation.Prepare != nil && run.prepareIdentity == "" {
			reason = "creation-outcome-unknown: retain owner; reconcile API outcome or reset"
			if evidence := run.result.Preparation; evidence != nil && evidence.PodUID != "" {
				reason = "identity-awaiting-container: " + evidence.PodUID + "; await same UID exact executor"
			}
		}
		if run.action.Kind == transitionRunKind && run.result != nil && run.result.Operation.Target != nil {
			for _, action := range run.result.Operation.Target.Actions {
				if c.runs[action.ID] != nil || action.Status == servingstate.NativeOperationCancel || c.omittedRouteCompleteLocked(action, *run.result.Operation.Target) {
					continue
				}
				reasons[run.owner] = append(reasons[run.owner], action.ID+": dependency/Ready/handoff proof missing; await exact Ready policy ACK")
			}
		}
		if reason == "" {
			reason = "await fresh exact ACK/untouched/absence proof, terminal siblings and settled power"
		}
		reasons[run.owner] = append(reasons[run.owner], run.action.ID+": "+reason)
	}
	reservations := make([]serving.SchedulerReservation, 0, len(byOwner))
	for _, reservation := range byOwner {
		ownerReasons := reasons[reservation.Owner]
		slices.Sort(ownerReasons)
		reservation.Reason = strings.Join(slices.Compact(ownerReasons), "; ")
		reservations = append(reservations, *reservation)
	}
	slices.SortFunc(reservations, func(a, b serving.SchedulerReservation) int {
		return cmp.Compare(a.Owner, b.Owner)
	})
	return reservations
}

func (c *reservationState) runReferencedLocked(id string) bool {
	parent := c.runs[id]
	for _, run := range c.runs {
		if !run.reserved && run.action.Status != servingstate.NativeOperationRunning && run.action.Status != servingstate.NativeOperationCancel {
			continue
		}
		if parent != nil && parent.action.Kind == transitionRunKind && parent.owner != "" && parent.owner == run.owner {
			return true
		}
		if run.result == nil || run.result.Operation.Target == nil {
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

func (c *reservationState) nodeExecuting(node *core.Node) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, run := range c.runs {
		if run.action.Kind != measurementLeaseKind && run.action.Status == servingstate.NativeOperationRunning && slices.Contains(run.scope.Nodes, serving.NodeReference{Name: node.Name, UID: string(node.UID)}) {
			return true
		}
	}
	return false
}

// reserveDeletion shares the operation ledger from acceptance through confirmed
// native deletion. Live owned Pods supplement snapshots after reset/removal.
func (s *ClusterState) ReserveDeletion(ctx context.Context, uid types.UID) error {
	c := &s.reservation
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	id := workloadDeletionKind + "/" + string(uid)
	c.mu.Lock()
	defer c.mu.Unlock()
	if run := c.runs[id]; run != nil && run.reserved {
		return nil
	}
	objects, err := s.store.OwnedResources(ctx, uid, s.namespace)
	if err != nil {
		return err
	}
	nodes, err := s.kube.CoreV1().Nodes().List(ctx, meta.ListOptions{})
	if err != nil {
		return err
	}
	operation := servingstate.Operation{ID: id, Kind: workloadDeletionKind, WorkloadUID: string(uid)}
	for _, object := range objects {
		if pod, ok := object.(*core.Pod); ok {
			for _, node := range nodes.Items {
				if node.Name == pod.Spec.NodeName {
					operation.Nodes = append(operation.Nodes, serving.NodeReference{Name: node.Name, UID: string(node.UID)})
				}
			}
		}
	}
	run := &actionRun{owner: id, action: serving.Action{ID: id, Kind: workloadDeletionKind, Status: servingstate.NativeOperationRunning}}
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

func (s *ClusterState) ReleaseDeletion(uid types.UID) {
	s.reservation.mu.Lock()
	defer s.reservation.mu.Unlock()
	delete(s.reservation.runs, workloadDeletionKind+"/"+string(uid))
}
