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

func changedWorkloadScope(target serving.SchedulerCandidate, snapshot serving.SchedulerSnapshot, allowed []string) []string {
	return slices.DeleteFunc(changedWorkloads(target, snapshot), func(uid string) bool {
		return allowed != nil && !slices.Contains(allowed, uid)
	})
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

// Split the global choice at existing cleanup ownership boundaries. A Ready
// refresh must not wait for another UID's preparation, or acquire its lifecycle.
func (c *SchedulerController) cleanupTargetsLocked(target serving.SchedulerCandidate, parents []*actionRun) (serving.SchedulerCandidate, []serving.SchedulerCandidate) {
	remaining := changedWorkloads(target, c.state.Snapshot)
	var cleanupUIDs []string
	blockedNodes := map[serving.NodeReference]bool{}
	var refreshes []serving.SchedulerCandidate
	for _, parent := range parents {
		if !parent.cleanup {
			continue
		}
		var uids []string
		for _, uid := range remaining {
			if slices.Contains(parent.scope.Workloads, uid) {
				uids = append(uids, uid)
			}
		}
		if len(uids) == 0 {
			continue
		}
		cleanupUIDs = append(cleanupUIDs, uids...)
		remaining = slices.DeleteFunc(remaining, func(uid string) bool { return slices.Contains(uids, uid) })
		for _, node := range parent.scope.Nodes {
			blockedNodes[node] = true
		}
		for _, action := range target.Actions {
			if action.Kind != operationRoute {
				continue
			}
			refresh := *controlledTarget(Operation{Target: &target, WorkloadUIDs: uids})
			refresh.ID += "/cleanup/" + parent.owner
			action.ID += "/cleanup/" + parent.owner
			action.DependsOn, action.Status = nil, ""
			refresh.Nodes, refresh.Actions = nil, []serving.Action{action}
			refreshes = append(refreshes, refresh)
		}
	}
	if len(cleanupUIDs) == 0 {
		return target, nil
	}
	for _, instance := range target.Instances {
		if slices.Contains(cleanupUIDs, instance.WorkloadUID) || slices.Contains(cleanupUIDs, instance.OwnerWorkloadUID) {
			blockedNodes[instance.Node] = true
		}
	}
	independent := *controlledTarget(Operation{Target: &target, WorkloadUIDs: remaining})
	independent.Actions = nil
	var removed []string
	for _, action := range target.Actions {
		if action.Kind == operationRoute && len(remaining) == 0 || blockedNodes[action.Node] {
			removed = append(removed, action.ID)
		} else {
			independent.Actions = append(independent.Actions, action)
		}
	}
	for i := range independent.Actions {
		action := &independent.Actions[i]
		action.DependsOn = slices.DeleteFunc(slices.Clone(action.DependsOn), func(id string) bool { return slices.Contains(removed, id) })
	}
	return independent, refreshes
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
