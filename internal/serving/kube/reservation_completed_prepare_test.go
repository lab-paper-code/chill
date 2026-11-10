package kube

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Control375 returned the completed prepare during capture, then late binding
// added its exact identities back without their already-returned Node scope.
func TestFailedOwnerCompletedPrepareReturnsBeforeLateBinding(t *testing.T) {
	c, api, prepare, parent := failedPrepareCompensationFixture(t, "unclaimed")
	actual := c.state.Snapshot.Instances[1]
	uid := actual.OwnerWorkloadUID
	if uid == "" {
		t.Fatal("prepared executor has no workload owner")
	}
	prepare.action.Status, prepare.result.Status = servingstate.NativeOperationComplete, servingstate.NativeOperationComplete
	prepare.result.Error, prepare.prepareIdentity = "", ""
	prepare.scope.Workloads, prepare.scope.Owners = nil, []string{uid}

	configMap := &core.ConfigMap{}
	if err := c.store.client.Get(c.ctx, client.ObjectKey{Namespace: "chill-system", Name: actual.PodName}, configMap); err != nil {
		t.Fatal(err)
	}
	var config native.Config
	if err := json.Unmarshal([]byte(configMap.Data["config.json"]), &config); err != nil {
		t.Fatal(err)
	}
	config.Policy.DecisionID, config.Policy.MaxBatchSize = parent.owner, prepare.result.Operation.Prepare.Instance.MaxBatch
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configMap.Data["config.json"] = string(raw)
	if err := c.store.client.Update(c.ctx, configMap); err != nil {
		t.Fatal(err)
	}

	// A sibling's unconfirmed Pod on another Node remains this failed owner's
	// cleanup duty. It must not prevent return of the acknowledged preparation.
	held := serving.NodeReference{Name: "unconfirmed", UID: "unconfirmed-node"}
	failed := &actionRun{owner: parent.owner, proofParent: parent, reserved: true,
		action: serving.Action{ID: "failed-sibling", Kind: servingstate.OperationRemove, Node: held, Instance: "unconfirmed-pod/container", Status: servingstate.NativeOperationPartial},
		scope:  resourceScope{Nodes: []serving.NodeReference{held}, Pods: []string{"unconfirmed-pod"}, Instances: []string{"unconfirmed-pod/container"}},
		result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: servingstate.OperationRemove, Node: held, PodUID: "unconfirmed-pod", Instance: "unconfirmed-pod/container"},
			Status: servingstate.NativeOperationPartial, EndedAt: parent.result.EndedAt, Error: "unconfirmed removal"}}
	c.runs[failed.action.ID] = failed
	parent.result.Operation.Target.Actions = []serving.Action{prepare.action, failed.action}
	parent.scope = resourceScope{Nodes: []serving.NodeReference{actual.Node, held}, Owners: []string{uid},
		Pods: []string{actual.PodUID, "unconfirmed-pod"}, Instances: []string{actual.ID, "unconfirmed-pod/container"}}

	for range 2 {
		snapshot, err := c.capture(c.ctx, time.Now(), c.config)
		if err != nil {
			t.Fatal(err)
		}
		// This is production order: capture returns scopes before reconciliation
		// discovers the first exact container for prepareIdentity.
		if prepare.reserved {
			t.Fatal("fresh exact prepare ACK did not return its scope during capture")
		}
		snapshot, _ = c.reconcileSnapshot(snapshot, snapshot.At, c.config.Settings.ObservationWindow, c.runtime)
		c.state.Snapshot = snapshot
		for _, reservation := range snapshot.Reservations {
			if slices.Contains(reservation.PodUIDs, actual.PodUID) || slices.Contains(reservation.Instances, actual.ID) {
				t.Fatal("late container binding reacquired the returned prepared executor")
			}
		}
		if !parent.reserved || !failed.reserved || !slices.Equal(parent.scope.Nodes, []serving.NodeReference{held}) ||
			c.authorize(servingstate.Operation{Kind: servingstate.OperationPrepare, Node: held}) == nil {
			t.Fatal("return discarded the failed sibling's unconfirmed cleanup scope")
		}
	}

	// No current WorkloadUID exists until routing. Reuse the immutable Pod owner
	// and cross real admission, native policy ACK and runtime routing boundaries.
	actual.WorkloadUID = uid
	next := serving.SchedulerCandidate{ID: "reuse-completed-prepare", Instances: []serving.SchedulerInstance{actual},
		Routing: map[string]map[string]int32{uid: {actual.ID: 1000}},
		Actions: []serving.Action{{ID: "reuse-prepared-route", Kind: servingstate.OperationRoute, Node: actual.Node}}}
	c.launch(c.ctx, next, c.config)
	c.wg.Wait()
	requireCompletedContinuationAction(t, c.runs["reuse-prepared-route"], "returned preparation routing")
	routing, _ := c.runtime.SchedulerRouting()
	if routing[uid][actual.ID] != 1000 || !failed.reserved || !parent.reserved || api.deleteCalls.Load() != 0 {
		t.Fatalf("prepared executor was not reused while retaining failed cleanup: %+v", routing)
	}
	if prepare.result.Status != servingstate.NativeOperationComplete || parent.result.Status != servingstate.NativeOperationFailed || failed.result.Status != servingstate.NativeOperationPartial {
		t.Fatal("return or reuse rewrote the original owner or child receipts")
	}
}
