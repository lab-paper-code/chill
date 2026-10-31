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
			stop := serving.Action{ID: "stop-original-physical", Kind: operationStop, Instance: source.ID, DependsOn: []string{ack.ID}}
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
			source := stage.Instances[0]
			source.ID, source.Weight = "blocker-source", 0
			terminal := stage.Instances[0]
			terminal.ID, terminal.WorkloadUID, terminal.Ready, terminal.Terminated = "terminal-w1", "w1", false, true
			stage.Instances = append(stage.Instances, source, terminal)
			stage.Routing = map[string]map[string]int32{uid: {stage.Instances[0].ID: 1}, "w1": {terminal.ID: 1}}
			c.state.Snapshot.Instances = append(c.state.Snapshot.Instances, source, terminal)
			c.state.Snapshot.Routing = stage.Routing
			future := goal.Instances[0]
			future.ID, future.WorkloadUID, future.Ready = "future-after-blocker-stop", "w1", false
			goal.Instances = []serving.SchedulerInstance{future, stage.Instances[0]}
			goal.Routing = map[string]map[string]int32{"w1": {future.ID: 1}, uid: {stage.Instances[0].ID: 1}}
			bridge := serving.Action{ID: "required-bridge", Kind: operationRoute, RouteTarget: &stage, DependsOn: []string{"prepare-bridge"}}
			stop := serving.Action{ID: "stop-blocker", Kind: operationStop, Instance: source.ID, DependsOn: []string{bridge.ID}}
			prepare := serving.Action{ID: "prepare-w1", Kind: operationPrepare, Instance: future.ID, DependsOn: []string{stop.ID}}
			goal.Actions = []serving.Action{bridge, stop, prepare}
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
			if c.runs[prepare.ID] != nil {
				t.Fatal("W1 prepare ran before blocker stop completed")
			}
			if receipt {
				c.runs[stop.ID].action.Status = nativeOperationComplete
				c.launchTargetLocked(goal, config)
				c.wg.Wait()
				if c.runs[prepare.ID] == nil {
					t.Fatal("W1 prepare did not follow the acknowledged W2 handoff and stop")
				}
			}
		})
	}
}

func TestSkippedBridgeChainConsumesExistingACKSCH51(t *testing.T) {
	for _, receipt := range []bool{true, false} {
		t.Run(map[bool]string{true: "acknowledged", false: "no-ack"}[receipt], func(t *testing.T) {
			c, goal, _ := acknowledgedHandoffFixture(t)
			c.ctx = context.Background()
			stage := *c.runs["acknowledged-route"].result.Operation.Target
			c.state.Snapshot.Routing = stage.Routing
			source := stage.Instances[0]
			source.ID, source.Weight = "first-bridge-source", 0
			stage.Instances = append(stage.Instances, source)
			c.state.Snapshot.Instances = append(c.state.Snapshot.Instances, source)
			first := serving.Action{ID: "skipped-first-bridge", Kind: operationRoute, RouteTarget: &stage}
			secondTarget := stage
			secondTarget.ID = "second-bridge-target"
			uid := stage.Instances[0].WorkloadUID
			secondTarget.Instances = append([]serving.SchedulerInstance(nil), stage.Instances...)
			secondTarget.Instances[0].MaxBatch++
			secondTarget.Routing = map[string]map[string]int32{uid: {stage.Instances[0].ID: 2}}
			second := serving.Action{ID: "second-bridge", Kind: operationRoute, RouteTarget: &secondTarget, DependsOn: []string{first.ID}}
			goal.Routing = map[string]map[string]int32{uid: {stage.Instances[0].ID: 3}}
			goal.Instances = append([]serving.SchedulerInstance(nil), stage.Instances...)
			goal.Instances[0].MaxBatch += 2
			stop := serving.Action{ID: "stop-first-source", Kind: operationStop, Instance: source.ID, DependsOn: []string{first.ID}}
			goal.Actions = []serving.Action{first, stop, second}
			if !receipt {
				delete(c.runs, "acknowledged-route")
			}
			installTestTransition(c, &goal)
			c.launchTargetLocked(goal, SchedulerControllerConfig{OnAction: func(OperationResult) error { return context.Canceled }})
			c.wg.Wait()
			if c.runs[first.ID] != nil {
				t.Fatal("unchanged first bridge manufactured a route run")
			}
			if (c.runs[second.ID] != nil) != receipt {
				t.Fatalf("second bridge launch=%t, existing ACK=%t", c.runs[second.ID] != nil, receipt)
			}
		})
	}
}

func TestSkippedBridgeChainPreservesTerminalUIDSCH51(t *testing.T) {
	for _, receipt := range []bool{true, false} {
		t.Run(map[bool]string{true: "acknowledged", false: "no-ack"}[receipt], func(t *testing.T) {
			c, goal, _ := acknowledgedHandoffFixture(t)
			c.ctx = context.Background()
			c.operations = &Operations{runtime: c.runtime}
			stage := *c.runs["acknowledged-route"].result.Operation.Target
			stage.ID = "w2-handoff"
			w2 := stage.Instances[0]
			terminal := w2
			terminal.ID, terminal.WorkloadUID, terminal.Ready, terminal.Terminated = "terminal-w1", "w1", false, true
			stage.Instances = append(stage.Instances, terminal)
			stage.Routing = map[string]map[string]int32{w2.WorkloadUID: {w2.ID: 1000}, "w1": {terminal.ID: 1}}
			ack := c.runs["acknowledged-route"].result.Operation.Target
			ack.Routing = map[string]map[string]int32{w2.WorkloadUID: {w2.ID: 1000}}
			if err := c.runtime.ApplySchedulerTarget(*ack); err != nil {
				t.Fatal(err)
			}
			c.state.Snapshot.Instances = append(c.state.Snapshot.Instances, terminal)
			c.state.Snapshot.Routing = stage.Routing
			recovered := terminal
			recovered.ID, recovered.Ready, recovered.Terminated = "recovered-w1", true, false
			next := stage
			next.ID = "w1-handoff"
			next.Instances = []serving.SchedulerInstance{w2, recovered}
			next.Routing = map[string]map[string]int32{w2.WorkloadUID: {w2.ID: 1000}, "w1": {recovered.ID: 1}}
			c.state.Snapshot.Instances = append(c.state.Snapshot.Instances, recovered)
			first := serving.Action{ID: "skip-w2", Kind: operationRoute, RouteTarget: &stage}
			second := serving.Action{ID: "restore-w1", Kind: operationRoute, RouteTarget: &next, DependsOn: []string{first.ID}}
			stop := serving.Action{ID: "old-w2-stop", Kind: operationStop, Instance: w2.ID, DependsOn: []string{first.ID}, Status: nativeOperationComplete}
			goal.Instances, goal.Routing, goal.Actions = next.Instances, next.Routing, []serving.Action{first, stop, second}
			if !receipt {
				delete(c.runs, "acknowledged-route")
			}
			installTestTransition(c, &goal)
			// The owner also controls W2, whose handoff has already completed.
			c.runs[transitionRunKind+"/"+goal.ID].result.Operation.WorkloadUIDs = []string{"w1", w2.WorkloadUID}
			c.launchTargetLocked(goal, SchedulerControllerConfig{})
			c.wg.Wait()
			if c.runs[first.ID] != nil || !c.runs[transitionRunKind+"/"+goal.ID].noEffectRoutes[first.ID] {
				t.Fatal("first bridge was not skipped")
			}
			run := c.runs[second.ID]
			if (run != nil) != receipt {
				t.Fatalf("W1 bridge launch=%t, W2 ACK=%t", run != nil, receipt)
			}
			if receipt && (run.result.EndedAt.IsZero() || run.result.Error == "") {
				t.Fatal("expected real operation execution to reject the fixture's unregistered W1 workload")
			}
		})
	}
}

func TestRunningBridgeCannotConsumeUnrelatedCompletedACKS2(t *testing.T) {
	for _, status := range []string{nativeOperationRunning, nativeOperationFailed, nativeOperationPartial} {
		t.Run(status, func(t *testing.T) {
			c, goal, _ := acknowledgedHandoffFixture(t)
			c.ctx = context.Background()
			stage := *c.runs["acknowledged-route"].result.Operation.Target
			source := stage.Instances[0]
			source.ID, source.Weight = "pending-bridge-source", 0
			stage.Instances = append(stage.Instances, source)
			bridge := serving.Action{ID: "pending-bridge", Kind: operationRoute, RouteTarget: &stage}
			stop := serving.Action{ID: "blocked-stop", Kind: operationStop, Instance: source.ID, DependsOn: []string{bridge.ID}}
			final := serving.Action{ID: "blocked-final", Kind: operationRoute, DependsOn: []string{bridge.ID}}
			goal.Actions = []serving.Action{bridge, stop, final}
			c.state.Snapshot.Routing = stage.Routing
			installTestTransition(c, &goal)
			running := bridge
			running.Status = status
			c.runs[bridge.ID] = &actionRun{action: running, owner: goal.ID}
			if c.dependenciesReady(stop, goal) || c.dependenciesReady(final, goal) {
				t.Fatal("stop/final route accepted an incomplete bridge despite its existing frozen effect")
			}
			c.launchTargetLocked(goal, SchedulerControllerConfig{OnAction: func(OperationResult) error { return context.Canceled }})
			c.wg.Wait()
			if c.runs[stop.ID] != nil || c.runs[final.ID] != nil {
				t.Fatal("incomplete bridge consumed an unrelated completed ACK and released its stop/final route")
			}
		})
	}
}

func TestReadySubsetACKPrecedesPrepareResultAndSkippedBridgeProgressS2(t *testing.T) {
	c, prepared := tickReadySubsetController(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c.ctx = ctx
	c.runtime.EnableSchedulerRouting()
	snapshot, err := c.capture(ctx, time.Now(), c.config)
	if err != nil {
		t.Fatal(err)
	}
	a, b := snapshot.Instances[0], snapshot.Instances[1]
	uid := snapshot.Workloads[0].UID
	a.WorkloadUID, b.WorkloadUID = uid, uid
	bridgeTarget := serving.SchedulerCandidate{ID: "handoff-b", Instances: []serving.SchedulerInstance{a, b},
		Routing: map[string]map[string]int32{uid: {b.ID: 1000}}}
	nextTarget := bridgeTarget
	nextTarget.ID = "next-handoff-b"
	nextTarget.Instances = append([]serving.SchedulerInstance(nil), bridgeTarget.Instances...)
	nextTarget.Instances[1].MaxWait = time.Millisecond
	prepare := serving.Action{ID: "prepare-b", Kind: operationPrepare, Instance: b.ID, Node: b.Node}
	bridge := serving.Action{ID: "skipped-handoff", Kind: operationRoute, RouteTarget: &bridgeTarget, DependsOn: []string{prepare.ID}}
	stop := serving.Action{ID: "stop-a", Kind: operationStop, Instance: a.ID, Node: a.Node, DependsOn: []string{bridge.ID}}
	next := serving.Action{ID: "next-handoff", Kind: operationRoute, RouteTarget: &nextTarget, DependsOn: []string{bridge.ID, stop.ID}}
	goal := bridgeTarget
	goal.ID, goal.Actions = "ready-before-result", []serving.Action{prepare, bridge, stop, next}
	c.state.Snapshot = snapshot
	installTestTransition(c, &goal)
	// Native B is Ready before the controller receives its prepare result.
	pending := prepare
	pending.Status = nativeOperationRunning
	c.runs[prepare.ID] = &actionRun{action: pending, owner: goal.ID,
		result: &OperationResult{Operation: Operation{ID: prepare.ID, Kind: operationPrepare, Target: &goal}, Status: starting}}
	prepared.Store(true)
	snapshot, err = c.capture(ctx, time.Now(), c.config)
	if err != nil {
		t.Fatal(err)
	}
	c.state.Snapshot = snapshot
	c.launchTargetLocked(goal, c.config)
	c.wg.Wait()
	var subset *actionRun
	for _, run := range c.runs {
		if run.action.From == readySubsetRoute {
			subset = run
		}
	}
	if subset == nil || subset.action.Status != nativeOperationComplete || subset.result.Actual.Routing[uid][b.ID] != 1000 {
		t.Fatalf("Ready B did not receive a real subset ACK before prepare completion: %+v", subset)
	}
	if c.runs[bridge.ID] != nil || c.runs[stop.ID] != nil {
		t.Fatal("bridge/stop ran before the prepare result")
	}
	for _, status := range []string{nativeOperationRunning, nativeOperationFailed, nativeOperationPartial} {
		t.Run(status, func(t *testing.T) {
			c.runs[prepare.ID].action.Status = status
			snapshot, err = c.capture(ctx, time.Now(), c.config)
			if err != nil {
				t.Fatal(err)
			}
			c.state.Snapshot = snapshot
			c.launchTargetLocked(goal, c.config)
			c.wg.Wait()
			if c.runs[stop.ID] != nil {
				t.Fatalf("stop ran after subset ACK with prepare %s", status)
			}
		})
	}
	c.runs[prepare.ID].action.Status = nativeOperationComplete
	c.runs[prepare.ID].result = &OperationResult{Operation: Operation{ID: prepare.ID, Kind: operationPrepare, Target: &goal,
		Prepare: &PrepareSpec{Instance: b}}, Status: nativeOperationComplete, EndedAt: time.Now()}
	snapshot, err = c.capture(ctx, time.Now(), c.config)
	if err != nil {
		t.Fatal(err)
	}
	c.state.Snapshot = snapshot
	c.launchTargetLocked(goal, c.config)
	c.wg.Wait()
	parent := c.runs[transitionRunKind+"/"+goal.ID]
	if c.runs[bridge.ID] != nil || !parent.noEffectRoutes[bridge.ID] {
		t.Fatal("ACK-covered bridge was not omitted")
	}
	if run := c.runs[stop.ID]; run == nil || run.action.Status != nativeOperationComplete {
		t.Fatalf("real source stop did not complete: %+v", run)
	}
	snapshot, err = c.capture(ctx, time.Now(), c.config)
	if err != nil {
		t.Fatal(err)
	}
	c.state.Snapshot = snapshot
	c.launchTargetLocked(goal, c.config)
	c.wg.Wait()
	if run := c.runs[next.ID]; run == nil || run.action.Status != nativeOperationComplete {
		t.Fatalf("next bridge did not complete after omitted bridge and stop: %+v", run)
	}
}
