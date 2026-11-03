package state

import (
	cmp "cmp"
	sha256 "crypto/sha256"
	hex "encoding/hex"
	json "encoding/json"
	serving "github.com/lab-paper-code/chill/internal/serving"
	slices "slices"
)

func ActionEquivalent(a, b serving.Action) bool {
	return a.Kind == b.Kind && a.Node == b.Node && a.Instance == b.Instance && a.From == b.From && a.To == b.To
}

// ReadySubset preserves serving sources while positive targets prepare, admitting
// independently placed Ready additions only through the capacity/weight judgment.
// Without a serving route, it normalizes the goal's matching Ready subset.
func (c *ReservationState) ReadySubset(target serving.SchedulerCandidate, rows []serving.CapacityRow, configuration func() serving.SchedulerSettings, runtime *serving.Runtime) *serving.SchedulerCandidate {
	snapshot, physical := c.state.Snapshot, runtime.Instances()
	partial := serving.SchedulerCandidate{Routing: map[string]map[string]int32{}}
	retiring := map[string]bool{}
	for _, action := range target.Actions {
		if action.Kind == OperationStop && c.DependenciesReady(action, target, runtime) {
			retiring[action.Instance] = true
		}
	}
	for uid, weights := range target.Routing {
		positive := 0
		for _, weight := range weights {
			if weight > 0 {
				positive++
			}
		}
		selected := []serving.SchedulerInstance{}
		for _, wanted := range target.Instances {
			if wanted.WorkloadUID == uid && weights[wanted.ID] > 0 && !retiring[wanted.ID] && schedulerTargetReady(wanted, snapshot, physical, false) {
				selected = append(selected, wanted)
			}
		}
		// SCH-20/AC-38: preserve verified serving capacity until this workload's
		// positive destinations are Ready; never revive stopped/unroutable paths.
		if len(selected) < positive {
			current := []serving.SchedulerInstance{}
			for _, actual := range snapshot.Instances {
				if actual.WorkloadUID == uid && snapshot.Routing[uid][actual.ID] > 0 && actual.Routable[uid] &&
					!retiring[actual.ID] && schedulerTargetReady(actual, snapshot, physical, false) {
					current = append(current, actual)
				}
			}
			if len(current) > 0 {
				added := []serving.SchedulerInstance{}
				for _, wanted := range selected {
					if !snapshotNodeHasPositiveRoute(snapshot, wanted.Node) && schedulerTargetReady(wanted, snapshot, physical, true) {
						added = append(added, wanted)
					}
				}
				selected, weights = current, snapshot.Routing[uid]
				if len(added) > 0 {
					if expanded, ok := serving.SchedulerReadyExpansion(configuration(), snapshot, rows, uid, current, added); ok {
						selected, weights = append(selected, added...), expanded
					}
				}
			}
		}
		if len(selected) == 0 {
			continue
		}
		partial.Routing[uid] = map[string]int32{}
		var total, cumulative, assigned int64
		for _, wanted := range selected {
			total += int64(weights[wanted.ID])
		}
		for _, wanted := range selected {
			cumulative += int64(weights[wanted.ID])
			normalized := cumulative * 1000 / total
			partial.Routing[uid][wanted.ID] = int32(normalized - assigned)
			assigned = normalized
			partial.Instances = append(partial.Instances, wanted)
		}
	}
	if len(partial.Instances) == 0 || c.RoutingTargetPresent(&partial, snapshot, physical, true, runtime) {
		return nil
	}
	// JSON map keys are sorted; physical identities and weights identify this
	// immutable subset independently of slow action completion order.
	raw, _ := json.Marshal(partial.Routing)
	sum := sha256.Sum256(raw)
	partial.ID = target.ID + "-ready-" + hex.EncodeToString(sum[:10])
	return &partial
}

func snapshotNodeHasPositiveRoute(snapshot serving.SchedulerSnapshot, node serving.NodeReference) bool {
	for _, instance := range snapshot.Instances {
		if instance.Node != node {
			continue
		}
		for _, route := range snapshot.Routing {
			if route[instance.ID] > 0 {
				return true
			}
		}
	}
	return false
}

func (c *ReservationState) DependenciesReady(action serving.Action, target serving.SchedulerCandidate, runtime *serving.Runtime) bool {
	plan := target
	if action.RouteTarget != nil {
		target = *action.RouteTarget
	}
	if action.Node.UID != "" && slices.ContainsFunc(c.state.Snapshot.Instances, func(instance serving.SchedulerInstance) bool {
		return instance.Node == action.Node && !instance.Ready && !instance.Stopped && !instance.Terminated
	}) {
		return false
	}
	if action.Kind == OperationRoute {
		uids := c.targetWorkloadsLocked(target)
		for _, wanted := range target.Instances {
			if !slices.Contains(uids, wanted.WorkloadUID) || target.Routing[wanted.WorkloadUID][wanted.ID] <= 0 {
				continue
			}
			if slices.ContainsFunc(c.state.Snapshot.Instances, func(actual serving.SchedulerInstance) bool {
				return (actual.ID == wanted.ID || actual.TargetID == wanted.ID) && !actual.Ready && !actual.Stopped
			}) {
				return false
			}
		}
	}
	return c.dependenciesComplete(action, plan, runtime)
}

func (c *ReservationState) dependenciesComplete(action serving.Action, plan serving.SchedulerCandidate, runtime *serving.Runtime) bool {
	for _, dep := range action.DependsOn {
		run := c.runs[dep]
		if run != nil && run.action.Status == NativeOperationComplete {
			if action.Kind == OperationStop && run.action.RouteTarget != nil {
				uids := c.handoffScope(action, run.action, plan)
				if run.result == nil || len(uids) == 0 || !run.result.Operation.ControlsWorkload(uids[0]) {
					return false
				}
				proof := run.result.Operation
				// Recheck this source's handoff, not unrelated routes.
				proof.WorkloadUIDs = uids
				if !c.RoutingTargetPresent(ControlledTarget(proof), c.state.Snapshot, runtime.Instances(), true, runtime) {
					return false
				}
			}
			continue
		}
		i := slices.IndexFunc(plan.Actions, func(a serving.Action) bool { return a.ID == dep && a.Kind == OperationRoute })
		if run != nil || i < 0 || (action.Kind != OperationStop && action.Kind != OperationRoute) ||
			!c.dependenciesComplete(plan.Actions[i], plan, runtime) ||
			!c.completedHandoffPresent(*cmp.Or(plan.Actions[i].RouteTarget, &plan), c.handoffScope(action, plan.Actions[i], plan), runtime) {
			return false
		}
	}
	return true
}

func (c *ReservationState) handoffScope(action, dependency serving.Action, plan serving.SchedulerCandidate) []string {
	if dependency.RouteTarget == nil || (action.Kind != OperationStop && action.Kind != OperationRoute) {
		return c.targetWorkloadsLocked(plan)
	}
	consumers := plan.Actions
	if action.Kind == OperationStop {
		consumers = []serving.Action{action}
	}
	uids := []string{}
	for _, stop := range consumers {
		if stop.Kind == OperationStop && slices.Contains(stop.DependsOn, dependency.ID) {
			if uid := handoffSourceUID(*dependency.RouteTarget, stop.Instance); uid != "" && !slices.Contains(uids, uid) {
				uids = append(uids, uid)
			}
		}
	}
	return uids
}

func handoffSourceUID(target serving.SchedulerCandidate, instance string) string {
	for _, source := range target.Instances {
		if source.ID == instance {
			return source.WorkloadUID
		}
	}
	return ""
}

func (c *ReservationState) completedHandoffPresent(target serving.SchedulerCandidate, uids []string, runtime *serving.Runtime) bool {
	physical := runtime.Instances()
	selected := ControlledTarget(Operation{Target: &target, WorkloadUIDs: uids})
	if !c.RoutingTargetPresent(selected, c.state.Snapshot, physical, false, runtime) {
		return false
	}
	for _, run := range c.runs {
		if run.action.Kind == OperationRoute && run.action.Status == NativeOperationComplete &&
			run.result != nil && run.result.Status == NativeOperationComplete &&
			c.RoutingTargetPresent(run.result.Operation.Target, c.state.Snapshot, physical, true, runtime) {
			return true
		}
	}
	return false
}
