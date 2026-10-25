package kube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const (
	starting               = "starting"
	slowPowerActionID      = "slow-power"
	finalRouteActionID     = "final-route"
	slowNodeName           = "slow"
	raisedPowerMode        = "max"
	policyAckPendingReason = "PolicyAckPending"
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
		Profile: &profile, Capacity: wanted.Capacity,
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
		if result.Status == starting {
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
	recorded := run.result.Status == starting && frozen != nil && frozen.ID == target.ID &&
		initial.Operation.Target == frozen && run.action.Status == nativeOperationRunning
	controller.mu.Unlock()
	close(release)
	controller.wg.Wait()
	if !recorded {
		t.Fatal("active preparation lacked its actual frozen target until terminal completion")
	}
}

func TestFailedGoalCleanupRetainsRunningRouteFrozenTargetAndAgedPrerequisite(t *testing.T) {
	for _, from := range []string{"", readySubsetRoute} {
		t.Run("from="+from, func(t *testing.T) {
			assertFailedGoalRetainsRunningRoute(t, from)
		})
	}
}

func assertFailedGoalRetainsRunningRoute(t *testing.T, from string) {
	t.Helper()
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
		From:      from,
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
				Status:    starting, StartedAt: now.Add(-10 * time.Second),
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
	if !slices.ContainsFunc(snapshot.Actions, func(a serving.Action) bool { return a.ID == routing.ID }) {
		t.Fatal("running route disappeared from planning prerequisites")
	}
	if !controller.conflictingActionRunning(serving.Action{Kind: operationStop, Instance: frozen.Instances[0].ID}) {
		t.Fatal("a new stop could race the running route and be undone by its final apply")
	}
}

func TestLaunchPreservesCompletedPreparationAcrossPhysicalContinuationAndEffectLoss(t *testing.T) {
	controller, acknowledged, physical := acknowledgedHandoffFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	controller.ctx, controller.cancel = ctx, cancel
	t.Cleanup(controller.Close)
	const (
		slot    = "prepared-slot"
		planned = "planned"
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
	operation := scheduledOperation(action, &goal, config, nil, nil)
	canceled := false
	run := &actionRun{action: action, result: &OperationResult{Operation: operation, Status: nativeOperationRunning},
		cancel: func() { canceled = true }, started: time.Now()}
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

// m4 r7: the rasp5 q1 prepare of plan-dc6ebf670833 started at 07:02:05 and
// still waited at 07:12:30, so every decision kept TransitionInProgress.
// A prepare runs under a timeout of its measured time plus one observation
// window. Past it the run fails, the goal is dropped, and a still running
// sibling prepare does not re-adopt it, so the next decision re-plans.
func TestPrepareTimeoutFailsGoal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prepare := serving.Action{ID: "prepare-rasp5-q1", Kind: operationPrepare, Node: serving.NodeReference{Name: "rasp5", UID: "rasp5-uid"}, Instance: "slot"}
	sibling := serving.Action{ID: "prepare-genesis3-q1", Kind: operationPrepare, Node: serving.NodeReference{Name: "genesis3", UID: "genesis3-uid"},
		Instance: "other", Status: nativeOperationRunning}
	goal := serving.SchedulerCandidate{ID: "plan-dc6ebf670833", Actions: []serving.Action{prepare, sibling}}
	controller := &SchedulerController{ctx: ctx, runs: map[string]*actionRun{sibling.ID: {action: sibling, started: time.Now(),
		result: &OperationResult{Operation: Operation{ID: sibling.ID, Kind: sibling.Kind, Target: &goal}, Status: nativeOperationRunning}}}}
	config := SchedulerControllerConfig{Settings: serving.SchedulerSettings{ObservationWindow: time.Millisecond}, OnAction: func(result OperationResult) error {
		if result.Status != nativeOperationFailed {
			time.Sleep(20 * time.Millisecond)
			return context.Canceled // Skip the physical Execute; the deadline has passed.
		}
		return nil
	}}
	controller.launch(goal, config)
	controller.wg.Wait()
	run := controller.runs[prepare.ID]
	if run.action.Status != nativeOperationFailed || !strings.HasPrefix(run.action.Reason, serving.ActionDeadlineExceeded) {
		t.Fatalf("timed out prepare: status %q reason %q", run.action.Status, run.action.Reason)
	}
	now := time.Now()
	for _, selected := range []*serving.SchedulerCandidate{&goal, nil} {
		snapshot, _ := controller.reconcileSnapshot(serving.SchedulerSnapshot{At: now}, selected, now, time.Minute)
		if controller.goal != nil || snapshot.InProgressTarget != nil {
			t.Fatalf("goal kept after prepare timeout: goal %v in progress %v", controller.goal, snapshot.InProgressTarget)
		}
	}
}

func TestReadySubsetRoutesBeforeIndependentPreparationsAndPowerFinish(t *testing.T) {
	controller, target, physical := acknowledgedHandoffFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	target.ID = "expand-with-slow-power"
	uid := target.Instances[0].WorkloadUID
	a := target.Instances[0]
	b := a
	b.ID = "fast-preparation-physical"
	other := physical
	other.State.ID = b.ID
	if err := controller.runtime.InstallInstances([]serving.ReadyInstance{physical, other}); err != nil {
		t.Fatal(err)
	}
	target.Instances = append(target.Instances, b)
	target.Actions = nil
	target.Routing[uid] = map[string]int32{a.ID: 200, b.ID: 200}
	for _, id := range []string{"c", "d", "power-destination"} {
		wanted := a
		wanted.ID = id
		wanted.Node = serving.NodeReference{Name: id, UID: id + "-uid"}
		target.Instances = append(target.Instances, wanted)
		target.Routing[uid][id] = 200
	}
	controller.ctx, controller.runs = ctx, map[string]*actionRun{}
	controller.state.Snapshot.Instances = []serving.SchedulerInstance{a, b}
	controller.state.Snapshot.Workloads = controller.runtime.SchedulerInputs(time.Now).Workloads
	controller.state.Snapshot.Routing = map[string]map[string]int32{uid: {a.ID: 1000}}
	seed := serving.SchedulerCandidate{Instances: []serving.SchedulerInstance{a}, Routing: controller.state.Snapshot.Routing}
	if err := controller.runtime.ApplySchedulerTarget(seed); err != nil {
		t.Fatal(err)
	}
	// b has just completed preparation; c/d and the independent power change
	// remain in progress. No fake completion of those slow actions is required.
	for _, instance := range target.Instances[1:4] {
		action := serving.Action{ID: "prepare-" + instance.ID, Kind: operationPrepare, Instance: instance.ID, Node: instance.Node, Status: nativeOperationRunning}
		if instance.ID == b.ID {
			action.Status = nativeOperationComplete
		}
		target.Actions = append(target.Actions, action)
		controller.runs[action.ID] = &actionRun{action: action}
	}
	power := serving.Action{ID: slowPowerActionID, Kind: operationPower, Node: target.Instances[4].Node, Status: nativeOperationRunning}
	target.Actions = append(target.Actions, power)
	controller.runs[power.ID] = &actionRun{action: power}
	deps := []string{}
	for _, action := range target.Actions {
		deps = append(deps, action.ID)
	}
	target.Actions = append(target.Actions, serving.Action{ID: finalRouteActionID, Kind: operationRoute, DependsOn: deps})
	var recorded []OperationResult
	controller.launch(target, SchedulerControllerConfig{OnAction: func(result OperationResult) error {
		recorded = append(recorded, result)
		// Keep this controller/runtime test independent of Kubernetes/native
		// policy transport (covered by operations_test.go). Freeze the actual
		// scheduled operation, then exercise its atomic runtime route below.
		return context.Canceled
	}})
	controller.wg.Wait()
	for _, result := range recorded {
		if result.Status != starting {
			continue
		}
		if result.Operation.Target == nil || len(result.Operation.Target.Instances) != 2 {
			t.Fatalf("intermediate operation lacks its frozen Ready subset: %+v", result)
		}
		if err := controller.runtime.ApplySchedulerTarget(*result.Operation.Target); err != nil {
			t.Fatal(err)
		}
		routing, _ := controller.runtime.SchedulerRouting()
		if routing[uid][a.ID] != 500 || routing[uid][b.ID] != 500 || len(routing[uid]) != 2 {
			t.Fatalf("Ready destination did not receive the normalized intermediate route: %v", routing)
		}
		if controller.goal.ID != target.ID || len(controller.goal.Instances) != 5 || controller.activeTargetLocked(operationRoute) != nil {
			t.Fatal("intermediate route replaced the frozen full goal")
		}
		if _, launched := controller.runs[finalRouteActionID]; launched || controller.runs[power.ID].action.Status != nativeOperationRunning {
			t.Fatal("intermediate route waited for or completed the slow final prerequisites")
		}
		return
	}
	t.Fatalf("no intermediate route scheduled: %+v", recorded)
}

// The fake native HTTP boundary acknowledges the policy mounted in its actual
// ConfigMap. Tick, source capture, route execution and runtime application are real.
func tickReadySubsetController(t *testing.T, observe func(int, *native.State)) (*SchedulerController, *atomic.Bool) {
	t.Helper()
	ctx := context.Background()
	manager, _ := newManagerHarness(t)
	createManagerCatalog(t, manager)
	if _, err := manager.CreateWorkload(ctx, "workload", managerWorkloadSpec()); err != nil {
		t.Fatal(err)
	}
	d := *manager.runtime.SchedulerInputs(time.Now).Workloads[0]
	workload, err := manager.store.GetWorkload(ctx, "workload")
	if err != nil {
		t.Fatal(err)
	}
	pod, node, profile, class := observerSourceFixture()
	measured := managerProfile(&d)
	profile.Condition.LogicalModel = d.Model.Name
	profile.Condition.ActiveProtocol.Dataset = measured.Condition.ActiveProtocol.Dataset
	profile.Condition.Preprocessor.Name = measured.Condition.Preprocessor.Name
	profile.Condition.Preprocessor.Inputs = measured.Condition.Preprocessor.Inputs
	profile.Condition.Preprocessor.Outputs = measured.Condition.Preprocessor.Outputs
	profile.Condition.Preprocessor.MaxNewTokens = measured.Condition.Preprocessor.MaxNewTokens
	profile.Condition.Artifact.ExecutionPath = d.Model.Paths[0].Name
	profile.Condition.Artifact.Name = d.Model.Paths[0].Artifact
	path := d.Model.Paths[0]
	path.Image, path.Files, path.Firstfloor = profile.Condition.Artifact.Image, profile.Condition.Artifact.Files, profile.Condition.Firstfloor
	path.RuntimeFamily, path.Backend = profile.Condition.RuntimeFamily, profile.Condition.Backend
	path.Preprocessor = serving.PreprocessorArtifact{Image: profile.Condition.Preprocessor.Image, Path: profile.Condition.Preprocessor.Path}
	d.Model.Paths, d.Generation = []serving.ModelPath{path}, d.Generation+1
	if err := manager.runtime.Publish(&d); err != nil {
		t.Fatal(err)
	}
	profile.RuntimeMetadata.RuntimeVersion = observerTestRuntime
	profile.RuntimeMetadata.Inputs = profile.Condition.Preprocessor.Inputs
	profile.RuntimeMetadata.Outputs = profile.Condition.Preprocessor.Outputs
	profile.RuntimeMetadata.InitializedBatches = []int{1}
	profile.Evidence = []evidencestore.ObjectIdentity{{Store: "raw", Key: "tick-profile", Size: 1}}
	profile.Batches[0].DurationNanos = []float64{float64(time.Millisecond)}
	prepared := &atomic.Bool{}
	kube := kubefake.NewSimpleClientset()
	profiles := []serving.Profile{}
	objects := []client.Object{&class, workload}
	for index, name := range []string{"a", "b"} {
		n, p, source := node.DeepCopy(), pod.DeepCopy(), profile
		n.Name, n.UID = name, types.UID(name+"-node")
		p.Name, p.UID, p.Spec.NodeName = name, types.UID(name+"-pod"), name
		p.ResourceVersion = "1"
		p.Labels[instancePodUIDLabel] = string(p.UID)
		source.ID, source.NodeName, source.Condition.NodeUID = name+"-profile", name, string(n.UID)
		p.Annotations[ServingProfileAnnotation] = source.ID
		p.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", Name: workload.Name, UID: workload.UID}}
		// Progress transport is outside this test. Retain a subscribed source so
		// client-go's empty fake Pod log does not falsely terminate the executor.
		follow, cancel := context.WithCancel(manager.observer.ctx)
		status := firstfloorStatus(p)
		manager.observer.sources[physicalID(p.UID, status.ContainerID)] = &nativeSource{
			pod: types.NamespacedName{Namespace: p.Namespace, Name: p.Name}, uid: p.UID,
			containerID: status.ContainerID, follow: follow, cancel: cancel,
		}
		if index == 0 {
			raw, _ := json.Marshal(map[string]int32{d.UID: 1000})
			p.Annotations[ServingRoutingAnnotation] = string(raw)
		}
		profiles = append(profiles, source)
		service := observerServiceFixture(p)
		service.Spec.ClusterIP = []string{"10.96.0.10", "10.96.0.11"}[index]
		for _, object := range []struct {
			resource string
			object   client.Object
		}{{"nodes", n}, {"pods", p}, {"services", service}} {
			if err := kube.Tracker().Create(core.SchemeGroupVersion.WithResource(object.resource), object.object, object.object.GetNamespace()); err != nil {
				t.Fatal(err)
			}
		}
		cfg := native.Config{Model: "chill_model", Policy: native.Policy{ProfileID: source.ID, MaxBatchSize: 1, DeadlinePolicy: "keep"}}
		raw, _ := json.Marshal(cfg)
		objects = append(objects, p, &core.ConfigMap{
			ObjectMeta: meta.ObjectMeta{Name: p.Name, Namespace: p.Namespace}, Data: map[string]string{"config.json": string(raw)},
		})
	}
	slow := node.DeepCopy()
	slow.Name, slow.UID = slowNodeName, slowNodeName
	if _, err := kube.CoreV1().Nodes().Create(ctx, slow, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	manager.store.client = fake.NewClientBuilder().WithScheme(manager.store.client.Scheme()).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if err := c.Patch(ctx, object, patch, opts...); err != nil {
				return err
			}
			if p, ok := object.(*core.Pod); ok {
				return kube.Tracker().Update(core.SchemeGroupVersion.WithResource("pods"), p.DeepCopy(), p.Namespace)
			}
			return nil
		},
	}).Build()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		services, err := kube.CoreV1().Services(pod.Namespace).List(r.Context(), meta.ListOptions{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		name := ""
		for _, service := range services.Items {
			if service.Spec.ClusterIP+":8080" == r.URL.Host {
				name = service.OwnerReferences[0].Name
			}
		}
		cm := &core.ConfigMap{}
		if err := manager.store.client.Get(r.Context(), client.ObjectKey{Namespace: pod.Namespace, Name: name}, cm); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var cfg native.Config
		if err := json.Unmarshal([]byte(cm.Data["config.json"]), &cfg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		index := slices.IndexFunc(profiles, func(p serving.Profile) bool { return p.ID == cfg.Policy.ProfileID })
		if index < 0 {
			http.Error(w, "unknown native profile", http.StatusInternalServerError)
			return
		}
		p := profiles[index]
		actual := *p.RuntimeMetadata
		actual.ModelFiles = deliveredModelFiles(p.Condition.Artifact.Files)
		state := native.State{At: time.Now().UTC(), Ready: index == 0 || prepared.Load(), Metadata: native.Metadata{
			Model: cfg.Model, Mode: "serving", ProfileID: p.ID, AppliedAt: time.Now().UTC(),
			Runtime: actual, MaxQueueRequests: 4, MaxBatchSize: 1, ModelInstanceCount: 1,
			DecisionID: cfg.Policy.DecisionID, Policy: cfg.Policy,
		}}
		if observe != nil {
			observe(index, &state)
		}
		if err := json.NewEncoder(w).Encode(state); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(func() {
		_ = manager.observer.Close()
		server.CloseClientConnections()
		server.Close()
	})
	proxy, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	manager.observer.native.HTTPClient = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	manager.observer.kube = kube
	manager.observer.SetProfiles(profiles)
	operations, err := NewOperations(manager.store, kube, manager.runtime, manager.observer, manager.observer.native,
		OperationConfig{Namespace: pod.Namespace, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	controller := &SchedulerController{
		ctx: ctx, store: manager.store, kube: kube, runtime: manager.runtime, observer: manager.observer,
		operations: operations, observationLog: manager.observationLog, capacity: serving.NewCapacityTable(1, nil, nil), runs: map[string]*actionRun{},
		config: SchedulerControllerConfig{Settings: serving.SchedulerSettings{
			ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second,
			MaxObservationAge: time.Minute, MaxReplicas: 3, MaxInstances: 3, MaxCPUs: 4,
			CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1, CapacityMinimumRequests: 1, PlanSearchWorkers: 1,
			AutonomousWorkloads: []string{d.UID},
		}},
	}
	return controller, prepared
}

func TestTickRoutesNewlyPreparedInstanceWhilePowerStillRuns(t *testing.T) {
	controller, prepared := tickReadySubsetController(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snapshot, err := controller.capture(ctx, time.Now(), controller.config)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Instances) != 1 || !snapshot.Instances[0].Ready {
		t.Fatalf("initial capture must contain only a Ready source: %+v; issues=%+v", snapshot.Instances, controller.observer.Issues())
	}
	a := snapshot.Instances[0]
	b := a
	b.ID, b.Ready = "b-pod/containerd://running", false
	for _, profile := range controller.observer.Profiles() {
		if profile.NodeName == "b" {
			b.Profile, b.Node = &profile, serving.NodeReference{Name: profile.NodeName, UID: profile.Condition.NodeUID}
		}
	}
	uid := snapshot.Workloads[0].UID
	a.WorkloadUID, b.WorkloadUID = uid, uid
	slow := a
	slow.ID, slow.Ready, slow.Node = "slow-slot", false, serving.NodeReference{Name: slowNodeName, UID: slowNodeName}
	slowProfile := *a.Profile
	slowProfile.Condition.NodeUID, slowProfile.Condition.PowerMode = slow.Node.UID, raisedPowerMode
	slow.Profile = &slowProfile
	for index := range snapshot.Nodes {
		if snapshot.Nodes[index].Node == slow.Node {
			snapshot.Nodes[index].PowerMode = raisedPowerMode
		}
	}
	goal := serving.SchedulerCandidate{ID: "tick-expand", Instances: []serving.SchedulerInstance{a, b, slow}, Nodes: snapshot.Nodes,
		Routing: map[string]map[string]int32{uid: {a.ID: 400, b.ID: 400, slow.ID: 200}}, Actions: []serving.Action{
			{ID: "prepare-b", Kind: operationPrepare, Instance: b.ID, Node: b.Node},
			{ID: slowPowerActionID, Kind: operationPower, Node: slow.Node, From: "fixed", To: raisedPowerMode},
			{ID: "prepare-slow", Kind: operationPrepare, Instance: slow.ID, Node: slow.Node, DependsOn: []string{slowPowerActionID}},
			{ID: finalRouteActionID, Kind: operationRoute, DependsOn: []string{"prepare-b", "prepare-slow"}},
		}}
	controller.runtime.EnableSchedulerRouting()
	prepared.Store(true)
	controller.goal = &goal
	for index, action := range goal.Actions[:2] {
		result := &OperationResult{Operation: Operation{ID: action.ID, Kind: action.Kind, Target: &goal}, Status: nativeOperationRunning}
		action.Status = nativeOperationRunning
		if index == 0 {
			action.Status, result.Status = nativeOperationComplete, nativeOperationComplete
			result.Operation.Prepare = &PrepareSpec{Instance: b}
			result.EndedAt = time.Now()
		}
		controller.runs[action.ID] = &actionRun{action: action, result: result, started: time.Now()}
	}
	var recorded []OperationResult
	controller.config.OnAction = func(result OperationResult) error {
		recorded = append(recorded, result)
		return nil
	}
	decision, err := controller.Tick(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	controller.wg.Wait()
	routing, _ := controller.runtime.SchedulerRouting()
	if decision.Target == nil || decision.Target.ID != goal.ID || !slices.Contains(decision.Reasons, "TransitionInProgress") ||
		routing[uid][a.ID] != 500 || routing[uid][b.ID] != 500 {
		t.Fatalf("first tick after preparation did not route Ready capacity: status=%s reasons=%v routing=%v", decision.Status, decision.Reasons, routing)
	}
	if _, launched := controller.runs[finalRouteActionID]; launched || controller.runs[slowPowerActionID].action.Status != nativeOperationRunning {
		t.Fatal("intermediate route completed or waited for slow final prerequisites")
	}
	if len(recorded) != 2 || recorded[1].Status != nativeOperationComplete || recorded[1].Actual.Routing[uid][b.ID] != 500 {
		t.Fatalf("real route operation was not applied/recorded: %+v", recorded)
	}
	decision, err = controller.Tick(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	controller.wg.Wait()
	if decision.Target == nil || decision.Target.ID != goal.ID || len(recorded) != 2 {
		t.Fatal("the completed intermediate route displaced the full goal or was needlessly reapplied")
	}
}

func TestPolicyAckPendingPreservesPhysicalOccupancyAcrossTicksAC34(t *testing.T) {
	var held atomic.Bool
	held.Store(true)
	waiting := make(chan struct{}, 1)
	controller, _ := tickReadySubsetController(t, func(_ int, state *native.State) {
		if held.Load() && state.Metadata.DecisionID != "" {
			state.Metadata.DecisionID, state.Metadata.Policy.DecisionID = "", ""
			select {
			case waiting <- struct{}{}:
			default:
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer func() { held.Store(false); controller.wg.Wait() }()
	pod := &core.Pod{}
	key := client.ObjectKey{Namespace: "chill-system", Name: "a"}
	if err := controller.store.client.Get(ctx, key, pod); err != nil {
		t.Fatal(err)
	}
	before := pod.DeepCopy()
	pod.Annotations[ServingSlotAnnotation] = "logical-slot-a"
	if err := controller.store.client.Patch(ctx, pod, client.MergeFrom(before)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := controller.capture(ctx, time.Now(), controller.config)
	if err != nil || len(snapshot.Instances) != 1 {
		t.Fatalf("initial physical capture: %+v %v", snapshot.Instances, err)
	}
	a := snapshot.Instances[0]
	uid := snapshot.Workloads[0].UID
	a.WorkloadUID = uid
	original := controller.runtime.Instances()[0]
	controller.runtime.EnableSchedulerRouting()
	goal := serving.SchedulerCandidate{ID: "distribution-change", Nodes: snapshot.Nodes, Instances: []serving.SchedulerInstance{a},
		Routing: map[string]map[string]int32{uid: {a.ID: 500}},
		Actions: []serving.Action{{ID: "held-policy-route", Kind: operationRoute}}}
	var routeStarts, prepareStarts atomic.Int32
	controller.config.OnAction = func(result OperationResult) error {
		if result.Status == starting {
			switch result.Operation.Kind {
			case operationRoute:
				routeStarts.Add(1)
			case operationPrepare:
				prepareStarts.Add(1)
			}
		}
		return nil
	}
	controller.state.Snapshot = snapshot
	controller.launch(goal, controller.config)
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal("route did not reach actual native ACK wait")
	}
	for range 3 {
		decision, err := controller.Tick(ctx, time.Now())
		if err != nil || decision.Target == nil || !slices.Contains(decision.Reasons, "TransitionInProgress") {
			t.Fatalf("pending route lost transition: %+v %v", decision, err)
		}
		assertPendingPhysicalCapture(t, controller, a, original, uid)
	}
	if prepareStarts.Load() != 0 || routeStarts.Load() != 1 {
		t.Fatalf("pending caused duplicate operations: prepare=%d route=%d", prepareStarts.Load(), routeStarts.Load())
	}
	for _, action := range controller.kube.(*kubefake.Clientset).Actions() {
		if action.GetVerb() == "create" && action.GetResource().Resource == "pods" {
			t.Fatal("ACK waiting created another physical Pod")
		}
	}
	held.Store(false)
	controller.wg.Wait()
	after, err := controller.capture(ctx, time.Now(), controller.config)
	if err != nil || len(after.Instances) != 1 || after.Instances[0].ID != a.ID || len(after.Instances[0].Reasons) != 0 {
		t.Fatalf("ACK did not clear pending on the same executor: %+v %v", after.Instances, err)
	}
	if actual, _ := controller.runtime.SchedulerRouting(); actual[uid][a.ID] != 500 {
		t.Fatalf("actual acknowledged distribution not applied: %v", actual)
	}
}

func assertPendingPhysicalCapture(t *testing.T, controller *SchedulerController, a serving.SchedulerInstance, original serving.InstanceState, uid string) {
	t.Helper()
	state := controller.State()
	if len(state.Snapshot.Instances) != 1 {
		t.Fatalf("ACK wait erased the physical allocation: %+v", state.Snapshot)
	}
	actual := state.Snapshot.Instances[0]
	if actual.ID != a.ID || actual.TargetID != a.TargetID || actual.OwnerWorkloadUID != uid ||
		actual.Node != a.Node || !actual.Ready || actual.Stopped || !slices.Equal(actual.Allocation.CPUs, a.Allocation.CPUs) ||
		!slices.Equal(actual.Reasons, []string{policyAckPendingReason}) {
		t.Fatalf("pending instance changed physical facts: %+v", actual)
	}
	for _, node := range state.Snapshot.Nodes {
		if node.Node == a.Node && slices.Contains(node.Reasons, serving.NodeInstanceUnavailable) {
			t.Fatalf("ACK pending became source unavailable: %+v", node)
		}
	}
	physical := schedulerPhysicalState(a.ID, controller.runtime.Instances())
	if physical == nil || physical.Native == nil || physical.Native.Metadata.DecisionID != "" ||
		physical.Native.At.Before(original.Native.At) || !physical.Ready || physical.Reason != policyAckPendingReason {
		t.Fatalf("actual previous ACK/state was replaced or dropped: %+v", physical)
	}
	pools := controller.runtime.Pools(state.Snapshot.Nodes)
	if len(pools.Workloads) != 1 || len(pools.Workloads[0].Instances) != 1 ||
		pools.Workloads[0].Instances[0].Reason != policyAckPendingReason ||
		slices.ContainsFunc(pools.Available, func(n serving.PoolNode) bool { return n.Node == a.Node }) {
		t.Fatalf("pending instance left its owned/bound POOL: %+v", pools)
	}
}

func TestPolicyAckPendingDoesNotMaskNativeReadOrMetadataFailure(t *testing.T) {
	for _, cause := range []string{"metadata", "read"} {
		t.Run(cause, func(t *testing.T) {
			var failed atomic.Bool
			controller, _ := tickReadySubsetController(t, func(_ int, state *native.State) {
				if !failed.Load() {
					return
				}
				if cause == "metadata" {
					state.Metadata.Runtime.RuntimeVersion = ""
				} else {
					state.At = time.Time{}
				}
			})
			ctx := context.Background()
			if snapshot, err := controller.capture(ctx, time.Now(), controller.config); err != nil || len(snapshot.Instances) != 1 {
				t.Fatalf("initial verified physical state missing: %+v %v", snapshot.Instances, err)
			}
			pod := &core.Pod{}
			if err := controller.store.client.Get(ctx, client.ObjectKey{Namespace: "chill-system", Name: "a"}, pod); err != nil {
				t.Fatal(err)
			}
			before := pod.DeepCopy()
			pod.Annotations[ServingPolicyAnnotation] = "unacknowledged"
			if err := controller.store.client.Patch(ctx, pod, client.MergeFrom(before)); err != nil {
				t.Fatal(err)
			}
			failed.Store(true)
			snapshot, err := controller.capture(ctx, time.Now(), controller.config)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Instances) != 0 || !slices.ContainsFunc(snapshot.Nodes, func(n serving.SchedulerNode) bool {
				return n.Node.Name == "a" && slices.Contains(n.Reasons, serving.NodeInstanceUnavailable)
			}) {
				t.Fatalf("failed native source revived old state merely because ACK was pending: %+v", snapshot)
			}
		})
	}
}

func pendingPowerController(t *testing.T, ctx context.Context) (*SchedulerController, serving.SchedulerSnapshot, *atomic.Bool, *atomic.Int32) {
	t.Helper()
	var outstanding atomic.Bool
	outstanding.Store(true)
	appliedAt := time.Now().UTC()
	controller, prepared := tickReadySubsetController(t, func(index int, state *native.State) {
		state.Metadata.AppliedAt = appliedAt
		if index == 0 && outstanding.Load() {
			state.Requests = []native.RequestState{
				{Context: native.RequestContext{RequestID: "queued"}, Phase: "queued"},
				{Context: native.RequestContext{RequestID: "running"}, Phase: "running", BatchID: 1, BatchSize: 1},
			}
		}
	})
	prepared.Store(true)
	kube := controller.kube.(*kubefake.Clientset)
	profiles := controller.observer.Profiles()
	for index := range profiles {
		if profiles[index].NodeName == "b" {
			profiles[index].NodeName, profiles[index].Condition.NodeUID = "a", "a-node"
		}
	}
	controller.observer.SetProfiles(profiles)
	for _, name := range []string{"a", "b"} {
		pod := &core.Pod{}
		if err := controller.store.client.Get(ctx, client.ObjectKey{Namespace: "chill-system", Name: name}, pod); err != nil {
			t.Fatal(err)
		}
		before := pod.DeepCopy()
		pod.Spec.NodeName = "a"
		if name == "a" {
			pod.Annotations[ServingPolicyAnnotation] = "desired-but-not-acknowledged"
		}
		if err := controller.store.client.Patch(ctx, pod, client.MergeFrom(before)); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := controller.capture(ctx, time.Now(), controller.config)
	if err != nil || len(snapshot.Instances) != 2 {
		t.Fatalf("pending A and normal B disappeared: %+v %v", snapshot.Instances, err)
	}
	a := snapshot.Instances[0]
	if !slices.Contains(a.Reasons, policyAckPendingReason) || len(snapshot.Instances[1].Reasons) != 0 {
		t.Fatalf("fixture lacks distinct pending/normal observations: %+v", snapshot.Instances)
	}
	physical := schedulerPhysicalState(a.ID, controller.runtime.Instances())
	if physical == nil || physical.Native == nil || len(physical.Native.Requests) != 2 {
		t.Fatalf("ACK pending erased native queued/running requests: %+v", physical)
	}
	class := &edge.DeviceClass{}
	if err := controller.store.client.Get(ctx, client.ObjectKey{Name: "cpu"}, class); err != nil {
		t.Fatal(err)
	}
	class.Spec.PowerModes = append(class.Spec.PowerModes, edge.PowerMode{Name: lowMode,
		Actuator: edge.PowerModeActuator{Kind: edge.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edge.JetsonNVPModelTarget{ID: 1}}})
	if err := controller.store.client.Update(ctx, class); err != nil {
		t.Fatal(err)
	}
	settle := settleAgent(class)
	node, err := kube.CoreV1().Nodes().Get(ctx, a.Node.Name, meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Labels[chillmeta.PowerMode] = a.Profile.Condition.PowerMode
	settle(node)
	if _, err := kube.CoreV1().Nodes().Update(ctx, node, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	var powerWrites atomic.Int32
	controller.store.client = &nodeAPI{Client: controller.store.client, kube: kube, agent: func(node *core.Node) {
		powerWrites.Add(1)
		settle(node)
	}}
	return controller, snapshot, &outstanding, &powerWrites
}

func TestPendingNativeRequestsBlockActualPowerUntilStopAndDrainAC35(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	controller, snapshot, outstanding, powerWrites := pendingPowerController(t, ctx)
	a := snapshot.Instances[0]
	power := serving.Action{ID: "actual-power", Kind: operationPower, Node: a.Node, To: lowMode}
	target := serving.SchedulerCandidate{}
	for _, instance := range snapshot.Instances {
		if err := controller.operations.StopInstance(ctx, instance.ID); err != nil {
			t.Fatal(err)
		}
		power.DependsOn = append(power.DependsOn, "drain-"+instance.ID)
	}
	routing, _ := controller.runtime.SchedulerRouting()
	for _, weights := range routing {
		for _, instance := range snapshot.Instances {
			if weights[instance.ID] > 0 {
				t.Fatalf("stop left a native destination routed: %v", routing)
			}
		}
	}
	if controller.dependenciesReady(power, target) {
		t.Fatal("power dependencies released before both drains")
	}
	if err := controller.operations.ChangePower(ctx, a.Node, "", lowMode, time.Second); err == nil || powerWrites.Load() != 0 {
		t.Fatalf("power changed with native queued/running requests: writes=%d err=%v", powerWrites.Load(), err)
	}
	outstanding.Store(false)
	state, err := controller.operations.Capture(ctx, a.Node)
	if err != nil || len(state.Native) != 2 {
		t.Fatalf("missing actual empty native state: %+v %v", state, err)
	}
	for _, actual := range state.Native {
		if len(actual.Requests) != 0 {
			t.Fatal("native completion did not clear outstanding requests")
		}
	}
	for _, instance := range snapshot.Instances {
		id := "drain-" + instance.ID
		result, err := controller.operations.Execute(ctx, Operation{ID: id, Kind: operationDrain, Node: a.Node, Instance: instance.ID})
		if err != nil || result.Status != nativeOperationComplete {
			t.Fatalf("actual drain failed: %+v %v", result, err)
		}
		controller.runs[id] = &actionRun{action: serving.Action{ID: id, Kind: operationDrain, Status: nativeOperationComplete}}
	}
	if !controller.dependenciesReady(power, target) {
		t.Fatal("empty native sources and both actual drains did not release power")
	}
	if err := controller.operations.ChangePower(ctx, a.Node, "", lowMode, time.Second); err != nil || powerWrites.Load() != 1 {
		t.Fatalf("drained Node did not execute exactly one real power request: writes=%d err=%v", powerWrites.Load(), err)
	}
}

func TestPhysicalLookupAndPolicyPreferExactExecutorAC36(t *testing.T) {
	controller, prepared := tickReadySubsetController(t, nil)
	prepared.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snapshot, err := controller.capture(ctx, time.Now(), controller.config)
	if err != nil || len(snapshot.Instances) != 2 {
		t.Fatalf("physical fixture: %+v %v", snapshot.Instances, err)
	}
	a, b := snapshot.Instances[0], snapshot.Instances[1]
	physical := controller.runtime.Instances()
	physical[0].TargetID = b.ID
	for _, order := range [][]serving.InstanceState{physical, {physical[1], physical[0]}} {
		if actual := schedulerPhysicalState(b.ID, order); actual == nil || actual.ID != b.ID {
			t.Fatalf("list order/alias shadowed exact executor: %+v", actual)
		}
	}
	physical[0].TargetID, physical[1].TargetID = "ambiguous", "ambiguous"
	if schedulerPhysicalState("ambiguous", physical) != nil {
		t.Fatal("genuinely ambiguous logical alias was selected")
	}
	updateSlot := func(name, slot string) {
		t.Helper()
		pod := &core.Pod{}
		if err := controller.store.client.Get(ctx, client.ObjectKey{Namespace: "chill-system", Name: name}, pod); err != nil {
			t.Fatal(err)
		}
		before := pod.DeepCopy()
		pod.Annotations[ServingSlotAnnotation] = slot
		if err := controller.store.client.Patch(ctx, pod, client.MergeFrom(before)); err != nil {
			t.Fatal(err)
		}
	}
	updateSlot("a", b.ID)
	policy := native.Policy{DecisionID: "exact-b-policy", ProfileID: b.Profile.ID, MaxBatchSize: b.MaxBatch, DeadlinePolicy: "keep"}
	result := OperationResult{}
	if err := controller.operations.applyPolicy(ctx, b.ID, policy, &result); err != nil {
		t.Fatal("earlier alias Pod shadowed exact policy target", err)
	}
	if _, ok := result.Actual.Native[b.ID]; !ok {
		t.Fatalf("policy ACK lacks exact physical target: %+v", result.Actual.Native)
	}
	cm := &core.ConfigMap{}
	if err := controller.store.client.Get(ctx, client.ObjectKey{Namespace: "chill-system", Name: "a"}, cm); err != nil {
		t.Fatal(err)
	}
	var config native.Config
	if err := json.Unmarshal([]byte(cm.Data["config.json"]), &config); err != nil || config.Policy.ProfileID != a.Profile.ID || config.Policy.DecisionID != "" {
		t.Fatalf("policy changed alias Pod instead of exact target: %+v %v", config, err)
	}
	updateSlot("a", "ambiguous")
	updateSlot("b", "ambiguous")
	if err := controller.operations.applyPolicy(ctx, "ambiguous", policy, &OperationResult{}); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("ambiguous policy alias silently chose the first Pod: %v", err)
	}
}

func TestInstanceUnavailableBlocksContinuedNodeActions(t *testing.T) {
	controller, target, _ := acknowledgedHandoffFixture(t)
	node := target.Instances[0].Node
	controller.state.Snapshot.Nodes = []serving.SchedulerNode{{Node: node, Reasons: []string{serving.NodeInstanceUnavailable}}}
	for _, kind := range []string{operationPrepare, operationPower, operationRemove} {
		action := serving.Action{Kind: kind, Node: node}
		if controller.dependenciesReady(action, target) {
			t.Fatalf("retained %s released on unknown Node", kind)
		}
		action.Node.UID = "healthy-other-uid"
		if !controller.dependenciesReady(action, target) {
			t.Fatalf("unknown Node blocked independent healthy %s", kind)
		}
	}
	route := serving.Action{Kind: operationRoute, Reason: serving.NodeInstanceUnavailable + ":" + node.Name + ":" + node.UID}
	if controller.dependenciesReady(route, target) {
		t.Fatal("blocked route executed after its missing prepare/power was suppressed")
	}
}
