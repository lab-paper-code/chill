package kube

import (
	"context"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
)

func acknowledgedHandoffFixture(t *testing.T) (*SchedulerController, serving.SchedulerCandidate, serving.ReadyInstance) {
	t.Helper()
	ctx := context.Background()
	manager, _ := newManagerHarness(t)
	if _, err := manager.CreateModel(ctx, "model", managerModelSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateWorkload(ctx, "workload", managerWorkloadSpec()); err != nil {
		t.Fatal(err)
	}
	d := manager.runtime.SchedulerDefinitions()[0]
	profile := managerProfile(d)
	profile.NodeName, profile.Condition.NodeUID = "handoff-destination", "destination-uid"
	profile.RuntimeMetadata = &native.RuntimeMetadata{CPUAffinity: []int{0}, IntraOpThreads: 1}
	node := serving.NodeReference{Name: profile.NodeName, UID: profile.Condition.NodeUID}
	wanted := serving.SchedulerInstance{
		ID: "destination-physical", Node: node, Pool: "pool", Profile: &profile,
		Allocation:   serving.SchedulerAllocation{CPUs: []int{0}, Threads: 1, MemoryBytes: 1024},
		WorkloadUIDs: []string{d.UID}, Ready: true, Capacity: 8, Weight: 1, MaxBatch: 1,
	}
	at := time.Now().UTC()
	measure := func(request string, extra time.Duration) {
		for _, event := range []serving.RequestEvent{
			{RequestID: request, WorkloadUID: d.UID, Definition: d.Identity(), Kind: "backend_exchange_finished", Outcome: "completed", At: at, ReceivedAt: at,
				Assignment:     &serving.Assignment{Instance: wanted.ID, Node: node, Profile: profile.ID, Artifact: profile.Condition.Artifact.Name, ExecutionPath: profile.Condition.Artifact.ExecutionPath},
				NativeProgress: &serving.BackendProgress{Phase: "finished", ProfileID: profile.ID, ExecutionComplete: true, NativeElapsedMS: 6}, ClientSpanNanos: int64(10 * time.Millisecond), ElapsedNanos: int64(20*time.Millisecond + extra)},
			{RequestID: request, Kind: "response_finished", Outcome: "completed", At: at, ReceivedAt: at, ElapsedNanos: int64(23*time.Millisecond + extra)},
		} {
			if err := manager.journal.RequestEvent(event); err != nil {
				t.Fatal(err)
			}
		}
		observed, err := manager.journal.ObservedOverhead(at.Add(-time.Second), at.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		observeOverhead(&wanted, observed)
		if !wanted.CompletionObserved {
			t.Fatal("actual completed trial did not produce a compatible typed completion proof")
		}
	}
	measure("acknowledged-trial", 0)
	oldTarget := serving.SchedulerCandidate{ID: "old-target", Instances: []serving.SchedulerInstance{wanted}, Routing: map[string]map[string]int32{d.UID: {wanted.ID: 1}}}
	physical := serving.ReadyInstance{
		State: serving.InstanceState{ID: wanted.ID, Node: node, Pool: wanted.Pool, Profile: profile.ID, Ready: true, Native: &native.State{Ready: true, Metadata: native.Metadata{
			ProfileID: profile.ID, Runtime: *profile.RuntimeMetadata, MaxQueueRequests: wanted.Capacity, MaxBatchSize: wanted.MaxBatch,
			Policy: native.Policy{ProfileID: profile.ID, MaxBatchSize: wanted.MaxBatch, CompletionOverheadMS: float64(wanted.CompletionOverhead) / float64(time.Millisecond), DeadlinePolicy: "keep"},
		}}},
		Profile: &profile, Weight: 1, Capacity: wanted.Capacity,
		Execute: func(context.Context, serving.Dispatch) (serving.BackendResult, error) {
			return serving.BackendResult{Finished: true}, nil
		},
	}
	manager.runtime.SetProfiles([]serving.Profile{profile})
	if err := manager.runtime.InstallInstances([]serving.ReadyInstance{physical}); err != nil {
		t.Fatal(err)
	}
	if err := manager.runtime.ApplySchedulerTarget(oldTarget); err != nil {
		t.Fatal(err)
	}
	controller := &SchedulerController{runtime: manager.runtime, runs: map[string]*actionRun{
		"acknowledged-route": {
			action: serving.Action{ID: "acknowledged-route", Kind: operationRoute, Status: nativeOperationComplete},
			result: &OperationResult{Operation: Operation{Target: &oldTarget}, Status: nativeOperationComplete},
		},
	}}
	controller.state.Snapshot.Instances = []serving.SchedulerInstance{wanted}
	measure("new-quantity-trial", time.Millisecond)
	newTarget := serving.SchedulerCandidate{ID: "updated-target", Instances: []serving.SchedulerInstance{wanted}, Routing: oldTarget.Routing, Actions: []serving.Action{
		{ID: "updated-policy-route", Kind: operationRoute, Status: "planned"},
		{ID: "stop-unrouted-source", Kind: operationStop, Instance: "old-source", DependsOn: []string{"updated-policy-route"}},
	}}
	return controller, newTarget, physical
}

func TestAcknowledgedExistingHandoffDoesNotStarveCleanupOnPolicyQuantityRefresh(t *testing.T) {
	controller, target, _ := acknowledgedHandoffFixture(t)
	if !controller.dependenciesReady(target.Actions[0], target) {
		t.Fatal("changed consumed policy quantity must still schedule its own route application")
	}
	if !controller.dependenciesReady(target.Actions[1], target) {
		t.Fatal("an acknowledged ready/profile/allocation-matched actual handoff should allow old-source stop independently of a refreshed policy route")
	}
}

func TestHandoffCleanupRequiresExistingAcknowledgedReadyEffectNotJustGatewayMap(t *testing.T) {
	for _, missing := range []string{"completed run", "native readiness", "profile identity", "allocation", "policy ack", "typed proof", "observed flag", "routing", "physical destination"} {
		t.Run(missing, func(t *testing.T) {
			controller, target, physical := acknowledgedHandoffFixture(t)
			switch missing {
			case "completed run":
				delete(controller.runs, "acknowledged-route")
			case "native readiness":
				physical.State.Native.Ready = false
			case "profile identity":
				physical.State.Native.Metadata.ProfileID = "another-profile"
			case "allocation":
				controller.state.Snapshot.Instances[0].Allocation.MemoryBytes++
			case "policy ack":
				physical.State.Native.Metadata.Policy.CompletionOverheadMS++
			case "typed proof":
				target.Instances[0].CompletionMeasurement = nil
			case "observed flag":
				target.Instances[0].CompletionObserved = false
			case "routing":
				target.Routing = map[string]map[string]int32{target.Instances[0].WorkloadUIDs[0]: {target.Instances[0].ID: 2}}
			case "physical destination":
				target.Instances[0].ID = "another-physical-slot"
			}
			if err := controller.runtime.InstallInstances([]serving.ReadyInstance{physical}); err != nil {
				t.Fatal(err)
			}
			if controller.dependenciesReady(target.Actions[1], target) {
				t.Fatalf("cleanup bypassed missing %s safeguard merely because a gateway route/old record existed", missing)
			}
		})
	}
}

func TestUnboundRuntimeAndSchedulerCaptureRetainVerifiedOwnerAndSlotFacts(t *testing.T) {
	controller, target, physical := acknowledgedHandoffFixture(t)
	uid := target.Instances[0].WorkloadUIDs[0]
	physical.OwnerWorkloadUID = uid
	physical.State.OwnerWorkloadUID = "not-the-runtime-owner-authority"
	physical.State.TargetID = "verified-native-slot"
	if err := controller.runtime.InstallInstances([]serving.ReadyInstance{physical}); err != nil {
		t.Fatal(err)
	}
	empty := target
	empty.Routing = map[string]map[string]int32{uid: {}}
	if err := controller.runtime.ApplySchedulerTarget(empty); err != nil {
		t.Fatal(err)
	}
	var unbound *serving.InstanceState
	for _, pool := range controller.runtime.Pools() {
		for index := range pool.Instances {
			if pool.Instances[index].ID == physical.State.ID {
				unbound = &pool.Instances[index]
			}
		}
	}
	if unbound == nil || len(unbound.Workloads) != 0 || unbound.OwnerWorkloadUID != uid || unbound.TargetID != physical.State.TargetID {
		t.Fatalf("drained routing bindings discarded or invented ownership/slot provenance: %+v", unbound)
	}
	pod, node, profile, _ := observerSourceFixture()
	status := firstfloorStatus(pod)
	id := physicalID(pod.UID, status.ContainerID)
	state := serving.InstanceState{
		ID: id, TargetID: unbound.TargetID, OwnerWorkloadUID: unbound.OwnerWorkloadUID,
		Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Pool: "pool", Profile: profile.ID, Ready: true,
		Native: &native.State{Ready: true, Metadata: native.Metadata{ProfileID: profile.ID, Runtime: *profile.RuntimeMetadata}},
	}
	pod.Annotations[ServingSlotAnnotation] = state.TargetID
	snapshot := serving.SchedulerSnapshot{}
	sn := serving.SchedulerNode{Node: state.Node, Pool: state.Pool, Ready: true}
	captureNodePods(&snapshot, &sn, node, &core.PodList{Items: []core.Pod{*pod}},
		map[string]serving.InstanceState{id: state}, map[string]*serving.Profile{profile.ID: &profile})
	if len(snapshot.Instances) != 1 || len(snapshot.Instances[0].WorkloadUIDs) != 0 ||
		snapshot.Instances[0].OwnerWorkloadUID != uid || snapshot.Instances[0].TargetID != state.TargetID {
		t.Fatalf("scheduler capture inferred ownerlessness from empty bindings: %+v", snapshot.Instances)
	}
}
