package kube

import (
	"slices"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// A rejected frozen goal is terminal failure, not successful completion. Live
// siblings and prepared/route effects retain their exact return-proof scope.
func (c *SchedulerController) reductionCommitAllowedLocked(target serving.SchedulerCandidate, config SchedulerControllerConfig, boundary *serving.Action) bool {
	parent := c.runs[transitionRunKind+"/"+target.ID]
	if parent == nil {
		return true
	}
	if parent.action.Status != nativeOperationRunning {
		return true // Existing failed-owner compensation is not a reduction commit.
	}
	if len(parent.reductionWorkloads) == 0 {
		return true
	}
	if boundary == nil {
		// Observe a burst during an already-started final handoff, without
		// canceling that remote write or treating it as a failed ACK.
		if !slices.ContainsFunc(target.Actions, func(a serving.Action) bool {
			run := c.runs[a.ID]
			return a.Kind == operationRoute && a.RouteTarget == nil && a.From != readySubsetRoute &&
				run != nil && (run.action.Status == nativeOperationRunning || run.action.Status == nativeOperationCancel)
		}) {
			return true
		}
	} else if !c.supplyReductionBoundary(*boundary) {
		return true
	}
	pending := slices.ContainsFunc(target.Actions, func(a serving.Action) bool {
		if c.runs[a.ID] != nil || a.Status == nativeOperationCancel || a.Status == nativeOperationComplete || a.Status == nativeOperationFailed {
			return false
		}
		return c.supplyReductionBoundary(a)
	})
	if !pending {
		return true
	}
	var rows []serving.CapacityRow
	if c.capacity != nil {
		rows = c.capacity.Rows()
	}
	reason := serving.SchedulerReductionCommit(config.Settings, c.state.Snapshot, rows, *parent.result.Operation.Target, parent.reductionWorkloads)
	if reason == "" {
		return true
	}
	reason = "ReductionCommitRejected: " + reason
	for k := range parent.result.Operation.Target.Actions {
		a := &parent.result.Operation.Target.Actions[k]
		if c.runs[a.ID] == nil && a.Status != nativeOperationComplete && a.Status != nativeOperationFailed {
			a.Status, a.Reason = nativeOperationCancel, "no-effect: "+reason
		}
	}
	parent.action.Status, parent.action.Reason = nativeOperationFailed, reason
	parent.result.Status, parent.result.Error, parent.result.EndedAt = nativeOperationFailed, reason, time.Now().UTC()
	parent.returnObserved = false
	return false
}

func (c *SchedulerController) supplyReductionBoundary(a serving.Action) bool {
	switch a.Kind {
	case operationRoute:
		return a.RouteTarget == nil && a.From != readySubsetRoute
	case operationStop, operationRemove:
		// Already-needed recovery retirement/cleanup removes no live supply.
		return !slices.ContainsFunc(c.state.Snapshot.Instances, func(i serving.SchedulerInstance) bool {
			return i.ID == a.Instance && (!i.Ready || i.Stopped || i.Terminated)
		})
	case operationPower:
		return slices.ContainsFunc(c.state.Snapshot.Instances, func(i serving.SchedulerInstance) bool {
			return i.Node == a.Node && i.Ready && !i.Stopped && !i.Terminated
		})
	}
	return false
}
