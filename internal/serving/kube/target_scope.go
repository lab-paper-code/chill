package kube

import (
	"maps"
	"slices"

	"github.com/lab-paper-code/chill/internal/serving"
)

// Scope is frozen when the operation is registered, not recomputed at completion.
func changedWorkloads(target serving.SchedulerCandidate, snapshot serving.SchedulerSnapshot) []string {
	changed := []string{}
	for uid, weights := range target.Routing {
		resolved := make(map[string]int32, len(weights))
		policyChanged := false
		for id, weight := range weights {
			physical := id
			for _, actual := range snapshot.Instances {
				if actual.ID == id || actual.TargetID == id {
					physical = actual.ID
					break
				}
			}
			resolved[physical] = weight
		}
		for _, wanted := range target.Instances {
			if wanted.WorkloadUID != uid {
				continue
			}
			found := false
			for _, actual := range snapshot.Instances {
				if actual.ID == wanted.ID || actual.TargetID == wanted.ID {
					found = true
					if serving.SchedulerInstanceState(actual) != serving.SchedulerInstanceState(wanted) ||
						actual.MaxBatch != wanted.MaxBatch || actual.MaxWait != wanted.MaxWait {
						policyChanged = true
					}
					break
				}
			}
			if !found {
				policyChanged = true
			}
		}
		if policyChanged || !maps.Equal(resolved, snapshot.Routing[uid]) {
			changed = append(changed, uid)
		}
	}
	slices.Sort(changed)
	return changed
}

func controlledTarget(operation Operation) *serving.SchedulerCandidate {
	if operation.Target == nil {
		return nil
	}
	target := *operation.Target
	target.Instances = nil
	target.Routing = map[string]map[string]int32{}
	for uid, route := range operation.Target.Routing {
		if operation.controlsWorkload(uid) {
			target.Routing[uid] = route
		}
	}
	for _, instance := range operation.Target.Instances {
		if operation.controlsWorkload(instance.WorkloadUID) {
			target.Instances = append(target.Instances, instance)
		}
	}
	return &target
}

func (c *SchedulerController) targetWorkloadsLocked(target serving.SchedulerCandidate) []string {
	if parent := c.runs[transitionRunKind+"/"+target.ID]; parent != nil {
		return parent.result.Operation.WorkloadUIDs
	}
	return changedWorkloads(target, c.state.Snapshot)
}

func (c *SchedulerController) routeOperationScopeLocked(parent, selected serving.SchedulerCandidate, intermediate bool) ([]string, bool) {
	scope := c.targetWorkloadsLocked(parent)
	if intermediate {
		scope = slices.DeleteFunc(changedWorkloads(selected, c.state.Snapshot), func(uid string) bool {
			return scope != nil && !slices.Contains(scope, uid)
		})
	}
	return scope, scope != nil && len(scope) == 0
}

func (c *SchedulerController) omittedRouteCompleteLocked(action serving.Action, target serving.SchedulerCandidate) bool {
	if action.Kind != operationRoute || c.runs[action.ID] != nil {
		return false
	}
	if action.RouteTarget == nil {
		if _, empty := c.routeOperationScopeLocked(target, target, false); empty {
			return true
		}
	}
	// A completed consumer proves the frozen bridge was passed, even after
	// a later stage supersedes its routing or policy.
	return slices.ContainsFunc(target.Actions, func(dependent serving.Action) bool {
		consumer := c.runs[dependent.ID]
		return consumer != nil && consumer.action.Status == nativeOperationComplete && slices.Contains(dependent.DependsOn, action.ID)
	})
}

func (c *SchedulerController) ownerDAGCompleteLocked(parent *actionRun) bool {
	for _, child := range c.runs {
		if child != parent && child.owner == parent.owner &&
			(child.action.Status == nativeOperationRunning || child.action.Status == nativeOperationCancel) {
			return false
		}
	}
	target := parent.result.Operation.Target
	for _, action := range target.Actions {
		if action.Status == nativeOperationCancel || c.omittedRouteCompleteLocked(action, *target) {
			continue
		}
		if run := c.runs[action.ID]; run == nil || run.action.Status != nativeOperationComplete {
			return false
		}
	}
	return true
}

func positiveWorkloadScope(target serving.SchedulerCandidate, allowed []string) []string {
	active := []string{}
	for uid, weights := range target.Routing {
		if allowed != nil && !slices.Contains(allowed, uid) {
			continue
		}
		for _, weight := range weights {
			if weight > 0 {
				active = append(active, uid)
				break
			}
		}
	}
	return active
}

// Routes inside one owner form a chain; a partial ACK never races its final
// route. Preparations of that owner's other Nodes still proceed independently.
func (c *SchedulerController) orderRouteChild(action serving.Action, owner string) serving.Action {
	if action.Kind != operationRoute {
		return action
	}
	if run := c.runs[action.ID]; run != nil && run.action.Status == nativeOperationComplete {
		return action
	}
	for _, run := range c.runs {
		if run.owner == owner && run.action.Kind == operationRoute && run.action.Status == nativeOperationRunning && run.action.ID != action.ID {
			action.DependsOn = append(slices.Clone(action.DependsOn), run.action.ID)
		}
	}
	return action
}
