package kube

import (
	"context"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
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

func TestRouteAdmissionAppliesReadySurvivorSubsetS3b(t *testing.T) {
	c, prepared := tickReadySubsetController(t, func(index int, state *native.State) {
		if index == 0 {
			state.Ready = false
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c.ctx = ctx
	c.runtime.EnableSchedulerRouting()
	prepared.Store(true)
	snapshot, err := c.capture(ctx, time.Now(), c.config)
	if err != nil {
		t.Fatal(err)
	}
	a, b := snapshot.Instances[0], snapshot.Instances[1]
	uid := snapshot.Workloads[0].UID
	a.WorkloadUID, b.WorkloadUID = uid, uid
	if a.Ready || !b.Ready {
		t.Fatalf("expected NotReady A and Ready survivor B: %+v", snapshot.Instances)
	}
	target := serving.SchedulerCandidate{ID: "route-only-recovery", Instances: []serving.SchedulerInstance{a, b},
		Routing: map[string]map[string]int32{uid: {a.ID: 500, b.ID: 500}},
		Actions: []serving.Action{{ID: "route", Kind: operationRoute}}}
	c.state.Snapshot = snapshot
	c.launch(target, c.config)
	c.wg.Wait()
	if c.runs[transitionRunKind+"/"+target.ID] == nil || c.runs["route"] != nil {
		t.Fatal("route-only recovery was not admitted while its full route waited")
	}
	var subset *actionRun
	for _, run := range c.runs {
		if run.action.From == readySubsetRoute {
			subset = run
		}
	}
	if subset == nil || subset.action.Status != nativeOperationComplete || subset.result.Actual.Routing[uid][b.ID] != 1000 ||
		subset.result.Actual.Routing[uid][a.ID] != 0 {
		t.Fatalf("Ready survivor did not receive an acknowledged subset: %+v", subset)
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
