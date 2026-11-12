package serving

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"
)

// decide turns the chosen plan set into the decision: keep, a route-only
// target, or a target whose actions come from schedulerPlan.
func (p *capacityPlanner) decide(decision SchedulerDecision, best capacityResult) SchedulerDecision {
	keepResult := p.evaluateCurrentKeep(best.period)
	keep := &decision.Keep
	keep.Feedback, keep.SLO, keep.ConditionalService = keepResult.feedback, keepResult.slo, keepResult.conditional
	keep.Assumptions, keep.Sources = keepResult.assumptions, keepResult.sources
	keep.ExpectedJoules, keep.WorstExceedance, keep.Status = &keepResult.joules, capacityWorst(keepResult.worst), schedulerStatusFeasible
	if !keepResult.feasible {
		keep.Status = SchedulerStatusOverload
	}
	if keepResult.exclusion != "" {
		keep.Status = schedulerStatusPending
		keep.Reasons = appendUnique(keep.Reasons, keepResult.exclusion)
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
	cellIndexes := map[string]int{}
	cellRefs := func(cells []SchedulerMissingEnergyCell) []int {
		var refs []int
		for _, cell := range cells {
			raw, _ := json.Marshal(cell)
			key := string(raw)
			index, exists := cellIndexes[key]
			if !exists {
				index = len(record.MissingCells)
				cellIndexes[key] = index
				record.MissingCells = append(record.MissingCells, cell)
			}
			if !slices.Contains(refs, index) {
				refs = append(refs, index)
			}
		}
		slices.Sort(refs)
		return refs
	}
	record.EnergyComparison.Status = "decidable"
	record.EnergyComparison.MissingCellRefs = cellRefs(best.missingCells)
	for _, candidate := range p.undecidable {
		record.UndecidableCandidates = append(record.UndecidableCandidates, SchedulerUndecidableCandidateRecord{
			Key: candidate.Key, MissingCellRefs: cellRefs(candidate.MissingCells), Excluded: candidate.Excluded, ReplaySkipped: candidate.ReplaySkipped,
		})
	}
	if len(best.missingCells) > 0 {
		record.EnergyComparison.Status = "undecidable"
	}
	if best.keep {
		if !slices.Contains(p.compared, true) {
			record.EnergyComparison.Status = "not-applicable"
		}
		for _, candidate := range p.undecidable {
			if candidate.Excluded {
				record.EnergyComparison.Status = "undecidable"
				for _, ref := range cellRefs(candidate.MissingCells) {
					if !slices.Contains(record.EnergyComparison.MissingCellRefs, ref) {
						record.EnergyComparison.MissingCellRefs = append(record.EnergyComparison.MissingCellRefs, ref)
					}
				}
			}
		}
	}
	slices.Sort(record.EnergyComparison.MissingCellRefs)
	_, beforePower := p.requestPowerTerms(keepResult)
	midPower, afterPower := p.requestPowerTerms(best)
	for n, plans := range p.plans {
		uid := p.snapshot.Nodes[n].Node.UID
		for j, plan := range plans {
			projected := plan.record
			projected.IdleCosts = maps.Clone(plan.record.IdleCosts)
			selected := j == 0 || j == best.choice[n]
			for phase, idle := range projected.IdleCosts {
				idle.RequestWatts, idle.RequestReferences = nil, nil
				if !selected {
					idle.PowerMode, idle.State = "", nil
				} else {
					power := beforePower[uid]
					if j == best.choice[n] {
						switch phase {
						case "mid":
							power = midPower[uid]
						case "after":
							power = afterPower[uid]
						}
						if best.bridge != nil && phase != "after" {
							start, end := 0.0, min(best.route, best.period)
							if !plan.keep {
								delay := min(best.bridge.delays[n], best.period)
								if phase == "before" {
									end = delay
								} else {
									start = min(delay+plan.preMean, best.period)
								}
							}
							if phase == "mid" || !plan.keep {
								power = p.bridgePhaseRequestPower(best, n, start, max(start, end))
							}
						}
					}
					watts := power.watts
					idle.RequestWatts, idle.RequestReferences = &watts, power.references
				}
				projected.IdleCosts[phase] = idle
			}
			if selected && len(projected.ActionCosts) > 0 {
				projected.ActionCosts = slices.Clone(projected.ActionCosts)
				candidate := SchedulerCandidate{Nodes: slices.Clone(p.keep.Nodes), Routing: p.keep.Routing}
				candidate.Nodes[n].PowerMode = plan.mode
				for _, instance := range p.snapshot.Instances {
					if instance.Node != plan.record.Node {
						candidate.Instances = append(candidate.Instances, instance)
					}
				}
				candidate.Instances = append(candidate.Instances, plan.instances...)
				for _, action := range schedulerPlan(p.settings, p.snapshot, candidate) {
					for k := range projected.ActionCosts {
						if projected.ActionCosts[k].Action == action.ID {
							seconds, _, joules := actionOperationCost(p.settings, p.snapshot.Costs, action)
							projected.ActionCosts[k].Seconds, projected.ActionCosts[k].Joules = &seconds, &joules
						}
					}
				}
			}
			record.Plans = append(record.Plans, projected)
		}
	}
	for w, ws := range p.ws {
		tests, keepTests := []SchedulerFeedbackRate{}, []SchedulerFeedbackRate{}
		if w < len(p.selectedRates) {
			tests = p.selectedRates[w]
		}
		if w < len(p.keepRates) {
			keepTests = p.keepRates[w]
		}
		record.Workloads = append(record.Workloads, SchedulerCapacityWorkload{
			UID: ws.d.UID, Rate: ws.rate, MinimumDemand: p.settings.MinimumDemand[ws.d.UID], Queued: ws.queued,
			RoutingRate: best.sums[w][0], RetainedRoutingRate: best.sums[w][1], Weights: best.weights[w], Tests: tests, KeepTests: keepTests,
		})
	}
	decision.Capacity = record
	target := SchedulerCandidate{Nodes: slices.Clone(p.keep.Nodes), Routing: map[string]map[string]int32{}, ExpectedJoules: &best.joules, WorstExceedance: capacityWorst(best.worst), Status: schedulerStatusFeasible}
	target.Feedback, target.SLO, target.ConditionalService = best.feedback, best.slo, best.conditional
	target.Assumptions, target.Sources = best.assumptions, best.sources
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

// testDecisionRates queries only keep/selected nominal positive routed shares.
// A failed test is evidence, never a second candidate admission gate.
func (p *capacityPlanner) testDecisionRates(best capacityResult) error {
	type queryKey struct {
		profile    *Profile
		node       NodeReference
		allocation SchedulerAllocation
		maxBatch   int
		maxWait    time.Duration
		fixedBatch int
		uid        string
		occupancy  time.Duration
		feedback   PathFeedback
		rate       float64
	}
	cache := map[queryKey]FeedbackCapacity{}
	test := func(candidate SchedulerCandidate) ([][]SchedulerFeedbackRate, error) {
		results := make([][]SchedulerFeedbackRate, len(p.ws))
		for w, ws := range p.ws {
			results[w] = []SchedulerFeedbackRate{}
			units := 0.0
			for _, weight := range candidate.Routing[ws.d.UID] {
				units += float64(max(0, weight))
			}
			if units == 0 || ws.rate == 0 {
				continue
			}
			for _, instance := range candidate.Instances {
				weight := candidate.Routing[ws.d.UID][instance.ID]
				if weight <= 0 || instance.Profile == nil {
					continue
				}
				rate := ws.rate * float64(weight) / units
				occupancy := ResolveDispatchOccupancy(p.settings, p.snapshot.DispatchOccupancy, instance)
				feedback := p.pathFeedback(ws.d)
				key := queryKey{instance.Profile, instance.Node, instance.Allocation, instance.MaxBatch, instance.MaxWait, instance.FixedBatchSize, ws.d.UID, occupancy.PerRequest, feedback, rate}
				value, found := cache[key]
				option := SchedulerOption{Instance: instance, CompatibleUIDs: []string{ws.d.UID}}
				if !found {
					var err error
					value, err = JudgeFeedbackRates(p.ctx, p.settings, option, ws.d, *occupancy, feedback, []float64{rate})
					if err != nil {
						return nil, err
					}
					cache[key] = value
				}
				input := NewCapacityInput(p.settings, p.snapshot.DispatchOccupancy, option, ws.d)
				input.CompletionMarginMS = feedback.PostNanos / 1e6
				results[w] = append(results[w], SchedulerFeedbackRate{InstanceID: instance.ID, Input: input, PathFeedback: feedback, FeedbackCapacity: value})
			}
		}
		return results, nil
	}
	var err error
	p.keepRates, err = test(p.keep)
	if err != nil {
		return err
	}
	p.selectedRates, err = test(p.candidate(best))
	return err
}
