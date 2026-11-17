package serving

import (
	"fmt"
	"slices"
)

// schedulerReplayScope removes only prediction authority. Physical inventory,
// allocations, requests and the operation owner's ledger remain unchanged.
// It depends solely on the snapshot, never on the candidate being compared.
type schedulerReplayScope struct {
	actions   map[string]string
	instances map[string]string
	nodes     map[NodeReference]bool
	evidence  []string
}

func schedulerReplayInstanceReason(i SchedulerInstance) string {
	if i.Terminated {
		return ""
	}
	if i.Profile == nil {
		return "MissingProfile"
	}
	for _, batch := range i.Profile.Batches {
		if batch.Batch <= i.MaxBatch && (batch.ServiceDistribution == nil || len(batch.ServiceSamples) == 0) {
			return "MissingConditionalServiceSupport"
		}
	}
	return ""
}

// A reservation's immutable target models an authorized, not-yet-created
// executor. The target never supplies current capacity or physical occupancy.
func schedulerReplayTargets(snapshot SchedulerSnapshot) []SchedulerInstance {
	var targets []SchedulerInstance
	for _, reservation := range snapshot.Reservations {
		if reservation.Target == nil {
			continue
		}
		for _, wanted := range reservation.Target.Instances {
			if !slices.Contains(reservation.Nodes, wanted.Node) || slices.ContainsFunc(targets, func(i SchedulerInstance) bool { return i.ID == wanted.ID }) {
				continue
			}
			targets = append(targets, wanted)
		}
	}
	return targets
}

func schedulerReplayInitialInstance(snapshot SchedulerSnapshot, targets []SchedulerInstance, id string) (SchedulerInstance, bool) {
	for _, actual := range snapshot.Instances {
		if actual.ID == id || actual.TargetID != "" && actual.TargetID == id {
			return actual, true
		}
	}
	for _, wanted := range targets {
		if wanted.ID == id {
			return wanted, true
		}
	}
	return SchedulerInstance{}, false
}

func schedulerReplayInitialScope(snapshot SchedulerSnapshot) schedulerReplayScope {
	if snapshot.replayScope != nil {
		return *snapshot.replayScope
	}
	scope := schedulerReplayScope{actions: map[string]string{}, instances: map[string]string{}, nodes: map[NodeReference]bool{}}
	targets := schedulerReplayTargets(snapshot)
	scope.excludeUnsupportedInstances(snapshot.Instances)
	for _, action := range snapshot.Actions {
		if action.Status == schedulerActionComplete || action.Status == schedulerActionCancel {
			continue
		}
		failedAttempt := (action.Status == schedulerActionFailed || action.Status == schedulerActionPartial) && action.PreparationFailure != nil
		reason := ""
		switch {
		case action.Status == schedulerActionFailed || action.Status == schedulerActionPartial:
			reason = "ActionNotExecutable:" + action.Status + ":" + action.Reason
		case action.Kind == schedulerActionPrepare:
			instance, found := schedulerReplayInitialInstance(snapshot, targets, action.Instance)
			switch {
			case !found:
				reason = "MissingActionInstanceState"
			case instance.Node != action.Node || schedulerState(instance) != action.To:
				reason = "PreparationTargetStateMismatch"
			default:
				reason = schedulerReplayInstanceReason(instance)
			}
		case action.Kind == schedulerActionRoute && action.Status == phaseRunning:
			reason = "InProgressRoutingAcknowledgementUnobserved"
		case action.Kind == schedulerActionRoute && action.RouteTarget == nil:
			reason = "MissingActionRouteTarget"
		}
		if reason == "" {
			continue
		}
		scope.actions[action.ID] = reason
		if action.Node.UID != "" && !failedAttempt {
			scope.nodes[action.Node] = true
		}
		if action.Instance != "" && action.Kind == schedulerActionPrepare {
			if !failedAttempt {
				scope.instances[action.Instance] = reason
			}
			// A failed attempt's Pod excludes only that instance; its Node
			// stays plannable so the Pod can be retired.
			for _, actual := range snapshot.Instances {
				if schedulerReplayActionAffectsInstance(action, actual) {
					scope.instances[actual.ID] = reason
					if !failedAttempt {
						scope.nodes[actual.Node] = true
					}
				}
			}
		}
		if action.Kind == schedulerActionPower {
			for _, actual := range snapshot.Instances {
				if actual.Node == action.Node {
					scope.instances[actual.ID] = reason
				}
			}
		}
		scope.evidence = append(scope.evidence, fmt.Sprintf("ReplayExcludedAction:%s:%s:%s:%s", action.Node.UID, action.Instance, action.ID, reason))
	}
	scope.propagate(snapshot)
	return scope
}

func (scope *schedulerReplayScope) excludeUnsupportedInstances(instances []SchedulerInstance) {
	for _, instance := range instances {
		if reason := schedulerReplayInstanceReason(instance); reason != "" {
			scope.instances[instance.ID] = reason
			scope.nodes[instance.Node] = true
			scope.evidence = append(scope.evidence, fmt.Sprintf("ReplayExcludedInstance:%s:%s:%s", instance.Node.UID, instance.ID, reason))
		}
	}
}

func schedulerReplayActionAffectsInstance(action Action, actual SchedulerInstance) bool {
	if (action.Status == schedulerActionFailed || action.Status == schedulerActionPartial) && action.PreparationFailure != nil {
		// A stable target may already name a later, successful attempt. Only
		// the failed attempt's exact physical Pod retains its uncertainty.
		pod := action.PreparationFailure.PodUID
		return pod != "" && (actual.PodUID == pod || actual.ID == pod)
	}
	return actual.ID == action.Instance || actual.TargetID == action.Instance
}

// blocks reports why replay cannot execute an unfinished action: physical work
// on an excluded Node, the preparation of an excluded instance, or a drain
// waiting on an excluded instance's outstanding work. Exclusion withdraws the
// prediction of an instance's service; its stop and remove stay executable.
func (scope *schedulerReplayScope) blocks(snapshot SchedulerSnapshot, action Action) string {
	if scope.instances[action.Instance] != "" {
		switch action.Kind {
		case schedulerActionPrepare:
			return "InstanceDependencyExcluded"
		case schedulerActionDrain:
			if slices.ContainsFunc(snapshot.Outstanding, func(r SchedulerRequest) bool { return r.Instance == action.Instance }) {
				return "InstanceDependencyExcluded"
			}
		}
	}
	if (action.Kind == schedulerActionPrepare || action.Kind == schedulerActionRemove || action.Kind == schedulerActionPower) && scope.nodes[action.Node] {
		return "NodeDependencyExcluded"
	}
	return ""
}

func (scope *schedulerReplayScope) propagate(snapshot SchedulerSnapshot) {
	// A blocked owner dependency also freezes physical work on the dependent
	// Node. Routing only blocks its own descendants, not unrelated destinations.
	for changed := true; changed; {
		changed = false
		for _, action := range snapshot.Actions {
			if action.Status == schedulerActionComplete || action.Status == schedulerActionCancel || scope.actions[action.ID] != "" {
				continue
			}
			reason := scope.blocks(snapshot, action)
			if action.Status != phaseRunning {
				for _, dep := range action.DependsOn {
					if scope.actions[dep] != "" {
						reason = "DependencyExcluded:" + dep
						break
					}
				}
			}
			if reason != "" {
				scope.actions[action.ID] = reason
				if action.Kind == schedulerActionPrepare || action.Kind == schedulerActionRemove || action.Kind == schedulerActionPower {
					scope.nodes[action.Node] = true
				}
				if action.Kind == schedulerActionPrepare {
					scope.instances[action.Instance] = reason
				}
				scope.evidence = append(scope.evidence, fmt.Sprintf("ReplayExcludedAction:%s:%s:%s:%s", action.Node.UID, action.Instance, action.ID, reason))
				changed = true
			}
		}
	}
}

func schedulerReplayScopedSnapshot(snapshot SchedulerSnapshot) SchedulerSnapshot {
	if snapshot.replayScope == nil {
		scope := schedulerReplayInitialScope(snapshot)
		snapshot.replayScope = &scope
	}
	return snapshot
}
