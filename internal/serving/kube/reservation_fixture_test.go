package kube

import (
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// These fixtures contain a single autonomous DAG; concurrency tests inspect
// explicit owners instead of selecting one target as scheduler-wide truth.
func installTestTransition(c *SchedulerController, target *serving.SchedulerCandidate) {
	if target == nil {
		panic("a live owner cannot be cleared by assigning a global goal")
	}
	id := transitionRunKind + "/" + target.ID
	operation := Operation{ID: id, Kind: transitionRunKind, Target: target}
	for uid := range target.Routing {
		operation.WorkloadUIDs = append(operation.WorkloadUIDs, uid)
	}
	for _, action := range target.Actions {
		if action.Node.UID != "" {
			operation.Nodes = append(operation.Nodes, action.Node)
		}
	}
	now := time.Now()
	run := &actionRun{owner: target.ID, action: serving.Action{ID: id, Kind: transitionRunKind, Status: nativeOperationRunning}, result: &OperationResult{Operation: operation, Status: nativeOperationRunning, StartedAt: now}, started: now}
	if !c.reserveRun(id, run, operation) {
		panic("fixture owner conflicts")
	}
}

func fixtureTarget(c *SchedulerController) *serving.SchedulerCandidate {
	var target *serving.SchedulerCandidate
	for _, run := range c.runs {
		if run.action.Kind == transitionRunKind && run.action.Status == nativeOperationRunning {
			if target != nil {
				panic("single-owner fixture contains multiple DAGs")
			}
			target = run.result.Operation.Target
		}
	}
	return target
}
