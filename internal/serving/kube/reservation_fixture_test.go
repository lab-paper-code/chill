package kube

import (
	"time"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	"github.com/lab-paper-code/chill/internal/serving"
)

// These fixtures contain a single autonomous DAG; concurrency tests inspect
// explicit owners instead of selecting one target as scheduler-wide truth.
func installTestTransition(c *SchedulerController, target *serving.SchedulerCandidate) {
	if target == nil {
		panic("a live owner cannot be cleared by assigning a global goal")
	}
	id := servingstate.TransitionRunKind + "/" + target.ID
	operation := servingstate.Operation{ID: id, Kind: servingstate.TransitionRunKind, Target: target}
	for uid := range target.Routing {
		operation.WorkloadUIDs = append(operation.WorkloadUIDs, uid)
	}
	for _, action := range target.Actions {
		if action.Node.UID != "" {
			operation.Nodes = append(operation.Nodes, action.Node)
		}
	}
	now := time.Now()
	run := &servingstate.ActionRun{owner: target.ID, action: serving.Action{ID: id, Kind: servingstate.TransitionRunKind, Status: servingstate.NativeOperationRunning}, result: &servingstate.OperationResult{Operation: operation, Status: servingstate.NativeOperationRunning, StartedAt: now}, started: now}
	if !c.ReserveRun(id, run, operation) {
		panic("fixture owner conflicts")
	}
}

func fixtureTarget(c *SchedulerController) *serving.SchedulerCandidate {
	var target *serving.SchedulerCandidate
	for _, run := range c.runs {
		if run.action.Kind == servingstate.TransitionRunKind && run.action.Status == servingstate.NativeOperationRunning {
			if target != nil {
				panic("single-owner fixture contains multiple DAGs")
			}
			target = run.result.Operation.Target
		}
	}
	return target
}
