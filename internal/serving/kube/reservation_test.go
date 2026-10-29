package kube

import (
	"context"
	"testing"
	"time"

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
)

func TestCommonReservationConflictAndReleaseAC38(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &SchedulerController{ctx: ctx, runs: map[string]*actionRun{}}
	n1 := serving.NodeReference{Name: "one", UID: "one-uid"}
	n2 := serving.NodeReference{Name: "two", UID: "two-uid"}
	release := make(chan struct{})
	done, _ := c.startNodeRun(Operation{ID: "route", Kind: operationRoute, Node: n1, WorkloadUID: "w1"}, SchedulerControllerConfig{}, func(context.Context, *OperationResult) error { <-release; return nil })
	for _, operation := range []Operation{
		{ID: "power", Kind: operationPower, Node: n1},
		{ID: "reset", Kind: operationReset, WorkloadUIDs: []string{"w1"}},
		{ID: "occupancy", Kind: occupancyKind, Node: n2, WorkloadUID: "w1"},
	} {
		ended, result := c.startNodeRun(operation, SchedulerControllerConfig{}, func(context.Context, *OperationResult) error { t.Error("conflicting operation executed"); return nil })
		<-ended
		if result.Status != nativeOperationFailed {
			t.Fatalf("conflict admitted: %+v", result)
		}
	}
	independent, _ := c.startNodeRun(Operation{ID: "independent", Kind: operationPrepare, Node: n2, WorkloadUID: "w2"}, SchedulerControllerConfig{}, func(context.Context, *OperationResult) error { return nil })
	<-independent
	close(release)
	<-done
	after, result := c.startNodeRun(Operation{ID: "after", Kind: operationPower, Node: n1}, SchedulerControllerConfig{}, func(context.Context, *OperationResult) error { return nil })
	<-after
	if result.Status != nativeOperationComplete {
		t.Fatalf("returned reservation remained held: %+v", result)
	}
}

func TestTerminatedRunRetainsReservationUntilPhysicalReturnAC38(t *testing.T) {
	for _, termination := range []string{timeoutOutcome, "cancel"} {
		t.Run(termination, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := &SchedulerController{ctx: ctx, runs: map[string]*actionRun{}}
			node := serving.NodeReference{Name: "one", UID: "one-uid"}
			done, result := c.startNodeRun(Operation{ID: "remote", Kind: operationPrepare, Node: node, PodUID: "pod"}, SchedulerControllerConfig{}, func(ctx context.Context, _ *OperationResult) error {
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
			if result.Status != nativeOperationFailed {
				t.Fatalf("execution did not terminate: %+v", result)
			}
			next := Operation{ID: "next", Kind: operationPower, Node: node}
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
	for _, evidence := range []string{"declaration", previousEvidence, "mismatch", staleEvidence, "unready", "native-unready", restartedEvidence, "pod-unready", unknownPodEvidence} {
		t.Run(evidence, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pod, node, profile, _ := observerSourceFixture()
			ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
			id := physicalID(pod.UID, firstfloorStatus(pod).ContainerID)
			wanted := serving.SchedulerInstance{ID: id, Node: ref, Profile: &profile, WorkloadUID: "workload", MaxBatch: 2}
			target := serving.SchedulerCandidate{ID: "new-policy", Instances: []serving.SchedulerInstance{wanted}}
			previous := native.Metadata{ProfileID: profile.ID, DecisionID: "old-policy", Policy: native.Policy{ProfileID: profile.ID, DecisionID: "old-policy", MaxBatchSize: 1}}
			c := &SchedulerController{ctx: ctx, runs: map[string]*actionRun{}}
			c.state.Snapshot.Instances = []serving.SchedulerInstance{{ID: id, PodUID: string(pod.UID), Node: ref, WorkloadUID: "workload"}}
			done, result := c.startNodeRun(Operation{ID: "manual-route:test", Kind: operationRoute, WorkloadUID: "workload", Target: &target}, SchedulerControllerConfig{}, func(_ context.Context, result *OperationResult) error {
				result.Before.Native = map[string]native.State{id: {Ready: true, Metadata: previous}}
				result.Status = nativeOperationPartial
				return context.DeadlineExceeded
			})
			<-done
			next := Operation{WorkloadUID: "workload"}
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
			case "unready":
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
	for _, kind := range []string{operationPrepare, operationRoute} {
		for _, evidence := range []string{"declaration", previousEvidence, staleEvidence, "parent-stale", restartedEvidence, unknownPodEvidence, "profile", "slot", "decision", policyEvidence, pendingPowerEvidence, "unready", "unknown-container", "other-workload"} {
			t.Run(kind+"/"+evidence, func(t *testing.T) {
				pod, node, profile, _ := observerSourceFixture()
				pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", UID: "workload"}}
				ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
				id := physicalID(pod.UID, firstfloorStatus(pod).ContainerID)
				ended := time.Now().UTC()
				wanted := serving.SchedulerInstance{ID: id, PodUID: string(pod.UID), Node: ref, Profile: &profile, WorkloadUID: "workload", MaxBatch: 2}
				if kind == operationPrepare {
					wanted.ID = "late-prepare-slot"
				}
				action := serving.Action{ID: "partial-child", Kind: kind, Node: ref, Instance: wanted.ID, Status: nativeOperationPartial}
				target := serving.SchedulerCandidate{ID: "autonomous-target", Instances: []serving.SchedulerInstance{wanted}, Actions: []serving.Action{action}}
				policy := native.Policy{ProfileID: profile.ID, DecisionID: target.ID, MaxBatchSize: wanted.MaxBatch}
				previous := native.Metadata{ProfileID: profile.ID, DecisionID: previousEvidence, Policy: native.Policy{ProfileID: profile.ID, DecisionID: previousEvidence, MaxBatchSize: 1}}
				operation := Operation{ID: action.ID, Kind: kind, Node: ref, Instance: wanted.ID, WorkloadUIDs: []string{"workload"}, Target: &target}
				if kind == operationPrepare {
					operation.Prepare = &PrepareSpec{Instance: wanted, Config: native.Config{Policy: policy}, Owner: &serving.WorkloadReference{UID: "workload"}}
				}
				child := &actionRun{owner: target.ID, action: action, reserved: true,
					result: &OperationResult{Operation: operation, Status: nativeOperationPartial, EndedAt: ended, Error: timeoutOutcome,
						Before: OperationState{Native: map[string]native.State{id: {Ready: true, Metadata: previous}}},
						Actual: OperationState{Native: map[string]native.State{id: {Metadata: previous}}}},
				}
				parent := &actionRun{owner: target.ID, reserved: true, action: serving.Action{ID: "parent", Kind: transitionRunKind, Status: nativeOperationRunning},
					result: &OperationResult{Operation: Operation{Kind: transitionRunKind, Target: &target, WorkloadUIDs: []string{"workload"}}, Status: nativeOperationRunning}}
				c := &SchedulerController{runs: map[string]*actionRun{action.ID: child, "parent": parent}}
				if kind == operationRoute {
					c.state.Snapshot.Instances = []serving.SchedulerInstance{{ID: id, PodUID: string(pod.UID), Node: ref, WorkloadUID: "workload"}}
				}
				// A late prepare's Pod was absent when the target acquired its scope.
				child.scope, parent.scope = c.operationScope(operation), c.operationScope(parent.result.Operation)
				if kind == operationPrepare {
					preparing := wanted
					preparing.ID, preparing.TargetID, preparing.OwnerWorkloadUID = id, wanted.ID, wanted.WorkloadUID
					child.action.Status = nativeOperationRunning
					c.reconcileSnapshot(serving.SchedulerSnapshot{Instances: []serving.SchedulerInstance{preparing}}, ended.Add(-time.Second), time.Minute)
					if evidence == restartedEvidence {
						preparing.ID += restartedContainerSuffix
						c.reconcileSnapshot(serving.SchedulerSnapshot{Instances: []serving.SchedulerInstance{preparing}}, ended.Add(-time.Second), time.Minute)
						if child.prepareIdentity != id {
							t.Fatal("a later container replaced the frozen preparation identity")
						}
					}
					child.action.Status = nativeOperationPartial
					// full-E5-6:7119 has no final identity: Capture used the
					// expired context. The live observation must carry it.
					child.result.Actual = OperationState{}
					if evidence != previousEvidence {
						child.result.Before.Native = nil
					}
				}
				c.state.Results = []OperationResult{*child.result}
				snapshot, _ := c.reconcileSnapshot(c.state.Snapshot, ended.Add(time.Second), time.Minute)
				if len(snapshot.Reservations) != 1 || c.authorize(Operation{WorkloadUID: "workload"}) == nil || parent.result.Status != nativeOperationFailed {
					t.Fatal("partial receipt did not retain the failed owner's reservation")
				}
				observed := ended.Add(2 * time.Second)
				state := serving.InstanceState{ID: id, PodUID: string(pod.UID), Node: ref, TargetID: wanted.ID, Profile: profile.ID, Ready: true, ObservedAt: observed,
					Native: &native.State{Ready: true, Metadata: native.Metadata{ProfileID: profile.ID, DecisionID: target.ID, Policy: policy}}}
				switch evidence {
				case previousEvidence:
					state.Native.Metadata = previous
				case staleEvidence:
					state.ObservedAt = ended
				case "parent-stale":
					state.ObservedAt = parent.result.EndedAt
				case restartedEvidence:
					pod.Status.ContainerStatuses[0].ContainerID += restartedContainerSuffix
				case unknownPodEvidence:
					pod.UID = "unknown"
				case "profile":
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
				case "unready":
					state.Ready = false
				case "unknown-container":
					child.scope.Instances = []string{wanted.ID}
					child.result.Before.Native, child.result.Actual.Native = nil, nil
					if kind == operationRoute {
						child.scope.Instances = nil
					}
				case "other-workload":
					operation.WorkloadUIDs = []string{"other"}
					child.result.Operation = operation
				}
				c.releaseObservedScopes([]core.Node{*node}, []core.Pod{*pod}, []serving.InstanceState{state}, observed)
				snapshot, lost := c.reconcileSnapshot(c.state.Snapshot, observed, time.Minute)
				assertPartialReservationReceipt(t, c, child, parent, snapshot, lost, evidence)
			})
		}
	}
}

func assertPartialReservationReceipt(t *testing.T, c *SchedulerController, child, parent *actionRun, snapshot serving.SchedulerSnapshot, lost []OperationResult, evidence string) {
	t.Helper()
	released := c.authorize(Operation{WorkloadUID: "workload"}) == nil
	wantReleased := evidence == "declaration" || evidence == previousEvidence
	if released != wantReleased || (len(snapshot.Reservations) == 0) != wantReleased {
		t.Fatalf("%s: released=%t, want %t; reservations=%+v", evidence, released, wantReleased, snapshot.Reservations)
	}
	if len(lost) != 0 || child.result.Status != nativeOperationPartial || child.action.Status != nativeOperationPartial ||
		child.result.Error != timeoutOutcome || parent.result.Status != nativeOperationFailed || len(c.state.Results) != 1 || c.state.Results[0].Status != nativeOperationPartial {
		t.Fatal("known state rewrote or discarded the failure receipt")
	}
}

func TestPartialOwnerKeepsUnconfirmedWorkloadAndPendingPowerAC38(t *testing.T) {
	pod, node, profile, _ := observerSourceFixture()
	ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	id := physicalID(pod.UID, firstfloorStatus(pod).ContainerID)
	ended := time.Now().UTC()
	policy := native.Policy{ProfileID: profile.ID, DecisionID: "partial", MaxBatchSize: 2}
	wanted := serving.SchedulerInstance{ID: id, Node: ref, Profile: &profile, WorkloadUID: "first", MaxBatch: 2}
	other := wanted
	other.ID, other.WorkloadUID = physicalID(otherPodUID, firstfloorStatus(pod).ContainerID), "second"
	target := &serving.SchedulerCandidate{ID: policy.DecisionID, Instances: []serving.SchedulerInstance{wanted, other}}
	scope := resourceScope{Nodes: []serving.NodeReference{ref}, Workloads: []string{"first", "second"}, Pods: []string{string(pod.UID), otherPodUID}, Instances: []string{id, other.ID}}
	child := &actionRun{owner: target.ID, reserved: true, scope: scope, action: serving.Action{ID: "route", Kind: operationRoute, Status: nativeOperationPartial},
		result: &OperationResult{Operation: Operation{Kind: operationRoute, WorkloadUIDs: scope.Workloads, Target: target}, Status: nativeOperationPartial, EndedAt: ended}}
	target.Actions = []serving.Action{child.action}
	parent := &actionRun{owner: target.ID, reserved: true, scope: scope, action: serving.Action{Kind: transitionRunKind, Status: nativeOperationFailed},
		result: &OperationResult{Operation: Operation{Kind: transitionRunKind, Target: target}, Status: nativeOperationFailed, EndedAt: ended}}
	powerRef := serving.NodeReference{Name: "pending-node", UID: "pending-node-uid"}
	parent.scope.Nodes = append([]serving.NodeReference{ref}, powerRef)
	power := &actionRun{owner: target.ID, reserved: true, scope: resourceScope{Nodes: []serving.NodeReference{powerRef}}, action: serving.Action{Kind: operationPower, Status: nativeOperationPartial},
		result: &OperationResult{Operation: Operation{Kind: operationPower}, Status: nativeOperationPartial, EndedAt: ended}}
	c := &SchedulerController{runs: map[string]*actionRun{"parent": parent, "route": child, "power": power}}
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
		c.reconcileSnapshot(serving.SchedulerSnapshot{}, observed, time.Minute)
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
	for _, evidence := range []string{"all-known", unknownPodEvidence, staleEvidence, "parent-stale", "self-stale", restartedEvidence, "node-uid", "slot", "profile", policyEvidence, pendingPowerEvidence, activeSiblingEvidence, "cancel-sibling", "other-parent", "same-owner-unfrozen", "different-action"} {
		t.Run(evidence, func(t *testing.T) {
			ended := time.Now().UTC()
			observed := ended.Add(3 * time.Second)
			target := &serving.SchedulerCandidate{ID: "frozen-transition"}
			c := &SchedulerController{runs: map[string]*actionRun{}}
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
				id := physicalID(pod.UID, firstfloorStatus(pod).ContainerID)
				wanted := serving.SchedulerInstance{ID: "slot-" + string(uid), Node: ref, Profile: &profile, WorkloadUID: "workload", MaxBatch: 2}
				policy := native.Policy{ProfileID: profile.ID, DecisionID: target.ID, MaxBatchSize: wanted.MaxBatch}
				action := serving.Action{ID: "prepare-" + string(uid), Kind: operationPrepare, Node: ref, Instance: wanted.ID, Status: nativeOperationComplete}
				if i == 2 {
					action.Status = nativeOperationPartial
				}
				child := &actionRun{owner: target.ID, reserved: true, action: action, prepareIdentity: id,
					scope: resourceScope{Workloads: []string{"workload"}, Pods: []string{string(uid)}, Instances: []string{id}},
					result: &OperationResult{Operation: Operation{ID: action.ID, Kind: operationPrepare, Node: ref, Instance: wanted.ID,
						WorkloadUIDs: []string{"workload"}, Target: target,
						Prepare: &PrepareSpec{Instance: wanted, Config: native.Config{Policy: policy}, Owner: &serving.WorkloadReference{UID: "workload"}}},
						Status: action.Status, EndedAt: ended}}
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
				action: serving.Action{ID: "parent", Kind: transitionRunKind, Status: nativeOperationFailed},
				result: &OperationResult{Operation: Operation{Kind: transitionRunKind, Target: target},
					Status: nativeOperationFailed, EndedAt: ended.Add(time.Second), Error: "child failed"}}
			c.runs["parent"] = parent
			mutateTerminalPreparationEvidence(evidence, nodes, pods, physical, children, parent)
			c.releaseObservedScopes(nodes, pods, physical, observed)
			snapshot, lost := c.reconcileSnapshot(serving.SchedulerSnapshot{}, observed, time.Minute)
			wantReleased := evidence == "all-known"
			if (c.authorize(Operation{WorkloadUID: "workload"}) == nil) != wantReleased ||
				(len(snapshot.Reservations) == 0) != wantReleased {
				t.Fatalf("%s: reservations=%+v, wantReleased=%t", evidence, snapshot.Reservations, wantReleased)
			}
			for _, child := range children {
				if child.reserved == wantReleased {
					t.Fatalf("%s: child %s reserved=%t", evidence, child.action.ID, child.reserved)
				}
			}
			if len(lost) != 0 || parent.result.Status != nativeOperationFailed || parent.result.Error != "child failed" ||
				children[2].result.Status != nativeOperationPartial || children[2].result.Error != timeoutOutcome ||
				len(c.state.Results) != 3 || c.state.Results[2].Status != nativeOperationPartial {
				t.Fatal("shared return proof rewrote or discarded terminal receipts")
			}
		})
	}
}

func mutateTerminalPreparationEvidence(evidence string, nodes []core.Node, pods []core.Pod, physical []serving.InstanceState, children []*actionRun, parent *actionRun) {
	switch evidence {
	case unknownPodEvidence:
		pods[1].UID = "unknown"
	case staleEvidence:
		physical[1].ObservedAt = children[0].result.EndedAt
	case "parent-stale":
		physical[1].ObservedAt = parent.result.EndedAt
	case "self-stale":
		children[2].result.EndedAt = physical[0].ObservedAt
	case restartedEvidence:
		pods[1].Status.ContainerStatuses[0].ContainerID += restartedContainerSuffix
	case "node-uid":
		physical[1].Node.UID = testReplacementUID
	case "slot":
		physical[1].TargetID = "unknown-slot"
	case "profile":
		physical[1].Native.Metadata.ProfileID = "unknown-profile"
	case policyEvidence:
		physical[1].Native.Metadata.Policy.MaxBatchSize++
	case pendingPowerEvidence:
		nodes[1].Labels = map[string]string{chillmeta.PowerModeState: chillmeta.PowerModePending}
	case activeSiblingEvidence:
		children[1].action.Status = nativeOperationRunning
	case "cancel-sibling":
		children[1].action.Status = nativeOperationCancel
	case "other-parent":
		children[1].owner = "other-transition"
	case "same-owner-unfrozen":
		target := parent.result.Operation.Target
		target.Actions = append(target.Actions[:1:1], target.Actions[2])
	case "different-action":
		children[1].action.Instance = "other-slot"
	}
}

func TestFailedCleanupAdoptsRegisteredRouteRefreshReceiptAC38(t *testing.T) {
	for _, evidence := range []string{"registered", "unregistered", staleEvidence, "active", "other-owner"} {
		t.Run(evidence, func(t *testing.T) {
			pod, node, profile, _ := observerSourceFixture()
			pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", UID: "workload"}}
			ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
			id := physicalID(pod.UID, firstfloorStatus(pod).ContainerID)
			ended := time.Now().UTC()
			observed := ended.Add(3 * time.Second)
			wanted := serving.SchedulerInstance{ID: id, Node: ref, Profile: &profile, WorkloadUID: "workload", MaxBatch: 2}
			routeAction := serving.Action{ID: "handoff", Kind: operationRoute, Status: nativeOperationComplete}
			removeAction := serving.Action{ID: "cleanup", Kind: operationRemove, Node: ref, Instance: "retiring", Status: nativeOperationFailed}
			target := &serving.SchedulerCandidate{ID: "original", Instances: []serving.SchedulerInstance{wanted}, Actions: []serving.Action{routeAction, removeAction}}
			scope := resourceScope{Nodes: []serving.NodeReference{ref}, Workloads: []string{"workload"}, Pods: []string{string(pod.UID)}, Instances: []string{id}}
			handoff := &actionRun{owner: target.ID, action: routeAction,
				result: &OperationResult{Operation: Operation{Kind: operationRoute, WorkloadUID: "workload", Target: target}, Status: nativeOperationComplete, EndedAt: ended}}
			cleanupScope := resourceScope{Nodes: []serving.NodeReference{{Name: "retiring-node", UID: "retiring-node-uid"}}, Workloads: []string{"workload"}, Pods: []string{"retiring-pod"}}
			cleanup := &actionRun{owner: target.ID, reserved: true, scope: cleanupScope, action: removeAction,
				result: &OperationResult{Operation: Operation{Kind: operationRemove}, Status: nativeOperationFailed, EndedAt: ended.Add(time.Second), Error: timeoutOutcome}}
			refreshTarget := &serving.SchedulerCandidate{ID: "cleanup-refresh", Instances: []serving.SchedulerInstance{wanted}}
			refresh := &actionRun{owner: target.ID, scope: scope, action: serving.Action{ID: "refresh", Kind: operationRoute, Status: nativeOperationComplete},
				result: &OperationResult{Operation: Operation{Kind: operationRoute, WorkloadUID: "workload", Target: refreshTarget},
					Status: nativeOperationComplete, EndedAt: ended}}
			parent := &actionRun{owner: target.ID, reserved: true, scope: scope, cleanup: true, cleanupRoutes: map[string]*actionRun{"workload": refresh},
				action: serving.Action{Kind: transitionRunKind, Status: nativeOperationFailed},
				result: &OperationResult{Operation: Operation{Kind: transitionRunKind, Target: target}, Status: nativeOperationFailed, EndedAt: ended.Add(time.Second), Error: "cleanup failed"}}
			parent.scope.Nodes = append([]serving.NodeReference{ref}, cleanupScope.Nodes...)
			c := &SchedulerController{runs: map[string]*actionRun{"parent": parent, routeAction.ID: handoff, removeAction.ID: cleanup, "refresh": refresh}}
			state := serving.InstanceState{ID: id, PodUID: string(pod.UID), Node: ref, Ready: true, ObservedAt: observed,
				Native: &native.State{Ready: true, Metadata: native.Metadata{ProfileID: profile.ID, DecisionID: refreshTarget.ID,
					Policy: native.Policy{ProfileID: profile.ID, DecisionID: refreshTarget.ID, MaxBatchSize: wanted.MaxBatch}}}}
			switch evidence {
			case "unregistered":
				parent.cleanupRoutes = nil
			case staleEvidence:
				state.ObservedAt = parent.result.EndedAt
			case "active":
				refresh.action.Status = nativeOperationRunning
			case "other-owner":
				refresh.owner = "unrelated"
			}
			retiring := pod.DeepCopy()
			retiring.UID = "retiring-pod"
			retiring.Spec.NodeName = "retiring-node"
			retiringNode := core.Node{ObjectMeta: meta.ObjectMeta{Name: "retiring-node", UID: "retiring-node-uid"}}
			c.releaseObservedScopes([]core.Node{*node, retiringNode}, []core.Pod{*pod, *retiring}, []serving.InstanceState{state}, observed)
			c.reconcileSnapshot(serving.SchedulerSnapshot{}, observed, time.Minute)
			if !parent.reserved || !cleanup.reserved {
				t.Fatal("refresh ACK released an unconfirmed cleanup responsibility")
			}
			// Retirement is now observed absent; only the registered refresh can
			// prove the survivor's current policy, not the original handoff.
			c.releaseObservedScopes([]core.Node{*node, retiringNode}, []core.Pod{*pod}, []serving.InstanceState{state}, observed)
			c.reconcileSnapshot(serving.SchedulerSnapshot{}, observed, time.Minute)
			if (c.authorize(Operation{WorkloadUID: "workload"}) == nil) != (evidence == "registered") {
				t.Fatalf("%s: unexpected cleanup reservation return", evidence)
			}
			if parent.result.Status != nativeOperationFailed || parent.result.Error != "cleanup failed" ||
				cleanup.result.Status != nativeOperationFailed || cleanup.result.Error != timeoutOutcome {
				t.Fatal("refresh return proof rewrote failed cleanup receipts")
			}
		})
	}
}
