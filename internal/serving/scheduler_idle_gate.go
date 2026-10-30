package serving

import "slices"

const schedulerWindowCapacityInsufficient = "ScaleDownWindowCapacityInsufficient"
const schedulerTempoFast = "fast"

// capacityEnergyLoad keeps protection demand out of actual request joules.
func capacityEnergyLoad(load []float64, greedy bool, actual, demand float64) {
	if demand <= actual {
		return
	}
	left := actual
	for k := range load {
		if greedy {
			load[k] = min(load[k], left)
			left -= load[k]
		} else {
			load[k] *= actual / demand
		}
	}
}

// reductionDemand protects a slow candidate without changing its energy load.
func (p *capacityPlanner) reductionDemand(w capacityWorkload, choice []int) (capacityWorkload, bool) {
	slow := false
	for n, j := range choice {
		plan := p.plans[n][j]
		if plan.keep || plan.record.Tempo != schedulerTempoSlow {
			continue
		}
		oldKappa, policy := 0.0, false
		for _, old := range p.snapshot.Instances {
			if old.Node != p.snapshot.Nodes[n].Node || old.WorkloadUID != w.d.UID && old.OwnerWorkloadUID != w.d.UID {
				continue
			}
			k, _ := p.currentKappa(old)
			oldKappa += k
			at := slices.IndexFunc(plan.instances, func(i SchedulerInstance) bool { return i.ID == old.ID })
			if at < 0 || plan.mode != p.snapshot.Nodes[n].PowerMode {
				slow = true
			} else {
				i := plan.instances[at]
				policy = policy || i.MaxBatch != old.MaxBatch || i.MaxWait != old.MaxWait
			}
		}
		slow = slow || policy && plan.record.Kappa[w.d.UID] <= oldKappa
	}
	if slow {
		w.rate = max(w.rate, p.snapshot.ScaleDown[w.d.UID].WindowMax)
	}
	return w, slow
}

// idleTransition checks the actual tentative DAG as idle cleanup must not bypass
// either horizon or reserve constraints after the search.
func (p *capacityPlanner) idleTransition(best capacityResult, removed map[string]bool) (string, []SchedulerTransitionReserve) {
	target := SchedulerCandidate{Nodes: slices.Clone(p.keep.Nodes), Routing: map[string]map[string]int32{}}
	for w, ws := range p.ws {
		target.Routing[ws.d.UID] = best.weights[w]
	}
	for n, j := range best.choice {
		plan := p.plans[n][j]
		projected, known := target.Nodes[n].AtMode(plan.mode)
		if !known {
			return NodePlacementInventoryMissing, nil
		}
		target.Nodes[n] = projected
		for _, i := range plan.instances {
			if !removed[i.ID] {
				target.Instances = append(target.Instances, i)
			}
		}
	}
	if !p.idleWindowAllowed(best, removed) {
		return schedulerWindowCapacityInsufficient, nil
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
		if a.Status != schedulerActionComplete && a.Status != schedulerActionCancel {
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
		demand := max(ws.rate, p.snapshot.ScaleDown[ws.d.UID].WindowMax)
		record := SchedulerTransitionReserve{UID: ws.d.UID, Fraction: p.settings.TransitionReserveFraction, Demand: demand, Required: demand * (1 + p.settings.TransitionReserveFraction)}
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

func (p *capacityPlanner) idleWindowAllowed(best capacityResult, removed map[string]bool) bool {
	for w, ws := range p.ws {
		affected := slices.ContainsFunc(p.snapshot.Instances, func(i SchedulerInstance) bool {
			return removed[i.ID] && (i.WorkloadUID == ws.d.UID || i.OwnerWorkloadUID == ws.d.UID)
		})
		if !affected {
			continue
		}
		knew, units := 0.0, 0.0
		for id, weight := range best.weights[w] {
			if !removed[id] && weight > 0 {
				units += float64(weight)
			}
		}
		for n, j := range best.choice {
			for _, item := range p.plans[n][j].items[w] {
				if !removed[item.id] && best.weights[w][item.id] > 0 {
					knew += item.kappa
				}
			}
		}
		ws.rate = max(ws.rate, p.snapshot.ScaleDown[ws.d.UID].WindowMax) * (1 + p.settings.ScaleDownHeadroomFraction)
		for n, j := range best.choice {
			for _, item := range p.plans[n][j].items[w] {
				if weight := best.weights[w][item.id]; !removed[item.id] && weight > 0 &&
					(units == 0 || ws.rate*float64(weight)/units > item.kappa) {
					return false
				}
			}
		}
		if !capacityJudge(ws, knew, best.sums[w][1], knew > 0, best.route).satisfied {
			return false
		}
	}
	return true
}

// admissionDemand separates final-route margin from bridge/reserve demand.
func (p *capacityPlanner) admissionDemand(w capacityWorkload, choice []int, retained float64) (capacityWorkload, bool, bool, float64) {
	demand, slow := p.reductionDemand(w, choice)
	recovery := slow && p.keepInsufficient(w)
	for n, j := range choice {
		if retained < w.rate && p.plans[n][j].mode != p.snapshot.Nodes[n].PowerMode && slices.ContainsFunc(p.snapshot.Instances, func(i SchedulerInstance) bool {
			return i.Node == p.snapshot.Nodes[n].Node && i.WorkloadUID == w.d.UID && schedulerInstanceRouted(p.keep.Routing, i.ID)
		}) {
			// A stopping power outage without surviving supply is not a handoff.
			recovery = false
		}
	}
	if recovery {
		demand = w
	}
	protected := demand.rate
	if slow && !recovery {
		protected *= 1 + p.settings.ScaleDownHeadroomFraction
	}
	return demand, slow, recovery, protected
}

// timingReason grants only this UID's fully proved recovery a timing exception.
// Callers still enforce bridge, peak, reserve and physical observation safety.
func (p *capacityPlanner) timingReason(w capacityWorkload, slow, recovered, satisfied bool, routes map[string]int32) string {
	if slow && !satisfied {
		return schedulerWindowCapacityInsufficient
	}
	if recovered {
		return ""
	}
	if w.growing {
		if slow {
			return schedulerDemandIncreasing
		}
		for id, weight := range p.keep.Routing[w.d.UID] {
			if weight > 0 && routes[id] <= 0 && !slices.ContainsFunc(p.snapshot.Instances, func(i SchedulerInstance) bool {
				return i.ID == id && i.Terminated
			}) {
				return schedulerDemandIncreasing
			}
		}
	}
	if slow && !p.snapshot.ScaleDown[w.d.UID].Allowed {
		return schedulerScaleDownStabilizing
	}
	return ""
}
