package kube

import (
	"maps"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

func TestRebasePreservesFrozenRoutingIntentOutsidePhysicalScope(t *testing.T) {
	c, base, source := acknowledgedHandoffFixture(t)
	uid := base.Instances[0].WorkloadUID
	var instances []serving.SchedulerInstance
	var all []serving.ReadyInstance
	var profiles []serving.Profile
	for _, id := range []string{"JetsonX", "rasp5", "JetsonO"} {
		instance := base.Instances[0]
		profile := *instance.Profile
		profile.ID, profile.NodeName, profile.Condition.NodeUID = id, id, id
		instance.ID, instance.Node, instance.Profile = id, serving.NodeReference{Name: id, UID: id}, &profile
		physical := source
		physical.Profile = &profile
		physical.State.ID, physical.State.Node, physical.State.Profile = id, instance.Node, id
		native := *source.State.Native
		native.Metadata.ProfileID, native.Metadata.Policy.ProfileID = id, id
		physical.State.Native = &native
		instances, all, profiles = append(instances, instance), append(all, physical), append(profiles, profile)
	}
	c.runtime.SetProfiles(profiles)
	if err := c.runtime.InstallInstances(all); err != nil {
		t.Fatal(err)
	}
	current := map[string]map[string]int32{uid: {"JetsonX": 52, "rasp5": 605, "JetsonO": 343}}
	c.state.Snapshot.Instances, c.state.Snapshot.Routing = instances, current
	goal := serving.SchedulerCandidate{ID: "control376", Instances: []serving.SchedulerInstance{instances[0], instances[2]},
		Routing: map[string]map[string]int32{uid: {"JetsonX": 537, "JetsonO": 463}}}
	c.runs[transitionRunKind+"/"+goal.ID] = &actionRun{owner: goal.ID, started: time.Unix(1, 0),
		scope: resourceScope{Nodes: []serving.NodeReference{instances[1].Node}, Instances: []string{"rasp5"}},
		result: &servingstate.OperationResult{Operation: servingstate.Operation{Target: &goal},
			Before: servingstate.OperationState{Instances: []serving.InstanceState{all[1].State},
				Routing: map[string]map[string]int32{uid: {"JetsonX": 39, "rasp5": 450, "JetsonO": 255, "genesis2": 256}}}}}
	for _, test := range []struct {
		name      string
		weights   map[string]int32
		instances []serving.SchedulerInstance
	}{
		{name: "final", weights: goal.Routing[uid], instances: goal.Instances},
		{name: "bridge", weights: map[string]int32{"JetsonX": 800, "JetsonO": 200}, instances: goal.Instances},
		{name: "ready-subset", weights: map[string]int32{"JetsonX": 1000}, instances: goal.Instances[:1]},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected := serving.SchedulerCandidate{Instances: test.instances, Routing: map[string]map[string]int32{uid: test.weights}}
			c.mu.Lock()
			rebased, policies, touched := c.rebaseRouteLocked(selected, goal.ID, current, c.runtime.Instances())
			c.mu.Unlock()
			if !maps.Equal(rebased.Routing[uid], test.weights) {
				t.Fatalf("stage routing intent lost outside physical scope: got %v, want %v", rebased.Routing[uid], test.weights)
			}
			if len(policies) != 0 || len(touched[uid]) != 1 || touched[uid][0] != "rasp5" {
				t.Fatalf("routing delta expanded policy/writer ownership: policies=%v touched=%v", policies, touched)
			}
		})
	}
	t.Run("newer-owner-preserves-owned-share", func(t *testing.T) {
		goal.Instances = instances[:2]
		goal.Routing = map[string]map[string]int32{uid: {"JetsonX": 600, "rasp5": 400}}
		parent := c.runs[transitionRunKind+"/"+goal.ID]
		parent.result.Before.Routing = map[string]map[string]int32{uid: {"JetsonX": 1000}}
		current := map[string]map[string]int32{uid: {"JetsonX": 700, "JetsonO": 300}}
		c.routeWriters = map[string]servingstate.RouteWriters{uid: {"JetsonO": time.Unix(2, 0)}}
		c.mu.Lock()
		rebased, _, _ := c.rebaseRouteLocked(goal, goal.ID, current, c.runtime.Instances())
		c.mu.Unlock()
		want := map[string]int32{"JetsonX": 300, "rasp5": 400, "JetsonO": 300}
		if !maps.Equal(rebased.Routing[uid], want) {
			t.Fatalf("intent-only weights diluted owned share behind newer fence: got %v, want %v", rebased.Routing[uid], want)
		}
		if !maps.Equal(current[uid], map[string]int32{"JetsonX": 700, "JetsonO": 300}) {
			t.Fatalf("rebase mutated applied routing: %v", current)
		}
	})
}
