package kube

import (
	"context"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// Raw deployB control-258: empty routes and unproved retirement never create
// an owner. Pending prepare/power/resize still declare physical work.
func TestEmptyRouteAdmissionAC38(t *testing.T) {
	for _, kind := range []string{"", operationPrepare, operationPower, operationResize, operationStop} {
		t.Run(kind, func(t *testing.T) {
			c := &SchedulerController{runs: map[string]*actionRun{}, runtime: &serving.Runtime{}}
			route := serving.Action{ID: "empty-route", Kind: operationRoute}
			target := serving.SchedulerCandidate{ID: "raw-empty", Actions: []serving.Action{route}}
			if kind != "" {
				target.Actions = append(target.Actions, serving.Action{ID: "waiting-duty", Kind: kind, DependsOn: []string{route.ID}})
			}
			c.launch(target, SchedulerControllerConfig{})
			if kind == "" || kind == operationStop {
				if len(c.runs) != 0 || len(c.reservationsLocked(c.state.Snapshot)) != 0 {
					t.Fatal("empty route/unproved retirement acquired ownership")
				}
			} else {
				if c.runs[transitionRunKind+"/"+target.ID] == nil {
					t.Fatal("pending physical duty was discarded")
				}
				if c.runs["waiting-duty"] != nil {
					t.Fatal("no-op declaration bypassed dependency/handoff")
				}
			}
		})
	}
}

func TestEmptyRouteAdmissionReplansWithoutOwnershipAC38(t *testing.T) {
	target := serving.SchedulerCandidate{ID: "empty-replan", Actions: []serving.Action{{ID: "empty-route", Kind: operationRoute}}}
	c := &SchedulerController{runs: map[string]*actionRun{}, runtime: &serving.Runtime{}}
	for range 2 {
		c.launch(target, SchedulerControllerConfig{})
		c.reconcileSnapshot(c.state.Snapshot, time.Now(), time.Minute)
		if len(c.runs) != 0 || len(c.reservationsLocked(c.state.Snapshot)) != 0 {
			t.Fatal("empty declaration left a parent, child, or reservation")
		}
	}
	target.Actions = append(target.Actions, serving.Action{ID: "prepare", Kind: operationPrepare, DependsOn: []string{"empty-route"}})
	c.launch(target, SchedulerControllerConfig{})
	if c.runs[transitionRunKind+"/"+target.ID] == nil || c.runs["prepare"] != nil {
		t.Fatal("new physical work was not admitted with its frozen dependency")
	}
}

func TestRouteAdmissionWaitsForReadyRootS3(t *testing.T) {
	instance := serving.SchedulerInstance{ID: "destination", WorkloadUID: "w"}
	c := &SchedulerController{ctx: context.Background(), runs: map[string]*actionRun{}, runtime: &serving.Runtime{}}
	c.state.Snapshot.Instances = []serving.SchedulerInstance{instance}
	target := serving.SchedulerCandidate{ID: "wait-ready", Instances: []serving.SchedulerInstance{instance},
		Routing: map[string]map[string]int32{"w": {instance.ID: 1}},
		Actions: []serving.Action{{ID: "route", Kind: operationRoute}}}
	config := SchedulerControllerConfig{OnAction: func(OperationResult) error { return context.Canceled }}
	c.launch(target, config)
	if len(c.runs) != 0 || len(c.reservationsLocked(c.state.Snapshot)) != 0 {
		t.Fatal("blocked route acquired an owner")
	}
	c.state.Snapshot.Instances[0].Ready = true
	c.launch(target, config)
	c.wg.Wait()
	if c.runs[transitionRunKind+"/"+target.ID] == nil || c.runs["route"] == nil {
		t.Fatal("Ready route was not replanned and admitted")
	}
}

func TestEmptyRouteNotReadyAndRetirementAC38(t *testing.T) {
	for _, ready := range []int{0, 1, 2} {
		c := &SchedulerController{runs: map[string]*actionRun{}, runtime: &serving.Runtime{}}
		instances := []serving.SchedulerInstance{{ID: "a", WorkloadUID: "w", Ready: ready > 0}, {ID: "b", WorkloadUID: "w", Ready: ready > 1}}
		routing := map[string]map[string]int32{"w": {"a": 500, "b": 500}}
		c.state.Snapshot = serving.SchedulerSnapshot{Instances: instances, Routing: routing}
		target := serving.SchedulerCandidate{ID: "unchanged", Instances: instances, Routing: routing, Actions: []serving.Action{{ID: "noop", Kind: operationRoute}}}
		c.launch(target, SchedulerControllerConfig{})
		if len(c.runs) != 0 {
			t.Fatalf("ready=%d: declaration-identical route acquired ownership", ready)
		}
		target.Actions = append(target.Actions, serving.Action{ID: "retire", Kind: operationStop, DependsOn: []string{"noop"}})
		c.launch(target, SchedulerControllerConfig{})
		c.reconcileSnapshot(c.state.Snapshot, time.Now(), time.Minute)
		if len(c.runs) != 0 || len(c.reservationsLocked(c.state.Snapshot)) != 0 {
			t.Fatalf("ready=%d: unproved retirement acquired ownership", ready)
		}
	}
}

func TestReservationReasonsDeterministicAC38(t *testing.T) {
	c := &SchedulerController{runs: map[string]*actionRun{}}
	for _, id := range []string{"z", "a"} {
		c.runs[id] = &actionRun{owner: "owner", reserved: true, action: serving.Action{ID: id, Reason: "missing proof"}}
	}
	want := "a: missing proof; z: missing proof"
	for range 100 {
		if got := c.reservationsLocked(serving.SchedulerSnapshot{})[0].Reason; got != want {
			t.Fatalf("nondeterministic blocker %q", got)
		}
	}
}
