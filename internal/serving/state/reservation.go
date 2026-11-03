package state

import (
	cmp "cmp"
	context "context"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	serving "github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	reflect "reflect"
	slices "slices"
	strings "strings"
	time "time"
)

const TransitionRunKind = "transition"

const MeasurementLeaseKind = "measurement-lease"

const workloadDeletionKind = "workload-deletion"

// ResourceScope is acquired atomically before an operation can have effects.
// A completed execution can still own its scope until observation proves return.
type ResourceScope struct {
	Nodes     []serving.NodeReference
	Workloads []string
	Pods      []string
	Instances []string
}

func (s ResourceScope) Overlaps(other ResourceScope) bool {
	return slices.ContainsFunc(s.Nodes, func(n serving.NodeReference) bool { return slices.Contains(other.Nodes, n) }) ||
		slices.ContainsFunc(s.Workloads, func(uid string) bool { return slices.Contains(other.Workloads, uid) }) ||
		slices.ContainsFunc(s.Pods, func(uid string) bool { return slices.Contains(other.Pods, uid) }) ||
		slices.ContainsFunc(s.Instances, func(id string) bool { return slices.Contains(other.Instances, id) })
}

func (c *ReservationState) OperationScope(operation Operation) ResourceScope {
	scope := ResourceScope{Workloads: slices.Clone(operation.WorkloadUIDs), Nodes: slices.Clone(operation.Nodes)}
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
			if IsRoutingOperation(operation.Kind) && operation.ControlsWorkload(uid) {
				scope.Workloads = append(scope.Workloads, uid)
			}
		}
		for _, instance := range operation.Target.Instances {
			if !IsRoutingOperation(operation.Kind) && (instance.ID == operation.Instance || instance.Node == operation.Node) && instance.WorkloadUID != "" {
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
func (c *ReservationState) ReserveRun(id string, run *ActionRun, operation Operation) bool {
	if existing := c.runs[id]; existing != nil && (existing.action.Status == NativeOperationRunning || existing.action.Status == NativeOperationCancel) {
		return false
	}
	scope := c.OperationScope(operation)
	// Live owned physical resources may no longer appear in the last snapshot.
	scope.Pods = append(scope.Pods, run.scope.Pods...)
	scope.Instances = append(scope.Instances, run.scope.Instances...)
	if run.owner == "" {
		run.owner = id
	}
	// Policy ACKs within one owner remain serialized.
	if operation.Kind == OperationRoute {
		for _, other := range c.runs {
			if other.owner == run.owner && other.action.Kind == OperationRoute &&
				(other.action.Status == NativeOperationRunning || other.action.Status == NativeOperationCancel) {
				return false
			}
		}
	}
	var inherited []string
	if operation.Kind == OperationReset {
		var err error
		inherited, err = c.ResetReservationOwnersLocked(scope, operation.Nodes)
		if err != nil {
			return false
		}
	}
	for otherID, other := range c.runs {
		if otherID != id && other.reserved && other.owner != run.owner && !slices.Contains(inherited, other.owner) && scope.Overlaps(other.scope) {
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
	if operation.Kind == OperationRoute {
		if parent := c.runs[TransitionRunKind+"/"+run.owner]; parent != nil && parent.owner == run.owner {
			if parent.routeReceipts == nil {
				parent.routeReceipts = map[string]*ActionRun{}
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
func (c *ReservationState) recordPreparationLocked(run *ActionRun, evidence *PreparationEvidence) {
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

func (c *ReservationState) registerMayTouchLocked(run *ActionRun, operation Operation, scope ResourceScope) {
	if operation.Kind == TransitionRunKind {
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
func operationMayTouch(operation Operation, scope ResourceScope, state serving.InstanceState) bool {
	switch operation.Kind {
	case OperationPrepare, OperationStop, OperationDrain, OperationRemove, OperationPower:
		return state.Node == operation.Node
	case OperationRoute, OperationUnroute, OperationRelease:
		return slices.Contains(scope.Nodes, state.Node) || slices.Contains(scope.Instances, state.ID)
	case OperationResize:
		return state.Node == operation.Node || slices.Contains(scope.Nodes, state.Node) || slices.Contains(scope.Instances, state.ID)
	default:
		return true
	}
}

func (c *ReservationState) returnProofParent(run *ActionRun) *ActionRun {
	if run.action.Kind == TransitionRunKind {
		return run
	}
	return run.proofParent
}

// This check also runs when there are no Pods: absence is not permission to
// release while an authorized sibling can still publish a remote write.
func (c *ReservationState) ownerReturnTerminal(run *ActionRun) bool {
	if parent := c.returnProofParent(run); parent != nil &&
		(parent.action.Status == NativeOperationRunning || parent.action.Status == NativeOperationCancel) {
		return false
	}
	for _, sibling := range c.runs {
		if sibling.owner == run.owner && (sibling.action.Status == NativeOperationRunning || sibling.action.Status == NativeOperationCancel) {
			return false
		}
	}
	return true
}

func (c *ReservationState) Authorize(operation Operation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	scope := c.OperationScope(operation)
	for _, run := range c.runs {
		if run.reserved && scope.Overlaps(run.scope) {
			return Refused("OperationConflict", "resources are reserved by "+run.owner)
		}
	}
	return nil
}

// Fresh observation proves physical return or an exact executor's known effect.
// Terminal transitions consume this proof in reconcileSnapshot without changing
// failed receipts. A taint remains owned by the admission lifecycle.
func (c *ReservationState) ReleaseObservedScopes(nodes []core.Node, pods []core.Pod, physical []serving.InstanceState, observedAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, run := range c.runs {
		if !run.reserved || run.action.Status == NativeOperationRunning || run.action.Status == NativeOperationCancel || run.result == nil || !run.result.EndedAt.Before(observedAt) || len(run.scope.Nodes) == 0 {
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
				if pod.Spec.NodeName == ref.Name && (slices.Contains(run.scope.Pods, string(pod.UID)) || slices.Contains(run.scope.Workloads, WorkloadOwner(pod.OwnerReferences))) && !c.knownOwnerEffectConfirmed(run, &pod, physical) {
					returned = false
				}
			}
		}
		if run.action.Kind == TransitionRunKind {
			run.returnObserved = returned
		} else if returned {
			run.reserved = false
		}
	}
}

// Terminal owner members share receipts through their authorization-time parent
// link, never an owner's name alone. Its baseline bounds previous-policy proof.
func (c *ReservationState) knownOwnerEffectConfirmed(run *ActionRun, pod *core.Pod, physical []serving.InstanceState) bool {
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
		if child != nil && child.owner == parent.owner && ActionEquivalent(action, child.action) && knownEffectConfirmed(child, pod, physical, after, preexisting) {
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
func untouchedExecutorProof(parent *ActionRun, pod *core.Pod, physical []serving.InstanceState, after time.Time) (preexisting, untouched bool) {
	status := FirstfloorStatus(pod)
	if status == nil {
		return false, false
	}
	id := PhysicalID(pod.UID, status.ContainerID)
	current := SchedulerPhysicalState(id, physical)
	if current == nil {
		return false, false
	}
	preexisting = slices.ContainsFunc(parent.result.Before.Instances, func(state serving.InstanceState) bool {
		return state.ID == id && state.PodUID == string(pod.UID) && state.Node == current.Node
	})
	untouched = preexisting && !parent.mayTouch[id] && SameRunningContainer(pod, pod) &&
		current.Node.Name == pod.Spec.NodeName && current.Ready && !current.Stopped &&
		current.Native != nil && current.Native.Ready && after.Before(current.ObservedAt)
	return preexisting, untouched
}

//nolint:gocyclo // Keep exact-executor and known-policy proof together at the reservation release boundary.
func knownEffectConfirmed(run *ActionRun, pod *core.Pod, physical []serving.InstanceState, after time.Time, allowPrevious bool) bool {
	if run.result == nil || run.action.Status == NativeOperationRunning || run.action.Status == NativeOperationCancel || !SameRunningContainer(pod, pod) {
		return false
	}
	operation := run.result.Operation
	if !IsRoutingOperation(operation.Kind) && operation.Kind != OperationPrepare && operation.Kind != OperationResize {
		return false
	}
	id := PhysicalID(pod.UID, FirstfloorStatus(pod).ContainerID)
	_, beforeKnown := run.result.Before.Native[id]
	_, actualKnown := run.result.Actual.Native[id]
	identityKnown := slices.Contains(run.scope.Pods, string(pod.UID)) && slices.Contains(run.scope.Instances, id)
	identityKnown = identityKnown || beforeKnown || actualKnown || slices.ContainsFunc(run.result.Actual.Instances, func(state serving.InstanceState) bool {
		return state.PodUID == string(pod.UID) && state.ID == id
	})
	state := SchedulerPhysicalState(id, physical)
	if !identityKnown || state == nil || state.ID != id || state.PodUID != string(pod.UID) || state.Node.Name != pod.Spec.NodeName || !slices.Contains(run.scope.Nodes, state.Node) || !state.Ready || state.Stopped || state.Native == nil || !state.Native.Ready || !run.result.EndedAt.Before(state.ObservedAt) || !after.Before(state.ObservedAt) {
		return false
	}
	actual := state.Native.Metadata
	if (operation.Kind == OperationPrepare || operation.Kind == OperationResize) && operation.Prepare != nil {
		wanted := operation.Prepare.Instance
		decision := operation.Prepare.Config.Policy.DecisionID
		if wanted.Profile != nil && state.TargetID == wanted.ID && wanted.Node == state.Node &&
			operation.ControlsWorkload(wanted.WorkloadUID) && operation.Prepare.Owner != nil &&
			operation.Prepare.Owner.UID == wanted.WorkloadUID && WorkloadOwner(pod.OwnerReferences) == wanted.WorkloadUID &&
			actual.ProfileID == wanted.Profile.ID && decision != "" && actual.DecisionID == decision &&
			actual.Policy.DecisionID == decision && schedulerPolicyAcknowledged(wanted, actual) {
			return true
		}
	}
	if IsRoutingOperation(operation.Kind) && operation.Target != nil && actual.DecisionID == operation.Target.ID && actual.Policy.DecisionID == operation.Target.ID {
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
func (c *ReservationState) CompensatePreparationsLocked(
	runtime *serving.Runtime, dispatch func(serving.SchedulerCandidate),
) {
	for _, parent := range c.runs {
		if parent.action.Kind != TransitionRunKind || parent.action.Status != NativeOperationFailed || !parent.reserved || parent.result == nil {
			continue
		}
		// A canceled/finishing sibling can still publish a policy or route.
		active := false
		for _, run := range c.runs {
			if run.owner == parent.owner && (run.action.Status == NativeOperationRunning || run.action.Status == NativeOperationCancel) {
				active = true
				break
			}
		}
		if active {
			continue
		}
		physical := runtime.Instances()
		for _, child := range c.runs {
			if child.owner != parent.owner || !child.reserved || child.action.Kind != OperationPrepare || child.result == nil ||
				(child.action.Status != NativeOperationFailed && child.action.Status != NativeOperationPartial) || child.result.Operation.Prepare == nil {
				continue
			}
			for _, actual := range physical {
				if !c.failedPreparationUnclaimed(parent, child, actual) {
					continue
				}
				dispatch(serving.SchedulerCandidate{ID: parent.owner, Actions: []serving.Action{{
					ID: child.action.ID + "/compensate/" + actual.ID, Kind: OperationRemove, Node: actual.Node, Instance: actual.ID,
				}}})
			}
		}
	}
}

func (c *ReservationState) failedPreparationUnclaimed(parent, child *ActionRun, actual serving.InstanceState) bool {
	if !actual.Ready || actual.Stopped || actual.Native == nil || !actual.Native.Ready ||
		!child.result.EndedAt.Before(actual.ObservedAt) || !parent.result.EndedAt.Before(actual.ObservedAt) ||
		!failedPreparationIdentityMatches(child, actual) || !PreparationCreatedSource(child, actual) {
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

func failedPreparationIdentityMatches(child *ActionRun, actual serving.InstanceState) bool {
	wanted := child.result.Operation.Prepare.Instance
	podUID, containerID, exact := strings.Cut(actual.ID, "/")
	return wanted.Profile != nil && child.result.Operation.Prepare.Owner != nil && child.result.Operation.Prepare.Owner.UID == wanted.WorkloadUID &&
		actual.OwnerWorkloadUID == wanted.WorkloadUID && actual.Node == wanted.Node && actual.TargetID == wanted.ID && actual.Profile == wanted.Profile.ID &&
		child.result.Operation.ControlsWorkload(wanted.WorkloadUID) && exact && containerID != "" && podUID == actual.PodUID &&
		actual.PodUID != "" && slices.Contains(child.scope.Pods, actual.PodUID) && slices.Contains(child.scope.Instances, actual.ID)
}

func PreparationCreatedSource(child *ActionRun, actual serving.InstanceState) bool {
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

func (c *ReservationState) preparationSourceUnclaimed(actual serving.InstanceState) bool {
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

func (c *ReservationState) ReleaseOwnerLocked(owner string) {
	for _, run := range c.runs {
		if run.owner == owner {
			run.reserved = false
			if run.action.Kind == MeasurementLeaseKind {
				run.action.Status = NativeOperationComplete
				run.result.Status, run.result.EndedAt = NativeOperationComplete, time.Now()
			}
		}
	}
}

func (c *ReservationState) ReservationsLocked() []serving.SchedulerReservation {
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
		if run.result != nil && (reservation.Target == nil || run.action.Kind == TransitionRunKind) {
			reservation.Target = run.result.Operation.Target
		}
		reason := run.action.Reason
		if run.result != nil && run.result.Operation.Prepare != nil && run.prepareIdentity == "" {
			reason = "creation-outcome-unknown: retain owner; reconcile API outcome or reset"
			if evidence := run.result.Preparation; evidence != nil && evidence.PodUID != "" {
				reason = "identity-awaiting-container: " + evidence.PodUID + "; await same UID exact executor"
			}
		}
		if run.action.Kind == TransitionRunKind && run.result != nil && run.result.Operation.Target != nil {
			for _, action := range run.result.Operation.Target.Actions {
				if c.runs[action.ID] != nil || action.Status == NativeOperationCancel || c.OmittedRouteCompleteLocked(action, *run.result.Operation.Target) {
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

func (c *ReservationState) RunReferencedLocked(id string) bool {
	parent := c.runs[id]
	for _, run := range c.runs {
		if !run.reserved && run.action.Status != NativeOperationRunning && run.action.Status != NativeOperationCancel {
			continue
		}
		if parent != nil && parent.action.Kind == TransitionRunKind && parent.owner != "" && parent.owner == run.owner {
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

func (c *ReservationState) NodeExecuting(node *core.Node) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, run := range c.runs {
		if run.action.Kind != MeasurementLeaseKind && run.action.Status == NativeOperationRunning && slices.Contains(run.scope.Nodes, serving.NodeReference{Name: node.Name, UID: string(node.UID)}) {
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
	run := &ActionRun{owner: id, action: serving.Action{ID: id, Kind: workloadDeletionKind, Status: NativeOperationRunning}}
	for _, object := range objects {
		if pod, ok := object.(*core.Pod); ok {
			run.scope.Pods = append(run.scope.Pods, string(pod.UID))
			if status := FirstfloorStatus(pod); status != nil && status.ContainerID != "" {
				run.scope.Instances = append(run.scope.Instances, PhysicalID(pod.UID, status.ContainerID))
			}
		}
	}
	if !c.ReserveRun(id, run, operation) {
		return Refused("OperationConflict", "Workload deletion resources are reserved")
	}
	return nil
}

func (s *ClusterState) ReleaseDeletion(uid types.UID) {
	s.reservation.mu.Lock()
	defer s.reservation.mu.Unlock()
	delete(s.reservation.runs, workloadDeletionKind+"/"+string(uid))
}
