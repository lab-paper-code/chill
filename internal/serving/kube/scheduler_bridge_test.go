package kube

import (
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestBridgeStopRequiresCurrentNativeACKAC45(t *testing.T) {
	const (
		bridgePlanned           = "planned"
		independentRouteChanged = "independent UID route changed"
	)
	for _, proof := range []string{nativeOperationComplete, independentRouteChanged, bridgePlanned, nativeOperationRunning, nativeOperationFailed, "readiness drift", "policy drift", "wrong owner UID"} {
		t.Run(proof, func(t *testing.T) {
			controller, target, physical := acknowledgedHandoffFixture(t)
			stage := *controller.runs["acknowledged-route"].result.Operation.Target
			stage.ID = "prepared-bridge"
			source := stage.Instances[0]
			source.ID, source.Weight = "old-source", 0
			stage.Instances = append(stage.Instances, source)
			ack := serving.Action{ID: "bridge-ack", Kind: operationRoute, From: "handoff-bridge", Status: nativeOperationComplete, RouteTarget: &stage}
			controller.runs[ack.ID] = &actionRun{action: ack, result: &OperationResult{Operation: Operation{Target: &stage}, Status: nativeOperationComplete}}
			stop := serving.Action{ID: "stop-original-physical", Kind: operationStop, Instance: "old-source", DependsOn: []string{ack.ID}}
			// The final goal is deliberately not ready: the stop depends on the
			// frozen intermediate proof, never on a substituted final target.
			future := target.Instances[0]
			future.ID, future.Ready = "future-not-prepared", false
			target.Instances = []serving.SchedulerInstance{future}
			target.Routing = map[string]map[string]int32{future.WorkloadUID: {future.ID: 1}}
			if proof == independentRouteChanged {
				stage.Routing["independent-uid"] = map[string]int32{"not-this-handoff": 1}
			}
			switch proof {
			case bridgePlanned, nativeOperationRunning, nativeOperationFailed:
				controller.runs[ack.ID].action.Status = proof
			case "readiness drift":
				physical.State.Native.Ready = false
			case "policy drift":
				physical.State.Native.Metadata.Policy.MaxBatchSize++
			case "wrong owner UID":
				controller.runs[ack.ID].result.Operation.WorkloadUIDs = []string{"not-this-owner"}
			}
			if got := controller.dependenciesReady(stop, target); got != (proof == nativeOperationComplete || proof == independentRouteChanged) {
				t.Fatalf("bridge ACK proof %s allowed stop=%t", proof, got)
			}
		})
	}
}

func TestRequiredBridgeFailureFailsOwnerSCH26AC38(t *testing.T) {
	for _, status := range []string{nativeOperationFailed, nativeOperationPartial, nativeOperationComplete} {
		t.Run(status, func(t *testing.T) {
			c, goal, _ := acknowledgedHandoffFixture(t)
			stage := *c.runs["acknowledged-route"].result.Operation.Target
			bridge := serving.Action{ID: "required-bridge", Kind: operationRoute, RouteTarget: &stage, Status: status}
			goal.Actions = []serving.Action{bridge, {ID: "blocked-stop", Kind: operationStop, DependsOn: []string{bridge.ID}}}
			installTestTransition(c, &goal)
			parent := c.runs[transitionRunKind+"/"+goal.ID]
			c.runs[bridge.ID] = &actionRun{action: bridge, owner: goal.ID,
				result: &OperationResult{Operation: Operation{Target: &stage}, Status: status}}
			// The bridge's successful effect can be superseded by the final
			// route; an execution failure must still terminate its owner.
			c.reconcileOwnersLocked(serving.SchedulerSnapshot{}, time.Now(), false)
			want := nativeOperationFailed
			if status == nativeOperationComplete {
				want = nativeOperationRunning
			}
			if parent.action.Status != want {
				t.Fatalf("bridge %s left parent %s, want %s", status, parent.action.Status, want)
			}
		})
	}
}
