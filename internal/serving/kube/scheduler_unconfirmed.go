package kube

import (
	"fmt"
	"slices"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

// This is the same precondition used by dispatch; missing identities and other
// dependency failures deliberately do not establish an unconfirmed frontier.
func (c *reservationState) unconfirmedActionReason(action serving.Action, target serving.SchedulerCandidate) string {
	if action.Kind != servingstate.OperationRoute {
		for _, node := range c.state.Snapshot.Nodes {
			if node.Node == action.Node && (!node.Ready || slices.Contains(node.Reasons, serving.NodeInstanceUnavailable)) {
				return fmt.Sprintf("%s: action %s node %s instance %s", serving.NodeInstanceUnavailable, action.ID, node.Node.UID, action.Instance)
			}
		}
	}
	for _, instance := range c.state.Snapshot.Instances {
		if action.Kind != servingstate.OperationRoute && action.Node.UID != "" && instance.Node == action.Node && !instance.Ready && !instance.Stopped && !instance.Terminated {
			return fmt.Sprintf("%s: action %s node %s instance %s", serving.NodeInstanceUnavailable, action.ID, instance.Node.UID, instance.ID)
		}
	}
	if action.Kind == servingstate.OperationRoute {
		uids := c.targetWorkloadsLocked(target)
		for _, wanted := range target.Instances {
			if !slices.Contains(uids, wanted.WorkloadUID) || target.Routing[wanted.WorkloadUID][wanted.ID] <= 0 {
				continue
			}
			// Other unknown occupants exclude physical work, not a confirmed
			// destination's route policy. The node itself must still be Ready.
			for _, node := range c.state.Snapshot.Nodes {
				if node.Node == wanted.Node && !node.Ready {
					return fmt.Sprintf("%s: action %s node %s instance %s", serving.NodeInstanceUnavailable, action.ID, node.Node.UID, wanted.ID)
				}
			}
			for _, actual := range c.state.Snapshot.Instances {
				if (actual.ID == wanted.ID || actual.TargetID == wanted.ID) && !actual.Ready && !actual.Stopped {
					return fmt.Sprintf("%s: action %s node %s instance %s", serving.NodeInstanceUnavailable, action.ID, actual.Node.UID, actual.ID)
				}
			}
		}
	}
	return ""
}

// Called only after the real sweep, including generated Ready-subset routes.
// Reservation publication stays under the existing ledger mutex.
func (c *SchedulerController) failUnconfirmedTransitionLocked(target serving.SchedulerCandidate, actions []serving.Action, blocked map[string]string, config SchedulerControllerConfig) {
	parent := c.runs[transitionRunKind+"/"+target.ID]
	if len(blocked) == 0 || parent == nil || parent.action.Status != servingstate.NativeOperationRunning || parent.result == nil || parent.admittedControls != config.Settings.Controls {
		return
	}
	for _, uid := range parent.result.Operation.WorkloadUIDs {
		if !slices.Contains(config.Settings.AutonomousWorkloads, uid) {
			return
		}
	}
	for _, run := range c.runs {
		if run != parent && run.owner == parent.owner && (run.action.Status == servingstate.NativeOperationRunning || run.action.Status == servingstate.NativeOperationCancel) {
			return
		}
	}
	completed := c.completedSweepActionsLocked(target, actions)
	for _, required := range parent.result.Operation.Target.Actions {
		if !completed[required.ID] && !slices.ContainsFunc(actions, func(action serving.Action) bool {
			return action.ID == required.ID && actionEquivalent(action, required)
		}) {
			return // A selected subset is not a sweep of the frozen required DAG.
		}
	}
	for _, action := range actions {
		if blocked[action.ID] != "" && !slices.ContainsFunc(action.DependsOn, func(id string) bool { return !completed[id] }) &&
			!c.dependenciesComplete(action, target, c.runtime) {
			delete(blocked, action.ID)
		}
	}
	if parent.action.Status != servingstate.NativeOperationRunning {
		return
	}
	reason := servingstate.UnconfirmedFrontier(actions, completed, blocked)
	if reason == "" {
		return
	}
	parent.action.Status, parent.action.Reason = servingstate.NativeOperationFailed, reason
	parent.result.Status, parent.result.Error, parent.result.EndedAt = servingstate.NativeOperationFailed, reason, time.Now().UTC()
	result := *parent.result
	c.state.Results = append(c.state.Results, result)
	if config.OnAction != nil {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			if err := config.OnAction(result); err != nil {
				c.mu.Lock()
				parent.result.Error = reason + "; record structural failure: " + err.Error()
				c.mu.Unlock()
			}
		}()
	}
}

func (c *reservationState) completedSweepActionsLocked(target serving.SchedulerCandidate, actions []serving.Action) map[string]bool {
	completed := map[string]bool{}
	for _, action := range actions {
		if action.Status == servingstate.NativeOperationComplete || action.Status == servingstate.NativeOperationCancel || c.omittedRouteCompleteLocked(action, target) {
			completed[action.ID] = true
		}
		if run := c.runs[action.ID]; run != nil && actionEquivalent(action, run.action) && run.action.Status == servingstate.NativeOperationComplete {
			completed[action.ID] = true
		}
		for _, id := range action.DependsOn {
			if run := c.runs[id]; run != nil && run.action.Status == servingstate.NativeOperationComplete {
				completed[id] = true
			}
		}
	}
	return completed
}
