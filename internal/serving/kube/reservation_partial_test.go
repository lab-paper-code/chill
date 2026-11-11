package kube

import (
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

func TestReservationAllowsIndependentSameWorkloadPrepare(t *testing.T) {
	a := serving.NodeReference{Name: "a", UID: "a"}
	b := serving.NodeReference{Name: "b", UID: "b"}
	c := &reservationState{runs: map[string]*actionRun{}}
	c.state.Snapshot.Instances = []serving.SchedulerInstance{{ID: "existing-a", Node: a, WorkloadUID: "w"}, {ID: "existing-b", Node: b, WorkloadUID: "w"}}
	prepare := func(node serving.NodeReference, id string) servingstate.Operation {
		return servingstate.Operation{Kind: servingstate.OperationPrepare, Node: node, Instance: id, WorkloadUID: "w", Prepare: &servingstate.PrepareSpec{Owner: &serving.WorkloadReference{UID: "w"}}}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.reserveRun("a", &actionRun{}, prepare(a, "new-a")) {
		t.Fatal("first preparation refused")
	}
	if !c.reserveRun("b", &actionRun{}, prepare(b, "new-b")) {
		t.Fatal("preparation on independent node frozen by same workload")
	}
	if c.reserveRun("same-node", &actionRun{}, prepare(a, "other-a")) {
		t.Fatal("same physical node admitted twice")
	}
	if c.reserveRun("same-instance", &actionRun{}, prepare(serving.NodeReference{UID: "c"}, "new-a")) {
		t.Fatal("same instance admitted twice")
	}
}

func TestReservationRoutesComposeWhenNewerPlanFinishesFirst(t *testing.T) {
	c, base, source := acknowledgedHandoffFixture(t)
	uid := base.Instances[0].WorkloadUID
	all := []serving.ReadyInstance{source}
	instances := []serving.SchedulerInstance{base.Instances[0]}
	profiles := []serving.Profile{*source.Profile}
	for _, id := range []string{"a", "b"} {
		instance := base.Instances[0]
		profile := *instance.Profile
		profile.ID, profile.NodeName, profile.Condition.NodeUID = id, id, id
		instance.ID, instance.Node, instance.Profile = id, serving.NodeReference{Name: id, UID: id}, &profile
		physical := source
		physical.Profile = &profile
		physical.State.ID, physical.State.Node, physical.State.Profile = id, instance.Node, profile.ID
		native := *source.State.Native
		native.Metadata.ProfileID, native.Metadata.Policy.ProfileID = id, id
		physical.State.Native = &native
		instances, all, profiles = append(instances, instance), append(all, physical), append(profiles, profile)
	}
	instances[1].Ready, all[1].State.Ready = false, false
	c.runtime.SetProfiles(profiles)
	if err := c.runtime.InstallInstances(all); err != nil {
		t.Fatal(err)
	}
	initial := map[string]map[string]int32{uid: {source.State.ID: 1000}}
	c.state.Snapshot.Instances, c.state.Snapshot.Routing = instances, initial
	if err := c.runtime.ApplySchedulerWorkloadsTarget(serving.SchedulerCandidate{Instances: instances[:1], Routing: initial}, []string{uid}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	goals := map[string]serving.SchedulerCandidate{}
	for index, owner := range []string{"a", "b"} {
		instance := instances[index+1]
		weight := int32(300)
		if owner == "a" {
			weight = 1000
		}
		goal := serving.SchedulerCandidate{ID: owner, Instances: []serving.SchedulerInstance{instances[0], instance}, Routing: map[string]map[string]int32{uid: {source.State.ID: 1000 - weight, instance.ID: weight}}}
		operation := servingstate.Operation{Kind: transitionRunKind, Nodes: []serving.NodeReference{instance.Node}, WorkloadUIDs: []string{uid}, Target: &goal}
		parent := &actionRun{owner: owner, started: time.Unix(int64(index+1), 0), action: serving.Action{Kind: transitionRunKind, Status: servingstate.NativeOperationRunning}, result: &servingstate.OperationResult{Operation: operation, Before: servingstate.OperationState{Routing: initial}}}
		if !c.reserveRun(transitionRunKind+"/"+owner, parent, operation) {
			t.Fatalf("independent parent %s refused", owner)
		}
		goals[owner] = goal
	}
	for _, owner := range []string{"b", "a"} {
		if owner == "a" {
			instances[1].Ready, all[1].State.Ready = true, true
			if err := c.runtime.InstallInstances(all); err != nil {
				t.Fatal(err)
			}
		}
		current, _ := c.runtime.SchedulerRouting()
		route, policies, _ := c.rebaseRouteLocked(goals[owner], owner, current, c.runtime.Instances())
		operation := servingstate.Operation{Kind: servingstate.OperationRoute, Target: &route, WorkloadUIDs: []string{uid}, PolicyInstances: policies}
		run := &actionRun{owner: owner, started: time.Now(), proofParent: c.runs[transitionRunKind+"/"+owner], action: serving.Action{Kind: servingstate.OperationRoute, Status: servingstate.NativeOperationRunning}}
		if !c.reserveRun("route-"+owner, run, operation) {
			t.Fatalf("route %s blocked by independent prepare", owner)
		}
		racing := operation
		racing.PolicyInstances = []string{}
		if c.reserveRun("racing-route", &actionRun{owner: "third"}, racing) {
			t.Fatal("same workload route writes were not serialized")
		}
		if err := c.runtime.ApplySchedulerWorkloadsTarget(route, []string{uid}); err != nil {
			t.Fatal(err)
		}
		run.action.Status = servingstate.NativeOperationComplete
		run.result = &servingstate.OperationResult{Operation: operation, Status: servingstate.NativeOperationComplete}
		run.scope.Routing, run.reserved = nil, false
		if owner == "a" {
			c.releaseOwnerLocked(owner)
		}
		// B's route finishes first, but its physical cleanup still owns B.
	}
	actual, _ := c.runtime.SchedulerRouting()
	if actual[uid]["a"] != 700 || actual[uid]["b"] != 300 {
		t.Fatalf("older final route overwrote newer committed capacity: %+v", actual)
	}
	if !c.actionEffectPresent(c.state.Snapshot, c.runs["route-b"], c.runtime) {
		t.Fatal("composition invalidated an earlier owner's acknowledged committed route")
	}
	c.releaseOwnerLocked("b")
}

func TestRouteScopeDoesNotWithdrawUnavailableIndependentWorkload(t *testing.T) {
	const readyUID = "ready"
	c := &reservationState{runs: map[string]*actionRun{}}
	c.state.Snapshot.Routing = map[string]map[string]int32{"unavailable": {"old": 1000}}
	target := serving.SchedulerCandidate{Routing: map[string]map[string]int32{
		"unavailable": {}, readyUID: {"new": 1000},
	}}
	uids, empty := c.routeOperationScopeLocked(target, target, false)
	if empty || len(uids) != 1 || uids[0] != readyUID {
		t.Fatalf("an empty unavailable route blocked the independent positive workload: %v", uids)
	}
}

func TestRebaseDoesNotReviveAnotherOwnersWithdrawnRetiree(t *testing.T) {
	c, stale, physical := acknowledgedHandoffFixture(t)
	uid := stale.Instances[0].WorkloadUID
	empty := serving.SchedulerCandidate{Routing: map[string]map[string]int32{uid: {}}}
	if err := c.runtime.ApplySchedulerUnrouteTarget(empty, []string{uid}); err != nil {
		t.Fatal(err)
	}
	current, _ := c.runtime.SchedulerRouting()
	c.state.Snapshot.Routing = current
	c.runs[transitionRunKind+"/older"] = &actionRun{owner: "older", started: time.Unix(1, 0),
		result: &servingstate.OperationResult{Operation: servingstate.Operation{Target: &stale},
			Before: servingstate.OperationState{Routing: map[string]map[string]int32{uid: {physical.State.ID: 1000}}}}}
	c.runs[transitionRunKind+"/retiring"] = &actionRun{owner: "retiring", started: time.Unix(2, 0), reserved: true,
		action: serving.Action{Kind: transitionRunKind}, scope: resourceScope{Nodes: []serving.NodeReference{physical.State.Node}},
		result: &servingstate.OperationResult{Operation: servingstate.Operation{Target: &empty}}}
	c.mu.Lock()
	defer c.mu.Unlock()
	rebased, policies, _ := c.rebaseRouteLocked(stale, "older", current, c.runtime.Instances())
	if rebased.Routing[uid][physical.State.ID] > 0 || len(policies) != 0 {
		t.Fatalf("stale route revived another owner's withdrawn resource: route=%v policies=%v", rebased.Routing, policies)
	}
}

func TestResetInheritsContainedLocalReservation(t *testing.T) {
	node := serving.NodeReference{Name: "node", UID: "node"}
	c := &reservationState{runs: map[string]*actionRun{}}
	operation := servingstate.Operation{Kind: servingstate.OperationPrepare, Node: node, WorkloadUID: "workload"}
	run := &actionRun{action: serving.Action{Status: servingstate.NativeOperationPartial},
		result: &servingstate.OperationResult{Operation: operation, EndedAt: time.Now()}}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.reserveRun("prepare", run, operation) {
		t.Fatal("initial local reservation refused")
	}
	reset := c.operationScope(servingstate.Operation{Kind: servingstate.OperationReset, WorkloadUIDs: []string{"workload"}, Nodes: []serving.NodeReference{node}})
	owners, err := c.resetReservationOwnersLocked(reset, []serving.NodeReference{node})
	if err != nil || len(owners) != 1 || owners[0] != "prepare" {
		t.Fatalf("reset lost contained local ownership identity: owners=%v error=%v", owners, err)
	}
}

func TestMixedPhysicalWorkloadKeepsIndependentRouteDelta(t *testing.T) {
	c, target, first := acknowledgedHandoffFixture(t)
	uid := target.Instances[0].WorkloadUID
	a, b := target.Instances[0], target.Instances[0]
	b.ID, b.Node = "second-ready", serving.NodeReference{Name: "second", UID: "second"}
	profile := *b.Profile
	profile.ID, profile.NodeName, profile.Condition.NodeUID = "second-profile", b.Node.Name, b.Node.UID
	b.Profile = &profile
	second := first
	second.Profile = &profile
	second.State.ID, second.State.Node, second.State.Profile = b.ID, b.Node, profile.ID
	native := *first.State.Native
	native.Metadata.ProfileID, native.Metadata.Policy.ProfileID = profile.ID, profile.ID
	second.State.Native = &native
	c.runtime.SetProfiles([]serving.Profile{*a.Profile, profile})
	if err := c.runtime.InstallInstances([]serving.ReadyInstance{first, second}); err != nil {
		t.Fatal(err)
	}
	current := serving.SchedulerCandidate{Instances: []serving.SchedulerInstance{a, b},
		Routing: map[string]map[string]int32{uid: {a.ID: 700, b.ID: 300}}}
	if err := c.runtime.ApplySchedulerWorkloadsTarget(current, []string{uid}); err != nil {
		t.Fatal(err)
	}
	c.state.Snapshot.Instances, c.state.Snapshot.Routing = current.Instances, current.Routing
	u := a
	u.ID, u.Node, u.WorkloadUID = "preparing-u", serving.NodeReference{Name: "physical", UID: "physical"}, "physical-workload"
	goal := serving.SchedulerCandidate{ID: "mixed-owner", Instances: []serving.SchedulerInstance{a, b, u},
		Routing: map[string]map[string]int32{uid: {a.ID: 300, b.ID: 700}, u.WorkloadUID: {u.ID: 1000}},
		Actions: []serving.Action{{ID: "prepare-u", Kind: servingstate.OperationPrepare, Node: u.Node, Instance: u.ID}}}
	installTestTransition(c, &goal)
	parent := c.runs[transitionRunKind+"/"+goal.ID]
	parent.result.Before.Routing = current.Routing
	selected := controlledTarget(servingstate.Operation{Target: &goal, WorkloadUID: uid})
	c.mu.Lock()
	rebased, _, _ := c.rebaseRouteLocked(*selected, goal.ID, current.Routing, c.runtime.Instances())
	c.mu.Unlock()
	if err := c.runtime.ApplySchedulerWorkloadsTarget(rebased, []string{uid}); err != nil {
		t.Fatal(err)
	}
	applied, _ := c.runtime.SchedulerRouting()
	if applied[uid][a.ID] != 300 || applied[uid][b.ID] != 700 {
		t.Fatalf("another workload's physical action swallowed the independent route delta: %v", applied)
	}
}

func TestActionSnapshotRouteContainsOnlyOwnerControlledWorkloads(t *testing.T) {
	target := &serving.SchedulerCandidate{ID: "owner", Instances: []serving.SchedulerInstance{
		{ID: "owned", WorkloadUID: "owned-workload"},
		{ID: "stale-unrelated", WorkloadUID: "unrelated-workload"},
	}, Routing: map[string]map[string]int32{
		"owned-workload":     {"owned": 1000},
		"unrelated-workload": {"stale-unrelated": 1000},
	}, Actions: []serving.Action{{ID: "frozen-prepare", Kind: servingstate.OperationPrepare}}}
	action := serving.Action{ID: "owner-route", Kind: servingstate.OperationRoute, Status: servingstate.NativeOperationRunning, RouteTarget: target}
	c := &reservationState{runs: map[string]*actionRun{action.ID: {
		action: action, result: &servingstate.OperationResult{Operation: servingstate.Operation{
			Kind: servingstate.OperationRoute, Target: target, WorkloadUIDs: []string{"owned-workload"},
		}},
	}}}
	snapshot := serving.SchedulerSnapshot{Actions: c.actionsLocked(time.Now())}
	scoped := snapshot.Actions[0].RouteTarget
	if scoped == nil || len(scoped.Routing) != 1 || scoped.Routing["owned-workload"]["owned"] != 1000 ||
		len(scoped.Instances) != 1 || scoped.Instances[0].ID != "owned" || len(scoped.Actions) != 0 {
		t.Fatalf("snapshot leaked unrelated frozen route rows: %+v", scoped)
	}
	if len(target.Routing) != 2 || len(target.Instances) != 2 || len(target.Actions) != 1 || c.runs[action.ID].action.RouteTarget != target {
		t.Fatal("snapshot projection rewrote the owner's operation ledger")
	}
}
