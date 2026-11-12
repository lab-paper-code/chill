package kube

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

// startTransition owns the original complete launch critical section. The
// controller supplies eligibility and physical dispatch, not another ledger.
func (c *reservationState) startTransition(target *serving.SchedulerCandidate, controls serving.SchedulerControls,
	reconcile func(), eligible func(serving.Action) bool, dispatch func(),
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	reconcile()
	if len(target.Actions) == 0 {
		return
	}
	id := transitionRunKind + "/" + target.ID
	if c.runs[id] != nil {
		return
	}
	if !slices.ContainsFunc(target.Actions, eligible) {
		return
	}
	operation := servingstate.Operation{ID: id, Kind: transitionRunKind, Target: target, WorkloadUIDs: changedWorkloads(*target, c.state.Snapshot)}
	for _, action := range target.Actions {
		if action.Node.UID != "" && !slices.Contains(operation.Nodes, action.Node) {
			operation.Nodes = append(operation.Nodes, action.Node)
		}
	}
	started := time.Now()
	before := servingstate.OperationState{At: started.UTC(), Instances: make([]serving.InstanceState, 0, len(c.state.Snapshot.Instances))}
	before.Routing = c.state.Snapshot.Routing
	for _, instance := range c.state.Snapshot.Instances {
		before.Instances = append(before.Instances, serving.InstanceState{ID: instance.ID, PodUID: instance.PodUID, Node: instance.Node, OwnerWorkloadUID: instance.OwnerWorkloadUID})
	}
	parent := &actionRun{owner: target.ID, action: serving.Action{ID: id, Kind: transitionRunKind, Status: servingstate.NativeOperationRunning}, result: &servingstate.OperationResult{Operation: operation, Before: before, Status: servingstate.NativeOperationRunning, StartedAt: started}, started: started}
	parent.admittedControls = controls
	if !c.reserveRun(id, parent, operation) {
		return
	}
	dispatch()
}

// completeRun publishes the receipt and terminal action together under the
// existing reservation mutex, before the controller signals completion.
func (c *reservationState) completeRun(run *actionRun, result *servingstate.OperationResult, effectStarted bool, err error, runCtx context.Context) {
	c.mu.Lock()
	run.result = result
	if !effectStarted {
		run.reserved = false
	}
	run.action.Status = servingstate.NativeOperationComplete
	if err != nil {
		run.action.Status = result.Status
		run.action.Reason = err.Error()
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			run.action.Status, run.action.Reason = servingstate.NativeOperationFailed, serving.ActionDeadlineExceeded+": "+run.action.Reason
		}
	}
	if run.action.Kind == servingstate.OperationRoute && run.action.Status == servingstate.NativeOperationComplete {
		run.scope.Routing = nil
	}
	run.action.Elapsed = result.EndedAt.Sub(result.StartedAt)
	c.recordPreparationFailureLocked(run, result, c.state.Snapshot)
	c.state.Results = append(c.state.Results, *result)
	c.mu.Unlock()
}

func (c *reservationState) cancelRunLocked(id string) {
	if run := c.runs[id]; run != nil && run.action.Cancelable && run.cancel != nil {
		run.cancel()
		run.action.Status = servingstate.NativeOperationCancel
	}
}

func (c *reservationState) cancelCancelableRuns() {
	c.mu.Lock()
	for _, run := range c.runs {
		if run.action.Status == servingstate.NativeOperationRunning && run.action.Cancelable && run.cancel != nil {
			run.cancel()
		}
	}
	c.mu.Unlock()
}

func (c *reservationState) startAdmittedRun(
	operation *servingstate.Operation, initial *servingstate.OperationResult, cancel context.CancelFunc, started time.Time,
) *actionRun {
	kind := operation.Kind
	switch kind {
	case servingstate.OperationUnroute, servingstate.OperationRelease, servingstate.OperationReset:
		kind = servingstate.OperationRoute
	}
	run := &actionRun{action: serving.Action{ID: operation.ID, Kind: kind, Node: operation.Node, From: externalActionSource, Status: servingstate.NativeOperationRunning}, result: initial, cancel: cancel, started: started}
	if operation.Holder != "" {
		run.owner = operation.Holder
	}
	c.mu.Lock()
	accepted := c.reserveRun(operation.ID, run, *operation)
	c.mu.Unlock()
	if !accepted {
		return nil
	}
	return run
}

func (c *reservationState) completeAdmittedRun(run *actionRun, result *servingstate.OperationResult, err error, kind string) {
	c.mu.Lock()
	run.result = result
	run.action.Status, run.action.Reason = result.Status, result.Error
	c.recordPreparationFailureLocked(run, result, c.state.Snapshot)
	if err == nil && result.Status == servingstate.NativeOperationComplete && len(run.returnCreations) == 0 || result.EffectsNotStarted && !run.inheritedReservation {
		run.reserved = false
		if kind != runnerKind {
			c.releaseOwnerLocked(run.owner)
		}
	}
	run.action.Elapsed = result.EndedAt.Sub(result.StartedAt)
	c.state.Results = append(c.state.Results, *result)
	c.mu.Unlock()
}

func (c *reservationState) retireRunsLocked(cutoff time.Time, snapshot serving.SchedulerSnapshot) {
	c.state.Results = slices.DeleteFunc(c.state.Results, func(r servingstate.OperationResult) bool { return r.EndedAt.Before(cutoff) })
	for id, run := range c.runs {
		if run.result != nil {
			c.recordPreparationFailureLocked(run, run.result, snapshot)
		}
		if serving.PreparationFailurePresent(run.action, snapshot) && !c.preparationFailureSupersededLocked(run.action) {
			continue
		}
		if !run.reserved && run.action.Status != servingstate.NativeOperationRunning && run.action.Status != servingstate.NativeOperationCancel && run.result != nil && run.result.EndedAt.Before(cutoff) && !c.runReferencedLocked(id) {
			delete(c.runs, id)
		}
	}
}

func (c *reservationState) recordPreparationFailureLocked(run *actionRun, result *servingstate.OperationResult, snapshot serving.SchedulerSnapshot) {
	if run.action.PreparationFailure != nil {
		return
	}
	run.action.PreparationFailure = result.PreparationFailure(snapshot)
	if run.action.PreparationFailure == nil {
		return
	}
	var previous *actionRun
	for _, candidate := range c.runs {
		if candidate.result != nil && !candidate.result.EndedAt.After(result.EndedAt) &&
			serving.PreparationFailureReproduces(run.action, candidate.action) &&
			(previous == nil || previous.result.EndedAt.Before(candidate.result.EndedAt)) {
			previous = candidate
		}
	}
	if previous != nil {
		serving.ObserveRepeatedPreparationFailure(&run.action, previous.action)
	}
}

func (c *reservationState) preparationFailureSupersededLocked(action serving.Action) bool {
	for _, run := range c.runs {
		if serving.PreparationFailureReproduces(run.action, action) &&
			run.action.PreparationFailure.RepeatCount > action.PreparationFailure.RepeatCount {
			return true
		}
	}
	return false
}

func (c *reservationState) scheduledActionTerminalLocked(action *serving.Action) bool {
	run := c.runs[action.ID]
	return run != nil && actionEquivalent(run.action, *action) &&
		run.action.Status != servingstate.NativeOperationRunning && run.action.Status != servingstate.NativeOperationCancel
}

func (c *reservationState) startScheduledActionLocked(action *serving.Action, initial *servingstate.OperationResult,
	started time.Time, owner string, newContext func() (context.Context, context.CancelFunc),
) (context.Context, *actionRun) {
	run := &actionRun{action: *action, result: initial, started: started, owner: owner, proofParent: c.runs[transitionRunKind+"/"+owner]}
	if !c.reserveRun(action.ID, run, initial.Operation) {
		return nil, nil
	}
	ctx, cancel := newContext()
	run.cancel = cancel
	return ctx, run
}

func (c *reservationState) recordPreparation(run *actionRun, evidence *servingstate.PreparationEvidence) {
	c.mu.Lock()
	c.recordPreparationLocked(run, evidence)
	c.mu.Unlock()
}

func (c *reservationState) reservedNodes() map[string]bool {
	c.mu.Lock()
	busy := map[string]bool{}
	for _, run := range c.runs {
		if run.reserved {
			for _, node := range run.scope.Nodes {
				busy[node.UID] = true
			}
		}
	}
	c.mu.Unlock()
	return busy
}

func (c *reservationState) stateViewLocked() SchedulerControllerState {
	result := c.state
	result.Actions = c.actionsLocked(time.Now())
	result.Snapshot.Reservations = c.reservationsLocked()
	result.Results = append([]servingstate.OperationResult(nil), c.state.Results...)
	return result
}

func (c *reservationState) snapshotLocked() serving.SchedulerSnapshot {
	return c.state.Snapshot
}

func (c *reservationState) publishSnapshotLocked(snapshot *serving.SchedulerSnapshot) {
	if snapshot.At.Before(c.state.Snapshot.At) {
		return
	}
	c.state.Snapshot = *snapshot
	for uid, writers := range c.routeWriters {
		if !slices.ContainsFunc(snapshot.Workloads, func(workload *serving.Definition) bool { return workload != nil && workload.UID == uid }) {
			delete(c.routeWriters, uid)
			continue
		}
		for instance := range writers {
			if !slices.ContainsFunc(snapshot.Instances, func(actual serving.SchedulerInstance) bool { return actual.ID == instance }) {
				delete(writers, instance)
			}
		}
	}
}

func (c *reservationState) publishDecisionLocked(snapshot *serving.SchedulerSnapshot, decision *serving.SchedulerDecision) {
	if snapshot.At.Before(c.state.Snapshot.At) {
		return
	}
	c.publishSnapshotLocked(snapshot)
	c.state.Decision = *decision
	c.markOverloadPrepares(*decision)
}
