package kube

import (
	"context"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
)

func TestCatalogRemovalDistinguishesCandidatesFromFrozenExecution(t *testing.T) {
	profile := &serving.Profile{ID: "chill-system/unused-gpu/uid"}
	candidate := serving.SchedulerCandidate{
		ID: "prospective-gpu", Instances: []serving.SchedulerInstance{{ID: "gpu-slot", Profile: profile}},
		Actions: []serving.Action{{ID: "prepare-gpu", Kind: operationPrepare, Status: "planned"}},
	}
	controller := &SchedulerController{
		state: SchedulerControllerState{
			Snapshot: serving.SchedulerSnapshot{Options: []serving.SchedulerOption{{
				Instance: candidate.Instances[0],
			}}},
			Decision: serving.SchedulerDecision{Target: &candidate, Candidates: []serving.SchedulerCandidate{candidate}},
		},
		runs: map[string]*actionRun{},
	}
	if reason := controller.CatalogRemovalBlocker(); reason != "" {
		t.Fatalf("prospective catalog placement blocked removal: %s", reason)
	}
	// A goal can be frozen before its dependencies permit any action to start.
	controller.goal = &candidate
	if reason := controller.CatalogRemovalBlocker(); reason == "" {
		t.Fatal("pending frozen goal permitted catalog removal")
	}
	controller.goal = nil
	// Live run targets precede the next snapshot and final Results publication.
	action := candidate.Actions[0]
	action.Status = nativeOperationRunning
	controller.runs[action.ID] = &actionRun{
		action: action, result: &OperationResult{Operation: Operation{Target: &candidate}, Status: "starting"},
	}
	if reason := controller.CatalogRemovalBlocker(); reason == "" {
		t.Fatal("running frozen operation permitted catalog removal before its next snapshot")
	}
	controller.runs[action.ID].action.Status = nativeOperationCancel
	if reason := controller.CatalogRemovalBlocker(); reason == "" {
		t.Fatal("canceling preparation permitted catalog removal before termination")
	}
	controller.runs[action.ID].action.Status = nativeOperationComplete
	if reason := controller.CatalogRemovalBlocker(); reason != "" {
		t.Fatalf("completed historical operation blocked removal: %s", reason)
	}
}

func acknowledgedHandoffFixture(t *testing.T) (*SchedulerController, serving.SchedulerCandidate, serving.ReadyInstance) {
	t.Helper()
	ctx := context.Background()
	manager, _ := newManagerHarness(t)
	createManagerCatalog(t, manager)
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

func TestRunningGlobalRouteSerializesDifferentTargetAndKeepsFrozenActualContext(t *testing.T) {
	controller, target, _ := acknowledgedHandoffFixture(t)
	var existing *actionRun
	for _, run := range controller.runs {
		existing = run
	}
	existing.action.Status, existing.action.To = nativeOperationRunning, "frozen-route-effect"
	existing.started = time.Now()
	frozen := existing.result.Operation.Target
	frozen.Actions = []serving.Action{existing.action}
	target.Actions[0].To = "different-route-effect"
	controller.goal = frozen
	controller.launch(serving.SchedulerCandidate{ID: "keep", Keep: true}, SchedulerControllerConfig{})
	if controller.goal != frozen {
		t.Fatal("Keep erased the unfinished operation target")
	}
	controller.launch(target, SchedulerControllerConfig{})
	if _, launched := controller.runs[target.Actions[0].ID]; launched {
		t.Fatal("a different global route overwrote the policy path before the existing native ACK")
	}
	snapshot := controller.reconcileSnapshot(controller.state.Snapshot, controller.goal, time.Now(), time.Minute)
	if snapshot.InProgressTarget != frozen || snapshot.InProgressTarget.ID == target.ID {
		t.Fatalf("actual running route was relabeled as the newly selected goal: %+v", snapshot.InProgressTarget)
	}
	controller.goal = nil
	snapshot = controller.reconcileSnapshot(controller.state.Snapshot, nil, time.Now(), time.Minute)
	if snapshot.InProgressTarget != frozen {
		t.Fatal("available frozen operation target was lost when the selected goal was absent")
	}
}

func TestIndependentPreparationRecordsFrozenTargetBeforeCompletionDuringGlobalRoute(t *testing.T) {
	controller, target, _ := acknowledgedHandoffFixture(t)
	for _, run := range controller.runs {
		run.action.Status = nativeOperationRunning
		run.started = time.Now()
		run.result.Operation.Target.Actions = []serving.Action{run.action}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controller.ctx = ctx
	preparation := serving.Action{ID: "independent-node-prepare", Kind: operationPrepare, Node: target.Instances[0].Node, Instance: target.Instances[0].ID}
	target.Actions = []serving.Action{preparation}
	started := make(chan OperationResult, 1)
	release := make(chan struct{})
	config := SchedulerControllerConfig{OnAction: func(result OperationResult) error {
		if result.Status == "starting" {
			started <- result
			<-release
			return context.Canceled // Exercise the real pre-execution journal failure path.
		}
		return nil
	}}
	controller.launch(target, config)
	var initial OperationResult
	select {
	case initial = <-started:
	case <-time.After(5 * time.Second):
		close(release)
		controller.wg.Wait()
		t.Fatal("global route unnecessarily serialized an independent Node preparation")
	}
	controller.mu.Lock()
	run := controller.runs[preparation.ID]
	frozen := run.result.Operation.Target
	recorded := run.result.Status == "starting" && frozen != nil && frozen.ID == target.ID &&
		initial.Operation.Target == frozen && run.action.Status == nativeOperationRunning
	controller.mu.Unlock()
	close(release)
	controller.wg.Wait()
	if !recorded {
		t.Fatal("active preparation lacked its actual frozen target until terminal completion")
	}
}

func TestFailedGoalCleanupRetainsRunningRouteFrozenTargetAndAgedPrerequisite(t *testing.T) {
	controller, failedGoal, _ := acknowledgedHandoffFixture(t)
	now := time.Now().UTC()
	var frozen *serving.SchedulerCandidate
	for _, run := range controller.runs {
		frozen = run.result.Operation.Target
	}
	removed := serving.Action{
		ID: "aged-route-prerequisite", Kind: operationRemove, Node: frozen.Instances[0].Node,
		Instance: "retired-physical-source", Status: nativeOperationComplete,
	}
	routing := serving.Action{
		ID: "still-running-frozen-route", Kind: operationRoute, Status: nativeOperationRunning,
		DependsOn: []string{removed.ID},
	}
	failed := serving.Action{
		ID: "failed-preparation-journal", Kind: operationPrepare, Status: nativeOperationFailed,
		Node: failedGoal.Instances[0].Node, Instance: failedGoal.Instances[0].ID,
	}
	frozen.Actions = []serving.Action{removed, routing}
	failedGoal.Actions = []serving.Action{failed}
	frozen.Actions[1].Status = "planned"
	failedGoal.Actions[0].Status = "planned"
	controller.goal = &failedGoal
	controller.runs = map[string]*actionRun{
		removed.ID: {
			action: removed, started: now.Add(-3 * time.Minute),
			result: &OperationResult{
				Operation: Operation{ID: removed.ID, Kind: removed.Kind, Node: removed.Node, Instance: removed.Instance},
				Status:    nativeOperationComplete, StartedAt: now.Add(-3 * time.Minute), EndedAt: now.Add(-2 * time.Minute),
			},
		},
		routing.ID: {
			action: routing, started: now.Add(-10 * time.Second),
			result: &OperationResult{
				Operation: Operation{ID: routing.ID, Kind: routing.Kind, Target: frozen},
				Status:    "starting", StartedAt: now.Add(-10 * time.Second),
			},
		},
		failed.ID: {
			action: failed, started: now.Add(-5 * time.Second),
			result: &OperationResult{
				Operation: Operation{ID: failed.ID, Kind: failed.Kind, Node: failed.Node, Instance: failed.Instance, Target: &failedGoal},
				Status:    nativeOperationFailed, StartedAt: now.Add(-5 * time.Second), EndedAt: now.Add(-4 * time.Second),
				Error: "pre-execution preparation journal unavailable",
			},
		},
	}
	snapshot := controller.reconcileSnapshot(controller.state.Snapshot, controller.goal, now, time.Minute)
	if controller.goal != nil || snapshot.InProgressTarget != frozen {
		t.Fatalf("failed selected goal erased or relabeled the actual running route: %+v", snapshot.InProgressTarget)
	}
	if controller.runs[removed.ID] == nil || controller.runs[routing.ID].action.Status != nativeOperationRunning {
		t.Fatal("terminal cleanup discarded the live route's aged prerequisite or completed its running operation")
	}
}
