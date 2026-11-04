package kube

import (
	"slices"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	"github.com/lab-paper-code/chill/internal/serving"
)

func (c *reservationState) actionEffectPresent(snapshot serving.SchedulerSnapshot, run *actionRun, runtime *serving.Runtime) bool {
	if run.result == nil || run.result.Status != servingstate.NativeOperationComplete {
		return false
	}
	a := run.action
	physical := runtime.Instances()
	switch a.Kind {
	case servingstate.OperationPrepare:
		p := run.result.Operation.Prepare
		if p == nil || p.Instance.Profile == nil {
			return false
		}
		for _, i := range physical {
			if (i.ID == a.Instance || i.TargetID == a.Instance) && !i.Terminated && i.Node == a.Node && i.Profile == p.Instance.Profile.ID && i.Native != nil && i.Native.Ready {
				return true
			}
		}
	case servingstate.OperationRemove:
		for _, i := range physical {
			if i.ID == a.Instance {
				return false
			}
		}
		return true
	case servingstate.OperationStop:
		for _, i := range physical {
			if i.ID == a.Instance {
				return i.Stopped
			}
		}
		return true
	case servingstate.OperationDrain:
		for _, r := range snapshot.Outstanding {
			if r.Instance == a.Instance {
				return false
			}
		}
		return true
	case servingstate.OperationPower:
		for _, n := range snapshot.Nodes {
			if n.Node == a.Node {
				return n.PowerMode == a.To
			}
		}
	case servingstate.OperationRoute:
		target := controlledTarget(run.result.Operation)
		if c.routingTargetPresent(target, snapshot, physical, true, runtime) {
			return true
		}
		return c.composedRoutePresent(run, target, snapshot, physical, runtime)
	}
	return false
}

// The completed receipt proves this owner's handoff. Later admitted routes may
// change shared shares; only its physical policy/withdrawal obligations survive.
// Do not depend on another owner's receipt outliving the action-view GC window.
func (c *reservationState) composedRoutePresent(run *actionRun, target *serving.SchedulerCandidate, snapshot serving.SchedulerSnapshot, physical []serving.InstanceState, runtime *serving.Runtime) bool {
	if target == nil || run.result.Status != servingstate.NativeOperationComplete || run.proofParent == nil ||
		run.proofParent.result.Before.Routing == nil || len(run.proofParent.scope.Nodes) == 0 {
		return false
	}
	current, _ := runtime.SchedulerRouting()
	for uid, route := range target.Routing {
		for _, wanted := range target.Instances {
			if wanted.WorkloadUID == uid && route[wanted.ID] > 0 && slices.Contains(run.proofParent.scope.Nodes, wanted.Node) &&
				!schedulerTargetReady(wanted, snapshot, physical, true) {
				return false
			}
		}
		for _, id := range run.proofParent.scope.Instances {
			if route[id] <= 0 && current[uid][id] > 0 {
				return false
			}
		}
		ready := slices.ContainsFunc(snapshot.Instances, func(actual serving.SchedulerInstance) bool {
			return current[uid][actual.ID] > 0 && schedulerTargetReady(actual, snapshot, physical, true)
		})
		if !ready {
			return false
		}
	}
	return len(target.Routing) > 0
}

// A failed observation is neither an effect proof nor a counterexample. Keep
// the completed receipt and its ownership until the same source is observable.
func (c *reservationState) actionEffectUnknown(snapshot serving.SchedulerSnapshot, run *actionRun, runtime *serving.Runtime) bool {
	if run.result == nil {
		return false
	}
	if run.action.Kind == servingstate.OperationPower {
		return slices.ContainsFunc(snapshot.Nodes, func(node serving.SchedulerNode) bool {
			return node.Node == run.action.Node && node.PowerMode == ""
		})
	}
	physical := runtime.Instances()
	if run.action.Kind == servingstate.OperationPrepare {
		return slices.ContainsFunc(physical, func(instance serving.InstanceState) bool {
			return (instance.ID == run.action.Instance || instance.TargetID == run.action.Instance) &&
				instance.Node == run.action.Node && !instance.Stopped && !instance.Ready
		})
	}
	if run.action.Kind == servingstate.OperationRoute && run.result.Operation.Target != nil {
		for _, wanted := range run.result.Operation.Target.Instances {
			if !run.result.Operation.ControlsWorkload(wanted.WorkloadUID) {
				continue
			}
			if state := schedulerPhysicalState(wanted.ID, physical); state != nil && !state.Stopped &&
				(!state.Ready || state.Reason == "PolicyAckPending") {
				return true
			}
		}
	}
	return false
}

func (c *reservationState) routingTargetPresent(target *serving.SchedulerCandidate, snapshot serving.SchedulerSnapshot, physical []serving.InstanceState, requirePolicy bool, runtime *serving.Runtime) bool {
	if target == nil {
		return false
	}
	actual, _ := runtime.SchedulerRouting()
	positive := false
	for uid, weights := range target.Routing {
		if len(actual[uid]) != len(weights) {
			return false
		}
		for id, weight := range weights {
			positive = positive || weight > 0
			state := schedulerPhysicalState(id, physical)
			if state == nil {
				return false
			}
			if weight > 0 && !slices.ContainsFunc(target.Instances, func(wanted serving.SchedulerInstance) bool {
				return wanted.WorkloadUID == uid && (wanted.ID == id || wanted.ID == state.TargetID)
			}) {
				return false
			}
			resolved := state.ID
			if actual[uid][resolved] != weight {
				return false
			}
		}
	}
	for _, wanted := range target.Instances {
		if target.Routing[wanted.WorkloadUID][wanted.ID] <= 0 {
			continue
		}
		if !schedulerTargetReady(wanted, snapshot, physical, requirePolicy) {
			return false
		}
	}
	return positive
}
