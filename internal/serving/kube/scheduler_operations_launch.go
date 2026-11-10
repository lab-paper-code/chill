package kube

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
)

func actionEquivalent(a, b serving.Action) bool {
	return a.Kind == b.Kind && a.Node == b.Node && a.Instance == b.Instance && a.From == b.From && a.To == b.To
}

func authorizedFixedContinuation(a serving.Action, observed []serving.Action) bool {
	return a.Fixed && slices.ContainsFunc(observed, func(old serving.Action) bool {
		return old.Fixed && old.ID == a.ID && actionEquivalent(old, a)
	})
}

func slotName(slot string) string {
	sum := sha256.Sum256([]byte(slot))
	return "serving-" + hex.EncodeToString(sum[:10])
}

func (c *SchedulerController) launch(ctx context.Context, target serving.SchedulerCandidate, config SchedulerControllerConfig) {
	dispatch := func(candidate serving.SchedulerCandidate) {
		c.launchTargetLocked(ctx, candidate, config)
	}
	c.startTransition(&target, config.Settings.Controls, func() {
		c.compensatePreparationsLocked(c.runtime, dispatch)
		c.continueTransitionsLocked(config.Settings, false, dispatch)
	}, func(action serving.Action) bool {
		if action.Kind == servingstate.OperationRoute {
			_, empty := c.routeOperationScopeLocked(target, *cmp.Or(action.RouteTarget, &target), action.RouteTarget != nil)
			return !empty
		}
		return action.Kind == servingstate.OperationPrepare || action.Kind == servingstate.OperationPower || action.Kind == servingstate.OperationResize ||
			c.dependenciesReady(action, target, c.runtime)
	}, func() {
		dispatch(target)
	})
}

func (c *SchedulerController) launchTargetLocked(ctx context.Context, target serving.SchedulerCandidate, config SchedulerControllerConfig) {
	actions, intermediate := c.intermediateActions(ctx, target)
	if ctx != nil && ctx.Err() != nil {
		return
	}
	var blocked map[string]string
	// Decide eligible old-source stops before starting policy refreshes that can
	// supersede the existing acknowledged handoff proof in this same tick.
	for phase := range 2 {
		for _, action := range actions {
			if (action.Kind == servingstate.OperationStop) != (phase == 0) {
				continue
			}
			// Evaluation can retain a running snapshot after its live run finishes.
			// Reconciliation, not a stale target, retires a terminal action identity.
			if c.scheduledActionTerminalLocked(&action) {
				continue
			}
			if action.Status == servingstate.NativeOperationCancel {
				c.cancelRunLocked(action.ID)
				continue
			}
			if action.Status == servingstate.NativeOperationComplete || action.Status == servingstate.NativeOperationFailed || c.omittedRouteCompleteLocked(action, target) {
				continue
			}
			selectedTarget := cmp.Or(action.RouteTarget, &target)
			dependencyTarget := &target
			if action.From == readySubsetRoute {
				selectedTarget, dependencyTarget = intermediate, intermediate
			}
			reason := c.unconfirmedActionReason(action, *selectedTarget)
			if reason != "" || !c.dependenciesComplete(action, *dependencyTarget, c.runtime) {
				if reason != "" {
					if blocked == nil {
						blocked = map[string]string{}
					}
					blocked[action.ID] = reason
				}
				if parent := c.runs[transitionRunKind+"/"+target.ID]; parent != nil && parent.action.Status == servingstate.NativeOperationFailed && action.Kind == servingstate.OperationStop {
					return
				}
				continue
			}
			operation := scheduledOperation(action, selectedTarget, config, c.powerTargets, c.snapshotLocked().Workloads)
			bindTerminatedPodRemoval(&operation, action, c.state.Snapshot.Instances)
			var routeTouches map[string][]string
			if action.Kind == servingstate.OperationRoute {
				scope, empty := c.routeOperationScopeLocked(target, *selectedTarget, selectedTarget != &target)
				operation.WorkloadUIDs = scope
				if empty {
					continue
				}
				current, _ := c.runtime.SchedulerRouting()
				physical := c.runtime.Instances()
				rebased, policies, touched := c.rebaseRouteLocked(*controlledTarget(operation), target.ID, current, physical)
				routeTouches = touched
				operation.Target, operation.PolicyInstances = &rebased, policies
				active := servingstate.PositiveWorkloadScope(rebased, scope)
				if len(active) == 0 {
					continue
				}
				if len(active) != len(scope) {
					operation.WorkloadUIDs = active
					operation.Target = controlledTarget(operation)
				}
			}
			action.Status = servingstate.NativeOperationRunning
			action.Cancelable = action.Kind == servingstate.OperationPrepare
			started := time.Now()
			initial := servingstate.OperationResult{Operation: operation, StartedAt: started.UTC(), Status: "starting"}
			runCtx, run := c.startScheduledActionLocked(
				&action, &initial, started, target.ID, func() (context.Context, context.CancelFunc) {
					return c.runContext(action, config)
				})
			if run == nil {
				continue
			}
			if action.Kind == servingstate.OperationRoute {
				c.recordRouteWriterLocked(target.ID, operation, routeTouches)
			}
			c.wg.Add(1)
			go c.executeScheduledAction(runCtx, initial, run, config)
		}
	}
	c.failUnconfirmedTransitionLocked(target, actions, blocked, config)
}

func (c *SchedulerController) executeScheduledAction(ctx context.Context, initial servingstate.OperationResult, run *actionRun, config SchedulerControllerConfig) {
	defer c.wg.Done()
	result := initial
	var err error
	if config.OnAction != nil {
		err = config.OnAction(result)
	}
	effectStarted := err == nil
	if err == nil {
		result, err = c.operations.Execute(ctx, initial.Operation)
		result.StartedAt = initial.StartedAt
	} else {
		result.Status = servingstate.NativeOperationFailed
		result.Error = "persist action start before physical effect: " + err.Error()
		result.EndedAt = time.Now().UTC()
	}
	// Publish creation evidence into the responsibility scope before
	// terminal state or the completion notification becomes visible.
	c.recordPreparation(run, result.Preparation)
	if config.OnAction != nil {
		if recordErr := config.OnAction(result); recordErr != nil {
			err = errors.Join(err, fmt.Errorf("record physical operation outcome: %w", recordErr))
			result.Error = err.Error()
			if result.Status == servingstate.NativeOperationComplete {
				result.Status = servingstate.NativeOperationPartial
			}
		}
	}
	c.completeRun(run, &result, effectStarted, err, ctx)
	select {
	case c.completions <- struct{}{}:
	default:
	}
}

func (c *SchedulerController) intermediateActions(
	ctx context.Context,
	target serving.SchedulerCandidate,
) ([]serving.Action, *serving.SchedulerCandidate) {
	target.Actions = slices.Clone(target.Actions)
	for i := range target.Actions {
		target.Actions[i] = c.orderRouteChild(target.Actions[i], target.ID)
	}
	// The final handoff still waits for its DAG. Independent Ready destinations
	// can receive traffic meanwhile through the same acknowledged route path.
	pendingRoute := slices.ContainsFunc(target.Actions, func(a serving.Action) bool {
		return a.Kind == servingstate.OperationRoute && !c.dependenciesReady(a, target, c.runtime)
	})
	if !pendingRoute {
		return target.Actions, nil
	}
	partial := c.readySubset(ctx, target, func() serving.SchedulerSettings { return c.config.Settings }, c.runtime)
	if partial == nil {
		return target.Actions, nil
	}
	route := serving.Action{ID: partial.ID, Kind: servingstate.OperationRoute, From: readySubsetRoute, To: partial.ID}
	route = c.orderRouteChild(route, target.ID)
	// Recovery must retain the running route's own frozen target even when the
	// full goal fails. Only final-goal completion ignores this intermediate action.
	partial.Actions = []serving.Action{route}
	actions := append(slices.Clone(target.Actions), route)
	return actions, partial
}

// readySubset preserves serving sources while positive targets prepare, admitting
// independently placed Ready additions only through the shared feedback judgment.
// Without a serving route, it normalizes the goal's matching Ready subset.
func (c *reservationState) readySubset(ctx context.Context, target serving.SchedulerCandidate, configuration func() serving.SchedulerSettings, runtime *serving.Runtime) *serving.SchedulerCandidate {
	snapshot, physical := c.state.Snapshot, runtime.Instances()
	partial := serving.SchedulerCandidate{Routing: map[string]map[string]int32{}}
	retiring := map[string]bool{}
	for _, action := range target.Actions {
		if action.Kind == servingstate.OperationStop && c.dependenciesReady(action, target, runtime) {
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
					if expanded, ok := serving.SchedulerReadyExpansion(ctx, configuration(), snapshot, uid, current, added); ok {
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
	if len(partial.Instances) == 0 || c.routingTargetPresent(&partial, snapshot, physical, true, runtime) {
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

// runContext bounds a prepare run by serving.ActionDeadline.
// ponytail: prepare-only deadline, widen when a non-prepare stall is measured
func (c *SchedulerController) runContext(action serving.Action, config SchedulerControllerConfig) (context.Context, context.CancelFunc) {
	if action.Kind == servingstate.OperationPrepare {
		return context.WithTimeout(c.ctx, serving.ActionDeadline(config.Settings.ObservationWindow, config.Costs, action))
	}
	return context.WithCancel(c.ctx)
}

func (c *reservationState) dependenciesReady(action serving.Action, target serving.SchedulerCandidate, runtime *serving.Runtime) bool {
	plan := target
	if action.RouteTarget != nil {
		target = *action.RouteTarget
	}
	if c.unconfirmedActionReason(action, target) != "" {
		return false
	}
	return c.dependenciesComplete(action, plan, runtime)
}

func (c *reservationState) dependenciesComplete(action serving.Action, plan serving.SchedulerCandidate, runtime *serving.Runtime) bool {
	for _, dep := range action.DependsOn {
		run := c.runs[dep]
		if run != nil && run.action.Status == servingstate.NativeOperationComplete {
			if action.Kind == servingstate.OperationStop && run.action.Kind == servingstate.OperationRoute {
				uids := c.handoffScope(action, run.action, plan)
				if run.result == nil || len(uids) == 0 || !run.result.Operation.ControlsWorkload(uids[0]) {
					return false
				}
				proof := run.result.Operation
				// Recheck this source's handoff, not unrelated routes.
				proof.WorkloadUIDs = uids
				selected, physical := controlledTarget(proof), runtime.Instances()
				if selected == nil {
					return false
				}
				for _, weights := range selected.Routing {
					if weights[action.Instance] > 0 {
						if parent := c.returnProofParent(run); parent != nil && parent.action.Status == servingstate.NativeOperationRunning {
							parent.action.Status, parent.action.Reason = servingstate.NativeOperationFailed, "completed route still assigns traffic to retiring source"
							parent.result.Status, parent.result.Error, parent.result.EndedAt = servingstate.NativeOperationFailed, parent.action.Reason, time.Now().UTC()
						}
						return false
					}
				}
				if !c.routingTargetPresent(selected, c.state.Snapshot, physical, true, runtime) && !c.composedRoutePresent(run, selected, c.state.Snapshot, physical, runtime) {
					return false
				}
			}
			continue
		}
		i := slices.IndexFunc(plan.Actions, func(a serving.Action) bool { return a.ID == dep && a.Kind == servingstate.OperationRoute })
		if run != nil || i < 0 || (action.Kind != servingstate.OperationStop && action.Kind != servingstate.OperationRoute) ||
			!c.dependenciesComplete(plan.Actions[i], plan, runtime) ||
			!c.completedHandoffPresent(*cmp.Or(plan.Actions[i].RouteTarget, &plan), c.handoffScope(action, plan.Actions[i], plan), runtime) {
			return false
		}
	}
	return true
}

func (c *reservationState) handoffScope(action, dependency serving.Action, plan serving.SchedulerCandidate) []string {
	if dependency.RouteTarget == nil || (action.Kind != servingstate.OperationStop && action.Kind != servingstate.OperationRoute) {
		return c.targetWorkloadsLocked(plan)
	}
	consumers := plan.Actions
	if action.Kind == servingstate.OperationStop {
		consumers = []serving.Action{action}
	}
	uids := []string{}
	for _, stop := range consumers {
		if stop.Kind == servingstate.OperationStop && slices.Contains(stop.DependsOn, dependency.ID) {
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

func (c *reservationState) completedHandoffPresent(target serving.SchedulerCandidate, uids []string, runtime *serving.Runtime) bool {
	physical := runtime.Instances()
	selected := controlledTarget(servingstate.Operation{Target: &target, WorkloadUIDs: uids})
	if !c.routingTargetPresent(selected, c.state.Snapshot, physical, false, runtime) {
		return false
	}
	for _, run := range c.runs {
		if run.action.Kind == servingstate.OperationRoute && run.action.Status == servingstate.NativeOperationComplete &&
			run.result != nil && run.result.Status == servingstate.NativeOperationComplete &&
			c.routingTargetPresent(run.result.Operation.Target, c.state.Snapshot, physical, true, runtime) {
			return true
		}
	}
	return false
}

func bindTerminatedPodRemoval(operation *servingstate.Operation, action serving.Action, instances []serving.SchedulerInstance) {
	if action.Kind == servingstate.OperationRemove {
		for _, instance := range instances {
			if instance.ID == action.Instance && instance.Node == action.Node && instance.PodUID != "" &&
				instance.ID == instance.PodUID && instance.Stopped && instance.Terminated {
				operation.Instance, operation.PodUID = "", instance.PodUID
				break
			}
		}
	}
}

// scheduledOperation builds the operation of one planned action. A preparation
// names the Workload its target instance serves as the Pod's owner, so the
// instance keeps its Workload while unrouted.
func scheduledOperation(action serving.Action, target *serving.SchedulerCandidate, config SchedulerControllerConfig, power map[string]time.Duration, workloads []*serving.Definition) servingstate.Operation {
	operation := servingstate.Operation{ID: action.ID, Kind: action.Kind, Node: action.Node, Instance: action.Instance, Target: target,
		ExpectedSeconds: action.ExpectedSeconds, ExpectedSource: action.ExpectedSource}
	if action.Kind == servingstate.OperationPrepare {
		for _, i := range target.Instances {
			if i.ID == action.Instance && i.Profile != nil && i.Profile.Condition.Firstfloor != nil {
				template, ok := config.Preparations[i.Profile.ID]
				if !ok {
					continue
				}
				template.Instance = i
				// Inventory's positive serving weight is not an applied route.
				// Planned Pods remain unrouted through their empty routing seed.
				template.Instance.Weight = max(1, i.Weight)
				template.Name = slotName(i.ID)
				template.Config.Policy = native.Policy{ProfileID: i.Profile.ID, DecisionID: target.ID,
					DeadlinePolicy: native.EffectiveDeadlinePolicy(i.Profile.Condition.Firstfloor.DeadlinePolicy),
					MaxBatchSize:   i.MaxBatch, MaxBatchWaitMS: float64(i.MaxWait) / float64(time.Millisecond), FixedBatchSize: i.FixedBatchSize}
				for _, d := range workloads {
					if d != nil && d.UID == i.WorkloadUID {
						template.Owner = &serving.WorkloadReference{Name: d.Name, UID: d.UID}
					}
				}
				operation.Prepare = &template
				break
			}
		}
	}
	if action.Kind == servingstate.OperationPower {
		if maxDuration, ok := power[action.Node.UID+"/"+action.To]; ok {
			operation.PowerMode, operation.MaxActuationDuration = action.To, maxDuration
		}
	}
	return operation
}
