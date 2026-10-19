package serving

import (
	"context"
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
func ProjectSchedulerTransition(ctx context.Context, settings SchedulerSettings, observed SchedulerSnapshot, source, target SchedulerCandidate) (SchedulerTransitionProjection, error) {
	if err := ctx.Err(); err != nil {
		return SchedulerTransitionProjection{}, err
	}
	out := SchedulerTransitionProjection{Status: schedulerStatusPending}
	if err := settings.Validate(); err != nil {
		out.Reasons = []string{err.Error()}
		return out, ctx.Err()
	}
	if settings.ObservationWindow != settings.Horizon {
		out.Reasons = []string{"ProjectionRequiresOneCompletedReceiptInterval"}
		return out, ctx.Err()
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
		return out, ctx.Err()
	}
	return projectSchedulerTransition(ctx, settings, observed, source, target, sourceKey == targetKey, nil)
}

func projectSchedulerTransition(ctx context.Context, settings SchedulerSettings, observed SchedulerSnapshot, source, target SchedulerCandidate, diagonal bool, compared map[NodeReference]bool) (SchedulerTransitionProjection, error) {
	if err := ctx.Err(); err != nil {
		return SchedulerTransitionProjection{}, err
	}
	out := SchedulerTransitionProjection{Status: schedulerStatusPending}
	// Validate the observed inventory, receipts and frozen definitions, not the
	// actual transient queue or hypothetical native readiness timestamps.
	snap := observed
	snap.Instances, snap.Outstanding, snap.Actions = nil, nil, nil
	snap.InProgressTarget, snap.RoutingCredits = nil, nil
	out.Reasons = schedulerValidateSnapshot(settings, snap)
	var reasons []string
	source, reasons = schedulerProjectionCandidate(ctx, observed, source, nil)
	out.Reasons = appendUnique(out.Reasons, reasons...)
	target, reasons = schedulerProjectionCandidate(ctx, observed, target, source.Instances)
	out.Reasons = appendUnique(out.Reasons, reasons...)
	if err := ctx.Err(); err != nil {
		return SchedulerTransitionProjection{}, err
	}
	out.Reasons = appendUnique(out.Reasons, schedulerValidateResources(settings, source.Nodes, source.Instances)...)
	out.Reasons = appendUnique(out.Reasons, schedulerValidateResources(settings, target.Nodes, target.Instances)...)
	if len(out.Reasons) != 0 {
		out.Status = schedulerIneligibleStatus(out.Reasons)
		return out, ctx.Err()
	}
	snap.Nodes, snap.Instances, snap.Routing = source.Nodes, source.Instances, source.Routing
	if diagonal {
		target = source
		target.Keep = true
	} else {
		target.Actions = schedulerPlan(snap, target)
	}
	energy := 0.0
	for replay := range settings.Replays {
		r, err := schedulerReplayModel(ctx, settings, snap, target, rand.New(rand.NewPCG(uint64(settings.Seed), uint64(replay))), true, compared)
		if err != nil {
			return SchedulerTransitionProjection{}, err
		}
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
		return out, ctx.Err()
	}
	out.ExpectedJoules, out.TransitionComplete, out.Status = &energy, true, schedulerStatusFeasible
	return out, ctx.Err()
}

// Resource facts always come from the captured inventory. Only the power
// choice belongs to a candidate. Physical IDs are private replay slots;
// matching unchanged execution conditions reuse a slot, never actual progress.
func schedulerProjectionCandidate(ctx context.Context, observed SchedulerSnapshot, c SchedulerCandidate, source []SchedulerInstance) (SchedulerCandidate, []string) {
	nodes, reasons := schedulerProjectionNodes(ctx, observed.Nodes, c.Nodes)
	out := SchedulerCandidate{Nodes: nodes, Routing: map[string]map[string]int32{}}
	ids, used := map[string]string{}, map[string]bool{}
	for index, instance := range c.Instances {
		if ctx.Err() != nil {
			return out, reasons
		}
		oldID := instance.ID
		instance.ID = fmt.Sprintf("projection-target-%d", index)
		if source == nil {
			instance.ID = fmt.Sprintf("projection-source-%d", index)
		} else {
			for _, previous := range source {
				if ctx.Err() != nil {
					return out, reasons
				}
				if !used[previous.ID] && previous.Node == instance.Node && schedulerState(previous) == schedulerState(instance) {
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
			if ctx.Err() != nil {
				return out, reasons
			}
			matched := false
			for _, d := range observed.Workloads {
				if ctx.Err() != nil {
					return out, reasons
				}
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
	routes := schedulerInstanceRouting(ctx, observed.Workloads, c.Instances)
	if ctx.Err() != nil {
		return out, reasons
	}
	for uid, weights := range c.Routing {
		if ctx.Err() != nil {
			return out, reasons
		}
		routes[uid] = weights
	}
	for uid, weights := range routes {
		if ctx.Err() != nil {
			return out, reasons
		}
		out.Routing[uid] = map[string]int32{}
		for id, weight := range weights {
			if ctx.Err() != nil {
				return out, reasons
			}
			if weight < 0 {
				reasons = appendUnique(reasons, "ProjectionInvalidRoutingWeight:"+uid)
			}
			out.Routing[uid][ids[id]] = weight
		}
	}
	return out, reasons
}

func schedulerProjectionNodes(ctx context.Context, observed, choices []SchedulerNode) ([]SchedulerNode, []string) {
	nodes := append([]SchedulerNode(nil), observed...)
	reasons := []string{}
	if len(choices) != len(observed) {
		reasons = append(reasons, "ProjectionPoolInventoryMismatch")
	}
	for index, n := range nodes {
		if ctx.Err() != nil {
			return nodes, reasons
		}
		found := false
		for _, choice := range choices {
			if ctx.Err() != nil {
				return nodes, reasons
			}
			if choice.Node == n.Node {
				nodes[index].PowerMode = choice.PowerMode
				found = true
				break
			}
		}
		if !found {
			reasons = appendUnique(reasons, "ProjectionPoolInventoryMismatch")
		}
	}
	return nodes, reasons
}
