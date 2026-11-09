package kube

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type defectEReturnFixture struct {
	c               *SchedulerController
	parent, child   *actionRun
	nodes           []core.Node
	pods            []core.Pod
	physical        []serving.InstanceState
	a, b            serving.NodeReference
	ended, observed time.Time
}

func newDefectEReturnFixture(t *testing.T) defectEReturnFixture {
	t.Helper()
	pod, node, _, _ := observerSourceFixture()
	pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", UID: "workload"}}
	b := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	a := serving.NodeReference{Name: "returned", UID: "returned-uid"}
	id := servingstate.PhysicalID(pod.UID, servingstate.FirstfloorStatus(pod).ContainerID)
	ended := time.Now().UTC()
	before := []serving.InstanceState{{ID: "removed/container", PodUID: "removed", Node: a}, {ID: id, PodUID: string(pod.UID), Node: b}}
	remove := serving.Action{ID: "completed-remove", Kind: servingstate.OperationRemove, Node: a, Instance: before[0].ID, Status: servingstate.NativeOperationComplete}
	stop := serving.Action{ID: "never-start-stop", Kind: servingstate.OperationStop, Node: b, Instance: id}
	target := &serving.SchedulerCandidate{ID: "failed-owner", Actions: []serving.Action{remove, stop}}
	parent := &actionRun{owner: target.ID, reserved: true,
		action: serving.Action{ID: transitionRunKind + "/" + target.ID, Kind: transitionRunKind, Status: servingstate.NativeOperationFailed},
		scope:  resourceScope{Nodes: []serving.NodeReference{a, b}, Owners: []string{"workload"}, Pods: []string{"removed", string(pod.UID)}, Instances: []string{before[0].ID, id}},
		result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: transitionRunKind, Nodes: []serving.NodeReference{a, b}, Target: target}, Before: servingstate.OperationState{Instances: before}, Status: servingstate.NativeOperationFailed, EndedAt: ended, Error: "unconfirmed frontier"}}
	child := &actionRun{owner: parent.owner, proofParent: parent, reserved: true, action: remove,
		scope:  resourceScope{Nodes: []serving.NodeReference{a}, Pods: []string{"removed"}, Instances: []string{before[0].ID}},
		result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: remove.Kind, Node: a, Instance: remove.Instance, PodUID: "removed"}, Status: servingstate.NativeOperationComplete, EndedAt: ended.Add(-time.Second)}}
	c := &SchedulerController{reservationState: &reservationState{runs: map[string]*actionRun{parent.action.ID: parent, child.action.ID: child}}}
	return defectEReturnFixture{c: c, parent: parent, child: child, a: a, b: b, ended: ended, observed: ended.Add(time.Second),
		nodes: []core.Node{{ObjectMeta: meta.ObjectMeta{Name: a.Name, UID: types.UID(a.UID)}}, *node}, pods: []core.Pod{*pod},
		physical: []serving.InstanceState{{ID: id, PodUID: string(pod.UID), Node: b, ObservedAt: ended.Add(time.Second), Native: &native.State{}}}}
}

func (f defectEReturnFixture) observe() {
	f.c.releaseObservedScopes(f.nodes, f.pods, f.physical, f.observed)
}

func (f defectEReturnFixture) assertAdmission(t *testing.T, aReturned bool) {
	t.Helper()
	if err := f.c.authorize(servingstate.Operation{Kind: servingstate.OperationPrepare, Node: f.a}); (err == nil) != aReturned {
		t.Fatalf("healthy admission error=%v, returned=%t", err, aReturned)
	}
	if err := f.c.authorize(servingstate.Operation{Kind: servingstate.OperationPrepare, Node: f.b}); err == nil {
		t.Fatal("unconfirmed exact node became available")
	}
	if !f.parent.reserved || f.parent.returnObserved || f.parent.result.Status != servingstate.NativeOperationFailed || f.parent.result.Error != "unconfirmed frontier" {
		t.Fatal("partial return discarded residual ownership or rewrote failure")
	}
}

// T6/T11: admission consumes the actual reduced parent and child scopes.
func TestDefectEIndependentReturnAdmitsExactlyOnce(t *testing.T) {
	f := newDefectEReturnFixture(t)
	f.assertAdmission(t, false)
	f.observed = f.ended
	f.observe()
	f.assertAdmission(t, false)
	f.observed = f.ended.Add(time.Second)
	before, actions := slices.Clone(f.parent.result.Before.Instances), slices.Clone(f.parent.result.Operation.Target.Actions)
	f.observe()
	f.assertAdmission(t, true)
	if f.child.reserved || len(f.child.scope.Nodes)+len(f.child.scope.Pods)+len(f.child.scope.Instances) != 0 ||
		!reflect.DeepEqual(before, f.parent.result.Before.Instances) || !reflect.DeepEqual(actions, f.parent.result.Operation.Target.Actions) {
		t.Fatal("return was not atomic across members, or changed admitted receipts")
	}
	var wg sync.WaitGroup
	admitted := make(chan bool, 2)
	for _, id := range []string{"next-1", "next-2"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			f.c.mu.Lock()
			defer f.c.mu.Unlock()
			admitted <- f.c.reserveRun(id, &actionRun{action: serving.Action{Kind: servingstate.OperationPrepare, Status: servingstate.NativeOperationRunning}}, servingstate.Operation{Kind: servingstate.OperationPrepare, Node: f.a, Instance: id})
		}(id)
	}
	wg.Wait()
	count := 0
	for range 2 {
		if <-admitted {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("same-node admissions=%d, want exactly one", count)
	}
}

// T5/T9: policy writers may have no action.Node; resize can touch other nodes.
func TestDefectEReturnRespectsWriterFootprintsAndPower(t *testing.T) {
	for _, variant := range []string{"parent-running", "parent-canceling", "route-running", "route-canceling", "resize", "power-running", "power-canceling", "power-pending", "independent-power", "unknown-writer", "late-receipt", "wrong-node-uid", "unknown-association"} {
		t.Run(variant, func(t *testing.T) {
			f := newDefectEReturnFixture(t)
			want := false
			switch variant {
			case "parent-running":
				f.parent.action.Status = servingstate.NativeOperationRunning
			case "parent-canceling":
				f.parent.action.Status = servingstate.NativeOperationCancel
			case "power-pending":
				f.nodes[0].Labels = map[string]string{chillmeta.PowerModeState: chillmeta.PowerModePending}
			case "wrong-node-uid":
				f.nodes[0].UID += replacementSuffix
			case "unknown-association":
				f.parent.scope.Instances = append(f.parent.scope.Instances, "unassociated")
			case "late-receipt":
				f.child.result.EndedAt = f.observed
			default:
				kind, node, scope := servingstate.OperationRoute, serving.NodeReference{}, resourceScope{Nodes: []serving.NodeReference{f.a}, Routing: []string{"workload"}}
				status := servingstate.NativeOperationRunning
				switch variant {
				case "route-canceling":
					status = servingstate.NativeOperationCancel
				case "resize":
					kind, node = servingstate.OperationResize, f.b
				case "power-running", "power-canceling":
					kind, node = servingstate.OperationPower, f.a
					if variant == "power-canceling" {
						status = servingstate.NativeOperationCancel
					}
				case "independent-power":
					kind, node, scope, want = servingstate.OperationPower, f.b, resourceScope{Nodes: []serving.NodeReference{f.b}}, true
				case "unknown-writer":
					kind, scope = "unknown-write", resourceScope{}
				}
				writer := &actionRun{owner: f.parent.owner, proofParent: f.parent, reserved: true, scope: scope,
					action: serving.Action{ID: "writer", Kind: kind, Node: node, Status: status},
					result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: kind, Node: node, Nodes: scope.Nodes}}}
				f.c.runs[writer.action.ID] = writer
			}
			f.observe()
			if got := f.c.authorize(servingstate.Operation{Kind: servingstate.OperationPrepare, Node: f.a}) == nil; got != want {
				t.Fatalf("healthy returned=%t, want %t", got, want)
			}
			if !f.parent.reserved {
				t.Fatal("terminal/active writer unlocked the owner")
			}
		})
	}
}

// T6/T10: completed-stop cleanup remains authorized after failure, unlike a
// never-started stop, so even fresh absence cannot release its future write.
func TestDefectEReturnWaitsForAllowedFutureCleanup(t *testing.T) {
	f := newDefectEReturnFixture(t)
	stop := serving.Action{ID: "completed-stop", Kind: servingstate.OperationStop, Node: f.a, Instance: "removed/container", Status: servingstate.NativeOperationComplete}
	drain := serving.Action{ID: "future-drain", Kind: servingstate.OperationDrain, Node: f.a, Instance: stop.Instance, DependsOn: []string{stop.ID}}
	remove := serving.Action{ID: "future-remove", Kind: servingstate.OperationRemove, Node: f.a, Instance: stop.Instance, DependsOn: []string{drain.ID}}
	f.parent.result.Operation.Target.Actions = append(f.parent.result.Operation.Target.Actions, stop, drain, remove)
	f.c.runs[stop.ID] = &actionRun{owner: f.parent.owner, proofParent: f.parent, action: stop,
		result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: stop.Kind, Node: f.a}, Status: servingstate.NativeOperationComplete, EndedAt: f.ended}}
	f.observe()
	f.assertAdmission(t, false)
	for _, action := range []serving.Action{drain, remove} {
		action.Status = servingstate.NativeOperationComplete
		f.c.runs[action.ID] = &actionRun{owner: f.parent.owner, proofParent: f.parent, action: action,
			result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: action.Kind, Node: action.Node}, Status: action.Status, EndedAt: f.ended}}
	}
	f.observe()
	f.assertAdmission(t, true)
	var dispatched []serving.Action
	f.c.continueTransitionsLocked(serving.SchedulerSettings{}, false, func(target serving.SchedulerCandidate) { dispatched = append(dispatched, target.Actions...) })
	if slices.ContainsFunc(dispatched, func(action serving.Action) bool {
		return action.ID == "never-start-stop" || action.Kind == servingstate.OperationRoute || action.Kind == servingstate.OperationPrepare
	}) {
		t.Fatal("partial return resurrected failed owner's unstarted work")
	}
}

// T7/T11: absence of the removed sibling does not prove an unconfirmed survivor.
func TestDefectEReturnRequiresExactSurvivorProof(t *testing.T) {
	const nativeUnready, registered, untouched = "native-unready", "registered", "untouched"
	const stale, containerChanged, podChanged, nodeChanged = "stale", "container", "changed-pod-uid", "node-identity"
	const otherOwner, policyChanged, profileChanged, activeReceipt = "wrong-receipt-owner", "policy", "return-profile-mismatch", "active-receipt"
	for _, variant := range []string{untouched, "dirty", stale, nativeUnready, containerChanged, podChanged, nodeChanged, registered, "unregistered", otherOwner, policyChanged, profileChanged, activeReceipt} {
		t.Run(variant, func(t *testing.T) {
			f := newDefectEReturnFixture(t)
			pod, _, profile, _ := observerSourceFixture()
			pod.UID, pod.Spec.NodeName = "survivor", f.a.Name
			pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", UID: "workload"}}
			id := servingstate.PhysicalID(pod.UID, servingstate.FirstfloorStatus(pod).ContainerID)
			state := serving.InstanceState{ID: id, PodUID: string(pod.UID), Node: f.a, Ready: true, ObservedAt: f.observed,
				Native: &native.State{Ready: true, Metadata: native.Metadata{ProfileID: profile.ID, DecisionID: "refresh", Policy: native.Policy{ProfileID: profile.ID, DecisionID: "refresh", MaxBatchSize: 2}}}}
			f.parent.result.Before.Instances = append(f.parent.result.Before.Instances, state)
			f.parent.scope.Pods = append(f.parent.scope.Pods, string(pod.UID))
			f.parent.scope.Instances = append(f.parent.scope.Instances, id)
			f.parent.mayTouch = map[string]bool{id: variant != untouched && variant != stale && variant != nativeUnready && variant != containerChanged && variant != podChanged && variant != nodeChanged}
			wanted := serving.SchedulerInstance{ID: id, PodUID: string(pod.UID), Node: f.a, WorkloadUID: "workload", Profile: &profile, MaxBatch: 2}
			target := &serving.SchedulerCandidate{ID: "refresh", Instances: []serving.SchedulerInstance{wanted}}
			receipt := &actionRun{owner: f.parent.owner, proofParent: f.parent, scope: resourceScope{Nodes: []serving.NodeReference{f.a}, Pods: []string{string(pod.UID)}, Instances: []string{id}},
				action: serving.Action{Kind: servingstate.OperationRoute, Status: servingstate.NativeOperationComplete},
				result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: servingstate.OperationRoute, WorkloadUID: "workload", Target: target}, EndedAt: f.ended}}
			if variant == registered || variant == otherOwner || variant == policyChanged || variant == profileChanged || variant == activeReceipt {
				f.parent.routeReceipts = map[string]*actionRun{"refresh": receipt}
			}
			switch variant {
			case stale:
				state.ObservedAt = f.ended
			case nativeUnready:
				state.Native.Ready = false
			case containerChanged:
				pod.Status.ContainerStatuses[0].ContainerID += replacementSuffix
			case podChanged:
				pod.UID += replacementSuffix
			case nodeChanged:
				state.Node.UID += replacementSuffix
			case otherOwner:
				receipt.owner = "unrelated-receipt-owner"
			case policyChanged:
				state.Native.Metadata.Policy.MaxBatchSize++
			case profileChanged:
				state.Native.Metadata.ProfileID += "-wrong"
			case activeReceipt:
				receipt.action.Status = servingstate.NativeOperationRunning
			}
			f.pods, f.physical = append(f.pods, *pod), append(f.physical, state)
			f.observe()
			f.assertAdmission(t, variant == untouched || variant == registered)
			if len(f.physical) != 2 {
				t.Fatal("return removed physical inventory")
			}
		})
	}
}

// T5/T11: fresh capture returns a healthy node, real normal admission reaches
// native/runtime, and the failed original owner cannot overwrite the new policy.
func TestDefectEPartialReturnContinuesThroughRealNativeRoute(t *testing.T) {
	for _, lostCompletion := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "lost-completion"}[lostCompletion], func(t *testing.T) {
			c, ready := tickReadySubsetController(t, nil)
			ready.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			c.ctx = ctx
			t.Cleanup(func() { cancel(); c.wg.Wait() })
			snapshot, err := c.capture(ctx, time.Now(), c.config)
			if err != nil {
				t.Fatal(err)
			}
			c.state.Snapshot = snapshot
			uid := snapshot.Workloads[0].UID
			a := snapshot.Instances[0]
			a.WorkloadUID = uid
			held := serving.NodeReference{Name: "still-unconfirmed", UID: "still-unconfirmed-uid"}
			ended := time.Now().Add(-time.Second)
			goal := &serving.SchedulerCandidate{ID: "old-failed", Actions: []serving.Action{{ID: "old-unstarted-route", Kind: servingstate.OperationRoute}}}
			before := c.runtime.Instances()
			parent := &actionRun{owner: goal.ID, reserved: true, started: ended,
				action: serving.Action{ID: transitionRunKind + "/" + goal.ID, Kind: transitionRunKind, Status: servingstate.NativeOperationFailed},
				scope:  resourceScope{Nodes: []serving.NodeReference{a.Node, held}, Owners: []string{uid}, Pods: []string{a.PodUID, "unknown-pod"}, Instances: []string{a.ID, "unknown-pod/container"}},
				result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: transitionRunKind, Target: goal}, Before: servingstate.OperationState{Instances: append(before, serving.InstanceState{ID: "unknown-pod/container", PodUID: "unknown-pod", Node: held})}, EndedAt: ended, Status: servingstate.NativeOperationFailed}}
			c.runs[parent.action.ID] = parent
			snapshot, err = c.capture(ctx, time.Now(), c.config)
			if err != nil {
				t.Fatal(err)
			}
			c.state.Snapshot = snapshot
			if c.authorize(servingstate.Operation{Kind: servingstate.OperationPrepare, Node: a.Node}) != nil || !parent.reserved || parent.returnObserved {
				t.Fatal("fresh capture did not independently return the known node")
			}
			a.MaxWait = 2 * time.Millisecond
			next := serving.SchedulerCandidate{ID: "new-healthy-owner", Instances: []serving.SchedulerInstance{a}, Routing: map[string]map[string]int32{uid: {a.ID: 1000}}, Actions: []serving.Action{{ID: "new-healthy-route", Kind: servingstate.OperationRoute, Node: a.Node}}}
			c.config.OnAction = func(result servingstate.OperationResult) error {
				if lostCompletion && result.Operation.ID == "new-healthy-route" && result.Status == servingstate.NativeOperationComplete {
					return errors.New("lost completion receipt")
				}
				return nil
			}
			c.launch(c.ctx, next, c.config)
			c.wg.Wait()
			child := c.runs["new-healthy-route"]
			if child == nil || child.result == nil {
				t.Fatal("healthy admission did not reach operation dispatcher")
			}
			routing, _ := c.runtime.SchedulerRouting()
			if routing[uid][a.ID] != 1000 {
				t.Fatalf("native route not installed: %v", routing)
			}
			cm := &core.ConfigMap{}
			if err := c.store.client.Get(ctx, client.ObjectKey{Namespace: "chill-system", Name: "a"}, cm); err != nil {
				t.Fatal(err)
			}
			var cfg native.Config
			if err := json.Unmarshal([]byte(cm.Data["config.json"]), &cfg); err != nil {
				t.Fatal(err)
			}
			if cfg.Policy.DecisionID != next.ID || cfg.Policy.MaxBatchWaitMS != 2 {
				t.Fatalf("real native policy was not updated: %+v", cfg.Policy)
			}
			watermark := c.routeWriters[uid][a.ID]
			if watermark.IsZero() {
				t.Fatal("new admission did not register its immutable writer watermark")
			}
			c.mu.Lock()
			delete(c.runs, "new-healthy-route") // action-view collection cannot erase the writer.
			c.mu.Unlock()
			if err := c.Continue(ctx, time.Now()); err != nil {
				t.Fatal(err)
			}
			c.wg.Wait()
			if c.runs["old-unstarted-route"] != nil || !c.routeWriters[uid][a.ID].Equal(watermark) || !parent.reserved {
				t.Fatal("old failed continuation rewrote a new owner or lost residual responsibility")
			}
			after, _ := c.runtime.SchedulerRouting()
			if !reflect.DeepEqual(routing, after) {
				t.Fatalf("old continuation changed newer route: %v", after)
			}
		})
	}
}

// T9: each whole-node power obligation needs both termination and a later,
// settled exact-node observation; absence of every Pod does not substitute.
func TestDefectEPartialPowerNeedsFreshSettledProof(t *testing.T) {
	f := newDefectEReturnFixture(t)
	power := &actionRun{owner: f.parent.owner, proofParent: f.parent, reserved: true,
		scope:  resourceScope{Nodes: []serving.NodeReference{f.a}},
		action: serving.Action{ID: "power", Kind: servingstate.OperationPower, Node: f.a, Status: servingstate.NativeOperationRunning},
		result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: servingstate.OperationPower, Node: f.a}}}
	f.c.runs[power.action.ID] = power
	f.observe()
	f.assertAdmission(t, false)
	power.action.Status, power.result.Status, power.result.EndedAt = servingstate.NativeOperationPartial, servingstate.NativeOperationPartial, f.observed
	f.observe()
	f.assertAdmission(t, false)
	f.observed = f.observed.Add(time.Second)
	f.nodes[0].Labels = map[string]string{chillmeta.PowerModeState: chillmeta.PowerModePending}
	f.observe()
	f.assertAdmission(t, false)
	f.nodes[0].Labels = nil
	f.observe()
	f.assertAdmission(t, true)
	if power.reserved || len(power.scope.Nodes) != 0 || power.result.Status != servingstate.NativeOperationPartial {
		t.Fatal("settled power did not return atomically, or its partial receipt changed")
	}
}

// T7: reservation return does not remove a surviving allocation from the
// physical inventory consumed by normal GPU/memory admission.
func TestDefectEReturnPreservesPhysicalOccupancy(t *testing.T) {
	f := newDefectEReturnFixture(t)
	pod, _, _, _ := observerSourceFixture()
	pod.UID, pod.Spec.NodeName = "survivor", f.a.Name
	id := servingstate.PhysicalID(pod.UID, servingstate.FirstfloorStatus(pod).ContainerID)
	survivor := serving.InstanceState{ID: id, PodUID: string(pod.UID), Node: f.a, Ready: true, ObservedAt: f.observed, Native: &native.State{Ready: true}}
	f.parent.result.Before.Instances = append(f.parent.result.Before.Instances, survivor)
	f.parent.scope.Pods, f.parent.scope.Instances = append(f.parent.scope.Pods, string(pod.UID)), append(f.parent.scope.Instances, id)
	f.pods, f.physical = append(f.pods, *pod), append(f.physical, survivor)
	old := serving.SchedulerAllocation{GPUUUID: "gpu-0", MemoryBytes: 60}
	f.c.state.Snapshot.Instances = []serving.SchedulerInstance{{ID: id, PodUID: string(pod.UID), Node: f.a, Allocation: old}}
	f.observe()
	f.assertAdmission(t, true)
	if len(f.c.state.Snapshot.Instances) != 1 || f.c.state.Snapshot.Instances[0].Allocation != old {
		t.Fatal("reservation return erased surviving physical allocation")
	}
	inventory := serving.SchedulerNode{Node: f.a, MemoryBytes: 100, GPUUUIDs: []string{"gpu-0", "gpu-1"}}
	for _, test := range []struct {
		name    string
		wanted  serving.SchedulerAllocation
		allowed bool
	}{
		{name: "overlapping GPU", wanted: serving.SchedulerAllocation{GPUUUID: "gpu-0", MemoryBytes: 10}},
		{name: "memory overflow", wanted: serving.SchedulerAllocation{GPUUUID: "gpu-1", MemoryBytes: 41}},
		{name: "independent fits", wanted: serving.SchedulerAllocation{GPUUUID: "gpu-1", MemoryBytes: 40}, allowed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			allocations := []serving.SchedulerAllocation{f.c.state.Snapshot.Instances[0].Allocation, test.wanted}
			if reasons := serving.ValidateNodeBudget(inventory, allocations, nil); (len(reasons) == 0) != test.allowed {
				t.Fatalf("physical occupancy consumer reasons=%v, allowed=%t", reasons, test.allowed)
			}
		})
	}
}

// T8: replay a terminal two-node owner with one unresolved prepare through the
// real reservation projection and capacity planner, not a synthetic count.
func TestDefectEPartialReturnKeepsPendingReplicaBudget(t *testing.T) {
	c := handoffController(t)
	base := handoffSnapshot(t, c)
	uid := base.Workloads[0].UID
	for _, variant := range []string{"uncreated", "created-slot", "late-identity", "inventory-loss"} {
		for _, limit := range []int{1, 2} {
			t.Run(variant+string(rune('0'+limit)), func(t *testing.T) {
				snapshot := base
				snapshot.Nodes = slices.Clone(base.Nodes)
				snapshot.Instances, snapshot.Routing = nil, map[string]map[string]int32{uid: {}}
				snapshot.Actions = nil
				a := c.config.Preparations["a-profile"].Instance
				pending := c.config.Preparations["b-profile"].Instance
				pending.ID, pending.WorkloadUID = "unknown-slot", uid
				target := &serving.SchedulerCandidate{ID: "pending-owner", Instances: []serving.SchedulerInstance{pending}}
				ended := snapshot.At.Add(-time.Second)
				parent := &actionRun{owner: target.ID, reserved: true, scope: resourceScope{Nodes: []serving.NodeReference{a.Node, pending.Node}, Owners: []string{uid}},
					action: serving.Action{ID: transitionRunKind + "/" + target.ID, Kind: transitionRunKind, Status: servingstate.NativeOperationFailed},
					result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: transitionRunKind, Target: target}, Status: servingstate.NativeOperationFailed, EndedAt: ended}}
				prepare := &actionRun{owner: parent.owner, proofParent: parent, reserved: true, scope: resourceScope{Nodes: []serving.NodeReference{pending.Node}, Instances: []string{pending.ID}},
					action: serving.Action{ID: "unknown-prepare", Kind: servingstate.OperationPrepare, Node: pending.Node, Instance: pending.ID, Status: servingstate.NativeOperationFailed},
					result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: servingstate.OperationPrepare, Node: pending.Node, Instance: pending.ID, Prepare: &servingstate.PrepareSpec{Instance: pending, Owner: &serving.WorkloadReference{UID: uid}}}, Status: servingstate.NativeOperationFailed, EndedAt: ended}}
				ledger := &reservationState{runs: map[string]*actionRun{parent.action.ID: parent, prepare.action.ID: prepare}}
				nodes := []core.Node{{ObjectMeta: meta.ObjectMeta{Name: a.Node.Name, UID: types.UID(a.Node.UID)}}, {ObjectMeta: meta.ObjectMeta{Name: pending.Node.Name, UID: types.UID(pending.Node.UID)}}}
				ledger.releaseObservedScopes(nodes, nil, nil, snapshot.At)
				if slices.Contains(parent.scope.Nodes, a.Node) || !slices.Contains(parent.scope.Nodes, pending.Node) || !prepare.reserved {
					t.Fatal("unknown creation released pending node, or independent node stayed held")
				}
				snapshot.Reservations = ledger.reservationsLocked()
				snapshot.Reservations = append(snapshot.Reservations, snapshot.Reservations[0])
				if variant == "created-slot" || variant == "late-identity" {
					actual := pending
					actual.ID, actual.TargetID, actual.Ready, actual.WorkloadUID, actual.OwnerWorkloadUID = "pending-pod/container", pending.ID, false, "", uid
					actual.PodUID = "pending-pod"
					if variant == "late-identity" {
						prepare.result.Operation.Prepare.Config.Policy.DecisionID = "pending-policy"
						prepare.result.Preparation = &servingstate.PreparationEvidence{PodUID: actual.PodUID, NodeUID: actual.Node.UID, DecisionID: "pending-policy", Created: true}
						ledger.freezePreparingExecutorsLocked(serving.SchedulerSnapshot{Instances: []serving.SchedulerInstance{actual}})
						if prepare.prepareIdentity != actual.ID || !slices.Contains(parent.scope.Pods, actual.PodUID) {
							t.Fatal("late exact creation identity did not remain linked to residual owner")
						}
					}
					snapshot.Instances = []serving.SchedulerInstance{actual}
				}
				if variant == "inventory-loss" {
					for i := range snapshot.Nodes {
						if snapshot.Nodes[i].Node == pending.Node {
							snapshot.Nodes[i].Reasons = append(slices.Clone(snapshot.Nodes[i].Reasons), serving.NodePlacementInventoryMissing)
						}
					}
				}
				settings := c.config.Settings
				settings.ControlInterval = settings.ObservationWindow
				snapshot.ObservedSince = snapshot.ObservationEnd.Add(-settings.ObservationWindow)
				settings.MaxReplicas, settings.MaxInstances = limit, limit
				settings.MinimumDemand = map[string]float64{uid: 1}
				decision, err := serving.EvaluateScheduler(context.Background(), settings, snapshot)
				if err != nil {
					t.Fatal(err)
				}
				if decision.Capacity == nil || len(decision.Capacity.Plans) == 0 {
					t.Fatalf("planner did not consume fixture: %v", decision.Reasons)
				}
				prepares := 0
				if decision.Target != nil {
					for _, action := range decision.Target.Actions {
						if action.Kind == servingstate.OperationPrepare {
							prepares++
						}
					}
				}
				if prepares != limit-1 {
					t.Fatalf("pending=1 maxReplicas/instances=%d independent prepares=%d reasons=%v", limit, prepares, decision.Reasons)
				}
				for _, plan := range decision.Capacity.Plans {
					if plan.Node == pending.Node && plan.Key == decision.Keep.ID && (plan.RoutingRate[uid] != 0 || plan.RetainedRoutingRate[uid] != 0) {
						t.Fatal("held pending executor supplied invented routing weight")
					}
				}
			})
		}
	}
}
