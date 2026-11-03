package kube

import (
	"context"
	"testing"
	"time"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const reusedEvidence = "reused"

// Raw plan-d2a9daa9759a: Create -> cancel -> empty Capture -> terminal ->
// first exact container and complete prepare ACK, without rewriting failure.
func TestCreatedUIDLateBindingReturnsAC38(t *testing.T) {
	for _, mode := range []string{"create-confirmed", reusedEvidence, "other-uid", "unknown-create", "other-node", "different-owner", "other-profile"} {
		t.Run(mode, func(t *testing.T) {
			pod, node, profile, _ := observerSourceFixture()
			pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", UID: "workload"}}
			ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
			wanted := serving.SchedulerInstance{ID: "late-slot", Node: ref, Profile: &profile, WorkloadUID: "workload", MaxBatch: 2}
			action := serving.Action{ID: "prepare", Kind: servingstate.OperationPrepare, Node: ref, Instance: wanted.ID, Status: servingstate.NativeOperationPartial}
			target := serving.SchedulerCandidate{ID: "raw-create-cancel", Instances: []serving.SchedulerInstance{wanted}, Actions: []serving.Action{action}}
			ended := time.Now().Add(-time.Second)
			evidence := &servingstate.PreparationEvidence{PodUID: string(pod.UID), NodeUID: ref.UID, DecisionID: target.ID, Created: mode != reusedEvidence}
			child := &servingstate.ActionRun{owner: target.ID, action: action, reserved: true, scope: servingstate.ResourceScope{Nodes: []serving.NodeReference{ref}, Workloads: []string{"workload"}}, result: &servingstate.OperationResult{Status: servingstate.NativeOperationPartial, Error: context.Canceled.Error(), EndedAt: ended, Preparation: evidence, Operation: servingstate.Operation{Kind: servingstate.OperationPrepare, Node: ref, WorkloadUIDs: []string{"workload"}, Prepare: &servingstate.PrepareSpec{Instance: wanted, Owner: &serving.WorkloadReference{UID: "workload"}, Config: native.Config{Policy: native.Policy{DecisionID: target.ID}}}}}}
			parent := &servingstate.ActionRun{owner: target.ID, reserved: true, returnObserved: true, scope: child.scope, action: serving.Action{ID: servingstate.TransitionRunKind + "/" + target.ID, Kind: servingstate.TransitionRunKind, Status: servingstate.NativeOperationFailed}, result: &servingstate.OperationResult{Status: servingstate.NativeOperationFailed, EndedAt: ended, Operation: servingstate.Operation{Target: &target, WorkloadUIDs: []string{"workload"}}}}
			child.proofParent = parent
			c := &SchedulerController{reservationState: &servingstate.ReservationState{runs: map[string]*servingstate.ActionRun{parent.action.ID: parent, action.ID: child}}}
			actual := wanted
			actual.PodUID = string(pod.UID)
			actual.ID = servingstate.PhysicalID(pod.UID, servingstate.FirstfloorStatus(pod).ContainerID)
			actual.TargetID, actual.OwnerWorkloadUID = wanted.ID, wanted.WorkloadUID
			switch mode {
			case "other-uid":
				actual.PodUID = "replacement"
				actual.ID = "replacement/container"
			case "unknown-create":
				child.result.Preparation = nil
			case "other-node":
				actual.Node.UID = "replacement-node"
			case "different-owner":
				actual.OwnerWorkloadUID = "replacement-owner"
			case "other-profile":
				p := profile
				p.ID = "replacement-profile"
				actual.Profile = &p
			}
			c.FreezePreparingExecutorsLocked(serving.SchedulerSnapshot{Instances: []serving.SchedulerInstance{actual}})
			accepted := mode == "create-confirmed" || mode == reusedEvidence
			if (child.prepareIdentity != "") != accepted {
				t.Fatalf("identity acceptance %q", child.prepareIdentity)
			}
			if !accepted {
				return
			}
			if parent.returnObserved {
				t.Fatal("binding retained stale return cache")
			}
			replacement := actual
			replacement.ID += "replacement"
			c.FreezePreparingExecutorsLocked(serving.SchedulerSnapshot{Instances: []serving.SchedulerInstance{replacement}})
			if child.prepareIdentity != actual.ID {
				t.Fatal("container rebound")
			}
			if len(parent.result.Before.Instances) != 0 {
				t.Fatal("binding rewrote admission baseline")
			}
			observed := time.Now()
			physical := serving.InstanceState{ID: actual.ID, PodUID: actual.PodUID, TargetID: wanted.ID, Node: ref, Ready: true, ObservedAt: observed, Native: &native.State{Ready: true, Metadata: native.Metadata{ProfileID: profile.ID, DecisionID: target.ID, Policy: native.Policy{ProfileID: profile.ID, DecisionID: target.ID, MaxBatchSize: 2}}}}
			c.ReleaseObservedScopes([]core.Node{*node}, []core.Pod{*pod}, []serving.InstanceState{physical}, observed)
			c.ReconcileSnapshot(serving.SchedulerSnapshot{}, observed, time.Minute, func() serving.SchedulerSettings { return c.config.Settings }, c.runtime)
			if parent.reserved || child.reserved {
				t.Fatal("late exact ACK did not return ownership")
			}
			if child.result.Status != servingstate.NativeOperationPartial || child.result.Error != context.Canceled.Error() {
				t.Fatal("late ACK rewrote original failure")
			}
			if mode == reusedEvidence && servingstate.PreparationCreatedSource(child, physical) {
				t.Fatal("reused Pod became compensation source")
			}
		})
	}
}
