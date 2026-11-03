package kube

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	"github.com/lab-paper-code/chill/internal/serving"
)

// Handoff ACK preserves the frozen owner until stop/drain/remove complete.
func TestCleanupRetainsOwnershipUntilRetirementCompletesAC38(t *testing.T) {
	c, _, physical := acknowledgedHandoffFixture(t)
	c.ctx = context.Background()
	base := c.state.Snapshot.Instances[0]
	uid := base.WorkloadUID
	goal := serving.SchedulerCandidate{ID: "cleanup-owner", Routing: map[string]map[string]int32{uid: {}}, Actions: []serving.Action{
		{ID: "final-route", Kind: servingstate.OperationRoute},
		{ID: "old-stop", Kind: servingstate.OperationStop, Instance: "old-source", Node: base.Node, DependsOn: []string{"final-route"}},
		{ID: "old-drain", Kind: servingstate.OperationDrain, Instance: "old-source", Node: base.Node, DependsOn: []string{"old-stop"}},
		{ID: "old-remove", Kind: servingstate.OperationRemove, Instance: "old-source", Node: base.Node, DependsOn: []string{"old-drain"}},
	}}
	installed := make([]serving.ReadyInstance, 0, 4)
	for k, weight := range []int32{255, 663, 82} {
		i, p := base, physical
		i.ID, i.Weight = fmt.Sprintf("ready-%d", k), weight
		p.State.ID = i.ID
		goal.Instances = append(goal.Instances, i)
		goal.Routing[uid][i.ID] = weight
		installed = append(installed, p)
	}
	old := base
	old.ID, old.PodUID, old.WorkloadUID, old.OwnerWorkloadUID = "old-source", "old-pod", "", uid
	oldPhysical := physical
	oldPhysical.State.ID, oldPhysical.State.PodUID = old.ID, old.PodUID
	installed = append(installed, oldPhysical)
	if err := c.runtime.InstallInstances(installed); err != nil {
		t.Fatal(err)
	}
	if err := c.runtime.ApplySchedulerTarget(goal); err != nil {
		t.Fatal(err)
	}
	c.state.Snapshot.Instances = append(slices.Clone(goal.Instances), old)
	c.state.Snapshot.Routing = goal.Routing
	c.runs = map[string]*actionRun{}
	installTestTransition(c, &goal)
	parent := c.runs[transitionRunKind+"/"+goal.ID]
	for _, instance := range c.state.Snapshot.Instances {
		parent.result.Before.Instances = append(parent.result.Before.Instances, serving.InstanceState{ID: instance.ID, PodUID: instance.PodUID, Node: instance.Node})
	}
	final := &actionRun{owner: goal.ID, reserved: true, action: goal.Actions[0], result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: servingstate.OperationRoute, Target: &goal, WorkloadUIDs: []string{uid}}, Status: servingstate.NativeOperationComplete}}
	final.action.Status = servingstate.NativeOperationRunning
	c.runs[final.action.ID] = final
	now := time.Now()
	before, _ := c.reconcileSnapshot(c.state.Snapshot, now, time.Minute, func() serving.SchedulerSettings { return c.config.Settings }, c.runtime)
	if len(before.Reservations) != 1 {
		t.Fatal("unfinished final route lost its reservation")
	}
	final.action.Status = servingstate.NativeOperationComplete
	for tick, rate := range []float64{95.3, 100.2, 97.5} {
		snapshot, lost := c.reconcileSnapshot(c.state.Snapshot, now.Add(time.Duration(tick+1)*10*time.Second), time.Minute, func() serving.SchedulerSettings { return c.config.Settings }, c.runtime)
		c.state.Snapshot = snapshot
		if len(lost) != 0 || parent.action.Status != servingstate.NativeOperationRunning {
			t.Fatalf("tick %.1f/s lost successful cleanup: %+v", rate, lost)
		}
		r := snapshot.Reservations[0]
		if !slices.Contains(r.PodUIDs, old.PodUID) || !slices.Contains(r.Nodes, old.Node) || r.Target != &goal {
			t.Fatalf("cleanup lost its frozen ownership: %+v", r)
		}
		refresh := goal
		refresh.ID = fmt.Sprintf("refresh-%d", tick)
		refresh.Routing = map[string]map[string]int32{uid: {"ready-0": 97, "ready-1": 252, "ready-2": 651}}
		refresh.Actions = []serving.Action{{ID: refresh.ID, Kind: servingstate.OperationRoute}}
		op := servingstate.Operation{Kind: servingstate.OperationRoute, Target: &refresh, WorkloadUIDs: []string{uid}}
		run := &actionRun{owner: refresh.ID, action: refresh.Actions[0], result: &servingstate.OperationResult{Operation: op, Status: servingstate.NativeOperationRunning}}
		run.action.Status = servingstate.NativeOperationRunning
		parent.returnObserved = true
		if c.reserveRun(refresh.ID, run, op) || run.owner != refresh.ID {
			t.Fatal("Ready route refresh bypassed the retirement owner")
		}
		if !c.dependenciesReady(goal.Actions[1], goal, c.runtime) {
			t.Fatal("safe old-source stop lost its completed handoff dependency")
		}
		if actions, partial := c.intermediateActions(goal); partial != nil || len(actions) != len(goal.Actions) {
			t.Fatal("rejected refresh changed the original handoff")
		}
	}
	assertCleanupCompletion(t, c, goal, old, now)
}

func assertCleanupCompletion(t *testing.T, c *SchedulerController, goal serving.SchedulerCandidate, old serving.SchedulerInstance, now time.Time) {
	t.Helper()
	parent := c.runs[transitionRunKind+"/"+goal.ID]
	final := c.runs[goal.Actions[0].ID]
	// All original retirement duties remain part of the same frozen DAG.
	snapshot, lost := c.reconcileSnapshot(c.state.Snapshot, now.Add(40*time.Second), time.Minute, func() serving.SchedulerSettings { return c.config.Settings }, c.runtime)
	if len(lost) != 0 || parent.action.Status != servingstate.NativeOperationRunning || final.result.Status != servingstate.NativeOperationComplete {
		t.Fatalf("unchanged handoff falsely failed parent: %+v", lost)
	}
	for _, action := range goal.Actions[1:] {
		c.runs[action.ID] = &actionRun{owner: goal.ID, action: action, result: &servingstate.OperationResult{Operation: servingstate.Operation{Kind: action.Kind}, Status: servingstate.NativeOperationComplete}}
		c.runs[action.ID].action.Status = servingstate.NativeOperationComplete
	}
	if stopped, err := c.runtime.StopSchedulerInstance(old.ID); err != nil || !stopped {
		t.Fatalf("old source stop: %v %v", stopped, err)
	}
	if drained, err := c.runtime.DetachInstance(old.ID); err != nil || !drained {
		t.Fatalf("old source drain: %v %v", drained, err)
	}
	if err := c.runtime.ConfirmInstanceStopped(old.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.runtime.RetirePod(old.PodUID); err != nil {
		t.Fatal(err)
	}
	snapshot.Instances = slices.Clone(goal.Instances)
	snapshot, lost = c.reconcileSnapshot(snapshot, now.Add(50*time.Second), time.Minute, func() serving.SchedulerSettings { return c.config.Settings }, c.runtime)
	if len(lost) != 0 || parent.action.Status != servingstate.NativeOperationComplete || len(snapshot.Reservations) != 0 {
		t.Fatalf("cleanup did not release only after all duties: %+v %+v", lost, snapshot.Reservations)
	}
}
