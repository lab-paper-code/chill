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
	for _, plans := range p.plans {
		for _, plan := range plans {
			record.Plans = append(record.Plans, plan.record)
		}
	}
	for w, ws := range p.ws {
		j := best.judgments[w]
		record.Workloads = append(record.Workloads, SchedulerCapacityWorkload{
			UID: ws.d.UID, Rate: ws.rate, MinimumDemand: p.settings.MinimumDemand[ws.d.UID], Queued: ws.queued, KappaNew: best.sums[w][0], KappaRetained: best.sums[w][1], Backlog: j.backlog,
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
	for n, j := range best.choice {
		plan := p.plans[n][j]
		if !plan.keep {
			target.Nodes[n].PowerMode = plan.mode
		}
		for _, i := range plan.instances {
			if reason := schedulerPhysicalChangeReason(p.snapshot, i.Node); reason != "" {
				decision.Reasons = appendUnique(decision.Reasons, fmt.Sprintf("%s:%s:%s", reason, i.Node.Name, i.Node.UID))
			}
			// The selected instances are exactly those scored by evaluate.
			target.Instances = append(target.Instances, i)
		}
	}
	for _, node := range best.unpaid {
		decision.Reasons = append(decision.Reasons, schedulerReplacementUnpaid+":"+node)
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
	if best.keep && (p.sameRouting(target.Routing) || best.unroutes) {
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

