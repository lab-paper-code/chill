package kube

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

const commitRouteCanceled = "route-cancel"
const commitRouteFailed = "failed-route"
const commitRouteRunning = "route-running"
const commitActionPlanned = "planned"

// SCH-55/56, AC-42: raw E5-5-7:7939 sees 95.1 before :7940 final
// route and :7963/:7965 stops. No unstarted suffix may commit afterward.
func TestResurgeClosesOnlyUnstartedFrozenSuffix(t *testing.T) {
	for _, phase := range []string{"before-prepare", "before-route", commitRouteRunning, commitRouteCanceled, "after-ack", commitRouteFailed} {
		t.Run(phase, func(t *testing.T) {
			now := time.Now()
			node := serving.NodeReference{Name: "old", UID: "old-uid"}
			old := serving.SchedulerInstance{ID: "old", Node: node, WorkloadUID: "mnv", Ready: true}
			next := serving.SchedulerInstance{ID: "prepared", Node: serving.NodeReference{Name: "new", UID: "new-uid"}, WorkloadUID: "mnv", Ready: true}
			c := &SchedulerController{ctx: context.Background(), runtime: &serving.Runtime{}, capacity: serving.NewCapacityTable(1, nil, nil), runs: map[string]*actionRun{}}
			c.config.Settings = serving.SchedulerSettings{ObservationWindow: time.Second, ControlInterval: time.Second, MaxObservationAge: time.Minute, AutonomousWorkloads: []string{"mnv"}}
			// If a regression reaches remote execution, retain its attempted run
			// as evidence without writing an external effect.
			c.config.OnAction = func(OperationResult) error { return errors.New("unexpected effect authorization") }
			c.state.Snapshot = serving.SchedulerSnapshot{At: now, ObservationEnd: now, ObservedSince: now.Add(-time.Minute), Workloads: []*serving.Definition{{UID: "mnv"}}, Instances: []serving.SchedulerInstance{old, next}, Routing: map[string]map[string]int32{"mnv": {old.ID: 1000}}}
			if phase == "before-prepare" {
				c.state.Snapshot.ScaleDown = map[string]serving.SchedulerDownState{"mnv": {Rate: 95.1, WindowMax: 95.1}}
			}
			goal := serving.SchedulerCandidate{ID: "raw-owner", Instances: []serving.SchedulerInstance{next}, Routing: map[string]map[string]int32{"mnv": {next.ID: 1000}}, Actions: []serving.Action{
				{ID: "prepare", Kind: operationPrepare, Instance: next.ID, Status: nativeOperationComplete},
				{ID: "final-route", Kind: operationRoute, Status: nativeOperationComplete, DependsOn: []string{"prepare"}},
				{ID: "stop", Kind: operationStop, Instance: old.ID, Node: node, Status: nativeOperationComplete, DependsOn: []string{"final-route"}},
			}}
			c.launch(goal, c.config)
			parent := c.runs[transitionRunKind+"/"+goal.ID]
			if parent == nil {
				t.Fatal("fixture did not admit a frozen owner")
			}
			for k := range parent.result.Operation.Target.Actions {
				parent.result.Operation.Target.Actions[k].Status = commitActionPlanned
			}
			prepare := &actionRun{owner: goal.ID, reserved: true, action: serving.Action{ID: "prepare", Kind: operationPrepare, Instance: next.ID, Status: nativeOperationComplete}, result: &OperationResult{Status: nativeOperationComplete}}
			c.runs["prepare"] = prepare
			var route *actionRun
			if phase == commitRouteRunning || phase == commitRouteCanceled || phase == "after-ack" || phase == commitRouteFailed {
				status := nativeOperationComplete
				if phase == commitRouteRunning {
					status = nativeOperationRunning
				} else if phase == commitRouteCanceled {
					status = nativeOperationCancel
				} else if phase == commitRouteFailed {
					status = nativeOperationPartial
				}
				route = &actionRun{owner: goal.ID, reserved: true, action: serving.Action{ID: "final-route", Kind: operationRoute, Status: status}, result: &OperationResult{Status: status, Error: "original receipt"}}
				c.runs["final-route"] = route
			}
			if phase == commitRouteFailed {
				// The normal terminal-failure reconciler already owns this case.
				parent.action.Status, parent.result.Status = nativeOperationFailed, nativeOperationFailed
			}
			c.state.Snapshot.ScaleDown = map[string]serving.SchedulerDownState{"mnv": {Rate: 95.1, WindowMax: 95.1, Supply: 47.78107637511205, Allowed: false}}
			c.mu.Lock()
			c.launchTargetLocked(*parent.result.Operation.Target, c.config)
			c.mu.Unlock()
			c.wg.Wait()
			if parent.action.Status != nativeOperationFailed || !parent.reserved || parent.returnObserved {
				t.Fatalf("unsafe owner return/status: %+v", parent)
			}
			if c.runs["stop"] != nil || route == nil && c.runs["final-route"] != nil {
				t.Fatal("unstarted reduction suffix was authorized")
			}
			if c.runs["prepare"] != prepare || !prepare.reserved || prepare.action.Status != nativeOperationComplete {
				t.Fatal("aborting suffix removed prepared capacity or its responsibility")
			}
			if route != nil && (c.runs["final-route"] != route || route.result.Error != "original receipt") {
				t.Fatal("aborting suffix rewrote the actual route/failure receipt")
			}
			if (phase == commitRouteRunning || phase == commitRouteCanceled) && c.ownerReturnTerminal(parent) {
				t.Fatal("active route sibling permitted reservation return")
			}
			assertCommitSuffixClosed(t, c, parent, phase == commitRouteFailed)
		})
	}
}

func assertCommitSuffixClosed(t *testing.T, c *SchedulerController, parent *actionRun, alreadyFailed bool) {
	t.Helper()
	if alreadyFailed {
		return
	}
	for _, a := range parent.result.Operation.Target.Actions {
		if c.runs[a.ID] == nil && (a.Status != nativeOperationCancel || a.Reason == "") {
			t.Fatal("unstarted suffix was not closed as no-effect")
		}
	}
}

// Continue must refresh demand itself, not retain Tick's last ScaleDown map.
func TestContinueObservesDemandBeforeGuardedCommitWithoutTick(t *testing.T) {
	c, prepared := tickReadySubsetController(t, nil)
	prepared.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snapshot, err := c.capture(ctx, time.Now(), c.config)
	if err != nil {
		t.Fatal(err)
	}
	uid := snapshot.Workloads[0].UID
	old, next := snapshot.Instances[0], snapshot.Instances[1]
	old.WorkloadUID, next.WorkloadUID = uid, uid
	c.config.Preparations = map[string]PrepareSpec{next.Profile.ID: {Instance: next}}
	c.state.Snapshot = snapshot
	c.state.Snapshot.Instances = []serving.SchedulerInstance{old, next}
	goal := serving.SchedulerCandidate{ID: "continue-reduction", Instances: []serving.SchedulerInstance{next},
		Routing: map[string]map[string]int32{uid: {next.ID: 1000}}, Actions: []serving.Action{
			{ID: "continue-route", Kind: operationRoute, Status: nativeOperationComplete},
			{ID: "continue-stop", Kind: operationStop, Instance: old.ID, Node: old.Node, Status: nativeOperationComplete, DependsOn: []string{"continue-route"}},
		}}
	c.launch(goal, c.config)
	parent := c.runs[transitionRunKind+"/"+goal.ID]
	if parent == nil {
		t.Fatal("missing frozen owner")
	}
	for k := range parent.result.Operation.Target.Actions {
		parent.result.Operation.Target.Actions[k].Status = commitActionPlanned
	}
	c.config.Settings.MinimumDemand = map[string]float64{uid: 95.1}
	before := c.State().Decision.At
	if err := c.Continue(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	state := c.State()
	if down, ok := state.Snapshot.ScaleDown[uid]; !ok || down.Rate < 95.1 {
		t.Fatalf("Continue did not Observe fresh demand: %+v", state.Snapshot.ScaleDown)
	}
	if parent.action.Status != nativeOperationFailed || !parent.reserved || c.runs["continue-route"] != nil || c.runs["continue-stop"] != nil {
		t.Fatal("Continue bypassed frozen-goal commit guard")
	}
	if !state.Decision.At.Equal(before) {
		t.Fatal("Continue evaluated a new candidate")
	}
}
