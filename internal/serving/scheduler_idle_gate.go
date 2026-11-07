package serving

import "slices"

const schedulerReductionCapacityInsufficient = "ReductionCapacityInsufficient"

// capacityEnergyLoad keeps protection demand out of actual request joules.
func capacityEnergyLoad(load []float64, actual, demand float64) {
	if demand <= actual {
		return
	}
	for k := range load {
		load[k] *= actual / demand
	}
}

// reductionDemand uses current effective demand and identifies removed supply.
func (p *capacityPlanner) reductionDemand(w capacityWorkload, choice []int) (capacityWorkload, bool) {
	slow := false
	for n, j := range choice {
		plan := p.plans[n][j]
		if plan.keep || !plan.reduces {
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
	return w, slow
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
	protected := demand.rate
	if slow && !recovery {
		protected *= 1 + p.settings.ScaleDownHeadroomFraction
	}
	return demand, slow, recovery, protected
}

