package kube

import (
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// Raw deployB control-258: changedWorkloads=[] and no child receipt must
// never create a parent. Retirement and pending prepare remain real duties.
func TestEmptyRouteAdmissionAC38(t *testing.T) {
	for _, kind := range []string{"", operationPrepare, operationPower, operationStop} {
		t.Run(kind, func(t *testing.T) {
			c := &SchedulerController{runs: map[string]*actionRun{}, runtime: &serving.Runtime{}}
			route := serving.Action{ID: "empty-route", Kind: operationRoute}
			target := serving.SchedulerCandidate{ID: "raw-empty", Actions: []serving.Action{route}}
			if kind != "" {
				target.Actions = append(target.Actions, serving.Action{ID: "waiting-duty", Kind: kind, DependsOn: []string{route.ID}})
			}
			c.launch(target, SchedulerControllerConfig{})
			if kind == "" {
				if len(c.runs) != 0 {
					t.Fatal("pure empty route acquired a parent reservation")
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

func TestExistingEmptyRouteOrphanReturnsAC38(t *testing.T) {
	target := serving.SchedulerCandidate{ID: "raw-orphan", Actions: []serving.Action{{ID: "empty-route", Kind: operationRoute}}}
	parent := &actionRun{owner: target.ID, reserved: true, action: serving.Action{ID: transitionRunKind + "/" + target.ID, Kind: transitionRunKind, Status: nativeOperationRunning}, result: &OperationResult{Operation: Operation{Target: &target, WorkloadUIDs: []string{}}, Status: nativeOperationRunning}}
	c := &SchedulerController{runs: map[string]*actionRun{parent.action.ID: parent}}
	c.reconcileSnapshot(serving.SchedulerSnapshot{}, time.Now(), time.Minute)
	if parent.reserved {
		t.Fatal("effect-free legacy parent did not return")
	}
	if parent.result.Status == nativeOperationComplete || c.runs["empty-route"] != nil {
		t.Fatal("no-op forged a successful route receipt")
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
		parent := c.runs[transitionRunKind+"/"+target.ID]
		if parent == nil || parent.action.Status != nativeOperationPartial || c.runs["retire"] != nil {
			t.Fatalf("ready=%d: unproved retirement was not closed as no-effect", ready)
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
