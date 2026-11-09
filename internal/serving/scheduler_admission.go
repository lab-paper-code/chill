package serving

import (
	"context"
	"slices"
)

// admits enforces physical plan and route eligibility independently of SLO.
// Exact Better ranks infeasible improvements; retirement has no separate SLO veto.
func (p *capacityPlanner) admits(res *capacityResult) bool {
	if res.exclusion != "" || res.unroutedPrepare || res.unroutes || !p.bounded(res.choice) {
		return false
	}
	for n, j := range res.choice {
		if !p.plans[n][j].record.Eligible {
			return false
		}
	}
	return true
}

// SchedulerTargetAdmission checks NEW admission of a frozen, explicitly routed
// target against current native plans, exact feedback and normal postfilters.
// It does not search or mutate snapshot/target, strip reservations, or remap IDs.
// A reserved/in-progress target may be unavailable here; that result grants no
// continuation or cancellation authority over an already-authorized transition.
// Empty reason admits a new target. Context cancellation is an error.
func SchedulerTargetAdmission(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot, target SchedulerCandidate) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := settings.Validate(); err != nil {
		return err.Error(), nil
	}
	snapshot = schedulerCanonical(snapshot)
	if reasons := SchedulerContinuationReasons(settings, snapshot); len(reasons) > 0 {
		return reasons[0], nil
	}
	if reasons := schedulerValidateResources(settings, target.Nodes, target.Instances); len(reasons) > 0 {
		return reasons[0], nil
	}
	keep := SchedulerCandidate{ID: "keep", Keep: true, Nodes: schedulerCommonNodes(snapshot), Instances: slices.Clone(snapshot.Instances), Routing: schedulerInstanceRouting(ctx, snapshot.Workloads, snapshot.Instances)}
	for uid, routes := range snapshot.Routing {
		keep.Routing[uid] = routes
	}
	fixed := schedulerPlanState{snap: snapshot, target: &keep, actions: []Action{}, nodeTail: map[string]string{}, prepares: []string{}, stops: map[string]string{}}
	fixed.changeNodes(keep)
	keep.Actions = fixed.actions
	decision := SchedulerDecision{Search: schedulerSearchPopulation(ctx, settings, snapshot), Workloads: schedulerWorkloadStates(settings, snapshot)}
	fixSchedulerWorkloads(&decision, snapshot)
	p := newCapacityPlanner(ctx, settings, snapshot, keep, decision)
	p.fixCapacityWorkloads(&decision)
	if len(p.ws) == 0 {
		if schedulerAdmissionSameTarget(target, keep) {
			return "", nil
		}
		return "TargetWorkloadOutsideControl", nil
	}
	covered := decision.Search.WindowEnd.Sub(decision.Search.WindowStart)
	if covered < min(settings.ObservationWindow, settings.ControlInterval) {
		return schedulerPartialObservation, nil
	}
	p.partial = covered < settings.ObservationWindow
	if err := p.build(); err != nil {
		return "", err
	}
	choice := make([]int, len(p.plans))
	matched := 0
	for n, node := range p.snapshot.Nodes {
		wanted := SchedulerCandidate{Routing: target.Routing}
		at := slices.IndexFunc(target.Nodes, func(candidate SchedulerNode) bool { return candidate.Node == node.Node })
		if at < 0 {
			return "TargetNodeMissing", nil
		}
		wanted.Nodes = []SchedulerNode{target.Nodes[at]}
		for _, instance := range target.Instances {
			if instance.Node == node.Node {
				wanted.Instances = append(wanted.Instances, instance)
			}
		}
		matched += len(wanted.Instances)
		j := slices.IndexFunc(p.plans[n], func(plan *capacityPlan) bool {
			if !plan.record.Eligible || plan.mode != wanted.Nodes[0].PowerMode {
				return false
			}
			return schedulerAdmissionSameInstances(plan.instances, wanted.Instances)
		})
		if j < 0 {
			return "TargetPhysicalPlanUnavailable", nil
		}
		choice[n] = j
	}
	if matched != len(target.Instances) {
		return "TargetNodeUnavailable", nil
	}
	bridge, reason := p.bridgeSchedule(choice)
	if reason != "" {
		return reason, nil
	}
	route := p.routeOf(choice)
	if bridge != nil {
		route = bridge.route
	}
	energy := p.evaluateEnergyForRoutes(choice, route, bridge, p.period, target.Routing)
	if !schedulerAdmissionSameTarget(p.candidate(energy), target) {
		return "TargetRouteUnavailable", nil
	}
	result := p.assessResult(energy)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	result.keepJoules = p.keepWatts * p.period
	if !p.admits(&result) {
		if result.exclusion != "" {
			return result.exclusion, nil
		}
		return "TargetPhysicalAdmissionRejected", nil
	}
	actual := p.evaluateCurrentKeep(p.period)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if schedulerAdmissionSameTarget(target, p.candidate(actual)) {
		return "", nil
	}
	if !result.better(&actual) {
		return "TargetNoLongerPreferred", nil
	}
	filtered := p.minimumGain(p.payback(result))
	if !p.admits(&filtered) || !schedulerAdmissionSameTarget(p.candidate(filtered), target) {
		return "TargetPostFilterRejected", nil
	}
	return "", ctx.Err()
}

func schedulerAdmissionSameInstances(actual, wanted []SchedulerInstance) bool {
	if len(actual) != len(wanted) {
		return false
	}
	for _, target := range wanted {
		if !slices.ContainsFunc(actual, func(current SchedulerInstance) bool {
			return schedulerSamePhysicalTarget(current, target) && current.Allocation == target.Allocation &&
				current.WorkloadUID == target.WorkloadUID && current.Capacity == target.Capacity &&
				current.MaxBatch == target.MaxBatch && current.MaxWait == target.MaxWait && current.FixedBatchSize == target.FixedBatchSize
		}) {
			return false
		}
	}
	return true
}

func schedulerAdmissionSameTarget(actual, wanted SchedulerCandidate) bool {
	if !schedulerAdmissionSameInstances(actual.Instances, wanted.Instances) || len(actual.Nodes) != len(wanted.Nodes) {
		return false
	}
	for _, target := range wanted.Nodes {
		if !slices.ContainsFunc(actual.Nodes, func(current SchedulerNode) bool {
			return current.Node == target.Node && current.PowerMode == target.PowerMode
		}) {
			return false
		}
	}
	for uid, routes := range actual.Routing {
		for id, weight := range routes {
			if weight > 0 && wanted.Routing[uid][id] != weight {
				return false
			}
		}
	}
	for uid, routes := range wanted.Routing {
		for id, weight := range routes {
			if weight > 0 && actual.Routing[uid][id] != weight {
				return false
			}
		}
	}
	return true
}
