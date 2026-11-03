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
		if action.Node.UID != "" {
			operation.Nodes = append(operation.Nodes, action.Node)
		}
	}
	started := time.Now()
	before := servingstate.OperationState{At: started.UTC(), Instances: make([]serving.InstanceState, 0, len(c.state.Snapshot.Instances))}
	for _, instance := range c.state.Snapshot.Instances {
		before.Instances = append(before.Instances, serving.InstanceState{ID: instance.ID, PodUID: instance.PodUID, Node: instance.Node})
	}
	parent := &actionRun{owner: target.ID, action: serving.Action{ID: id, Kind: transitionRunKind, Status: servingstate.NativeOperationRunning}, result: &servingstate.OperationResult{Operation: operation, Before: before, Status: servingstate.NativeOperationRunning, StartedAt: started}, started: started}
	parent.admittedControls = controls
	// Freeze only physical same-Node/Workload replacements, not route-only
	// policy changes or added capacity. Completion is observed in reconcileSnapshot.
	for _, next := range target.Instances {
		if next.Profile == nil {
			continue
		}
		for _, old := range c.state.Snapshot.Instances {
			if old.Node == next.Node && old.WorkloadUID == next.WorkloadUID && old.Profile != nil &&
				old.Profile.ID != next.Profile.ID && slices.ContainsFunc(target.Actions, func(a serving.Action) bool {
				return a.Kind == servingstate.OperationRemove && a.Instance == old.ID
			}) {
				parent.replacements = append(parent.replacements, serving.SchedulerReplacement{
					NodeUID: next.Node.UID, WorkloadUID: next.WorkloadUID, ProfileID: next.Profile.ID,
				})
			}
		}
	}
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

func (c *reservationState) startRun(run *actionRun, operation servingstate.Operation) bool {
	c.mu.Lock()
	accepted := c.reserveRun(operation.ID, run, operation)
	c.mu.Unlock()
	return accepted
}

func (c *reservationState) completeAdmittedRun(run *actionRun, result *servingstate.OperationResult, err error, kind string) {
	c.mu.Lock()
	run.result = result
	run.action.Status, run.action.Reason = result.Status, result.Error
	c.recordPreparationFailureLocked(run, result, c.state.Snapshot)
	if err == nil && result.Status == servingstate.NativeOperationComplete || result.EffectsNotStarted && !run.inheritedReservation {
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
		if serving.PreparationFailureSupersedes(run.action, action) {
			return true
		}
	}
	return false
}
