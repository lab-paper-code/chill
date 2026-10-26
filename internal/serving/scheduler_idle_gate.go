package serving

import "slices"

// idleTransition checks the actual tentative DAG as idle cleanup must not bypass
// either horizon or reserve constraints after the search.
func (p *capacityPlanner) idleTransition(best capacityResult, removed map[string]bool) (string, []SchedulerTransitionReserve) {
	target := SchedulerCandidate{Nodes: slices.Clone(p.keep.Nodes), Routing: map[string]map[string]int32{}}
	for w, ws := range p.ws {
		target.Routing[ws.d.UID] = best.weights[w]
	}
	for n, j := range best.choice {
		plan := p.plans[n][j]
		target.Nodes[n].PowerMode = plan.mode
		for _, i := range plan.instances {
			if !removed[i.ID] {
				target.Instances = append(target.Instances, i)
			}
		}
	}
	actions := schedulerPlan(p.snapshot, target)
	ends := map[string]float64{}
	byID := map[string]Action{}
	for _, a := range actions {
		byID[a.ID] = a
	}
	var end func(string) float64
	reason := ""
	end = func(id string) float64 {
		if value, ok := ends[id]; ok {
			return value
		}
		a := byID[id]
		value := 0.0
		for _, dep := range a.DependsOn {
			value = max(value, end(dep))
		}
		if !a.Fixed && a.Status != schedulerActionComplete && a.Status != schedulerActionCancel {
			switch a.Kind {
			case schedulerActionPrepare, schedulerActionRemove, schedulerActionPower:
				_, longest, _, ok := capacityOperationCost(schedulerCost(p.snapshot.Costs, a.CostID))
				if !ok {
					reason = schedulerPlanMissingOperationCost
				}
				value += longest
			}
		}
		ends[id] = value
		return value
	}
	for _, a := range actions {
		if end(a.ID) > p.period {
			return schedulerTransitionHorizon, nil
		}
	}
	if reason != "" {
		return reason, nil
	}
	if p.settings.TransitionReserveFraction == 0 {
		return "", nil
	}
	records := []SchedulerTransitionReserve{}
	for _, ws := range p.ws {
		affected := false
		for _, old := range p.snapshot.Instances {
			affected = affected || removed[old.ID] && (old.WorkloadUID == ws.d.UID || old.OwnerWorkloadUID == ws.d.UID)
		}
		if !affected {
			continue
		}
		record := SchedulerTransitionReserve{UID: ws.d.UID, Fraction: p.settings.TransitionReserveFraction, Demand: ws.rate, Required: ws.rate * (1 + p.settings.TransitionReserveFraction)}
		for _, i := range target.Instances {
			if !p.reserveAvailable(i.ID, ws.d.UID) {
				continue
			}
			k, _ := p.currentKappa(i)
			record.AfterRoute += k
		}
		record.BeforeRoute = record.AfterRoute
		record.Allowed = record.AfterRoute >= record.Required
		if !record.Allowed {
			record.Reason = schedulerTransitionReserveInsufficient
			reason = record.Reason
		}
		records = append(records, record)
	}
	return reason, records
}
