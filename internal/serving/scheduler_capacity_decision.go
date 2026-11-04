package serving

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"
)

// decide turns the chosen plan set into the decision: keep, a route-only
// target, or a target whose actions come from schedulerPlan.
func (p *capacityPlanner) decide(decision SchedulerDecision, best capacityResult) SchedulerDecision {
	keepChoice := make([]int, len(p.plans))
	keepResult := p.evaluateAtWindow(keepChoice, 0, nil, best.period)
	keep := &decision.Keep
	keep.ExpectedJoules, keep.WorstExceedance, keep.Status = &keepResult.joules, capacityWorst(keepResult.worst), schedulerStatusFeasible
	if !keepResult.feasible {
		keep.Status = SchedulerStatusOverload
	}
	decision.Search.Examined = p.examined
	for n, compared := range p.compared {
		if compared {
			decision.Search.ComparedNodes = append(decision.Search.ComparedNodes, p.snapshot.Nodes[n].Node)
		}
	}
	search := p.searched
	record := &SchedulerCapacity{Period: time.Duration(best.period * 1e9), Route: time.Duration(best.route * 1e9), Search: &search, GainUncertaintyJoules: best.gainUncertainty}
	record.TransitionReserves = best.reserves
	for n, plans := range p.plans {
		for _, plan := range plans {
			record.Plans = append(record.Plans, p.projectPlan(plan, p.plans[n][best.choice[n]] == plan && p.admits(&best)))
		}
	}
	for w, ws := range p.ws {
		j := best.judgments[w]
		down := p.snapshot.ScaleDown[ws.d.UID]
		record.Workloads = append(record.Workloads, SchedulerCapacityWorkload{
			UID: ws.d.UID, Rate: ws.rate, MinimumDemand: p.settings.MinimumDemand[ws.d.UID], Queued: ws.queued, KappaNew: best.sums[w][0], KappaRetained: best.sums[w][1], Backlog: j.backlog,
			ScaleDown:   &down,
			WaitSeconds: j.wait, DrainSeconds: j.drain, FinalExceedance: j.final, TransitionExceedance: j.transition, Weights: best.weights[w], Rows: best.rows[w],
		})
	}
	decision.Capacity = record
	target := SchedulerCandidate{Nodes: slices.Clone(p.keep.Nodes), Routing: map[string]map[string]int32{}, ExpectedJoules: &best.joules, WorstExceedance: capacityWorst(best.worst), Status: schedulerStatusFeasible}
	for _, d := range p.snapshot.Workloads {
		if d == nil {
			continue
		}
		if w, controlled := p.index[d.UID]; controlled {
			target.Routing[d.UID] = best.weights[w]
		} else if routes := maps.Clone(p.keep.Routing[d.UID]); routes != nil {
			target.Routing[d.UID] = routes
		} else {
			target.Routing[d.UID] = map[string]int32{}
		}
	}
	removed := false
	idleRemoved := map[string]bool{}
	for n, j := range best.choice {
		plan := p.plans[n][j]
		if !plan.keep {
			target.Nodes[n].PowerMode = plan.mode
		}
		for _, i := range plan.instances {
			if reason := schedulerPhysicalChangeReason(p.snapshot, i.Node); reason != "" {
				decision.Reasons = appendUnique(decision.Reasons, fmt.Sprintf("%s:%s:%s", reason, i.Node.Name, i.Node.UID))
			}
			if best.bridge == nil && p.idleRemovable(i, best, target.Routing) {
				idleRemoved[i.ID] = true
				reason, reserves := p.idleTransition(best, idleRemoved)
				record.TransitionReserves = append(record.TransitionReserves, reserves...)
				if reason != "" {
					delete(idleRemoved, i.ID)
					target.Instances = append(target.Instances, i)
					decision.Reasons = appendUnique(decision.Reasons, reason)
					continue
				}
				decision.Reasons = append(decision.Reasons, schedulerIdleInstanceRemoved+":"+i.ID)
				removed = true
				continue
			}
			target.Instances = append(target.Instances, i)
		}
	}
	for _, node := range best.unpaid {
		decision.Reasons = append(decision.Reasons, schedulerReplacementUnpaid+":"+node)
	}
	for _, node := range best.dwelling {
		decision.Reasons = append(decision.Reasons, schedulerReplacementDwell+":"+node)
	}
	if best.withinUncertainty {
		decision.Reasons = append(decision.Reasons, schedulerGainWithinUncertainty)
	}
	if !best.feasible {
		target.Status = SchedulerStatusOverload
		decision.Reasons = append(decision.Reasons, schedulerNoPlanMeetsSLO)
		if best.weightsExceeded {
			decision.Reasons = append(decision.Reasons, schedulerRouteWeightExceeds)
		}
	}
	// A protected Workload keeps its serving routes when keep is the fallback.
	if best.keep && !removed && (p.sameRouting(target.Routing) || best.unroutes) {
		chosen := decision.Keep
		chosen.Status = target.Status
		decision.Target, decision.Status = &chosen, schedulerStatusKeep
		if !best.feasible {
			decision.Status = SchedulerStatusOverload
		}
		return decision
	}
	sum := sha256.Sum256([]byte(best.key + fmt.Sprint(target.Routing)))
	target.ID = "plan-" + hex.EncodeToString(sum[:6])
	target.Actions = best.bridge.actionsFor(p.settings, p.snapshot, target)
	if best.bridge != nil {
		record.BridgeStages = best.bridge.stages
	}
	decision.Target, decision.Status = &target, target.Status
	return decision
}

// idleRemovable reports an instance unrouted for one observation window that
// the chosen plan set leaves unrouted: owned by or serving a controlled
// Workload whose judgments still hold without its κ, or owned by nobody. The
// removal is for measurability and the lowest mode of an emptied Node, not
// energy.
func (p *capacityPlanner) idleRemovable(i SchedulerInstance, best capacityResult, routing map[string]map[string]int32) bool {
	if schedulerPhysicalChangeReason(p.snapshot, i.Node) != "" || !p.idle(i) || schedulerInstanceRouted(routing, i.ID) {
		return false
	}
	// Measurement and excluded Nodes keep their instances.
	if slices.ContainsFunc(p.snapshot.Nodes, func(n SchedulerNode) bool {
		return n.Node == i.Node && (n.Measurement || slices.Contains(n.Reasons, NodeExcluded))
	}) {
		return false
	}
	uid := i.WorkloadUID
	if uid == "" {
		uid = i.OwnerWorkloadUID
	}
	if uid == "" && !p.snapshot.EmptyNodeScaleDownAllowed || uid != "" && !p.snapshot.ScaleDown[uid].Allowed {
		return false
	}
	// An instance owned by nobody and routed by no Workload serves nobody.
	w, controlled := p.index[uid]
	if uid == "" || !controlled {
		return uid == ""
	}
	if !best.feasible || i.WorkloadUID == "" {
		return true
	}
	knew := best.sums[w][0]
	demand := p.ws[w]
	demand.rate = max(demand.rate, p.snapshot.ScaleDown[uid].WindowMax) * (1 + p.settings.ScaleDownHeadroomFraction)
	return capacityJudge(demand, knew, best.sums[w][1], knew > 0, best.route).satisfied
}

// sameRouting reports whether the controlled Workloads keep their current routes.
func (p *capacityPlanner) sameRouting(routing map[string]map[string]int32) bool {
	for _, w := range p.ws {
		current := maps.Clone(p.keep.Routing[w.d.UID])
		maps.DeleteFunc(current, func(_ string, weight int32) bool { return weight <= 0 })
		if !maps.Equal(current, routing[w.d.UID]) {
			return false
		}
	}
	return true
}

// capacityWorst is the recorded worst exceedance: never below zero, and the
// largest float for an unbounded one (no capacity), which JSON can carry.
func capacityWorst(worst float64) float64 {
	return min(max(0, worst), math.MaxFloat64)
}

func (p *capacityPlanner) projectPlan(plan *capacityPlan, admitted bool) SchedulerNodePlan {
	record := plan.record
	if len(record.RequiresRecovery) == 0 || admitted {
		return record
	}
	record.Eligible, record.Reason = false, schedulerScaleDownStabilizing
	for _, ws := range p.ws {
		if ws.growing && slices.Contains(record.RequiresRecovery, ws.d.UID) {
			record.Reason = schedulerDemandIncreasing
		}
	}
	return record
}
