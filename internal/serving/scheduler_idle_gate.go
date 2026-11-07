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

// reducesWorkload identifies removed supply or a non-increasing serving policy.
func (p *capacityPlanner) reducesWorkload(w capacityWorkload, choice []int) bool {
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
	return slow
}

// admissionDemand separates final-route margin from bridge/reserve demand.
func (p *capacityPlanner) admissionDemand(w capacityWorkload, choice []int, retained float64) (capacityWorkload, bool, bool, float64) {
	slow := p.reducesWorkload(w, choice)
	recovery := slow && p.keepInsufficient(w)
	for n, j := range choice {
		if retained < w.rate && p.plans[n][j].mode != p.snapshot.Nodes[n].PowerMode && slices.ContainsFunc(p.snapshot.Instances, func(i SchedulerInstance) bool {
			return i.Node == p.snapshot.Nodes[n].Node && i.WorkloadUID == w.d.UID && schedulerInstanceRouted(p.keep.Routing, i.ID)
		}) {
			// A stopping power outage without surviving supply is not a handoff.
			recovery = false
		}
	}
	protected := w.rate
	if slow && !recovery {
		protected *= 1 + p.settings.ScaleDownHeadroomFraction
	}
	return w, slow, recovery, protected
}
