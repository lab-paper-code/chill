package kube

import (
	"context"
	"testing"
	"time"

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
	for _, evidence := range []string{"unclaimed", "claimed", "identity-mismatch", "other-owner", "pending-power", "active-sibling", "preexisting", "policy-ack"} {
		t.Run(evidence, func(t *testing.T) {
			c, api, child, parent := failedPrepareCompensationFixture(t, evidence)
			actual := c.state.Snapshot.Instances[1]
			uid := c.config.Settings.AutonomousWorkloads[0]
			ctx := c.ctx
			writes := api.podWrites
			c.launch(serving.SchedulerCandidate{}, c.config)
			c.wg.Wait()
			cleanupID := child.action.ID + "/compensate/" + actual.ID
			cleanup := c.runs[cleanupID]
			if evidence != "unclaimed" {
				if cleanup != nil || api.podWrites != writes || api.deleteCalls != 0 || !parent.reserved || !child.reserved {
					t.Fatalf("%s changed protected Pod/reservation", evidence)
				}
				return
			}
			if cleanup == nil || cleanup.action.Status != nativeOperationComplete || cleanup.owner != parent.owner || api.deleteCalls != 1 {
				t.Fatalf("existing stop/drain/remove did not complete: %+v, deletes=%d", cleanup, api.deleteCalls)
			}
			if len(cleanup.result.DrainNative[actual.ID].Requests) != 0 {
				t.Fatal("retirement bypassed native drain")
			}
			if c.authorize(Operation{WorkloadUID: uid}) == nil {
				t.Fatal("cleanup completion released owner before fresh physical-return proof")
			}
			fresh, err := c.capture(ctx, time.Now(), c.config)
			if err != nil {
				t.Fatal(err)
			}
			fresh, _ = c.reconcileSnapshot(fresh, fresh.At, c.config.Settings.ObservationWindow)
			if len(fresh.Reservations) != 0 || c.authorize(Operation{WorkloadUID: uid}) != nil {
				t.Fatalf("retired Pod did not return reservation: %+v", fresh.Reservations)
			}
			if child.result.Status != nativeOperationFailed || child.result.Error != serving.ActionDeadlineExceeded || parent.result.Status != nativeOperationFailed {
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

func failedPrepareCompensationFixture(t *testing.T, evidence string) (*SchedulerController, *resetAPI, *actionRun, *actionRun) {
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
	pod.Annotations[ServingSlotAnnotation] = failedPreparationSlot
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
	action := serving.Action{ID: "failed-prepare", Kind: operationPrepare, Node: actual.Node, Instance: wanted.ID, Status: nativeOperationFailed}
	target := serving.SchedulerCandidate{ID: "failed-owner", Instances: []serving.SchedulerInstance{wanted}, Actions: []serving.Action{action}}
	scope := resourceScope{Nodes: []serving.NodeReference{actual.Node}, Workloads: []string{uid}, Pods: []string{actual.PodUID}, Instances: []string{actual.ID}}
	child := &actionRun{owner: target.ID, action: action, reserved: true, scope: scope, prepareIdentity: actual.ID,
		result: &OperationResult{Status: nativeOperationFailed, Error: serving.ActionDeadlineExceeded, EndedAt: ended,
			Operation: Operation{ID: action.ID, Kind: operationPrepare, Node: actual.Node, WorkloadUIDs: []string{uid}, Prepare: &PrepareSpec{
				Instance: wanted, Owner: &serving.WorkloadReference{UID: uid}, Config: native.Config{Policy: native.Policy{DecisionID: target.ID}},
			}}}}
	parent := &actionRun{owner: target.ID, reserved: true, scope: scope,
		action: serving.Action{ID: transitionRunKind + "/" + target.ID, Kind: transitionRunKind, Status: nativeOperationFailed},
		result: &OperationResult{Status: nativeOperationFailed, EndedAt: ended, Operation: Operation{Kind: transitionRunKind, Target: &target}}}
	c.runs = map[string]*actionRun{parent.action.ID: parent, action.ID: child}
	switch evidence {
	case "claimed":
		c.state.Snapshot.Instances[1].WorkloadUID = uid
	case "identity-mismatch":
		child.prepareIdentity += restartedContainerSuffix
	case "other-owner":
		child.result.Operation.Prepare.Owner.UID = "other-workload"
	case "pending-power":
		c.state.Snapshot.Nodes[1].PowerMode = ""
	case "active-sibling":
		c.runs["active"] = &actionRun{owner: target.ID, action: serving.Action{Status: nativeOperationCancel}}
	case "preexisting":
		child.result.Before.Instances = []serving.InstanceState{{ID: actual.ID, PodUID: actual.PodUID}}
	}
	return c, api, child, parent
}
