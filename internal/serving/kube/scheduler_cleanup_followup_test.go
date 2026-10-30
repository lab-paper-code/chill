package kube

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// Two physical destinations share a completed route receipt. The original
// retirement DAG remains pending, with either A alone or both UIDs reserved.
func routeOnlyHandoffPair(t *testing.T, both bool) (*SchedulerController, *serving.SchedulerCandidate, string, string) {
	t.Helper()
	c, _, physical := acknowledgedHandoffFixture(t)
	c.ctx = context.Background()
	a := c.state.Snapshot.Instances[0]
	definitions := c.runtime.SchedulerInputs(time.Now).Workloads
	bDefinition := *definitions[0]
	bDefinition.Name, bDefinition.UID = "independent", "independent-uid"
	if err := c.runtime.Publish(&bDefinition); err != nil {
		t.Fatal(err)
	}
	b := a
	profile := *a.Profile
	profile.ID, profile.NodeName, profile.Condition.NodeUID = "independent-profile", "independent-node", "independent-node-uid"
	b.ID, b.Node, b.Profile, b.WorkloadUID = "independent-physical", serving.NodeReference{Name: profile.NodeName, UID: profile.Condition.NodeUID}, &profile, bDefinition.UID
	other := physical
	nativeState := *physical.State.Native
	other.State.Native = &nativeState
	other.State.ID, other.State.Node, other.State.Profile = b.ID, b.Node, profile.ID
	other.State.Native.Metadata.ProfileID, other.State.Native.Metadata.Policy.ProfileID = profile.ID, profile.ID
	other.Profile = &profile
	c.runtime.SetProfiles([]serving.Profile{*a.Profile, profile})
	if err := c.runtime.InstallInstances([]serving.ReadyInstance{physical, other}); err != nil {
		t.Fatal(err)
	}
	initial := serving.SchedulerCandidate{Instances: []serving.SchedulerInstance{a, b}, Routing: map[string]map[string]int32{a.WorkloadUID: {a.ID: 1}, b.WorkloadUID: {b.ID: 1}}}
	if err := c.runtime.ApplySchedulerTarget(initial); err != nil {
		t.Fatal(err)
	}
	c.state.Snapshot.Instances, c.state.Snapshot.Routing = initial.Instances, initial.Routing
	c.state.Snapshot.Workloads = append(definitions, &bDefinition)
	goal := serving.SchedulerCandidate{ID: "handoff-owner", Instances: []serving.SchedulerInstance{a}, Routing: map[string]map[string]int32{a.WorkloadUID: {a.ID: 1}}, Actions: []serving.Action{
		{ID: "pair-final-route", Kind: operationRoute},
		{ID: "pending-retirement", Kind: operationDrain, Node: a.Node, Instance: "retiree", DependsOn: []string{"unfinished-stop"}},
	}}
	if both {
		goal.Instances = initial.Instances
		goal.Routing = initial.Routing
	}
	c.runs = map[string]*actionRun{}
	installTestTransition(c, &goal)
	parent := c.runs[transitionRunKind+"/"+goal.ID]
	parent.cleanup = true
	uids := []string{a.WorkloadUID}
	if both {
		uids = append(uids, b.WorkloadUID)
	}
	op := Operation{Kind: operationRoute, Target: &goal, WorkloadUIDs: uids}
	final := &actionRun{owner: goal.ID, reserved: true, scope: c.operationScope(op), action: goal.Actions[0], result: &OperationResult{Operation: op, Status: nativeOperationComplete}}
	final.action.Status = nativeOperationComplete
	c.runs[final.action.ID] = final
	return c, &goal, a.WorkloadUID, b.WorkloadUID
}

func TestRouteOnlyHandoffSplitsIndependentLifecycleTarget(t *testing.T) {
	c, goal, aUID, bUID := routeOnlyHandoffPair(t, false)
	a, oldB := c.state.Snapshot.Instances[0], c.state.Snapshot.Instances[1]
	newB := oldB
	newB.ID, newB.Node = "new-independent-slot", serving.NodeReference{Name: "new-node", UID: "new-node-uid"}
	target := serving.SchedulerCandidate{ID: "mixed-target", Instances: []serving.SchedulerInstance{a, newB}, Routing: map[string]map[string]int32{aUID: {a.ID: 1000}, bUID: {newB.ID: 1000}}, Actions: []serving.Action{
		{ID: "independent-power", Kind: operationPower, Node: newB.Node, To: "max"},
		{ID: "independent-prepare", Kind: operationPrepare, Node: newB.Node, Instance: newB.ID, DependsOn: []string{"independent-power"}},
		{ID: "mixed-route", Kind: operationRoute, DependsOn: []string{"independent-prepare"}},
		{ID: "independent-stop", Kind: operationStop, Node: oldB.Node, Instance: oldB.ID, DependsOn: []string{"mixed-route"}},
		{ID: "independent-drain", Kind: operationDrain, Node: oldB.Node, Instance: oldB.ID, DependsOn: []string{"independent-stop"}},
		{ID: "independent-remove", Kind: operationRemove, Node: oldB.Node, Instance: oldB.ID, DependsOn: []string{"independent-drain"}},
	}}
	started, release := make(chan OperationResult, 2), make(chan struct{})
	defer func() { close(release); c.wg.Wait() }()
	c.launch(target, SchedulerControllerConfig{OnAction: func(result OperationResult) error {
		if result.Status == "starting" {
			started <- result
			<-release
		}
		return context.Canceled // Observe admission; do not execute test power commands.
	}})
	seen := map[string]OperationResult{}
	for range 2 {
		select {
		case result := <-started:
			seen[result.Operation.Kind] = result
		case <-time.After(5 * time.Second):
			t.Fatal("mixed target blocked cleanup route or independent power")
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	route := seen[operationRoute].Operation
	if len(route.WorkloadUIDs) != 1 || route.WorkloadUIDs[0] != aUID || c.runs[route.ID].owner != goal.ID || len(route.Target.Actions[0].DependsOn) != 0 {
		t.Fatal("cleanup route did not inherit an independent, Ready-only owner scope")
	}
	independent := c.runs[transitionRunKind+"/"+target.ID]
	if independent == nil || independent.owner == goal.ID || len(independent.result.Operation.Target.Actions) != len(target.Actions) || len(independent.scope.Workloads) != 1 || independent.scope.Workloads[0] != bUID {
		t.Fatal("independent lifecycle DAG was dropped or kept cleanup ownership")
	}
	if _, leaked := independent.result.Operation.Target.Routing[aUID]; leaked {
		t.Fatal("independent parent reacquired cleanup UID")
	}
	if seen[operationPower].Operation.ID != "independent-power" || !slices.ContainsFunc(independent.result.Operation.Target.Actions, func(a serving.Action) bool { return a.ID == "independent-remove" }) {
		t.Fatal("independent power/prepare/retirement obligations were lost")
	}
}

func TestRouteOnlyHandoffRetainsEveryUIDEffectProof(t *testing.T) {
	for _, earlierRefresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "unrefreshed-UID", true: "earlier-refreshed-UID"}[earlierRefresh], func(t *testing.T) {
			c, goal, aUID, bUID := routeOnlyHandoffPair(t, true)
			refresh := func(uid string) string {
				t.Helper()
				i := c.state.Snapshot.Instances[0]
				if uid == bUID {
					i = c.state.Snapshot.Instances[1]
				}
				target := serving.SchedulerCandidate{ID: "refresh-" + uid, Instances: []serving.SchedulerInstance{i}, Routing: map[string]map[string]int32{uid: {i.ID: 1000}}, Actions: []serving.Action{{ID: "route-" + uid, Kind: operationRoute}}}
				op := Operation{Kind: operationRoute, Target: &target, WorkloadUIDs: []string{uid}}
				run := &actionRun{owner: target.ID, action: target.Actions[0], result: &OperationResult{Operation: op, Status: nativeOperationComplete}}
				if !c.reserveRun(run.action.ID, run, op) {
					t.Fatal("Ready-only refresh was refused")
				}
				run.action.Status = nativeOperationComplete
				if err := c.runtime.ApplySchedulerWorkloadsTarget(target, []string{uid}); err != nil {
					t.Fatal(err)
				}
				return run.action.ID
			}
			aRoute := refresh(aUID)
			lostUID, lostRoute := bUID, goal.Actions[0].ID
			if earlierRefresh {
				refresh(bUID)
				lostUID, lostRoute = aUID, aRoute
			}
			empty := serving.SchedulerCandidate{Routing: map[string]map[string]int32{lostUID: {}}}
			if err := c.runtime.ApplySchedulerUnrouteTarget(empty, []string{lostUID}); err != nil {
				t.Fatal(err)
			}
			c.state.Snapshot.Routing, _ = c.runtime.SchedulerRouting()
			snapshot, lost := c.reconcileSnapshot(c.state.Snapshot, time.Now(), time.Minute)
			parent := c.runs[transitionRunKind+"/"+goal.ID]
			if len(lost) != 1 || c.runs[lostRoute].action.Status != nativeOperationFailed || parent.action.Status != nativeOperationFailed || !parent.reserved || len(snapshot.Reservations) == 0 {
				t.Fatalf("supersession erased UID %s proof: lost=%+v parent=%+v", lostUID, lost, parent.action)
			}
		})
	}
}
