package serving

import (
	"fmt"
	"math/rand/v2"
)

// SchedulerTransitionProjection is a measured counterfactual whole-pool cost
// over one completed receipt interval, not an actual execution/SLO decision.
type SchedulerTransitionProjection struct {
	ExpectedJoules     *float64 `json:"expectedJoules"`
	TransitionComplete bool     `json:"transitionComplete"`
	Status             string   `json:"status"`
	Reasons            []string `json:"reasons"`
	References         []string `json:"references"`
}

// ProjectSchedulerTransition installs a settled hypothetical source and reuses
// the real planner and energy replay for its directed transition to target.
// ObservationWindow must equal Horizon: the historical receipts are used once.
func ProjectSchedulerTransition(settings SchedulerSettings, observed SchedulerSnapshot, source, target SchedulerCandidate) SchedulerTransitionProjection {
	out := SchedulerTransitionProjection{Status: schedulerStatusPending}
	if err := settings.Validate(); err != nil {
		out.Reasons = []string{err.Error()}
		return out
	}
	if settings.ObservationWindow != settings.Horizon {
		out.Reasons = []string{"ProjectionRequiresOneCompletedReceiptInterval"}
		return out
	}
	sourceKey, sourceErr := SchedulerLogicalConfigKey(observed, source)
	targetKey, targetErr := SchedulerLogicalConfigKey(observed, target)
	if sourceErr != nil || targetErr != nil {
		if sourceErr != nil {
			out.Reasons = append(out.Reasons, "InvalidProjectionSource:"+sourceErr.Error())
		}
		if targetErr != nil {
			out.Reasons = append(out.Reasons, "InvalidProjectionTarget:"+targetErr.Error())
		}
		return out
	}
	// Validate the observed inventory, receipts and frozen definitions, not the
	// actual transient queue or hypothetical native readiness timestamps.
	snap := observed
	snap.Instances, snap.Outstanding, snap.Actions = nil, nil, nil
	snap.InProgressTarget, snap.RoutingCredits = nil, nil
	out.Reasons = schedulerValidateSnapshot(settings, snap)
	var reasons []string
	source, reasons = schedulerProjectionCandidate(observed, source, nil)
	out.Reasons = appendUnique(out.Reasons, reasons...)
	target, reasons = schedulerProjectionCandidate(observed, target, source.Instances)
	out.Reasons = appendUnique(out.Reasons, reasons...)
	out.Reasons = appendUnique(out.Reasons, schedulerValidateResources(settings, source.Nodes, source.Instances)...)
	out.Reasons = appendUnique(out.Reasons, schedulerValidateResources(settings, target.Nodes, target.Instances)...)
	if len(out.Reasons) != 0 {
		out.Status = schedulerIneligibleStatus(out.Reasons)
		return out
	}
	snap.Nodes, snap.Instances, snap.Routing = source.Nodes, source.Instances, source.Routing
	if sourceKey == targetKey {
		target = source
		target.Keep = true
	} else {
		target.Actions = schedulerPlan(snap, target)
	}
	energy := 0.0
	for replay := range settings.Replays {
		r := schedulerReplayModel(settings, snap, target, rand.New(rand.NewPCG(uint64(settings.Seed), uint64(replay))), true)
		out.References = appendUnique(out.References, r.sources...)
		out.Reasons = appendUnique(out.Reasons, r.reasons...)
		if len(r.reasons) != 0 {
			break
		}
		if !r.transitionComplete {
			out.Reasons = appendUnique(out.Reasons, "ProjectionTransitionExceedsInterval")
			break
		}
		if !schedulerFinite(r.energy) || r.energy < 0 {
			out.Reasons = appendUnique(out.Reasons, "ExpectedEnergyOutsidePhysicalNumericalRange")
			break
		}
		energy += (r.energy - energy) / float64(replay+1)
	}
	if len(out.Reasons) != 0 {
		out.Status = schedulerIneligibleStatus(out.Reasons)
		return out
	}
	out.ExpectedJoules, out.TransitionComplete, out.Status = &energy, true, schedulerStatusFeasible
	return out
}

// Resource facts always come from the captured inventory. Only the logical pool
// and power choice belong to a candidate. Physical IDs are private replay slots;
// matching unchanged execution conditions reuse a slot, never actual progress.
func schedulerProjectionCandidate(observed SchedulerSnapshot, c SchedulerCandidate, source []SchedulerInstance) (SchedulerCandidate, []string) {
	out := SchedulerCandidate{Nodes: append([]SchedulerNode(nil), observed.Nodes...), Routing: map[string]map[string]int32{}}
	reasons := []string{}
	if len(c.Nodes) != len(observed.Nodes) {
		reasons = append(reasons, "ProjectionPoolInventoryMismatch")
	}
	for index, n := range out.Nodes {
		found := false
		for _, choice := range c.Nodes {
			if choice.Node == n.Node {
				out.Nodes[index].Pool, out.Nodes[index].PowerMode = choice.Pool, choice.PowerMode
				found = true
				break
			}
		}
		if !found {
			reasons = appendUnique(reasons, "ProjectionPoolInventoryMismatch")
		}
	}
	ids, used := map[string]string{}, map[string]bool{}
	for index, instance := range c.Instances {
		oldID := instance.ID
		instance.ID = fmt.Sprintf("projection-target-%d", index)
		if source == nil {
			instance.ID = fmt.Sprintf("projection-source-%d", index)
		} else {
			for _, previous := range source {
				if !used[previous.ID] && previous.Node == instance.Node && previous.Pool == instance.Pool && schedulerState(previous) == schedulerState(instance) {
					instance.ID = previous.ID
					used[previous.ID] = true
					break
				}
			}
		}
		ids[oldID] = instance.ID
		instance.TargetID, instance.OwnerWorkloadUID = "", ""
		instance.Ready, instance.Stopped, instance.ArrivalAges = true, false, nil
		for _, uid := range instance.WorkloadUIDs {
			matched := false
			for _, d := range observed.Workloads {
				if d != nil && d.UID == uid && schedulerProfileMatches(d, instance.Profile) {
					matched = true
				}
			}
			if !matched {
				reasons = appendUnique(reasons, "ProjectionProfileDefinitionMismatch:"+uid)
			}
		}
		out.Instances = append(out.Instances, instance)
	}
	// Make all default routes explicit before replacing physical identifiers.
	routes := schedulerInstanceRouting(observed.Workloads, c.Instances)
	for uid, weights := range c.Routing {
		routes[uid] = weights
	}
	for uid, weights := range routes {
		out.Routing[uid] = map[string]int32{}
		for id, weight := range weights {
			if weight < 0 {
				reasons = appendUnique(reasons, "ProjectionInvalidRoutingWeight:"+uid)
			}
			out.Routing[uid][ids[id]] = weight
		}
	}
	return out, reasons
}
