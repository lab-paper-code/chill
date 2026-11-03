package state

import (
	context "context"
	errors "errors"
	serving "github.com/lab-paper-code/chill/internal/serving"
	slices "slices"
	time "time"
)

// StartTransition owns the original complete launch critical section. The
// controller supplies eligibility and physical dispatch, not another ledger.
func (c *ReservationState) StartTransition(target *serving.SchedulerCandidate, controls serving.SchedulerControls,
	reconcile func(), eligible func(serving.Action) bool, dispatch func(),
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	reconcile()
	if len(target.Actions) == 0 {
		return
	}
	id := TransitionRunKind + "/" + target.ID
	if c.runs[id] != nil {
		return
	}
	if !slices.ContainsFunc(target.Actions, eligible) {
		return
	}
	operation := Operation{ID: id, Kind: TransitionRunKind, Target: target, WorkloadUIDs: ChangedWorkloads(*target, c.state.Snapshot)}
	for _, action := range target.Actions {
		if action.Node.UID != "" {
			operation.Nodes = append(operation.Nodes, action.Node)
		}
	}
	started := time.Now()
	before := OperationState{At: started.UTC(), Instances: make([]serving.InstanceState, 0, len(c.state.Snapshot.Instances))}
	for _, instance := range c.state.Snapshot.Instances {
		before.Instances = append(before.Instances, serving.InstanceState{ID: instance.ID, PodUID: instance.PodUID, Node: instance.Node})
	}
	parent := &ActionRun{owner: target.ID, action: serving.Action{ID: id, Kind: TransitionRunKind, Status: NativeOperationRunning}, result: &OperationResult{Operation: operation, Before: before, Status: NativeOperationRunning, StartedAt: started}, started: started}
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
				return a.Kind == OperationRemove && a.Instance == old.ID
			}) {
				parent.replacements = append(parent.replacements, serving.SchedulerReplacement{
					NodeUID: next.Node.UID, WorkloadUID: next.WorkloadUID, ProfileID: next.Profile.ID,
				})
			}
		}
	}
	if !c.ReserveRun(id, parent, operation) {
		return
	}
	dispatch()
}

// CompleteRun publishes the receipt and terminal action together under the
// existing reservation mutex, before the controller signals completion.
func (c *ReservationState) CompleteRun(run *ActionRun, result *OperationResult, effectStarted bool, err error, runCtx context.Context) {
	c.mu.Lock()
	run.result = result
	if !effectStarted {
		run.reserved = false
	}
	run.action.Status = NativeOperationComplete
	if err != nil {
		run.action.Status = result.Status
		run.action.Reason = err.Error()
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			run.action.Status, run.action.Reason = NativeOperationFailed, serving.ActionDeadlineExceeded+": "+run.action.Reason
		}
	}
	run.action.Elapsed = result.EndedAt.Sub(result.StartedAt)
	c.state.Results = append(c.state.Results, *result)
	c.mu.Unlock()
}

func (c *ReservationState) CancelRunLocked(id string) {
	if run := c.runs[id]; run != nil && run.action.Cancelable && run.cancel != nil {
		run.cancel()
		run.action.Status = NativeOperationCancel
	}
}

func (c *ReservationState) CancelCancelableRuns() {
	c.mu.Lock()
	for _, run := range c.runs {
		if run.action.Status == NativeOperationRunning && run.action.Cancelable && run.cancel != nil {
			run.cancel()
		}
	}
	c.mu.Unlock()
}

func (c *ReservationState) StartAdmittedRun(
	operation *Operation, initial *OperationResult, cancel context.CancelFunc, started time.Time,
) *ActionRun {
	kind := operation.Kind
	switch kind {
	case OperationUnroute, OperationRelease, OperationReset:
		kind = OperationRoute
	}
	run := &ActionRun{action: serving.Action{ID: operation.ID, Kind: kind, Node: operation.Node, From: externalActionSource, Status: NativeOperationRunning}, result: initial, cancel: cancel, started: started}
	if operation.Holder != "" {
		run.owner = operation.Holder
	}
	c.mu.Lock()
	accepted := c.ReserveRun(operation.ID, run, *operation)
	c.mu.Unlock()
	if !accepted {
		return nil
	}
	return run
}

func (c *ReservationState) CompleteAdmittedRun(run *ActionRun, result *OperationResult, err error, kind string) {
	c.mu.Lock()
	run.result = result
	run.action.Status, run.action.Reason = result.Status, result.Error
	if err == nil && result.Status == NativeOperationComplete || result.EffectsNotStarted && !run.inheritedReservation {
		run.reserved = false
		if kind != RunnerKind {
			c.ReleaseOwnerLocked(run.owner)
		}
	}
	run.action.Elapsed = result.EndedAt.Sub(result.StartedAt)
	c.state.Results = append(c.state.Results, *result)
	c.mu.Unlock()
}

func (c *ReservationState) retireRunsLocked(cutoff time.Time) {
	c.state.Results = slices.DeleteFunc(c.state.Results, func(r OperationResult) bool { return r.EndedAt.Before(cutoff) })
	for id, run := range c.runs {
		if !run.reserved && run.action.Status != NativeOperationRunning && run.action.Status != NativeOperationCancel && run.result != nil && run.result.EndedAt.Before(cutoff) && !c.RunReferencedLocked(id) {
			delete(c.runs, id)
		}
	}
}

func (c *ReservationState) ScheduledActionTerminalLocked(action *serving.Action) bool {
	run := c.runs[action.ID]
	return run != nil && ActionEquivalent(run.action, *action) &&
		run.action.Status != NativeOperationRunning && run.action.Status != NativeOperationCancel
}

func (c *ReservationState) StartScheduledActionLocked(action *serving.Action, initial *OperationResult,
	started time.Time, owner string, newContext func() (context.Context, context.CancelFunc),
) (context.Context, *ActionRun) {
	run := &ActionRun{action: *action, result: initial, started: started, owner: owner, proofParent: c.runs[TransitionRunKind+"/"+owner]}
	if !c.ReserveRun(action.ID, run, initial.Operation) {
		return nil, nil
	}
	ctx, cancel := newContext()
	run.cancel = cancel
	return ctx, run
}

func (c *ReservationState) RecordPreparation(run *ActionRun, evidence *PreparationEvidence) {
	c.mu.Lock()
	c.recordPreparationLocked(run, evidence)
	c.mu.Unlock()
}

func (c *ReservationState) ReservedNodes() map[string]bool {
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

func (c *ReservationState) StateViewLocked() SchedulerControllerState {
	result := c.state
	result.Actions = c.ActionsLocked(time.Now())
	result.Snapshot.Reservations = c.ReservationsLocked()
	result.Results = append([]OperationResult(nil), c.state.Results...)
	return result
}

func (c *ReservationState) SnapshotLocked() serving.SchedulerSnapshot {
	return c.state.Snapshot
}


func (c *ReservationState) PublishSnapshotLocked(snapshot *serving.SchedulerSnapshot) {
	c.state.Snapshot = *snapshot
}

func (c *ReservationState) PublishDecisionLocked(snapshot *serving.SchedulerSnapshot, decision *serving.SchedulerDecision) {
	c.state.Snapshot = *snapshot
	c.state.Decision = *decision
	c.MarkOverloadPrepares(*decision)
}
