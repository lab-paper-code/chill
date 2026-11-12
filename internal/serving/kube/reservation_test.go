package kube

import (
	"context"
	"testing"
	"time"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/lab-paper-code/chill/internal/serving"
)

const (
	previousEvidence         = "previous"
	restartedContainerSuffix = "-new"
	restartedEvidence        = "restarted"
	staleEvidence            = "stale"
	unknownPodEvidence       = "unknown-pod"
	policyEvidence           = "policy"
	pendingPowerEvidence     = "pending-power"
	activeSiblingEvidence    = "active-sibling"
	cancelSiblingEvidence    = "cancel-sibling"
	unknownPodUID            = "unknown"
	unreadyReason            = "unready"
	nodeUIDEvidence          = "node-uid"
)

func TestCommonReservationConflictAndReleaseAC38(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &SchedulerController{ctx: ctx, reservationState: &reservationState{runs: map[string]*actionRun{}}}
	n1 := serving.NodeReference{Name: "one", UID: "one-uid"}
	n2 := serving.NodeReference{Name: "two", UID: "two-uid"}
	release := make(chan struct{})
	done, _ := c.startNodeRun(servingstate.Operation{ID: "route", Kind: servingstate.OperationRoute, Node: n1, WorkloadUID: "w1"}, SchedulerControllerConfig{}, func(context.Context, *servingstate.OperationResult) error { <-release; return nil })
	for _, operation := range []servingstate.Operation{
		{ID: "power", Kind: servingstate.OperationPower, Node: n1},
		{ID: "reset", Kind: servingstate.OperationReset, WorkloadUIDs: []string{"w1"}},
		{ID: "occupancy", Kind: occupancyKind, Node: n2, WorkloadUID: "w1"},
	} {
		ended, result := c.startNodeRun(operation, SchedulerControllerConfig{}, func(context.Context, *servingstate.OperationResult) error {
			t.Error("conflicting operation executed")
			return nil
		})
		<-ended
		if result.Status != servingstate.NativeOperationFailed {
			t.Fatalf("conflict admitted: %+v", result)
		}
	}
	independent, _ := c.startNodeRun(servingstate.Operation{ID: "independent", Kind: servingstate.OperationPrepare, Node: n2, WorkloadUID: "w2"}, SchedulerControllerConfig{}, func(context.Context, *servingstate.OperationResult) error { return nil })
	<-independent
	close(release)
	<-done
	after, result := c.startNodeRun(servingstate.Operation{ID: "after", Kind: servingstate.OperationPower, Node: n1}, SchedulerControllerConfig{}, func(context.Context, *servingstate.OperationResult) error { return nil })
	<-after
	if result.Status != servingstate.NativeOperationComplete {
		t.Fatalf("returned reservation remained held: %+v", result)
	}
}

func TestTerminatedRunRetainsReservationUntilPhysicalReturnAC38(t *testing.T) {
	for _, termination := range []string{timeoutOutcome, "cancel"} {
		t.Run(termination, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := &SchedulerController{ctx: ctx, reservationState: &reservationState{runs: map[string]*actionRun{}}}
			node := serving.NodeReference{Name: "one", UID: "one-uid"}
			done, result := c.startNodeRun(servingstate.Operation{ID: "remote", Kind: servingstate.OperationPrepare, Node: node, PodUID: "pod"}, SchedulerControllerConfig{}, func(ctx context.Context, _ *servingstate.OperationResult) error {
				if termination == timeoutOutcome {
					return context.DeadlineExceeded
				}
				<-ctx.Done()
				return ctx.Err()
			})
			if termination == "cancel" {
				c.mu.Lock()
				c.runs["remote"].cancel()
				c.mu.Unlock()
			}
			<-done
			if result.Status != servingstate.NativeOperationFailed {
				t.Fatalf("execution did not terminate: %+v", result)
			}
			next := servingstate.Operation{ID: "next", Kind: servingstate.OperationPower, Node: node}
			assertHeld := func() {
				t.Helper()
				if err := c.authorize(next); err == nil {
					t.Fatal("unconfirmed remote effect released reservation")
				}
			}
			assertHeld()
			observed := result.EndedAt.Add(time.Second)
			settled := core.Node{ObjectMeta: meta.ObjectMeta{Name: node.Name, UID: "one-uid"}}
			pod := core.Pod{ObjectMeta: meta.ObjectMeta{UID: "pod"}, Spec: core.PodSpec{NodeName: node.Name}}
			c.releaseObservedScopes([]core.Node{settled}, []core.Pod{pod}, nil, observed)
			assertHeld()
			pending := settled.DeepCopy()
			pending.Labels = map[string]string{chillmeta.PowerModeState: chillmeta.PowerModePending}
			c.releaseObservedScopes([]core.Node{*pending}, nil, nil, observed)
			assertHeld()
			// A fresh observation after reset/cleanup proves both Pod absence
			// and settled power; execution termination alone never does.
			c.releaseObservedScopes([]core.Node{settled}, nil, nil, observed)
			if err := c.authorize(next); err != nil {
				t.Fatalf("physical-return proof did not release reservation: %v", err)
			}
			c.wg.Wait()
		})
	}
}

func TestFailedManualRoutingReleasesOnlyFreshKnownEffectsAC38(t *testing.T) {
	for _, evidence := range []string{"declaration", previousEvidence, "mismatch", staleEvidence, unreadyReason, "native-unready", restartedEvidence, "pod-unready", unknownPodEvidence} {
		t.Run(evidence, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pod, node, profile, _ := observerSourceFixture()
			ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
			id := servingstate.PhysicalID(pod.UID, servingstate.FirstfloorStatus(pod).ContainerID)
			wanted := serving.SchedulerInstance{ID: id, Node: ref, Profile: &profile, WorkloadUID: "workload", MaxBatch: 2}
			target := serving.SchedulerCandidate{ID: "new-policy", Instances: []serving.SchedulerInstance{wanted}}
			previous := native.Metadata{ProfileID: profile.ID, DecisionID: "old-policy", Policy: native.Policy{ProfileID: profile.ID, DecisionID: "old-policy", MaxBatchSize: 1}}
			c := &SchedulerController{ctx: ctx, reservationState: &reservationState{runs: map[string]*actionRun{}}}
			c.state.Snapshot.Instances = []serving.SchedulerInstance{{ID: id, PodUID: string(pod.UID), Node: ref, WorkloadUID: "workload"}}
			done, result := c.startNodeRun(servingstate.Operation{ID: "manual-route:test", Kind: servingstate.OperationRoute, WorkloadUID: "workload", Target: &target}, SchedulerControllerConfig{}, func(_ context.Context, result *servingstate.OperationResult) error {
				result.Before.Native = map[string]native.State{id: {Ready: true, Metadata: previous}}
				result.Status = servingstate.NativeOperationPartial
				return context.DeadlineExceeded
			})
			<-done
			next := servingstate.Operation{WorkloadUID: "workload"}
			if err := c.authorize(next); err == nil {
				t.Fatal("failed execution alone released reservation")
			}
			observed := result.EndedAt.Add(time.Second)
			state := serving.InstanceState{ID: id, PodUID: string(pod.UID), Node: ref, Ready: true, ObservedAt: observed, Native: &native.State{Ready: true, Metadata: native.Metadata{
				ProfileID: profile.ID, DecisionID: target.ID, Policy: native.Policy{ProfileID: profile.ID, DecisionID: target.ID, MaxBatchSize: wanted.MaxBatch},
			}}}
			switch evidence {
			case previousEvidence:
				state.Native.Metadata = previous
			case "mismatch":
				state.Native.Metadata.Policy.MaxBatchSize++
			case staleEvidence:
				state.ObservedAt = result.EndedAt
			case unreadyReason:
				state.Ready = false
			case "native-unready":
				state.Native.Ready = false
			case restartedEvidence:
				pod.Status.ContainerStatuses[0].ContainerID += restartedContainerSuffix
			case "pod-unready":
				pod.Status.ContainerStatuses[0].Ready = false
			case unknownPodEvidence:
				pod.UID = "manual-route-replacement"
				pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", UID: "workload"}}
			}
			c.releaseObservedScopes([]core.Node{*node}, []core.Pod{*pod}, []serving.InstanceState{state}, observed)
			released := c.authorize(next) == nil
			wantReleased := evidence == "declaration" || evidence == previousEvidence
			if released != wantReleased {
				t.Fatalf("evidence %s: released=%t, want %t", evidence, released, wantReleased)
			}
			c.wg.Wait()
		})
	}
}

// reservation-lifecycle-01/full-E5-6-02: a timed-out preparation can become
// Ready later. -02~04/full-E5-7-02: a partial autonomous route can expose an
// exact known policy later. Neither event rewrites the original failure.
func TestPartialAutonomousOwnerAdoptsOnlyFreshKnownEffectsAC38(t *testing.T) {
	for _, kind := range []string{servingstate.OperationPrepare, servingstate.OperationRoute} {
		for _, evidence := range []string{"declaration", previousEvidence, staleEvidence, "parent-stale", restartedEvidence, unknownPodEvidence, "profile", "slot", "decision", policyEvidence, pendingPowerEvidence, unreadyReason, "unknown-container", "other-workload"} {
			t.Run(kind+"/"+evidence, func(t *testing.T) {
				pod, node, profile, _ := observerSourceFixture()
				pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", UID: "workload"}}
				ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
				id := servingstate.PhysicalID(pod.UID, servingstate.FirstfloorStatus(pod).ContainerID)
				ended := time.Now().UTC()
				wanted := serving.SchedulerInstance{ID: id, PodUID: string(pod.UID), Node: ref, Profile: &profile, WorkloadUID: "workload", MaxBatch: 2}
				if kind == servingstate.OperationPrepare {
					wanted.ID = "late-prepare-slot"
				}
				action := serving.Action{ID: "partial-child", Kind: kind, Node: ref, Instance: wanted.ID, Status: servingstate.NativeOperationPartial}
				target := serving.SchedulerCandidate{ID: "autonomous-target", Instances: []serving.SchedulerInstance{wanted}, Actions: []serving.Action{action}}
				policy := native.Policy{ProfileID: profile.ID, DecisionID: target.ID, MaxBatchSize: wanted.MaxBatch}
				previous := native.Metadata{ProfileID: profile.ID, DecisionID: previousEvidence, Policy: native.Policy{ProfileID: profile.ID, DecisionID: previousEvidence, MaxBatchSize: 1}}
				operation := servingstate.Operation{ID: action.ID, Kind: kind, Node: ref, Instance: wanted.ID, WorkloadUIDs: []string{"workload"}, Target: &target}
				if kind == servingstate.OperationPrepare {
					operation.Prepare = &servingstate.PrepareSpec{Instance: wanted, Config: native.Config{Policy: policy}, Owner: &serving.WorkloadReference{UID: "workload"}}
				}
				child := &actionRun{owner: target.ID, action: action, reserved: true,
					result: &servingstate.OperationResult{Operation: operation, Status: servingstate.NativeOperationPartial, EndedAt: ended, Error: timeoutOutcome,
						Before: servingstate.OperationState{Native: map[string]native.State{id: {Ready: true, Metadata: previous}}},
						Actual: servingstate.OperationState{Native: map[string]native.State{id: {Metadata: previous}}}},
				}
				parent := &actionRun{owner: target.ID, reserved: true, action: serving.Action{ID: "parent", Kind: transitionRunKind, Status: servingstate.NativeOperationRunning},
					result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: transitionRunKind, Target: &target, WorkloadUIDs: []string{"workload"}}, Status: servingstate.NativeOperationRunning}}
				child.proofParent = parent
				c := &SchedulerController{reservationState: &reservationState{runs: map[string]*actionRun{action.ID: child, "parent": parent}}}
				if kind == servingstate.OperationRoute {
					c.state.Snapshot.Instances = []serving.SchedulerInstance{{ID: id, PodUID: string(pod.UID), Node: ref, WorkloadUID: "workload"}}
				}
				// A late prepare's Pod was absent when the target acquired its scope.
				child.scope, parent.scope = c.operationScope(operation), c.operationScope(parent.result.Operation)
				if kind == servingstate.OperationPrepare {
					child.result.Preparation = &servingstate.PreparationEvidence{PodUID: string(pod.UID), NodeUID: ref.UID, DecisionID: policy.DecisionID, Created: true}
					preparing := wanted
					preparing.ID, preparing.TargetID, preparing.OwnerWorkloadUID = id, wanted.ID, wanted.WorkloadUID
					child.action.Status = servingstate.NativeOperationRunning
					c.reconcileSnapshot(serving.SchedulerSnapshot{Instances: []serving.SchedulerInstance{preparing}}, ended.Add(-time.Second), time.Minute, c.runtime)
					if evidence == restartedEvidence {
						preparing.ID += restartedContainerSuffix
						c.reconcileSnapshot(serving.SchedulerSnapshot{Instances: []serving.SchedulerInstance{preparing}}, ended.Add(-time.Second), time.Minute, c.runtime)
						if child.prepareIdentity != id {
							t.Fatal("a later container replaced the frozen preparation identity")
						}
					}
					child.action.Status = servingstate.NativeOperationPartial
					// full-E5-6:7119 has no final identity: Capture used the
					// expired context. The live observation must carry it.
					child.result.Actual = servingstate.OperationState{}
					if evidence != previousEvidence {
						child.result.Before.Native = nil
					}
				}
				c.state.Results = []servingstate.OperationResult{*child.result}
				snapshot, _ := c.reconcileSnapshot(c.state.Snapshot, ended.Add(time.Second), time.Minute, c.runtime)
				if len(snapshot.Reservations) != 1 || c.authorize(servingstate.Operation{WorkloadUID: "workload"}) == nil || parent.result.Status != servingstate.NativeOperationFailed {
					t.Fatal("partial receipt did not retain the failed owner's reservation")
				}
				observed := ended.Add(2 * time.Second)
				state := serving.InstanceState{ID: id, PodUID: string(pod.UID), Node: ref, TargetID: wanted.ID, Profile: profile.ID, Ready: true, ObservedAt: observed,
					Native: &native.State{Ready: true, Metadata: native.Metadata{ProfileID: profile.ID, DecisionID: target.ID, Policy: policy}}}
				switch evidence {
				case previousEvidence:
					state.Native.Metadata = previous
					parent.result.Before.Instances = []serving.InstanceState{{ID: id, PodUID: string(pod.UID), Node: ref}}
				case staleEvidence:
					state.ObservedAt = ended
				case "parent-stale":
					state.ObservedAt = parent.result.EndedAt
				case restartedEvidence:
					pod.Status.ContainerStatuses[0].ContainerID += restartedContainerSuffix
				case unknownPodEvidence:
					pod.UID = unknownPodUID
				case profileInitContainerName:
					state.Native.Metadata.ProfileID = "other-profile"
				case "slot":
					state.TargetID = "other-slot"
					// Physical routing targets carry their logical alias, too.
					target.Instances[0].TargetID = "expected-slot"
				case "decision":
					state.Native.Metadata.Policy.DecisionID = "other-decision"
				case policyEvidence:
					state.Native.Metadata.Policy.MaxBatchSize++
				case pendingPowerEvidence:
					node.Labels = map[string]string{chillmeta.PowerModeState: chillmeta.PowerModePending}
				case unreadyReason:
					state.Ready = false
				case "unknown-container":
					child.scope.Instances = []string{wanted.ID}
					child.result.Before.Native, child.result.Actual.Native = nil, nil
					if kind == servingstate.OperationRoute {
						child.scope.Instances = nil
					}
				case "other-workload":
					operation.WorkloadUIDs = []string{"other"}
					child.result.Operation = operation
				}
				c.releaseObservedScopes([]core.Node{*node}, []core.Pod{*pod}, []serving.InstanceState{state}, observed)
				snapshot, lost := c.reconcileSnapshot(c.state.Snapshot, observed, time.Minute, c.runtime)
				assertPartialReservationReceipt(t, c, child, parent, snapshot, lost, evidence)
			})
		}
	}
}

func assertPartialReservationReceipt(t *testing.T, c *SchedulerController, child, parent *actionRun, snapshot serving.SchedulerSnapshot, lost []servingstate.OperationResult, evidence string) {
	t.Helper()
	released := c.authorize(servingstate.Operation{WorkloadUID: "workload"}) == nil
	wantReleased := evidence == "declaration" || evidence == previousEvidence
	if released != wantReleased || (len(snapshot.Reservations) == 0) != wantReleased {
		t.Fatalf("%s: released=%t, want %t; reservations=%+v", evidence, released, wantReleased, snapshot.Reservations)
	}
	if len(lost) != 0 || child.result.Status != servingstate.NativeOperationPartial || child.action.Status != servingstate.NativeOperationPartial ||
		child.result.Error != timeoutOutcome || parent.result.Status != servingstate.NativeOperationFailed || len(c.state.Results) != 1 || c.state.Results[0].Status != servingstate.NativeOperationPartial {
		t.Fatal("known state rewrote or discarded the failure receipt")
	}
}

func TestPartialOwnerKeepsUnconfirmedWorkloadAndPendingPowerAC38(t *testing.T) {
	pod, node, profile, _ := observerSourceFixture()
	ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	id := servingstate.PhysicalID(pod.UID, servingstate.FirstfloorStatus(pod).ContainerID)
	ended := time.Now().UTC()
	policy := native.Policy{ProfileID: profile.ID, DecisionID: "partial", MaxBatchSize: 2}
	wanted := serving.SchedulerInstance{ID: id, Node: ref, Profile: &profile, WorkloadUID: "first", MaxBatch: 2}
	other := wanted
	other.ID, other.WorkloadUID = servingstate.PhysicalID(otherPodUID, servingstate.FirstfloorStatus(pod).ContainerID), "second"
	target := &serving.SchedulerCandidate{ID: policy.DecisionID, Instances: []serving.SchedulerInstance{wanted, other}}
	scope := resourceScope{Nodes: []serving.NodeReference{ref}, Workloads: []string{"first", "second"}, Pods: []string{string(pod.UID), otherPodUID}, Instances: []string{id, other.ID}}
	child := &actionRun{owner: target.ID, reserved: true, scope: scope, action: serving.Action{ID: "route", Kind: servingstate.OperationRoute, Status: servingstate.NativeOperationPartial},
		result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: servingstate.OperationRoute, WorkloadUIDs: scope.Workloads, Target: target}, Status: servingstate.NativeOperationPartial, EndedAt: ended}}
	target.Actions = []serving.Action{child.action}
	parent := &actionRun{owner: target.ID, reserved: true, scope: scope, action: serving.Action{Kind: transitionRunKind, Status: servingstate.NativeOperationFailed},
		result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: transitionRunKind, Target: target}, Status: servingstate.NativeOperationFailed, EndedAt: ended}}
	child.proofParent = parent
	powerRef := serving.NodeReference{Name: "pending-node", UID: "pending-node-uid"}
	parent.scope.Nodes = append([]serving.NodeReference{ref}, powerRef)
	power := &actionRun{owner: target.ID, reserved: true, scope: resourceScope{Nodes: []serving.NodeReference{powerRef}}, action: serving.Action{Kind: servingstate.OperationPower, Status: servingstate.NativeOperationPartial},
		result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: servingstate.OperationPower}, Status: servingstate.NativeOperationPartial, EndedAt: ended}}
	power.proofParent = parent
	c := &SchedulerController{reservationState: &reservationState{runs: map[string]*actionRun{"parent": parent, "route": child, "power": power}}}
	pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", UID: "first"}}
	otherPod := pod.DeepCopy()
	otherPod.UID, otherPod.OwnerReferences[0].UID = otherPodUID, "second"
	observed := ended.Add(time.Second)
	state := serving.InstanceState{ID: id, PodUID: string(pod.UID), Node: ref, Ready: true, ObservedAt: observed,
		Native: &native.State{Ready: true, Metadata: native.Metadata{ProfileID: profile.ID, DecisionID: target.ID, Policy: policy}}}
	otherState := state
	otherState.ID, otherState.PodUID = other.ID, otherPodUID
	otherState.Native = &native.State{Ready: true, Metadata: state.Native.Metadata}
	otherState.Native.Metadata.Policy.MaxBatchSize++
	pending := core.Node{ObjectMeta: meta.ObjectMeta{Name: powerRef.Name, UID: "pending-node-uid", Labels: map[string]string{chillmeta.PowerModeState: chillmeta.PowerModePending}}}
	observe := func() {
		c.releaseObservedScopes([]core.Node{*node, pending}, []core.Pod{*pod, *otherPod}, []serving.InstanceState{state, otherState}, observed)
		c.reconcileSnapshot(serving.SchedulerSnapshot{}, observed, time.Minute, c.runtime)
	}
	observe()
	if !child.reserved || !parent.reserved || !power.reserved {
		t.Fatal("one acknowledged workload released an unconfirmed workload or pending power")
	}
	otherState.Native.Metadata.Policy = policy
	observe()
	if child.reserved || !parent.reserved || !power.reserved {
		t.Fatal("confirmed route responsibility was not reduced independently of pending power")
	}
	pending.Labels = nil
	observe()
	if child.reserved || parent.reserved || power.reserved {
		t.Fatal("fresh exact policies and settled power did not release the failed owner")
	}
}

// Cycle 2 C1: two complete preparations and one partial preparation retain the
// same three-Node workload footprint, but each receipt knows only its own Pod.
func TestTerminalPreparationsShareOnlyFrozenSiblingReceiptsAC38(t *testing.T) {
	for _, evidence := range []string{"all-known", unknownPodEvidence, staleEvidence, "parent-stale", "self-stale", restartedEvidence, nodeUIDEvidence, "slot", "profile", policyEvidence, pendingPowerEvidence, activeSiblingEvidence, cancelSiblingEvidence, "other-parent", "same-owner-unfrozen", "different-action"} {
		t.Run(evidence, func(t *testing.T) {
			ended := time.Now().UTC()
			observed := ended.Add(3 * time.Second)
			target := &serving.SchedulerCandidate{ID: "frozen-transition"}
			c := &SchedulerController{reservationState: &reservationState{runs: map[string]*actionRun{}}}
			var nodes []core.Node
			var pods []core.Pod
			var physical []serving.InstanceState
			var children []*actionRun
			for i, uid := range []types.UID{"one", "two", "three"} {
				pod, node, profile, _ := observerSourceFixture()
				node.Name, node.UID = string(uid), uid
				pod.UID, pod.Spec.NodeName = uid, node.Name
				pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", UID: "workload"}}
				ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
				id := servingstate.PhysicalID(pod.UID, servingstate.FirstfloorStatus(pod).ContainerID)
				wanted := serving.SchedulerInstance{ID: "slot-" + string(uid), Node: ref, Profile: &profile, WorkloadUID: "workload", MaxBatch: 2}
				policy := native.Policy{ProfileID: profile.ID, DecisionID: target.ID, MaxBatchSize: wanted.MaxBatch}
				action := serving.Action{ID: "prepare-" + string(uid), Kind: servingstate.OperationPrepare, Node: ref, Instance: wanted.ID, Status: servingstate.NativeOperationComplete}
				if i == 2 {
					action.Status = servingstate.NativeOperationPartial
				}
				child := &actionRun{owner: target.ID, reserved: true, action: action, prepareIdentity: id,
					scope: resourceScope{Workloads: []string{"workload"}, Pods: []string{string(uid)}, Instances: []string{id}},
					result: &servingstate.OperationResult{Operation: servingstate.Operation{ID: action.ID, Kind: servingstate.OperationPrepare, Node: ref, Instance: wanted.ID,
						WorkloadUIDs: []string{"workload"}, Target: target,
						Prepare: &servingstate.PrepareSpec{Instance: wanted, Config: native.Config{Policy: policy}, Owner: &serving.WorkloadReference{UID: "workload"}}},
						Status: action.Status, EndedAt: ended,
						Preparation: &servingstate.PreparationEvidence{PodUID: string(uid), NodeUID: ref.UID, DecisionID: target.ID, Created: true}}}
				if i == 2 {
					child.result.Error = timeoutOutcome
				}
				target.Instances = append(target.Instances, wanted)
				target.Actions = append(target.Actions, action)
				c.runs[action.ID] = child
				children = append(children, child)
				nodes, pods = append(nodes, *node), append(pods, *pod)
				physical = append(physical, serving.InstanceState{ID: id, PodUID: string(uid), Node: ref, TargetID: wanted.ID,
					Ready: true, ObservedAt: observed, Native: &native.State{Ready: true,
						Metadata: native.Metadata{ProfileID: profile.ID, DecisionID: target.ID, Policy: policy}}})
			}
			scope := resourceScope{Workloads: []string{"workload"}}
			for _, node := range nodes {
				scope.Nodes = append(scope.Nodes, serving.NodeReference{Name: node.Name, UID: string(node.UID)})
			}
			for _, child := range children {
				child.scope.Nodes = scope.Nodes
				c.state.Results = append(c.state.Results, *child.result)
			}
			parent := &actionRun{owner: target.ID, reserved: true, scope: scope,
				action: serving.Action{ID: "parent", Kind: transitionRunKind, Status: servingstate.NativeOperationFailed},
				result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: transitionRunKind, Target: target},
					Status: servingstate.NativeOperationFailed, EndedAt: ended.Add(time.Second), Error: "child failed"}}
			c.runs["parent"] = parent
			for _, child := range children {
				child.proofParent = parent
			}
			mutateTerminalPreparationEvidence(evidence, nodes, pods, physical, children, parent)
			c.releaseObservedScopes(nodes, pods, physical, observed)
			snapshot, lost := c.reconcileSnapshot(serving.SchedulerSnapshot{}, observed, time.Minute, c.runtime)
			wantReleased := evidence == "all-known"
			if (c.authorize(servingstate.Operation{WorkloadUID: "workload"}) == nil) != wantReleased ||
				(len(snapshot.Reservations) == 0) != wantReleased {
				t.Fatalf("%s: reservations=%+v, wantReleased=%t", evidence, snapshot.Reservations, wantReleased)
			}
			for _, child := range children {
				if child.reserved == wantReleased {
					t.Fatalf("%s: child %s reserved=%t", evidence, child.action.ID, child.reserved)
				}
			}
			if len(lost) != 0 || parent.result.Status != servingstate.NativeOperationFailed || parent.result.Error != "child failed" ||
				children[2].result.Status != servingstate.NativeOperationPartial || children[2].result.Error != timeoutOutcome ||
				len(c.state.Results) != 3 || c.state.Results[2].Status != servingstate.NativeOperationPartial {
				t.Fatal("shared return proof rewrote or discarded terminal receipts")
			}
		})
	}
}

func mutateTerminalPreparationEvidence(evidence string, nodes []core.Node, pods []core.Pod, physical []serving.InstanceState, children []*actionRun, parent *actionRun) {
	switch evidence {
	case unknownPodEvidence:
		pods[1].UID = unknownPodUID
	case staleEvidence:
		physical[1].ObservedAt = children[0].result.EndedAt
	case "parent-stale":
		physical[1].ObservedAt = parent.result.EndedAt
	case "self-stale":
		children[2].result.EndedAt = physical[0].ObservedAt
	case restartedEvidence:
		pods[1].Status.ContainerStatuses[0].ContainerID += restartedContainerSuffix
	case nodeUIDEvidence:
		physical[1].Node.UID = testReplacementUID
	case "slot":
		physical[1].TargetID = "unknown-slot"
	case profileInitContainerName:
		physical[1].Native.Metadata.ProfileID = "unknown-profile"
	case policyEvidence:
		physical[1].Native.Metadata.Policy.MaxBatchSize++
	case pendingPowerEvidence:
		nodes[1].Labels = map[string]string{chillmeta.PowerModeState: chillmeta.PowerModePending}
	case activeSiblingEvidence:
		children[1].action.Status = servingstate.NativeOperationRunning
	case cancelSiblingEvidence:
		children[1].action.Status = servingstate.NativeOperationCancel
	case "other-parent":
		children[1].owner = "other-transition"
		children[1].proofParent = nil
	case "same-owner-unfrozen":
		target := parent.result.Operation.Target
		target.Actions = append(target.Actions[:1:1], target.Actions[2])
		children[1].proofParent = nil
	case "different-action":
		children[1].action.Instance = "other-slot"
		children[1].proofParent = nil
	}
}

func TestFailedOwnerAdoptsRegisteredRouteReceiptAC38(t *testing.T) {
	for _, evidence := range []string{"registered", "unregistered", staleEvidence, "active", "other-owner"} {
		t.Run(evidence, func(t *testing.T) {
			pod, node, profile, _ := observerSourceFixture()
			pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", UID: "workload"}}
			ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
			id := servingstate.PhysicalID(pod.UID, servingstate.FirstfloorStatus(pod).ContainerID)
			ended := time.Now().UTC()
			observed := ended.Add(3 * time.Second)
			wanted := serving.SchedulerInstance{ID: id, Node: ref, Profile: &profile, WorkloadUID: "workload", MaxBatch: 2}
			routeAction := serving.Action{ID: "handoff", Kind: servingstate.OperationRoute, Status: servingstate.NativeOperationComplete}
			removeAction := serving.Action{ID: "cleanup", Kind: servingstate.OperationRemove, Node: ref, Instance: "retiring", Status: servingstate.NativeOperationFailed}
			target := &serving.SchedulerCandidate{ID: "original", Instances: []serving.SchedulerInstance{wanted}, Actions: []serving.Action{routeAction, removeAction}}
			scope := resourceScope{Nodes: []serving.NodeReference{ref}, Workloads: []string{"workload"}, Pods: []string{string(pod.UID)}, Instances: []string{id}}
			handoff := &actionRun{owner: target.ID, action: routeAction,
				result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: servingstate.OperationRoute, WorkloadUID: "workload", Target: target}, Status: servingstate.NativeOperationComplete, EndedAt: ended}}
			cleanupScope := resourceScope{Nodes: []serving.NodeReference{{Name: "retiring-node", UID: "retiring-node-uid"}}, Workloads: []string{"workload"}, Pods: []string{"retiring-pod"}}
			cleanup := &actionRun{owner: target.ID, reserved: true, scope: cleanupScope, action: removeAction,
				result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: servingstate.OperationRemove}, Status: servingstate.NativeOperationFailed, EndedAt: ended.Add(time.Second), Error: timeoutOutcome}}
			refreshTarget := &serving.SchedulerCandidate{ID: "cleanup-refresh", Instances: []serving.SchedulerInstance{wanted}}
			refresh := &actionRun{owner: target.ID, scope: scope, action: serving.Action{ID: "refresh", Kind: servingstate.OperationRoute, Status: servingstate.NativeOperationComplete},
				result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: servingstate.OperationRoute, WorkloadUID: "workload", Target: refreshTarget},
					Status: servingstate.NativeOperationComplete, EndedAt: ended}}
			parent := &actionRun{owner: target.ID, reserved: true, scope: scope,
				routeReceipts: map[string]*actionRun{refresh.action.ID: refresh},
				action:        serving.Action{Kind: transitionRunKind, Status: servingstate.NativeOperationFailed},
				result:        &servingstate.OperationResult{Operation: servingstate.Operation{Kind: transitionRunKind, Target: target}, Status: servingstate.NativeOperationFailed, EndedAt: ended.Add(time.Second), Error: "cleanup failed"}}
			handoff.proofParent, cleanup.proofParent, refresh.proofParent = parent, parent, parent
			parent.scope.Nodes = append([]serving.NodeReference{ref}, cleanupScope.Nodes...)
			c := &SchedulerController{reservationState: &reservationState{runs: map[string]*actionRun{"parent": parent, routeAction.ID: handoff, removeAction.ID: cleanup, "refresh": refresh}}}
			state := serving.InstanceState{ID: id, PodUID: string(pod.UID), Node: ref, Ready: true, ObservedAt: observed,
				Native: &native.State{Ready: true, Metadata: native.Metadata{ProfileID: profile.ID, DecisionID: refreshTarget.ID,
					Policy: native.Policy{ProfileID: profile.ID, DecisionID: refreshTarget.ID, MaxBatchSize: wanted.MaxBatch}}}}
			switch evidence {
			case "unregistered":
				parent.routeReceipts = nil
			case staleEvidence:
				state.ObservedAt = parent.result.EndedAt
			case "active":
				refresh.action.Status = servingstate.NativeOperationRunning
			case "other-owner":
				refresh.owner = "unrelated"
			}
			retiring := pod.DeepCopy()
			retiring.UID = "retiring-pod"
			retiring.Spec.NodeName = "retiring-node"
			retiringNode := core.Node{ObjectMeta: meta.ObjectMeta{Name: "retiring-node", UID: "retiring-node-uid"}}
			c.releaseObservedScopes([]core.Node{*node, retiringNode}, []core.Pod{*pod, *retiring}, []serving.InstanceState{state}, observed)
			c.reconcileSnapshot(serving.SchedulerSnapshot{}, observed, time.Minute, c.runtime)
			if !parent.reserved || !cleanup.reserved {
				t.Fatal("refresh ACK released an unconfirmed cleanup responsibility")
			}
			// Retirement is now observed absent; only the registered refresh can
			// prove the survivor's current policy, not the original handoff.
			c.releaseObservedScopes([]core.Node{*node, retiringNode}, []core.Pod{*pod}, []serving.InstanceState{state}, observed)
			c.reconcileSnapshot(serving.SchedulerSnapshot{}, observed, time.Minute, c.runtime)
			if (c.authorize(servingstate.Operation{WorkloadUID: "workload"}) == nil) != (evidence == "registered") {
				t.Fatalf("%s: unexpected cleanup reservation return", evidence)
			}
			if parent.result.Status != servingstate.NativeOperationFailed || parent.result.Error != "cleanup failed" ||
				cleanup.result.Status != servingstate.NativeOperationFailed || cleanup.result.Error != timeoutOutcome {
				t.Fatal("refresh return proof rewrote failed cleanup receipts")
			}
		})
	}
}

// #226: autonomy-off cancels prepare after a Ready-subset route completed.
// Its surviving exact ACK is a return proof, not an unclaimed Pod to remove.
func TestCanceledPrepareAdoptsRegisteredReadySubsetReceiptAC38(t *testing.T) {
	const supersededFailure = "superseded-failed"
	const parentAfterWindow = "parent-after-window"
	for _, evidence := range []string{"registered", "dynamic-route", "unregistered", "subset-owner-mismatch", staleEvidence, activeSiblingEvidence, policyEvidence, "previous-new", "previous-existing", "previous-node", "previous-container", "previous-pod", "superseded", supersededFailure, parentAfterWindow} {
		t.Run(evidence, func(t *testing.T) {
			pod, node, profile, _ := observerSourceFixture()
			pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", UID: "workload"}}
			ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
			id := servingstate.PhysicalID(pod.UID, servingstate.FirstfloorStatus(pod).ContainerID)
			ended := time.Now().UTC()
			observed := ended.Add(3 * time.Second)
			wanted := serving.SchedulerInstance{ID: id, Node: ref, Profile: &profile, WorkloadUID: "workload", MaxBatch: 2}
			scope := resourceScope{Nodes: []serving.NodeReference{ref}, Workloads: []string{"workload"}, Pods: []string{string(pod.UID)}, Instances: []string{id}}
			prepare := &actionRun{owner: "original", reserved: true, scope: scope,
				action: serving.Action{ID: "prepare", Kind: servingstate.OperationPrepare, Instance: "pending", Status: servingstate.NativeOperationPartial},
				result: &servingstate.OperationResult{Status: servingstate.NativeOperationPartial, EndedAt: ended, Error: context.Canceled.Error(),
					Operation: servingstate.Operation{Kind: servingstate.OperationPrepare, Prepare: &servingstate.PrepareSpec{Instance: wanted}}}}
			target := &serving.SchedulerCandidate{ID: "original", Actions: []serving.Action{prepare.action}}
			parent := &actionRun{owner: target.ID, reserved: true, scope: scope,
				action: serving.Action{Kind: transitionRunKind, Status: servingstate.NativeOperationFailed},
				result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: transitionRunKind, Target: target}, Status: servingstate.NativeOperationFailed, EndedAt: ended}}
			prepare.proofParent = parent
			subset := &serving.SchedulerCandidate{ID: "original-ready-subset", Instances: []serving.SchedulerInstance{wanted},
				Routing: map[string]map[string]int32{"workload": {id: 1000}}}
			operation := servingstate.Operation{Kind: servingstate.OperationRoute, Target: subset, WorkloadUIDs: []string{"workload"}}
			route := &actionRun{owner: target.ID, scope: scope,
				action: serving.Action{ID: subset.ID, Kind: servingstate.OperationRoute, From: readySubsetRoute, Status: servingstate.NativeOperationComplete},
				result: &servingstate.OperationResult{Operation: operation, Status: servingstate.NativeOperationComplete, EndedAt: ended}}
			c := &SchedulerController{reservationState: &reservationState{runs: map[string]*actionRun{transitionRunKind + "/" + target.ID: parent, prepare.action.ID: prepare}}}
			if evidence == "dynamic-route" {
				route.action.From = ""
			}
			if evidence == "unregistered" {
				c.runs[route.action.ID] = route
			} else if !c.reserveRun(route.action.ID, route, operation) {
				t.Fatal("same-owner route was not admitted")
			}
			state := serving.InstanceState{ID: id, PodUID: string(pod.UID), Node: ref, Ready: true, ObservedAt: observed,
				Native: &native.State{Ready: true, Metadata: native.Metadata{ProfileID: profile.ID, DecisionID: subset.ID,
					Policy: native.Policy{ProfileID: profile.ID, DecisionID: subset.ID, MaxBatchSize: wanted.MaxBatch}}}}
			switch evidence {
			case "subset-owner-mismatch":
				route.owner = evidence
			case staleEvidence:
				state.ObservedAt = ended
			case activeSiblingEvidence:
				route.action.Status = servingstate.NativeOperationRunning
			case policyEvidence:
				state.Native.Metadata.Policy.MaxBatchSize++
			case "previous-new", "previous-existing", "previous-node", "previous-container", "previous-pod":
				state.Native.Metadata.DecisionID = previousEvidence
				state.Native.Metadata.Policy.DecisionID = previousEvidence
				route.result.Before.Native = map[string]native.State{id: *state.Native}
				if evidence != "previous-new" {
					baseline := serving.InstanceState{ID: id, PodUID: string(pod.UID), Node: ref}
					switch evidence {
					case "previous-node":
						baseline.Node.UID = testReplacementUID
					case "previous-container":
						baseline.ID += restartedContainerSuffix
					case "previous-pod":
						baseline.PodUID = testReplacementUID
					}
					parent.result.Before.Instances = []serving.InstanceState{baseline}
				}
			case "superseded", supersededFailure:
				other := wanted
				other.ID = "other-executor"
				next := &serving.SchedulerCandidate{ID: "next-subset", Instances: []serving.SchedulerInstance{other},
					Routing: map[string]map[string]int32{"workload": {other.ID: 1000}}}
				nextOperation := servingstate.Operation{Kind: servingstate.OperationRoute, Target: next, WorkloadUIDs: []string{"workload"}}
				nextRun := &actionRun{owner: parent.owner, scope: scope,
					action: serving.Action{ID: next.ID, Kind: servingstate.OperationRoute, From: readySubsetRoute, Status: servingstate.NativeOperationComplete},
					result: &servingstate.OperationResult{Operation: nextOperation, Status: servingstate.NativeOperationComplete, EndedAt: ended,
						Before: servingstate.OperationState{Native: map[string]native.State{id: *state.Native}}}}
				if evidence == supersededFailure {
					nextRun.action.Status, nextRun.result.Status = servingstate.NativeOperationFailed, servingstate.NativeOperationFailed
				}
				if !c.reserveRun(next.ID, nextRun, nextOperation) {
					t.Fatal("successive subset was not admitted")
				}
				// Receipt retention must not depend on the action-view window.
				delete(c.runs, route.action.ID)
			case parentAfterWindow:
				parent.reserved = false // Other children can still own unconfirmed effects.
				observed = ended.Add(3 * time.Minute)
				state.ObservedAt = observed
				c.reconcileSnapshot(serving.SchedulerSnapshot{}, ended.Add(2*time.Minute), time.Minute, c.runtime)
				if c.runs[transitionRunKind+"/"+target.ID] == nil {
					t.Fatal("action-view expiry erased a still-owned transition baseline")
				}
			}
			c.releaseObservedScopes([]core.Node{*node}, []core.Pod{*pod}, []serving.InstanceState{state}, observed)
			c.reconcileSnapshot(serving.SchedulerSnapshot{}, observed, time.Minute, c.runtime)
			wantReleased := evidence == "registered" || evidence == "dynamic-route" || evidence == "previous-existing" || evidence == "superseded" || evidence == supersededFailure || evidence == parentAfterWindow
			if released := c.authorize(servingstate.Operation{WorkloadUID: "workload"}) == nil; released != wantReleased {
				t.Fatalf("%s: reservation released=%t", evidence, released)
			}
			if evidence == parentAfterWindow && c.runs[transitionRunKind+"/"+target.ID] != nil {
				t.Fatal("confirmed owner kept its expired baseline after responsibility returned")
			}
			if prepare.result.Status != servingstate.NativeOperationPartial || prepare.result.Error != context.Canceled.Error() || parent.result.Status != servingstate.NativeOperationFailed {
				t.Fatal("known-effect proof rewrote the cancellation receipt")
			}
		})
	}
}

func TestReservationReturnsIndependentNodesDespiteUnknownAssociation(t *testing.T) {
	for _, conflicting := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "conflicting"}[conflicting], func(t *testing.T) {
			refs := []serving.NodeReference{{Name: "one", UID: "one"}, {Name: "two", UID: "two"}, {Name: "three", UID: "three"}}
			ended := time.Now()
			unknown := &actionRun{owner: "owner", reserved: true,
				action: serving.Action{Kind: servingstate.OperationRoute, Status: servingstate.NativeOperationPartial},
				scope:  resourceScope{Nodes: refs[:2], Instances: []string{"unknown-executor"}},
				result: &servingstate.OperationResult{EndedAt: ended, Status: servingstate.NativeOperationPartial}}
			settled := &actionRun{owner: unknown.owner, reserved: true,
				action: serving.Action{Kind: servingstate.OperationRemove, Status: servingstate.NativeOperationPartial},
				scope:  resourceScope{Nodes: refs[2:]},
				result: &servingstate.OperationResult{EndedAt: ended, Status: servingstate.NativeOperationPartial}}
			if conflicting {
				unknown.scope.Nodes, settled.scope.Nodes = refs[:1], refs[1:]
				unknown.result.Before.Instances = []serving.InstanceState{{ID: "unknown-executor", Node: refs[0]}}
				unknown.result.Actual.Instances = []serving.InstanceState{{ID: "unknown-executor", Node: refs[1]}}
			}
			c := &reservationState{runs: map[string]*actionRun{"unknown": unknown, "settled": settled}}
			nodes := []core.Node{}
			for _, ref := range refs {
				nodes = append(nodes, core.Node{ObjectMeta: meta.ObjectMeta{Name: ref.Name, UID: types.UID(ref.UID)}})
			}
			c.releaseObservedScopes(nodes, nil, nil, ended.Add(time.Second))
			for index, ref := range refs {
				allowed := c.authorize(servingstate.Operation{Node: ref}) == nil
				if allowed != (index == 2) {
					t.Errorf("Node %s available=%t, want %t", ref.Name, allowed, index == 2)
				}
			}
		})
	}
}

func TestReservationReturnsPodOnlyTargetAfterAbsence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &SchedulerController{ctx: ctx, reservationState: &reservationState{runs: map[string]*actionRun{}}}
	node := serving.NodeReference{Name: "one", UID: "one"}
	target := &serving.SchedulerCandidate{Instances: []serving.SchedulerInstance{
		{ID: "failed-pod", PodUID: "failed-pod", Node: node, WorkloadUID: "workload", Stopped: true, Terminated: true},
	}}
	done, result := c.startNodeRun(servingstate.Operation{ID: "unroute", Kind: servingstate.OperationUnroute, WorkloadUID: "workload", Target: target},
		SchedulerControllerConfig{}, func(_ context.Context, result *servingstate.OperationResult) error {
			result.Before.Instances = []serving.InstanceState{{PodUID: "failed-pod", Node: node}}
			return context.DeadlineExceeded
		})
	<-done
	defer c.wg.Wait()
	c.mu.Lock()
	reservations := c.reservationsLocked()
	c.mu.Unlock()
	for _, reservation := range reservations {
		if len(reservation.Instances) != 0 || len(reservation.PodUIDs) == 0 || reservation.PodUIDs[0] != "failed-pod" {
			t.Errorf("Pod-only target has wrong public responsibility: %+v", reservation)
		}
	}
	observed := result.EndedAt.Add(time.Second)
	actual := core.Node{ObjectMeta: meta.ObjectMeta{Name: node.Name, UID: types.UID(node.UID)}}
	pod := core.Pod{ObjectMeta: meta.ObjectMeta{UID: "failed-pod", OwnerReferences: []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", UID: "workload"}}},
		Spec: core.PodSpec{NodeName: node.Name}}
	c.releaseObservedScopes([]core.Node{actual}, []core.Pod{pod}, nil, observed)
	if c.authorize(servingstate.Operation{Node: node}) == nil {
		t.Fatal("Pod-only responsibility returned before Pod absence")
	}
	c.releaseObservedScopes([]core.Node{actual}, nil, nil, observed)
	if err := c.authorize(servingstate.Operation{Node: node}); err != nil {
		t.Fatalf("Pod display alias retained executor responsibility after Pod absence: %v", err)
	}
}

func TestResetInheritedUncertaintyAcceptsFreshExactIdentity(t *testing.T) {
	refs := []serving.NodeReference{{Name: "one", UID: "one"}, {Name: "two", UID: "two"}, {Name: "three", UID: "three"}}
	ended := time.Now()
	old := &actionRun{owner: "old", reserved: true,
		action: serving.Action{Kind: servingstate.OperationRoute, Status: servingstate.NativeOperationPartial},
		scope:  resourceScope{Nodes: refs[:2], Workloads: []string{"workload"}, Instances: []string{"pod/container"}},
		result: &servingstate.OperationResult{EndedAt: ended, Status: servingstate.NativeOperationPartial}}
	c := &reservationState{runs: map[string]*actionRun{"old": old}}
	operation := servingstate.Operation{ID: "reset", Kind: servingstate.OperationReset, WorkloadUIDs: []string{"workload"}, Nodes: refs}
	reset := &actionRun{owner: operation.ID, action: serving.Action{Kind: servingstate.OperationRoute, Status: servingstate.NativeOperationPartial},
		result: &servingstate.OperationResult{Operation: operation, EndedAt: ended, Status: servingstate.NativeOperationPartial}}
	if !c.reserveRun(operation.ID, reset, operation) {
		t.Fatal("terminal responsibility did not transfer to reset")
	}
	delete(c.runs, "old")
	nodes := []core.Node{}
	for _, ref := range refs {
		nodes = append(nodes, core.Node{ObjectMeta: meta.ObjectMeta{Name: ref.Name, UID: types.UID(ref.UID)}})
	}
	c.releaseObservedScopes(nodes, nil, []serving.InstanceState{{ID: "pod/container", PodUID: "pod", Node: refs[0], Terminated: true}}, ended.Add(time.Second))
	for _, ref := range refs {
		if err := c.authorize(servingstate.Operation{Node: ref}); err != nil {
			t.Errorf("fresh exact identity did not resolve inherited Node uncertainty for %s: %v", ref.Name, err)
		}
	}
}
