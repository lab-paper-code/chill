package kube

import (
	"context"
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

func TestSkippedRequiredBridgeConsumesReadySubsetACKSCH51(t *testing.T) {
	for _, receipt := range []bool{true, false} {
		t.Run(map[bool]string{true: "acknowledged", false: "no-ack"}[receipt], func(t *testing.T) {
			c, goal, _ := acknowledgedHandoffFixture(t)
			c.ctx = context.Background()
			stage := *c.runs["acknowledged-route"].result.Operation.Target
			stage.ID = "independent-bridge"
			uid := stage.Instances[0].WorkloadUID
			c.state.Snapshot.Routing = stage.Routing
			future := goal.Instances[0]
			future.ID, future.Ready = "future-after-blocker-stop", false
			goal.Instances = []serving.SchedulerInstance{future}
			goal.Routing = map[string]map[string]int32{uid: {future.ID: 1}}
			bridge := serving.Action{ID: "required-bridge", Kind: operationRoute, RouteTarget: &stage, DependsOn: []string{"prepare-bridge"}}
			stop := serving.Action{ID: "stop-blocker", Kind: operationStop, Instance: "old-source", DependsOn: []string{bridge.ID}}
			goal.Actions = []serving.Action{bridge, stop}
			c.runs["prepare-bridge"] = &actionRun{action: serving.Action{Status: nativeOperationComplete}}
			if !receipt {
				delete(c.runs, "acknowledged-route")
			}
			installTestTransition(c, &goal)
			config := SchedulerControllerConfig{OnAction: func(OperationResult) error { return context.Canceled }}
			c.launchTargetLocked(goal, config)
			c.wg.Wait()
			if c.runs[bridge.ID] != nil {
				t.Fatal("unchanged bridge manufactured a route run")
			}
			if !c.runs[transitionRunKind+"/"+goal.ID].noEffectRoutes[bridge.ID] {
				t.Fatal("bridge was not skipped at the execution boundary")
			}
			if (c.runs[stop.ID] != nil) != receipt {
				t.Fatalf("stop launch=%t, existing ACK=%t", c.runs[stop.ID] != nil, receipt)
			}
		})
	}
}
