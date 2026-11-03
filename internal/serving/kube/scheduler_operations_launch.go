package kube

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
)


func authorizedFixedContinuation(a serving.Action, observed []serving.Action) bool {
	return a.Fixed && slices.ContainsFunc(observed, func(old serving.Action) bool {
		return old.Fixed && old.ID == a.ID && servingstate.ActionEquivalent(old, a)
	})
}

func slotName(slot string) string {
	sum := sha256.Sum256([]byte(slot))
	return "serving-" + hex.EncodeToString(sum[:10])
}

func (c *SchedulerController) launch(target serving.SchedulerCandidate, config SchedulerControllerConfig) {
	dispatch := func(candidate serving.SchedulerCandidate) {
		c.launchTargetLocked(candidate, config)
	}
	c.StartTransition(&target, config.Settings.Controls, func() {
		c.CompensatePreparationsLocked(c.runtime, dispatch)
		c.ContinueTransitionsLocked(config.Settings, false, dispatch)
	}, func(action serving.Action) bool {
		if action.Kind == servingstate.OperationRoute {
			_, empty := c.RouteOperationScopeLocked(target, *cmp.Or(action.RouteTarget, &target), action.RouteTarget != nil)
			return !empty
		}
		return action.Kind == servingstate.OperationPrepare || action.Kind == servingstate.OperationPower || action.Kind == servingstate.OperationResize ||
			c.DependenciesReady(action, target, c.runtime)
	}, func() {
		dispatch(target)
	})
}

func (c *SchedulerController) launchTargetLocked(target serving.SchedulerCandidate, config SchedulerControllerConfig) {
	actions, intermediate := c.intermediateActions(target)
	// Decide eligible old-source stops before starting policy refreshes that can
	// supersede the existing acknowledged handoff proof in this same tick.
	for phase := range 2 {
		for _, action := range actions {
			if (action.Kind == servingstate.OperationStop) != (phase == 0) {
				continue
			}
			// Evaluation can retain a running snapshot after its live run finishes.
			// Reconciliation, not a stale target, retires a terminal action identity.
			if c.ScheduledActionTerminalLocked(&action) {
				continue
			}
			if action.Status == servingstate.NativeOperationCancel {
				c.CancelRunLocked(action.ID)
				continue
			}
			if action.Status == servingstate.NativeOperationComplete || action.Status == servingstate.NativeOperationFailed || c.OmittedRouteCompleteLocked(action, target) {
				continue
			}
			selectedTarget := cmp.Or(action.RouteTarget, &target)
			dependencyTarget := &target
			if action.From == servingstate.ReadySubsetRoute {
				selectedTarget, dependencyTarget = intermediate, intermediate
			}
			if !c.DependenciesReady(action, *dependencyTarget, c.runtime) {
				continue
			}
			operation := scheduledOperation(action, selectedTarget, config, c.powerTargets, c.SnapshotLocked().Workloads)
			if action.Kind == servingstate.OperationRoute {
				scope, empty := c.RouteOperationScopeLocked(target, *selectedTarget, selectedTarget != &target)
				operation.WorkloadUIDs = scope
				if empty {
					continue
				}
				operation.Target = servingstate.ControlledTarget(operation)
			}
			action.Status = servingstate.NativeOperationRunning
			action.Cancelable = action.Kind == servingstate.OperationPrepare
			started := time.Now()
			initial := servingstate.OperationResult{Operation: operation, StartedAt: started.UTC(), Status: "starting"}
			runCtx, run := c.StartScheduledActionLocked(
				&action, &initial, started, target.ID, func() (context.Context, context.CancelFunc) {
					return c.runContext(action, config)
				})
			if run == nil {
				continue
			}
			c.wg.Add(1)
			go func(operation servingstate.Operation, run *servingstate.ActionRun) {
				defer c.wg.Done()
				result := initial
				var err error
				if config.OnAction != nil {
					err = config.OnAction(result)
				}
				effectStarted := err == nil
				if err == nil {
					result, err = c.operations.Execute(runCtx, operation)
					result.StartedAt = initial.StartedAt
				} else {
					result.Status = servingstate.NativeOperationFailed
					result.Error = "persist action start before physical effect: " + err.Error()
					result.EndedAt = time.Now().UTC()
				}
				// Publish creation evidence into the responsibility scope before
				// terminal state or the completion notification becomes visible.
				c.RecordPreparation(run, result.Preparation)
				if config.OnAction != nil {
					if recordErr := config.OnAction(result); recordErr != nil {
						err = errors.Join(err, fmt.Errorf("record physical operation outcome: %w", recordErr))
						result.Error = err.Error()
						if result.Status == servingstate.NativeOperationComplete {
							result.Status = servingstate.NativeOperationPartial
						}
					}
				}
				c.CompleteRun(run, &result, effectStarted, err, runCtx)
				select {
				case c.completions <- struct{}{}:
				default:
				}
			}(operation, run)
		}
	}
}

func (c *SchedulerController) intermediateActions(
	target serving.SchedulerCandidate,
) ([]serving.Action, *serving.SchedulerCandidate) {
	target.Actions = slices.Clone(target.Actions)
	for i := range target.Actions {
		target.Actions[i] = c.OrderRouteChild(target.Actions[i], target.ID)
	}
	// The final handoff still waits for its DAG. Independent Ready destinations
	// can receive traffic meanwhile through the same acknowledged route path.
	pendingRoute := slices.ContainsFunc(target.Actions, func(a serving.Action) bool {
		return a.Kind == servingstate.OperationRoute && !c.DependenciesReady(a, target, c.runtime)
	})
	if !pendingRoute {
		return target.Actions, nil
	}
	var rows []serving.CapacityRow
	if c.capacity != nil {
		rows = c.capacity.Rows()
	}
	partial := c.ReadySubset(target, rows, func() serving.SchedulerSettings { return c.config.Settings }, c.runtime)
	if partial == nil {
		return target.Actions, nil
	}
	route := serving.Action{ID: partial.ID, Kind: servingstate.OperationRoute, From: servingstate.ReadySubsetRoute, To: partial.ID}
	route = c.OrderRouteChild(route, target.ID)
	// Recovery must retain the running route's own frozen target even when the
	// full goal fails. Only final-goal completion ignores this intermediate action.
	partial.Actions = []serving.Action{route}
	actions := append(slices.Clone(target.Actions), route)
	return actions, partial
}



// runContext bounds a prepare run by serving.ActionDeadline.
// ponytail: prepare-only deadline, widen when a non-prepare stall is measured
func (c *SchedulerController) runContext(action serving.Action, config SchedulerControllerConfig) (context.Context, context.CancelFunc) {
	if action.Kind == servingstate.OperationPrepare {
		return context.WithTimeout(c.ctx, serving.ActionDeadline(config.Settings.ObservationWindow, config.Costs, action))
	}
	return context.WithCancel(c.ctx)
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
