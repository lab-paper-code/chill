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

	"github.com/lab-paper-code/chill/internal/serving"
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
	for _, termination := range []string{"timeout", "cancel"} {
		t.Run(termination, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := &SchedulerController{ctx: ctx, runs: map[string]*actionRun{}}
			node := serving.NodeReference{Name: "one", UID: "one-uid"}
			done, result := c.startNodeRun(Operation{ID: "remote", Kind: operationPrepare, Node: node, PodUID: "pod"}, SchedulerControllerConfig{}, func(ctx context.Context, _ *OperationResult) error {
				if termination == "timeout" {
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
	for _, evidence := range []string{"declaration", "previous", "mismatch", "stale", "unready", "native-unready", "restarted", "pod-unready", "unknown-pod"} {
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
			case "previous":
				state.Native.Metadata = previous
			case "mismatch":
				state.Native.Metadata.Policy.MaxBatchSize++
			case "stale":
				state.ObservedAt = result.EndedAt
			case "unready":
				state.Ready = false
			case "native-unready":
				state.Native.Ready = false
			case "restarted":
				pod.Status.ContainerStatuses[0].ContainerID += "-new"
			case "pod-unready":
				pod.Status.ContainerStatuses[0].Ready = false
			case "unknown-pod":
				pod.UID = "manual-route-replacement"
				pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", UID: "workload"}}
			}
			c.releaseObservedScopes([]core.Node{*node}, []core.Pod{*pod}, []serving.InstanceState{state}, observed)
			released := c.authorize(next) == nil
			wantReleased := evidence == "declaration" || evidence == "previous"
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
		for _, evidence := range []string{"declaration", "previous", "stale", "parent-stale", "restarted", "unknown-pod", "profile", "slot", "decision", "policy", "pending-power", "unready", "unknown-container", "other-workload"} {
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
				previous := native.Metadata{ProfileID: profile.ID, DecisionID: "previous", Policy: native.Policy{ProfileID: profile.ID, DecisionID: "previous", MaxBatchSize: 1}}
				operation := Operation{ID: action.ID, Kind: kind, Node: ref, Instance: wanted.ID, WorkloadUIDs: []string{"workload"}, Target: &target}
				if kind == operationPrepare {
					operation.Prepare = &PrepareSpec{Instance: wanted, Config: native.Config{Policy: policy}, Owner: &serving.WorkloadReference{UID: "workload"}}
				}
				child := &actionRun{owner: target.ID, action: action, reserved: true,
					result: &OperationResult{Operation: operation, Status: nativeOperationPartial, EndedAt: ended, Error: "timeout",
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
					if evidence == "restarted" {
						preparing.ID += "-new"
						c.reconcileSnapshot(serving.SchedulerSnapshot{Instances: []serving.SchedulerInstance{preparing}}, ended.Add(-time.Second), time.Minute)
						if child.prepareIdentity != id {
							t.Fatal("a later container replaced the frozen preparation identity")
						}
					}
					child.action.Status = nativeOperationPartial
					// full-E5-6:7119 has no final identity: Capture used the
					// expired context. The live observation must carry it.
					child.result.Actual = OperationState{}
					if evidence != "previous" {
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
				case "previous":
					state.Native.Metadata = previous
				case "stale":
					state.ObservedAt = ended
				case "parent-stale":
					state.ObservedAt = parent.result.EndedAt
				case "restarted":
					pod.Status.ContainerStatuses[0].ContainerID += "-new"
				case "unknown-pod":
					pod.UID = "unknown"
				case "profile":
					state.Native.Metadata.ProfileID = "other-profile"
				case "slot":
					state.TargetID = "other-slot"
					// Physical routing targets carry their logical alias, too.
					target.Instances[0].TargetID = "expected-slot"
				case "decision":
					state.Native.Metadata.Policy.DecisionID = "other-decision"
				case "policy":
					state.Native.Metadata.Policy.MaxBatchSize++
				case "pending-power":
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
				released := c.authorize(Operation{WorkloadUID: "workload"}) == nil
				wantReleased := evidence == "declaration" || evidence == "previous"
				if released != wantReleased || (len(snapshot.Reservations) == 0) != wantReleased {
					t.Fatalf("%s: released=%t, want %t; reservations=%+v", evidence, released, wantReleased, snapshot.Reservations)
				}
				if len(lost) != 0 || child.result.Status != nativeOperationPartial || child.action.Status != nativeOperationPartial ||
					child.result.Error != "timeout" || parent.result.Status != nativeOperationFailed || len(c.state.Results) != 1 || c.state.Results[0].Status != nativeOperationPartial {
					t.Fatal("known state rewrote or discarded the failure receipt")
				}
			})
		}
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
	other.ID, other.WorkloadUID = physicalID("other-pod", firstfloorStatus(pod).ContainerID), "second"
	target := &serving.SchedulerCandidate{ID: policy.DecisionID, Instances: []serving.SchedulerInstance{wanted, other}}
	scope := resourceScope{Nodes: []serving.NodeReference{ref}, Workloads: []string{"first", "second"}, Pods: []string{string(pod.UID), "other-pod"}, Instances: []string{id, other.ID}}
	child := &actionRun{owner: target.ID, reserved: true, scope: scope, action: serving.Action{Kind: operationRoute, Status: nativeOperationPartial},
		result: &OperationResult{Operation: Operation{Kind: operationRoute, WorkloadUIDs: scope.Workloads, Target: target}, Status: nativeOperationPartial, EndedAt: ended}}
	parent := &actionRun{owner: target.ID, reserved: true, scope: scope, action: serving.Action{Kind: transitionRunKind, Status: nativeOperationFailed},
		result: &OperationResult{Operation: Operation{Kind: transitionRunKind, Target: target}, Status: nativeOperationFailed, EndedAt: ended}}
	powerRef := serving.NodeReference{Name: "pending-node", UID: "pending-node-uid"}
	parent.scope.Nodes = append([]serving.NodeReference{ref}, powerRef)
	power := &actionRun{owner: target.ID, reserved: true, scope: resourceScope{Nodes: []serving.NodeReference{powerRef}}, action: serving.Action{Kind: operationPower, Status: nativeOperationPartial},
		result: &OperationResult{Operation: Operation{Kind: operationPower}, Status: nativeOperationPartial, EndedAt: ended}}
	c := &SchedulerController{runs: map[string]*actionRun{"parent": parent, "route": child, "power": power}}
	pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", UID: "first"}}
	otherPod := pod.DeepCopy()
	otherPod.UID, otherPod.OwnerReferences[0].UID = "other-pod", "second"
	observed := ended.Add(time.Second)
	state := serving.InstanceState{ID: id, PodUID: string(pod.UID), Node: ref, Ready: true, ObservedAt: observed,
		Native: &native.State{Ready: true, Metadata: native.Metadata{ProfileID: profile.ID, DecisionID: target.ID, Policy: policy}}}
	otherState := state
	otherState.ID, otherState.PodUID = other.ID, "other-pod"
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
