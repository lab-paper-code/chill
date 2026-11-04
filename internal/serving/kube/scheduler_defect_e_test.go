package kube

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

func loseDefectEProbe(c *SchedulerController, host string) *atomic.Bool {
	lost := &atomic.Bool{}
	previous := c.observer.native.HTTPClient.Transport.(*http.Transport).Proxy
	c.observer.native.HTTPClient.Transport = &http.Transport{Proxy: func(request *http.Request) (*url.URL, error) {
		if lost.Load() && request.URL.Host == host {
			return nil, errors.New("executor unreachable")
		}
		return previous(request)
	}}
	return lost
}

// Five physical nodes, four real stop/drain/remove chains and one unavailable
// frontier reproduce the incident's ownership shape, not fabricated live receipts.
func TestDefectEFiveNodeOwnerReturnsOnlyProvedScopes(t *testing.T) {
	c := handoffController(t, "e", "f")
	snapshot := handoffSnapshot(t, c)
	uid := snapshot.Workloads[0].UID
	unroute := serving.SchedulerCandidate{ID: "withdraw-fixture", Routing: map[string]map[string]int32{uid: {}}}
	if result, err := c.operations.Execute(c.ctx, servingstate.Operation{ID: unroute.ID, Kind: servingstate.OperationUnroute, Target: &unroute}); err != nil {
		t.Fatalf("initial explicit withdrawal: %v %+v", err, result)
	}
	snapshot = handoffSnapshot(t, c)
	goal := serving.SchedulerCandidate{ID: "incident-shaped-owner", Routing: map[string]map[string]int32{uid: {}}}
	var unknown serving.SchedulerInstance
	for _, instance := range snapshot.Instances {
		if instance.Node.Name == "f" {
			unknown = instance
		}
		stop, drain, remove := "stop-"+instance.Node.UID, "drain-"+instance.Node.UID, "remove-"+instance.Node.UID
		goal.Actions = append(goal.Actions,
			serving.Action{ID: stop, Kind: servingstate.OperationStop, Node: instance.Node, Instance: instance.ID},
			serving.Action{ID: drain, Kind: servingstate.OperationDrain, Node: instance.Node, Instance: instance.ID, DependsOn: []string{stop}},
			serving.Action{ID: remove, Kind: servingstate.OperationRemove, Node: instance.Node, Instance: instance.ID, DependsOn: []string{drain}})
	}
	if len(snapshot.Instances) != 5 || unknown.ID == "" {
		t.Fatalf("five exact physical identities required: %+v", snapshot.Instances)
	}
	goal.Actions = append(goal.Actions, serving.Action{ID: "pending-final-route", Kind: servingstate.OperationRoute, DependsOn: []string{"remove-" + unknown.Node.UID}})
	c.startTransition(&goal, c.config.Settings.Controls, func() {}, func(serving.Action) bool { return true }, func() {})
	lost := loseDefectEProbe(c, "10.96.0.15:8080")
	lost.Store(true)
	parent := c.runs[transitionRunKind+"/"+goal.ID]
	var reported atomic.Int32
	c.config.OnAction = func(result servingstate.OperationResult) error {
		if result.Operation.ID == parent.action.ID && result.Status == servingstate.NativeOperationFailed {
			reported.Add(1)
		}
		return nil
	}
	for range 6 {
		if err := c.Continue(c.ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		c.wg.Wait()
	}
	if parent.action.Status != servingstate.NativeOperationFailed || !strings.Contains(parent.action.Reason, unknown.ID) || !parent.reserved {
		t.Fatalf("exhausted exact unconfirmed frontier did not fail and retain unknown: %+v", parent.action)
	}
	if reported.Load() != 1 {
		t.Fatal("structural failure did not publish one terminal owner result")
	}
	completed := 0
	for _, action := range goal.Actions {
		if run := c.runs[action.ID]; run != nil && run.action.Status == servingstate.NativeOperationComplete {
			completed++
		}
	}
	if completed != 12 || c.runs["stop-"+unknown.Node.UID] != nil {
		t.Fatalf("expected twelve genuine completions and no unknown stop; completed=%d", completed)
	}
	busy := c.reservedNodes()
	if len(busy) != 1 || !busy[unknown.Node.UID] {
		t.Fatalf("proved healthy nodes did not return independently: %v", busy)
	}
	// A new normal owner must cross reservation, native policy and runtime routing.
	spec := c.config.Preparations["c-profile"]
	wanted := spec.Instance
	wanted.ID = "post-partial-return-slot"
	next := serving.SchedulerCandidate{ID: "post-partial-return", Instances: []serving.SchedulerInstance{wanted}, Routing: map[string]map[string]int32{uid: {wanted.ID: 1000}}, Actions: []serving.Action{
		{ID: "post-partial-prepare", Kind: servingstate.OperationPrepare, Node: wanted.Node, Instance: wanted.ID},
		{ID: "post-partial-route", Kind: servingstate.OperationRoute, DependsOn: []string{"post-partial-prepare"}},
	}}
	c.launch(next, c.config)
	c.wg.Wait()
	for range 3 {
		if err := c.Continue(c.ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		c.wg.Wait()
	}
	requireCompletedContinuationAction(t, c.runs["post-partial-prepare"], "returned node real preparation")
	requireCompletedContinuationAction(t, c.runs["post-partial-route"], "returned node native/runtime routing")
	route, _ := c.runtime.SchedulerRouting()
	if route[uid][c.runs["post-partial-prepare"].prepareIdentity] <= 0 || !c.reservedNodes()[unknown.Node.UID] {
		t.Fatalf("new route missing or unknown scope escaped: %v %v", route, c.reservedNodes())
	}
	lost.Store(false)
	if err := c.Continue(c.ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	if parent.reserved || c.runs["stop-"+unknown.Node.UID] != nil {
		t.Fatal("exact recovered untouched source did not return, or failed original stop resurrected")
	}
}

func TestDefectEUnknownAndLiveFrontiersDoNotFail(t *testing.T) {
	for _, scenario := range []string{"unknown-dependency", "external-dependency", "running-prepare", "canceling-route", "controls-off"} {
		t.Run(scenario, func(t *testing.T) {
			c := handoffController(t)
			snapshot := handoffSnapshot(t, c)
			instance := snapshot.Instances[len(snapshot.Instances)-1]
			goal := serving.SchedulerCandidate{ID: "held-" + scenario, Routing: map[string]map[string]int32{}, Actions: []serving.Action{{ID: "held-stop", Kind: servingstate.OperationStop, Node: instance.Node, Instance: instance.ID}}}
			c.startTransition(&goal, c.config.Settings.Controls, func() {}, func(serving.Action) bool { return true }, func() {})
			parent := c.runs[transitionRunKind+"/"+goal.ID]
			lost := loseDefectEProbe(c, "10.96.0.13:8080")
			lost.Store(true)
			switch scenario {
			case "unknown-dependency", "external-dependency":
				goal.Actions[0].DependsOn = []string{"not-in-dag"}
				if scenario == "external-dependency" {
					c.runs["not-in-dag"] = &actionRun{owner: "external", action: serving.Action{Status: servingstate.NativeOperationRunning}}
				}
			case "running-prepare", "canceling-route":
				kind, status := servingstate.OperationPrepare, servingstate.NativeOperationRunning
				if scenario == "canceling-route" {
					kind, status = servingstate.OperationRoute, servingstate.NativeOperationCancel
				}
				c.runs["live-generated-child"] = &actionRun{owner: goal.ID, action: serving.Action{Kind: kind, Status: status}}
			case "controls-off":
				c.config.Settings.Controls.Routing = false
			}
			// The admitted operation points at the same frozen target, including
			// its deliberately unknown dependency in these negative fixtures.
			parent.result.Operation.Target = &goal
			if err := c.Continue(c.ctx, time.Now()); err != nil {
				t.Fatal(err)
			}
			c.wg.Wait()
			if parent.action.Status != servingstate.NativeOperationRunning {
				t.Fatalf("%s is not an exclusively unconfirmed frontier: %+v", scenario, parent.action)
			}
		})
	}
}

func TestDefectEReadyWithdrawalRunsBeforeStructuralFailure(t *testing.T) {
	c := handoffController(t)
	snapshot := handoffSnapshot(t, c)
	uid := snapshot.Workloads[0].UID
	var healthy, dead serving.SchedulerInstance
	for _, instance := range snapshot.Instances {
		if instance.Node.Name == "a" {
			dead = instance
		}
		if instance.Node.Name == "c" {
			healthy = instance
			healthy.WorkloadUID = uid
		}
	}
	goal := serving.SchedulerCandidate{ID: "withdraw-before-fail", Instances: []serving.SchedulerInstance{healthy}, Routing: map[string]map[string]int32{uid: {healthy.ID: 1000}}, Actions: []serving.Action{
		{ID: "withdraw-route", Kind: servingstate.OperationRoute, Node: healthy.Node},
		{ID: "dead-stop", Kind: servingstate.OperationStop, Node: dead.Node, Instance: dead.ID, DependsOn: []string{"withdraw-route"}},
	}}
	c.startTransition(&goal, c.config.Settings.Controls, func() {}, func(serving.Action) bool { return true }, func() {})
	lost := loseDefectEProbe(c, "10.96.0.10:8080")
	lost.Store(true)
	if err := c.Continue(c.ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	parent := c.runs[transitionRunKind+"/"+goal.ID]
	if c.runs["withdraw-route"] == nil {
		t.Fatalf("withdrawal did not dispatch: parent=%+v uids=%v snapshot=%+v goal=%+v", parent.action, parent.result.Operation.WorkloadUIDs, c.State().Snapshot, goal)
	}
	requireCompletedContinuationAction(t, c.runs["withdraw-route"], "independent withdrawal must really execute")
	if parent.action.Status != servingstate.NativeOperationRunning {
		t.Fatal("owner failed before dispatched route completion")
	}
	if err := c.Continue(c.ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	route, _ := c.runtime.SchedulerRouting()
	if route[uid][dead.ID] != 0 || route[uid][healthy.ID] <= 0 || parent.action.Status != servingstate.NativeOperationFailed {
		t.Fatalf("withdrawal/native progress or subsequent failure missing: %v %s", route, parent.action.Status)
	}
	if !slices.ContainsFunc(c.State().Results, func(result servingstate.OperationResult) bool {
		return result.Operation.ID == parent.action.ID && result.Status == servingstate.NativeOperationFailed
	}) {
		t.Fatalf("structural terminal result not published: %s", parent.action.Reason)
	}
}

func TestDefectEGeneratedReadySubsetPrecedesFailure(t *testing.T) {
	c := handoffController(t)
	snapshot := handoffSnapshot(t, c)
	uid := snapshot.Workloads[0].UID
	unroute := serving.SchedulerCandidate{ID: "subset-withdraw-fixture", Routing: map[string]map[string]int32{uid: {}}}
	if _, err := c.operations.Execute(c.ctx, servingstate.Operation{ID: unroute.ID, Kind: servingstate.OperationUnroute, Target: &unroute}); err != nil {
		t.Fatal(err)
	}
	snapshot = handoffSnapshot(t, c)
	goal := serving.SchedulerCandidate{ID: "generated-before-failure", Routing: map[string]map[string]int32{uid: {}}}
	var dead serving.SchedulerInstance
	for _, instance := range snapshot.Instances {
		if instance.Node.Name == "a" || instance.Node.Name == "d" {
			instance.WorkloadUID = uid
			goal.Instances = append(goal.Instances, instance)
			goal.Routing[uid][instance.ID] = 500
			if instance.Node.Name == "d" {
				dead = instance
			}
		}
	}
	goal.Actions = []serving.Action{
		{ID: "subset-blocked-stop", Kind: servingstate.OperationStop, Node: dead.Node, Instance: dead.ID},
		{ID: "subset-final-route", Kind: servingstate.OperationRoute, Node: goal.Instances[0].Node, DependsOn: []string{"subset-blocked-stop"}},
	}
	c.startTransition(&goal, c.config.Settings.Controls, func() {}, func(serving.Action) bool { return true }, func() {})
	lost := loseDefectEProbe(c, "10.96.0.13:8080")
	lost.Store(true)
	if err := c.Continue(c.ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	parent := c.runs[transitionRunKind+"/"+goal.ID]
	if parent.action.Status != servingstate.NativeOperationRunning {
		t.Fatal("generated runnable route was not exhausted before diagnosis")
	}
	generated := false
	for _, run := range c.runs {
		if run.owner == goal.ID && run.action.From == readySubsetRoute {
			requireCompletedContinuationAction(t, run, "generated Ready-subset native route")
			generated = true
		}
	}
	if !generated {
		t.Fatalf("fixture did not dispatch a generated Ready-subset change: parent=%+v uids=%v snapshot=%+v goal=%+v", parent.action, parent.result.Operation.WorkloadUIDs, c.State().Snapshot, goal)
	}
	if err := c.Continue(c.ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	if parent.action.Status != servingstate.NativeOperationFailed {
		t.Fatal("exhausted generated route left structural frontier Running")
	}
}
