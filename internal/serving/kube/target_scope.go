package kube

import (
	"maps"
	"slices"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

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

func controlledTarget(operation servingstate.Operation) *serving.SchedulerCandidate {
	if operation.Target == nil {
		return nil
	}
	target := *operation.Target
	target.Instances = nil
	target.Routing = map[string]map[string]int32{}
	for uid, route := range operation.Target.Routing {
		if operation.ControlsWorkload(uid) {
			target.Routing[uid] = route
		}
	}
	for _, instance := range operation.Target.Instances {
		if operation.ControlsWorkload(instance.WorkloadUID) {
			target.Instances = append(target.Instances, instance)
		}
	}
	return &target
}

func (c *reservationState) targetWorkloadsLocked(target serving.SchedulerCandidate) []string {
	if parent := c.runs[transitionRunKind+"/"+target.ID]; parent != nil {
		return parent.result.Operation.WorkloadUIDs
	}
	return changedWorkloads(target, c.state.Snapshot)
}

func (c *reservationState) routeOperationScopeLocked(parent, selected serving.SchedulerCandidate, intermediate bool) ([]string, bool) {
	scope := c.targetWorkloadsLocked(parent)
	if intermediate {
		scope = slices.DeleteFunc(changedWorkloads(selected, c.state.Snapshot), func(uid string) bool {
			return scope != nil && !slices.Contains(scope, uid)
		})
	}
	scope = servingstate.PositiveWorkloadScope(selected, scope)
	return scope, len(scope) == 0
}

func (c *reservationState) omittedRouteCompleteLocked(action serving.Action, target serving.SchedulerCandidate) bool {
	if action.Kind != servingstate.OperationRoute || c.runs[action.ID] != nil {
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
		return consumer != nil && consumer.action.Status == servingstate.NativeOperationComplete && slices.Contains(dependent.DependsOn, action.ID)
	})
}

func (c *reservationState) ownerDAGCompleteLocked(parent *actionRun) bool {
	for _, child := range c.runs {
		if child != parent && child.owner == parent.owner &&
			(child.action.Status == servingstate.NativeOperationRunning || child.action.Status == servingstate.NativeOperationCancel) {
			return false
		}
	}
	target := parent.result.Operation.Target
	for _, action := range target.Actions {
		if action.Status == servingstate.NativeOperationCancel || c.omittedRouteCompleteLocked(action, *target) {
			continue
		}
		if run := c.runs[action.ID]; run == nil || run.action.Status != servingstate.NativeOperationComplete {
			return false
		}
	}
	return true
}

// Routes inside one owner form a chain; a partial ACK never races its final
// route. Preparations of that owner's other Nodes still proceed independently.
func (c *reservationState) orderRouteChild(action serving.Action, owner string) serving.Action {
	if action.Kind != servingstate.OperationRoute {
		return action
	}
	if run := c.runs[action.ID]; run != nil && run.action.Status == servingstate.NativeOperationComplete {
		return action
	}
	for _, run := range c.runs {
		if run.owner == owner && run.action.Kind == servingstate.OperationRoute && run.action.Status == servingstate.NativeOperationRunning && run.action.ID != action.ID {
			action.DependsOn = append(slices.Clone(action.DependsOn), run.action.ID)
		}
	}
	return action
}

// Rebase under the ledger lock immediately before admission. The owner keeps
// its original delta, not an obsolete whole-workload route declaration.
func (c *reservationState) rebaseRouteLocked(selected serving.SchedulerCandidate, owner string, current map[string]map[string]int32, physical []serving.InstanceState) (serving.SchedulerCandidate, []string, map[string][]string) {
	parent := c.runs[transitionRunKind+"/"+owner]
	if parent == nil {
		return selected, nil, nil
	}
	result := selected
	result.Instances = nil
	result.Routing = make(map[string]map[string]int32, len(selected.Routing))
	policies := []string{}
	touched := make(map[string][]string, len(selected.Routing))
	for uid, wanted := range selected.Routing {
		routeOnly := !servingstate.HasPhysicalWorkloadScope(parent.result.Operation.Target.Instances, parent.result.Before, parent.scope.Nodes, uid)
		ready, newer, held, withdrawn := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
		instances := map[string]serving.SchedulerInstance{}
		owned := map[string]bool{}
		for _, original := range parent.result.Operation.Target.Instances {
			if original.WorkloadUID != uid {
				continue
			}
			id := original.ID
			actual := schedulerPhysicalState(id, physical)
			if actual != nil {
				id = actual.ID
			}
			policyChanged := actual != nil && actual.Native != nil && !schedulerPolicyAcknowledged(original, actual.Native.Metadata)
			if routeOnly || slices.Contains(parent.scope.Nodes, original.Node) || policyChanged {
				owned[id] = true
			}
		}
		for id := range parent.result.Before.Routing[uid] {
			if routeOnly || slices.Contains(parent.scope.Instances, id) {
				owned[id] = true
			}
		}
		// Routing intent changes weights without granting policy or writer ownership.
		deltaIDs := maps.Clone(owned)
		before, final := parent.result.Before.Routing[uid], parent.result.Operation.Target.Routing[uid]
		for _, weights := range [2]map[string]int32{before, final} {
			for id := range weights {
				if before[id] == final[id] {
					continue
				}
				if actual := schedulerPhysicalState(id, physical); actual != nil {
					id = actual.ID
				}
				deltaIDs[id] = true
			}
		}
		for _, actual := range c.state.Snapshot.Instances {
			newer[actual.ID], held[actual.ID], withdrawn[actual.ID] = c.routeInstanceFenceLocked(parent, uid, current[uid], actual)
			if schedulerTargetReady(actual, c.state.Snapshot, physical, false) {
				ready[actual.ID] = true
				actual.WorkloadUID = uid
				instances[actual.ID] = actual
			}
		}
		delta := map[string]int32{}
		for id := range deltaIDs {
			// Owned destinations retain their existing explicit withdrawal.
			if owned[id] || !newer[id] && !withdrawn[id] {
				delta[id] = 0
			}
		}
		for _, instance := range selected.Instances {
			if instance.WorkloadUID != uid {
				continue
			}
			id := instance.ID
			actual := schedulerPhysicalState(id, physical)
			if actual != nil {
				id = actual.ID
			}
			if !deltaIDs[id] || newer[id] || withdrawn[id] {
				continue
			}
			delta[id] = wanted[instance.ID]
			if schedulerTargetReady(instance, c.state.Snapshot, physical, false) {
				instance.ID = id
				ready[id], instances[id] = true, instance
				if owned[id] && delta[id] > 0 && (!held[id] || !schedulerPolicyAcknowledged(instance, actual.Native.Metadata)) {
					policies = append(policies, id)
				}
			}
		}
		touched[uid] = servingstate.RouteWriterDestinations(owned, held)
		// Owned weights claim their existing share first. Intent-only weights
		// determine the ratio of the remaining share after newer writer fences.
		rebaseCurrent := current[uid]
		copiedCurrent := false
		for id, weight := range delta {
			if owned[id] {
				continue
			}
			if !copiedCurrent {
				rebaseCurrent = maps.Clone(current[uid])
				if rebaseCurrent == nil {
					rebaseCurrent = map[string]int32{}
				}
				copiedCurrent = true
			}
			rebaseCurrent[id] = weight
			delete(delta, id)
		}
		result.Routing[uid] = serving.SchedulerRebaseRouting(rebaseCurrent, delta, ready, newer)
		ids := make([]string, 0, len(result.Routing[uid]))
		for id := range result.Routing[uid] {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			result.Instances = append(result.Instances, instances[id])
		}
	}
	return result, policies, touched
}

func (c *reservationState) routeInstanceFenceLocked(parent *actionRun, uid string, current map[string]int32, actual serving.SchedulerInstance) (newer, held, withdrawn bool) {
	newer = c.routeWriters[uid].Protects(parent.started, actual.ID)
	for _, other := range c.runs {
		if other.owner == parent.owner || !other.reserved || !slices.Contains(other.scope.Nodes, actual.Node) {
			continue
		}
		held = true
		commitment := c.returnProofParent(other)
		if commitment == nil || commitment.result == nil || commitment.result.Operation.Target == nil {
			continue
		}
		newer = newer || commitment.started.After(parent.started)
		goal := commitment.result.Operation.Target
		if current[actual.ID] <= 0 && goal.Routing[uid][actual.ID] <= 0 && goal.Routing[uid][actual.TargetID] <= 0 {
			withdrawn = true
		}
	}
	return newer, held, withdrawn
}

// The same mu critical section admits the route and advances its writer fence.
// A missing/failed completion cannot roll back an already authorized write.
func (c *reservationState) recordRouteWriterLocked(owner string, operation servingstate.Operation, touched map[string][]string) {
	parent := c.runs[transitionRunKind+"/"+owner]
	if parent == nil {
		return
	}
	if c.routeWriters == nil {
		c.routeWriters = map[string]servingstate.RouteWriters{}
	}
	for uid, instances := range touched {
		if !operation.ControlsWorkload(uid) {
			continue
		}
		writers := c.routeWriters[uid]
		if writers == nil {
			writers = servingstate.RouteWriters{}
			c.routeWriters[uid] = writers
		}
		for _, instance := range instances {
			if !writers[instance].After(parent.started) {
				writers[instance] = parent.started
			}
		}
	}
}
