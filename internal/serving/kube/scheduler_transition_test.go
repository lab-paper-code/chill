package kube

import (
	"context"
	"slices"
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
	createManagerCatalog(t, manager)
	if _, err := manager.CreateWorkload(ctx, "workload", managerWorkloadSpec()); err != nil {
		t.Fatal(err)
	}
	d := manager.runtime.SchedulerInputs(time.Now).Workloads[0]
	profile := managerProfile(d)
	profile.NodeName, profile.Condition.NodeUID = "handoff-destination", "destination-uid"
	profile.RuntimeMetadata = &native.RuntimeMetadata{CPUAffinity: []int{0}, IntraOpThreads: 1}
	node := serving.NodeReference{Name: profile.NodeName, UID: profile.Condition.NodeUID}
	wanted := serving.SchedulerInstance{
		ID: "destination-physical", Node: node, Profile: &profile,
		Allocation:  serving.SchedulerAllocation{CPUs: []int{0}, Threads: 1, MemoryBytes: 1024},
		WorkloadUID: d.UID, Ready: true, Capacity: 8, Weight: 1, MaxBatch: 1,
	}
	oldTarget := serving.SchedulerCandidate{ID: "old-target", Instances: []serving.SchedulerInstance{wanted}, Routing: map[string]map[string]int32{d.UID: {wanted.ID: 1}}}
	physical := serving.ReadyInstance{
		State: serving.InstanceState{ID: wanted.ID, Node: node, Profile: profile.ID, Ready: true, Native: &native.State{Ready: true, Metadata: native.Metadata{
			ProfileID: profile.ID, Runtime: *profile.RuntimeMetadata, MaxQueueRequests: wanted.Capacity, MaxBatchSize: wanted.MaxBatch,
			Policy: native.Policy{ProfileID: profile.ID, MaxBatchSize: wanted.MaxBatch, DeadlinePolicy: "keep"},
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
	controller := &SchedulerController{capacity: serving.NewCapacityTable(1, nil, nil), runtime: manager.runtime, observationLog: manager.observationLog, runs: map[string]*actionRun{
		"acknowledged-route": {
			action: serving.Action{ID: "acknowledged-route", Kind: operationRoute, Status: nativeOperationComplete},
			result: &OperationResult{Operation: Operation{Target: &oldTarget}, Status: nativeOperationComplete},
		},
	}}
	controller.state.Snapshot.Instances = []serving.SchedulerInstance{wanted}
	newTarget := serving.SchedulerCandidate{ID: "updated-target", Instances: []serving.SchedulerInstance{wanted}, Routing: oldTarget.Routing, Actions: []serving.Action{
		{ID: "updated-policy-route", Kind: operationRoute, Status: "planned"},
		{ID: "stop-unrouted-source", Kind: operationStop, Instance: "old-source", DependsOn: []string{"updated-policy-route"}},
	}}
	return controller, newTarget, physical
}

func TestAcknowledgedExistingHandoffDoesNotStarveCleanupOnPolicyRefresh(t *testing.T) {
	controller, target, _ := acknowledgedHandoffFixture(t)
	if !controller.dependenciesReady(target.Actions[0], target) {
		t.Fatal("a refreshed policy route must still schedule its own route application")
	}
	if !controller.dependenciesReady(target.Actions[1], target) {
		t.Fatal("an acknowledged ready/profile/allocation-matched actual handoff should allow old-source stop independently of a refreshed policy route")
	}
}

func TestHandoffCleanupRequiresExistingAcknowledgedReadyEffectNotJustGatewayMap(t *testing.T) {
	for _, missing := range []string{"completed run", "native readiness", "profile identity", "allocation", "policy ack", "fixed batch ack", "routing", "physical destination"} {
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
				physical.State.Native.Metadata.Policy.MaxBatchSize++
			case "fixed batch ack":
				physical.State.Native.Metadata.Policy.FixedBatchSize = 1
			case "routing":
				target.Routing = map[string]map[string]int32{target.Instances[0].WorkloadUID: {target.Instances[0].ID: 2}}
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
	uid := target.Instances[0].WorkloadUID
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
	for _, instance := range controller.runtime.Instances() {
		if instance.ID == physical.State.ID {
			unbound = &instance
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
		Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Profile: profile.ID, Ready: true,
		Native: &native.State{Ready: true, Metadata: native.Metadata{ProfileID: profile.ID, Runtime: *profile.RuntimeMetadata}},
	}
	pod.Annotations[ServingSlotAnnotation] = state.TargetID
	snapshot := serving.SchedulerSnapshot{}
	sn := serving.SchedulerNode{Node: state.Node, Ready: true}
	captureNodePods(&snapshot, &sn, node, &core.PodList{Items: []core.Pod{*pod}},
		map[string]serving.InstanceState{id: state}, map[string]*serving.Profile{profile.ID: &profile})
	if len(snapshot.Instances) != 1 || snapshot.Instances[0].WorkloadUID != "" ||
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
	snapshot, _ := controller.reconcileSnapshot(controller.state.Snapshot, controller.goal, time.Now(), time.Minute)
	if snapshot.InProgressTarget != frozen || snapshot.InProgressTarget.ID == target.ID {
		t.Fatalf("actual running route was relabeled as the newly selected goal: %+v", snapshot.InProgressTarget)
	}
	controller.goal = nil
	snapshot, _ = controller.reconcileSnapshot(controller.state.Snapshot, nil, time.Now(), time.Minute)
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
			return context.Canceled // Exercise the real pre-execution observation log failure path.
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
		ID: "failed-preparation-observation-log", Kind: operationPrepare, Status: nativeOperationFailed,
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
				Error: "pre-execution preparation observation log unavailable",
			},
		},
	}
	snapshot, _ := controller.reconcileSnapshot(controller.state.Snapshot, controller.goal, now, time.Minute)
	if controller.goal != nil || snapshot.InProgressTarget != frozen {
		t.Fatalf("failed selected goal erased or relabeled the actual running route: %+v", snapshot.InProgressTarget)
	}
	if controller.runs[removed.ID] == nil || controller.runs[routing.ID].action.Status != nativeOperationRunning {
		t.Fatal("terminal cleanup discarded the live route's aged prerequisite or completed its running operation")
	}
}

func TestLaunchPreservesCompletedPreparationAcrossPhysicalContinuationAndEffectLoss(t *testing.T) {
	controller, acknowledged, physical := acknowledgedHandoffFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	controller.ctx, controller.cancel = ctx, cancel
	t.Cleanup(controller.Close)
	const (
		slot     = "prepared-slot"
		planned  = "planned"
		starting = "starting"
	)
	physical.State.TargetID = slot
	if err := controller.runtime.InstallInstances([]serving.ReadyInstance{physical}); err != nil {
		t.Fatal(err)
	}
	wanted := acknowledged.Instances[0]
	wanted.ID, wanted.Ready = slot, false
	action := serving.Action{ID: "finished-prepare", Kind: operationPrepare, Node: wanted.Node, Instance: slot,
		From: "unloaded", To: serving.SchedulerInstanceState(wanted), Status: nativeOperationRunning, Cancelable: true}
	goal := serving.SchedulerCandidate{ID: "preparing-target", Instances: []serving.SchedulerInstance{wanted},
		Routing: map[string]map[string]int32{wanted.WorkloadUID: {slot: 1}}, Actions: []serving.Action{action}}
	config := SchedulerControllerConfig{Preparations: map[string]PrepareSpec{wanted.Profile.ID: {}}}
	operation := scheduledOperation(action, &goal, config, nil, nil, time.Now())
	canceled := false
	run := &actionRun{action: action, result: &OperationResult{Operation: operation, Status: nativeOperationRunning},
		cancel: func() { canceled = true }}
	controller.runs = map[string]*actionRun{action.ID: run}
	controller.goal = &goal
	observed := acknowledged.Instances[0]
	observed.TargetID = slot
	snapshot, _ := controller.reconcileSnapshot(serving.SchedulerSnapshot{
		At: time.Now().UTC(), Instances: []serving.SchedulerInstance{observed},
	}, &goal, time.Now().UTC(), time.Minute)
	// The planner continues a running goal as keep with the goal's instances,
	// each resolved to its captured physical identity.
	continued := goal
	continued.Keep, continued.Actions = true, snapshot.Actions
	continued.Instances = []serving.SchedulerInstance{goal.Instances[0]}
	continued.Instances[0].ID, continued.Instances[0].TargetID = physical.State.ID, slot
	decision := serving.SchedulerDecision{Target: &continued}
	if decision.Target.Instances[0].ID != physical.State.ID ||
		decision.Target.Actions[0].Instance != slot || decision.Target.Actions[0].Status != nativeOperationRunning {
		t.Fatal("fixture did not preserve the captured running logical action in its physical continuation", decision.Target)
	}
	// Publish completion only after capturing the old running action, just as a
	// real executor can finish while the controller evaluates that snapshot.
	publish := make(chan struct{})
	published := make(chan struct{})
	receipt := OperationResult{Operation: operation, Status: nativeOperationComplete, EndedAt: time.Now().UTC(),
		Actual: OperationState{Node: wanted.Node, Instances: []serving.InstanceState{physical.State}}}
	controller.wg.Add(1)
	go func() {
		defer controller.wg.Done()
		select {
		case <-publish:
		case <-ctx.Done():
			return
		}
		controller.mu.Lock()
		run.action.Status, run.result = nativeOperationComplete, &receipt
		controller.state.Results = append(controller.state.Results, receipt)
		controller.mu.Unlock()
		close(published)
	}()
	close(publish)
	<-published
	controller.wg.Wait()
	if !controller.actionEffectPresent(snapshot, run) {
		t.Fatal("completed receipt lacks its existing consumer-visible Ready physical effect")
	}
	starts := 0
	config.OnAction = func(result OperationResult) error {
		if result.Status == starting {
			starts++
			return context.Canceled // Trap an erroneous second launch before any physical write.
		}
		return nil
	}
	controller.launch(*decision.Target, config)
	controller.wg.Wait()
	staleCancel := *decision.Target
	staleCancel.Actions = append([]serving.Action(nil), staleCancel.Actions...)
	staleCancel.Actions[0].Status = nativeOperationCancel
	controller.launch(staleCancel, config)
	if starts != 0 || canceled || controller.runs[action.ID] != run || run.result != &receipt ||
		run.action.Status != nativeOperationComplete || len(controller.State().Results) != 1 {
		t.Fatalf("stale target replayed/canceled or overwrote the completed receipt: starts=%d canceled=%v results=%+v",
			starts, canceled, controller.State().Results)
	}
	if !controller.actionEffectPresent(snapshot, run) {
		t.Fatal("stale continuation changed the completed physical effect")
	}
	if err := controller.runtime.InstallInstances(nil); err != nil {
		t.Fatal(err)
	}
	requireLostEffectFailsGoal(t, controller, action.ID)
	fresh := goal
	fresh.ID = "after-physical-loss"
	fresh.Actions = []serving.Action{action}
	fresh.Actions[0].ID, fresh.Actions[0].Status = "fresh-prepare", planned
	started := make(chan OperationResult, 1)
	release := make(chan struct{})
	config.OnAction = func(result OperationResult) error {
		if result.Status == starting {
			started <- result
			select {
			case <-release:
			case <-ctx.Done():
			}
			return context.Canceled // Existing pre-execution observation log failure fixture path.
		}
		return nil
	}
	controller.launch(fresh, config)
	select {
	case initial := <-started:
		controller.mu.Lock()
		current := controller.runs[fresh.Actions[0].ID]
		valid := current != nil && current.action.Status == nativeOperationRunning &&
			initial.Operation.Prepare != nil && initial.Operation.Instance == slot
		controller.mu.Unlock()
		close(release)
		controller.wg.Wait()
		if !valid {
			t.Fatal("fresh identity did not reach the real launch path after effect loss")
		}
	case <-time.After(5 * time.Second):
		cancel()
		controller.wg.Wait()
		t.Fatal("completed identity blacklisted a fresh action for the same logical slot")
	}
}

// requireLostEffectFailsGoal checks m4 r2: the prepared instance died after its
// prepare completed. The goal fails with that action instead of preparing it
// again under TransitionInProgress, and the failure expires with the window.
func requireLostEffectFailsGoal(t *testing.T, controller *SchedulerController, id string) {
	t.Helper()
	now := time.Now().UTC()
	snapshot, lost := controller.reconcileSnapshot(serving.SchedulerSnapshot{At: now}, controller.goal, now, time.Minute)
	if controller.goal != nil || snapshot.InProgressTarget != nil || controller.runs[id].action.Status != nativeOperationFailed ||
		len(lost) != 1 || lost[0].Operation.ID != id || lost[0].Status != nativeOperationFailed ||
		!slices.ContainsFunc(snapshot.Actions, func(a serving.Action) bool { return a.ID == id && a.Status == nativeOperationFailed }) {
		t.Fatalf("lost prepared effect kept the goal: goal %v lost %+v actions %+v", controller.goal, lost, snapshot.Actions)
	}
	controller.reconcileSnapshot(serving.SchedulerSnapshot{}, nil, now.Add(2*time.Minute), time.Minute)
	if controller.runs[id] != nil {
		t.Fatal("failed action outlived the observation window")
	}
}
