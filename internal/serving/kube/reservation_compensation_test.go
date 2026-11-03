package kube

import (
	"context"
	"testing"
	"time"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const failedPreparationSlot = "failed-slot"

// #218: prepare times out before policy ACK, but its exact executor later
// becomes Ready. Compensation must retire it, not turn failure into success.
// SCH-AC-38; P2 §3 R4 compensation/idempotent cleanup alternative.
func TestFailedPrepareCompensatesOnlyOwnUnclaimedPodAC38(t *testing.T) {
	for _, evidence := range []string{"unclaimed", "canceled", "claimed", "identity-mismatch", "other-owner", pendingPowerEvidence, activeSiblingEvidence, "preexisting", "policy-ack"} {
		t.Run(evidence, func(t *testing.T) {
			c, api, child, parent := failedPrepareCompensationFixture(t, evidence)
			actual := c.state.Snapshot.Instances[1]
			uid := c.config.Settings.AutonomousWorkloads[0]
			ctx := c.ctx
			writes := api.podWrites
			status, failure := child.result.Status, child.result.Error
			c.launch(serving.SchedulerCandidate{}, c.config)
			c.wg.Wait()
			cleanupID := child.action.ID + "/compensate/" + actual.ID
			cleanup := c.runs[cleanupID]
			if evidence != "unclaimed" && evidence != "canceled" {
				if cleanup != nil || api.podWrites != writes || api.deleteCalls != 0 || !parent.reserved || !child.reserved {
					t.Fatalf("%s changed protected Pod/reservation", evidence)
				}
				return
			}
			if cleanup == nil || cleanup.action.Status != servingstate.NativeOperationComplete || cleanup.owner != parent.owner || api.deleteCalls != 1 {
				t.Fatalf("existing stop/drain/remove did not complete: %+v, deletes=%d", cleanup, api.deleteCalls)
			}
			if len(cleanup.result.DrainNative[actual.ID].Requests) != 0 {
				t.Fatal("retirement bypassed native drain")
			}
			if c.Authorize(servingstate.Operation{WorkloadUID: uid}) == nil {
				t.Fatal("cleanup completion released owner before fresh physical-return proof")
			}
			fresh, err := c.capture(ctx, time.Now(), c.config)
			if err != nil {
				t.Fatal(err)
			}
			fresh, _ = c.ReconcileSnapshot(fresh, fresh.At, c.config.Settings.ObservationWindow, func() serving.SchedulerSettings { return c.config.Settings }, c.runtime)
			if len(fresh.Reservations) != 0 || c.Authorize(servingstate.Operation{WorkloadUID: uid}) != nil {
				t.Fatalf("retired Pod did not return reservation: %+v", fresh.Reservations)
			}
			if child.result.Status != status || child.result.Error != failure || parent.result.Status != servingstate.NativeOperationFailed {
				t.Fatal("compensation rewrote failure receipt")
			}
			if _, err := c.kube.CoreV1().Pods("chill-system").Get(ctx, "a", meta.GetOptions{}); err != nil {
				t.Fatal("normal serving Pod changed", err)
			}
			c.state.Snapshot = fresh
			c.launch(serving.SchedulerCandidate{}, c.config)
			c.wg.Wait()
			if api.deleteCalls != 1 {
				t.Fatal("compensation was not idempotent")
			}
		})
	}
}

// A subset's Before.Native is not the requested ACK of a sibling's new
// preparation. Historical evidence must not bypass R4b retirement/absence.
func TestUnacknowledgedPrepareCannotBorrowPreviousRoutePolicyAC38(t *testing.T) {
	c, api, child, parent := failedPrepareCompensationFixture(t, "canceled")
	actual := *servingstate.SchedulerPhysicalState(c.state.Snapshot.Instances[1].ID, c.runtime.Instances())
	wanted := child.result.Operation.Prepare.Instance
	target := &serving.SchedulerCandidate{ID: parent.owner + "-ready-unacknowledged",
		Instances: []serving.SchedulerInstance{wanted},
		Routing:   map[string]map[string]int32{wanted.WorkloadUID: {actual.ID: 1000}}}
	operation := servingstate.Operation{Kind: servingstate.OperationRoute, Target: target, WorkloadUIDs: []string{wanted.WorkloadUID}}
	route := &servingstate.ActionRun{owner: parent.owner, scope: child.scope,
		action: serving.Action{ID: target.ID, Kind: servingstate.OperationRoute, From: servingstate.ReadySubsetRoute, Status: servingstate.NativeOperationComplete},
		result: &servingstate.OperationResult{Operation: operation, Status: servingstate.NativeOperationComplete, EndedAt: child.result.EndedAt,
			Before: servingstate.OperationState{Native: map[string]native.State{actual.ID: *actual.Native}}}}
	for _, state := range c.runtime.Instances() {
		if state.Native != nil {
			route.result.Before.Native[state.ID] = *state.Native
		}
		if state.ID != actual.ID {
			parent.result.Before.Instances = append(parent.result.Before.Instances, serving.InstanceState{ID: state.ID, PodUID: state.PodUID, Node: state.Node})
		}
	}
	if !c.ReserveRun(route.action.ID, route, operation) {
		t.Fatal("subset was not authorized under the parent")
	}
	fresh, err := c.capture(c.ctx, time.Now(), c.config)
	if err != nil {
		t.Fatal(err)
	}
	fresh, _ = c.ReconcileSnapshot(fresh, fresh.At, c.config.Settings.ObservationWindow, func() serving.SchedulerSettings { return c.config.Settings }, c.runtime)
	if !parent.reserved || !child.reserved || c.Authorize(servingstate.Operation{WorkloadUID: wanted.WorkloadUID}) == nil {
		t.Fatal("a sibling's previous policy released an unacknowledged new source")
	}
	c.state.Snapshot = fresh
	c.launch(serving.SchedulerCandidate{}, c.config)
	c.wg.Wait()
	if api.deleteCalls != 1 || !parent.reserved || !child.reserved {
		t.Fatal("R4b did not retire once while retaining the physical-return obligation")
	}
	fresh, err = c.capture(c.ctx, time.Now(), c.config)
	if err != nil {
		t.Fatal(err)
	}
	fresh, _ = c.ReconcileSnapshot(fresh, fresh.At, c.config.Settings.ObservationWindow, func() serving.SchedulerSettings { return c.config.Settings }, c.runtime)
	if len(fresh.Reservations) != 0 || c.Authorize(servingstate.Operation{WorkloadUID: wanted.WorkloadUID}) != nil {
		t.Fatal("fresh Pod absence did not return the compensated reservation")
	}
	if child.result.Status != servingstate.NativeOperationPartial || child.result.Error != context.Canceled.Error() || parent.result.Status != servingstate.NativeOperationFailed {
		t.Fatal("compensation rewrote failed receipts")
	}
}

func failedPrepareCompensationFixture(t *testing.T, evidence string) (*SchedulerController, *resetAPI, *servingstate.ActionRun, *servingstate.ActionRun) {
	t.Helper()
	c, prepared := tickReadySubsetController(t, func(index int, state *native.State) {
		if evidence == "policy-ack" && index == 1 {
			state.Metadata.DecisionID = "failed-owner"
			state.Metadata.Policy.DecisionID = "failed-owner"
			state.Metadata.Policy.MaxBatchSize = 16
		}
	})
	prepared.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	c.ctx = ctx
	t.Cleanup(func() { cancel(); c.wg.Wait() })
	api := &resetAPI{Client: c.store.client, kube: c.kube.(*kubefake.Clientset), terminateOnDelete: true}
	c.store.client = api
	pod := &core.Pod{}
	key := client.ObjectKey{Namespace: "chill-system", Name: "b"}
	if err := api.Get(ctx, key, pod); err != nil {
		t.Fatal(err)
	}
	before := pod.DeepCopy()
	pod.Annotations[servingstate.ServingSlotAnnotation] = failedPreparationSlot
	if err := api.Patch(ctx, pod, client.MergeFrom(before)); err != nil {
		t.Fatal(err)
	}
	ended := time.Now().Add(-time.Second)
	snapshot, err := c.capture(ctx, time.Now(), c.config)
	if err != nil {
		t.Fatal(err)
	}
	c.state.Snapshot = snapshot
	actual := snapshot.Instances[1]
	if actual.Node.Name != "b" || !actual.Ready || actual.WorkloadUID != "" || actual.TargetID != failedPreparationSlot {
		t.Fatalf("fixture is not a Ready unclaimed preparation: %+v", actual)
	}
	uid := c.config.Settings.AutonomousWorkloads[0]
	wanted := actual
	wanted.ID, wanted.PodUID, wanted.WorkloadUID, wanted.MaxBatch = failedPreparationSlot, "", uid, 16
	action := serving.Action{ID: "failed-prepare", Kind: servingstate.OperationPrepare, Node: actual.Node, Instance: wanted.ID, Status: servingstate.NativeOperationFailed}
	target := serving.SchedulerCandidate{ID: "failed-owner", Instances: []serving.SchedulerInstance{wanted}, Actions: []serving.Action{action}}
	scope := servingstate.ResourceScope{Nodes: []serving.NodeReference{actual.Node}, Workloads: []string{uid}, Pods: []string{actual.PodUID}, Instances: []string{actual.ID}}
	child := &servingstate.ActionRun{owner: target.ID, action: action, reserved: true, scope: scope, prepareIdentity: actual.ID,
		result: &servingstate.OperationResult{Status: servingstate.NativeOperationFailed, Error: serving.ActionDeadlineExceeded, EndedAt: ended,
			Preparation: &servingstate.PreparationEvidence{PodUID: actual.PodUID, NodeUID: actual.Node.UID, DecisionID: target.ID, Created: true},
			Operation: servingstate.Operation{ID: action.ID, Kind: servingstate.OperationPrepare, Node: actual.Node, WorkloadUIDs: []string{uid}, Prepare: &servingstate.PrepareSpec{
				Instance: wanted, Owner: &serving.WorkloadReference{UID: uid}, Config: native.Config{Policy: native.Policy{DecisionID: target.ID}},
			}}}}
	parent := &servingstate.ActionRun{owner: target.ID, reserved: true, scope: scope,
		action: serving.Action{ID: servingstate.TransitionRunKind + "/" + target.ID, Kind: servingstate.TransitionRunKind, Status: servingstate.NativeOperationFailed},
		result: &servingstate.OperationResult{Status: servingstate.NativeOperationFailed, EndedAt: ended, Operation: servingstate.Operation{Kind: servingstate.TransitionRunKind, Target: &target}}}
	child.proofParent = parent
	c.runs = map[string]*servingstate.ActionRun{parent.action.ID: parent, action.ID: child}
	switch evidence {
	case "canceled":
		child.action.Status, child.result.Status = servingstate.NativeOperationPartial, servingstate.NativeOperationPartial
		child.result.Error = context.Canceled.Error()
	case "claimed":
		c.state.Snapshot.Instances[1].WorkloadUID = uid
	case "identity-mismatch":
		child.prepareIdentity += restartedContainerSuffix
	case "other-owner":
		child.result.Operation.Prepare.Owner.UID = "other-workload"
	case pendingPowerEvidence:
		c.state.Snapshot.Nodes[1].PowerMode = ""
	case activeSiblingEvidence:
		c.runs["active"] = &servingstate.ActionRun{owner: target.ID, action: serving.Action{Status: servingstate.NativeOperationCancel}}
	case "preexisting":
		child.result.Before.Instances = []serving.InstanceState{{ID: actual.ID, PodUID: actual.PodUID}}
	}
	return c, api, child, parent
}
