package state

import (
	serving "github.com/lab-paper-code/chill/internal/serving"
	slices "slices"
)

func (c *ReservationState) ActionEffectPresent(snapshot serving.SchedulerSnapshot, run *ActionRun, runtime *serving.Runtime) bool {
	if run.result == nil || run.result.Status != NativeOperationComplete {
		return false
	}
	a := run.action
	physical := runtime.Instances()
	switch a.Kind {
	case OperationPrepare:
		p := run.result.Operation.Prepare
		if p == nil || p.Instance.Profile == nil {
			return false
		}
		for _, i := range physical {
			if (i.ID == a.Instance || i.TargetID == a.Instance) && !i.Terminated && i.Node == a.Node && i.Profile == p.Instance.Profile.ID && i.Native != nil && i.Native.Ready {
				return true
			}
		}
	case OperationRemove:
		for _, i := range physical {
			if i.ID == a.Instance {
				return false
			}
		}
		return true
	case OperationStop:
		for _, i := range physical {
			if i.ID == a.Instance {
				return i.Stopped
			}
		}
		return true
	case OperationDrain:
		for _, r := range snapshot.Outstanding {
			if r.Instance == a.Instance {
				return false
			}
		}
		return true
	case OperationPower:
		for _, n := range snapshot.Nodes {
			if n.Node == a.Node {
				return n.PowerMode == a.To
			}
		}
	case OperationRoute:
		return c.RoutingTargetPresent(ControlledTarget(run.result.Operation), snapshot, physical, true, runtime)
	}
	return false
}

// A failed observation is neither an effect proof nor a counterexample. Keep
// the completed receipt and its ownership until the same source is observable.
func (c *ReservationState) actionEffectUnknown(snapshot serving.SchedulerSnapshot, run *ActionRun, runtime *serving.Runtime) bool {
	if run.result == nil {
		return false
	}
	if run.action.Kind == OperationPower {
		return slices.ContainsFunc(snapshot.Nodes, func(node serving.SchedulerNode) bool {
			return node.Node == run.action.Node && node.PowerMode == ""
		})
	}
	physical := runtime.Instances()
	if run.action.Kind == OperationPrepare {
		return slices.ContainsFunc(physical, func(instance serving.InstanceState) bool {
			return (instance.ID == run.action.Instance || instance.TargetID == run.action.Instance) &&
				instance.Node == run.action.Node && !instance.Stopped && !instance.Ready
		})
	}
	if run.action.Kind == OperationRoute && run.result.Operation.Target != nil {
		for _, wanted := range run.result.Operation.Target.Instances {
			if !run.result.Operation.ControlsWorkload(wanted.WorkloadUID) {
				continue
			}
			if state := SchedulerPhysicalState(wanted.ID, physical); state != nil && !state.Stopped &&
				(!state.Ready || state.Reason == "PolicyAckPending") {
				return true
			}
		}
	}
	return false
}

func (c *ReservationState) RoutingTargetPresent(target *serving.SchedulerCandidate, snapshot serving.SchedulerSnapshot, physical []serving.InstanceState, requirePolicy bool, runtime *serving.Runtime) bool {
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
			state := SchedulerPhysicalState(id, physical)
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
