package kube

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// full-E5-5-02: the final route completed at 07:06:35, but the next three
// demand ticks (95.3, 100.2, 97.5/s) were Fixed until old-source removal.
func TestCleanupKeepsPhysicalOwnershipButAllowsSerializedReadyRoutesAC38(t *testing.T) {
	c, _, physical := acknowledgedHandoffFixture(t)
	c.ctx = context.Background()
	base := c.state.Snapshot.Instances[0]
	uid := base.WorkloadUID
	goal := serving.SchedulerCandidate{ID: "cleanup-owner", Routing: map[string]map[string]int32{uid: {}}, Actions: []serving.Action{
		{ID: "final-route", Kind: operationRoute},
		{ID: "old-stop", Kind: operationStop, Instance: "old-source", Node: base.Node, DependsOn: []string{"final-route"}},
		{ID: "old-drain", Kind: operationDrain, Instance: "old-source", Node: base.Node, DependsOn: []string{"old-stop"}},
		{ID: "old-remove", Kind: operationRemove, Instance: "old-source", Node: base.Node, DependsOn: []string{"old-drain"}},
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
	final := &actionRun{owner: goal.ID, reserved: true, action: goal.Actions[0], result: &OperationResult{Operation: Operation{Kind: operationRoute, Target: &goal, WorkloadUIDs: []string{uid}}, Status: nativeOperationComplete}}
	final.action.Status = nativeOperationRunning
	c.runs[final.action.ID] = final
	now := time.Now()
	before, _ := c.reconcileSnapshot(c.state.Snapshot, now, time.Minute)
	if len(before.Reservations) != 1 || len(before.Reservations[0].RouteOnly) != 0 {
		t.Fatal("unfinished final route opened cleanup routing")
	}
	final.action.Status = nativeOperationComplete
	for tick, rate := range []float64{95.3, 100.2, 97.5} {
		snapshot, lost := c.reconcileSnapshot(c.state.Snapshot, now.Add(time.Duration(tick+1)*10*time.Second), time.Minute)
		c.state.Snapshot = snapshot
		if len(lost) != 0 || !parent.cleanup || parent.action.Status != nativeOperationRunning {
			t.Fatalf("tick %.1f/s lost successful cleanup: %+v", rate, lost)
		}
		r := snapshot.Reservations[0]
		if len(r.RouteOnly[uid]) != 3 || slices.Contains(r.RouteOnly[uid], old.ID) || !slices.Contains(r.PodUIDs, old.PodUID) || !slices.Contains(r.Nodes, old.Node) || r.Target != &goal {
			t.Fatalf("cleanup lost ownership or admitted old source: %+v", r)
		}
		refresh := goal
		refresh.ID = fmt.Sprintf("refresh-%d", tick)
		refresh.Routing = map[string]map[string]int32{uid: {"ready-0": 97, "ready-1": 252, "ready-2": 651}}
		refresh.Actions = []serving.Action{{ID: refresh.ID, Kind: operationRoute}}
		op := Operation{Kind: operationRoute, Target: &refresh, WorkloadUIDs: []string{uid}}
		// No source resurrection, new preparation/power, or policy change.
		for _, kind := range []string{operationPrepare, operationPower, "retiring-source", "policy"} {
			bad := op
			badTarget := refresh
			badTarget.Actions = slices.Clone(refresh.Actions)
			bad.Target = &badTarget
			switch kind {
			case operationPrepare, operationPower:
				bad.Kind = kind
				badTarget.Actions[0].Kind = kind
			case "retiring-source":
				badTarget.Routing = map[string]map[string]int32{uid: maps.Clone(refresh.Routing[uid])}
				badTarget.Routing[uid][old.ID] = 1
			case "policy":
				badTarget.Instances = slices.Clone(refresh.Instances)
				badTarget.Instances[0].MaxBatch++
			}
			if c.reserveRun("rejected-"+kind, &actionRun{owner: "different-owner"}, bad) {
				t.Fatalf("cleanup admitted %s", kind)
			}
		}
		run := &actionRun{owner: refresh.ID, action: refresh.Actions[0], result: &OperationResult{Operation: op, Status: nativeOperationRunning}}
		run.action.Status = nativeOperationRunning
		parent.returnObserved = true
		if !c.reserveRun(refresh.ID, run, op) || run.owner != parent.owner {
			t.Fatal("Ready route refresh did not inherit cleanup owner")
		}
		if parent.returnObserved {
			t.Fatal("adopted route retained an earlier return proof")
		}
		for _, instance := range parent.result.Before.Instances {
			if !parent.mayTouch[instance.ID] {
				t.Fatalf("adopted route omitted baseline executor %s, including excluded sources", instance.ID)
			}
		}
		if c.reserveRun("concurrent-route", &actionRun{owner: "another"}, op) {
			t.Fatal("cleanup policy ACKs overlapped")
		}
		if !c.dependenciesReady(goal.Actions[1], goal) {
			t.Fatal("safe old-source stop lost its completed handoff dependency")
		}
		if actions, partial := c.intermediateActions(goal); partial != nil || len(actions) != len(goal.Actions) {
			t.Fatal("refresh resurrected the original handoff distribution")
		}
		if err := c.runtime.ApplySchedulerTarget(refresh); err != nil {
			t.Fatal(err)
		}
		c.state.Snapshot.Routing = refresh.Routing
		run.action.Status, run.result.Status = nativeOperationComplete, nativeOperationComplete
	}
	assertCleanupCompletion(t, c, goal, old, now)
}

func assertCleanupCompletion(t *testing.T, c *SchedulerController, goal serving.SchedulerCandidate, old serving.SchedulerInstance, now time.Time) {
	t.Helper()
	parent := c.runs[transitionRunKind+"/"+goal.ID]
	final := c.runs[goal.Actions[0].ID]
	// A newer acknowledged route supersedes only the old route proof, not its
	// immutable receipt, stop/drain/remove duties, or physical responsibility.
	snapshot, lost := c.reconcileSnapshot(c.state.Snapshot, now.Add(40*time.Second), time.Minute)
	if len(lost) != 0 || parent.action.Status != nativeOperationRunning || final.result.Status != nativeOperationComplete {
		t.Fatalf("route supersession falsely failed parent: %+v", lost)
	}
	for _, action := range goal.Actions[1:] {
		c.runs[action.ID] = &actionRun{owner: goal.ID, action: action, result: &OperationResult{Operation: Operation{Kind: action.Kind}, Status: nativeOperationComplete}}
		c.runs[action.ID].action.Status = nativeOperationComplete
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
	snapshot, lost = c.reconcileSnapshot(snapshot, now.Add(50*time.Second), time.Minute)
	if len(lost) != 0 || parent.action.Status != nativeOperationComplete || len(snapshot.Reservations) != 0 {
		t.Fatalf("cleanup did not release only after all duties: %+v %+v", lost, snapshot.Reservations)
	}
}
