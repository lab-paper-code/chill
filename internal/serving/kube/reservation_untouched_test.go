package kube

import (
	"context"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// SCH-AC-38: the collision lease is wider than a Node-local prepare's writes.
func TestUntouchedAdmissionBaselineAC38(t *testing.T) {
	for _, evidence := range []string{"untouched", "local-prepare", "route-cancel", "stop-cancel", "power-cancel", "unknown", "stale", "unready", "pod-unready", "native-unready", "pending-power", "native-unknown", "restart", "node-replaced", "not-baseline", "sibling", "cancel-sibling", "absent-sibling", "gc"} {
		t.Run(evidence, func(t *testing.T) {
			pod, node, _, _ := observerSourceFixture()
			ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
			id := physicalID(pod.UID, firstfloorStatus(pod).ContainerID)
			ended := time.Now().Add(-time.Minute)
			state := serving.InstanceState{ID: id, PodUID: string(pod.UID), Node: ref, Ready: true, ObservedAt: ended.Add(time.Second), Native: &native.State{Ready: true}}
			baseline := serving.InstanceState{ID: id, PodUID: string(pod.UID), Node: ref}
			target := &serving.SchedulerCandidate{ID: "owner"}
			parent := &actionRun{owner: target.ID, reserved: true, action: serving.Action{ID: "transition/owner", Kind: transitionRunKind, Status: nativeOperationFailed}, scope: resourceScope{Nodes: []serving.NodeReference{ref}, Pods: []string{string(pod.UID)}, Instances: []string{id}}, result: &OperationResult{Operation: Operation{Kind: transitionRunKind, Target: target}, Before: OperationState{Instances: []serving.InstanceState{baseline}}, EndedAt: ended}}
			c := &SchedulerController{runs: map[string]*actionRun{parent.action.ID: parent}}
			c.state.Snapshot.Instances = []serving.SchedulerInstance{{ID: id, PodUID: string(pod.UID), Node: ref}}
			switch evidence {
			case "local-prepare", "route-cancel", "stop-cancel", "power-cancel", "unknown":
				kind, effectNode := operationPrepare, serving.NodeReference{Name: "nano", UID: "nano-uid"}
				switch evidence {
				case "route-cancel":
					kind = operationRoute
				case "stop-cancel":
					kind = operationStop
					effectNode = ref
				case "power-cancel":
					kind = operationPower
					effectNode = ref
				case "unknown":
					kind = "future-write"
				}
				action := serving.Action{ID: "effect", Kind: kind, Node: effectNode, Status: nativeOperationRunning}
				target.Actions = []serving.Action{action}
				op := Operation{Kind: kind, Node: effectNode, Target: target, Nodes: []serving.NodeReference{ref}}
				run := &actionRun{owner: parent.owner, proofParent: parent, action: action, result: &OperationResult{Operation: op, EndedAt: ended}}
				parent.returnObserved = true
				if !c.reserveRun(action.ID, run, op) {
					t.Fatal("authorization failed")
				}
				if parent.returnObserved {
					t.Fatal("new authorization retained an old return proof")
				}
				run.action.Status = nativeOperationFailed
				run.reserved = false // action-view retirement cannot erase the parent's dirty set
				delete(c.runs, action.ID)
			case "stale":
				state.ObservedAt = ended
			case "unready":
				state.Ready = false
			case "pod-unready":
				pod.Status.ContainerStatuses[0].Ready = false
			case "native-unready":
				state.Native.Ready = false
			case "pending-power":
				node.Labels = map[string]string{chillmeta.PowerModeState: chillmeta.PowerModePending}
			case "native-unknown":
				state.Native = nil
			case "restart":
				pod.Status.ContainerStatuses[0].ContainerID += "-replacement"
			case "node-replaced":
				state.Node.UID += "-replacement"
			case "not-baseline":
				parent.result.Before.Instances = nil
			case "sibling", "cancel-sibling", "absent-sibling":
				status := nativeOperationRunning
				if evidence == "cancel-sibling" {
					status = nativeOperationCancel
				}
				c.runs["sibling"] = &actionRun{owner: parent.owner, action: serving.Action{Status: status}}
			case "gc":
				parent.reserved = false
				c.runs["held"] = &actionRun{owner: parent.owner, reserved: true}
				if !c.runReferencedLocked(parent.action.ID) {
					t.Fatal("baseline collected while owner responsibility remained")
				}
				parent.reserved = true
			}
			pods := []core.Pod{*pod}
			if evidence == "absent-sibling" {
				pods = nil
			}
			c.releaseObservedScopes([]core.Node{*node}, pods, []serving.InstanceState{state}, ended.Add(2*time.Second))
			want := evidence == "untouched" || evidence == "local-prepare" || evidence == "gc"
			if parent.returnObserved != want {
				t.Fatalf("returnObserved=%t, want %t", parent.returnObserved, want)
			}
			if evidence == "gc" {
				parent.reserved = false
				c.runs["held"].reserved = false
				if c.runReferencedLocked(parent.action.ID) {
					t.Fatal("returned owner still retained its baseline")
				}
			}
		})
	}
}

func TestPartialPrepareLateACKMultiNodeReturnsAC38(t *testing.T) {
	c, api, child, parent := failedPrepareCompensationFixture(t, "policy-ack")
	child.action.Status, child.result.Status = nativeOperationPartial, nativeOperationPartial
	child.result.Error = context.DeadlineExceeded.Error()
	parent.result.Operation.Target.Actions = append(parent.result.Operation.Target.Actions, serving.Action{ID: "never-started-route", Kind: operationRoute, DependsOn: []string{child.action.ID}})
	// Add two preexisting executors outside nano's capture. Neither has a policy
	// receipt from this owner, but no authorized action can have written to them.
	pod, node, _, _ := observerSourceFixture()
	ended := parent.result.EndedAt
	physical := c.runtime.Instances()
	pods := []core.Pod{}
	nodes := []core.Node{}
	for _, suffix := range []string{"genesis3", "jetsonx", "nano-old"} {
		p, n := pod.DeepCopy(), node.DeepCopy()
		p.Name = suffix
		p.UID += types.UID(suffix)
		n.Name = suffix
		n.UID += types.UID(suffix)
		if suffix == "nano-old" {
			n.Name = child.result.Operation.Node.Name
			n.UID = types.UID(child.result.Operation.Node.UID)
		}
		p.Spec.NodeName = n.Name
		if suffix == "genesis3" {
			source := physical[0]
			var err error
			p, err = c.kube.CoreV1().Pods("chill-system").Get(c.ctx, "a", meta.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			n, err = c.kube.CoreV1().Nodes().Get(c.ctx, source.Node.Name, meta.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
		}
		ref := serving.NodeReference{Name: n.Name, UID: string(n.UID)}
		id := physicalID(p.UID, firstfloorStatus(p).ContainerID)
		s := serving.InstanceState{ID: id, PodUID: string(p.UID), Node: ref, Ready: true, ObservedAt: ended.Add(time.Second), Native: &native.State{Ready: true}}
		if suffix == "nano-old" {
			child.result.Before.Native = map[string]native.State{id: *s.Native}
		}
		parent.result.Before.Instances = append(parent.result.Before.Instances, s)
		for _, run := range []*actionRun{parent, child} {
			run.scope.Nodes = append(run.scope.Nodes, ref)
			run.scope.Pods = append(run.scope.Pods, string(p.UID))
			run.scope.Instances = append(run.scope.Instances, id)
		}
		pods = append(pods, *p)
		nodes = append(nodes, *n)
		physical = append(physical, s)
	}
	if !c.reserveRun(child.action.ID, child, child.result.Operation) {
		t.Fatal("Node-local prepare authorization failed")
	}
	c.launch(serving.SchedulerCandidate{}, c.config)
	c.wg.Wait()
	if api.deleteCalls != 0 {
		t.Fatal("late ACK was compensated")
	}
	fresh, err := c.capture(c.ctx, time.Now(), c.config)
	if err != nil {
		t.Fatal(err)
	}
	livePods, err := c.kube.CoreV1().Pods("").List(c.ctx, meta.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	liveNodes, err := c.kube.CoreV1().Nodes().List(c.ctx, meta.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pods = append(pods, livePods.Items...)
	nodes = append(nodes, liveNodes.Items...)
	physical = append(physical[len(physical)-3:], c.runtime.Instances()...)
	parent.reserved = true
	child.reserved = true
	parent.returnObserved = false
	c.releaseObservedScopes(nodes, pods, physical, time.Now())
	fresh, _ = c.reconcileSnapshot(fresh, time.Now(), c.config.Settings.ObservationWindow)
	if parent.reserved || child.reserved || len(fresh.Reservations) != 0 || api.deleteCalls != 0 {
		t.Fatal("eligible fresh tick did not return the owner without compensation")
	}
	if c.runs["never-started-route"] != nil || child.result.Status != nativeOperationPartial || parent.result.Status != nativeOperationFailed {
		t.Fatal("return rewrote the failure or started the frozen route")
	}
	if _, err := c.kube.CoreV1().Pods("chill-system").Get(c.ctx, "b", meta.GetOptions{}); err != nil {
		t.Fatal("late-ACK executor did not survive", err)
	}
}
