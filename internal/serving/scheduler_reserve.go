package serving

import "slices"

const schedulerTransitionHorizon = "TransitionExceedsHorizon"
const schedulerTransitionReserveInsufficient = "TransitionReserveInsufficient"

func (p *capacityPlanner) reserveAvailable(id, uid string) bool {
	return slices.ContainsFunc(p.snapshot.Instances, func(i SchedulerInstance) bool {
		if i.ID != id || !i.Ready || i.Stopped || i.WorkloadUID != uid || !i.Routable[uid] || schedulerReservedNode(p.snapshot, i.Node) {
			return false
		}
		return !slices.ContainsFunc(p.snapshot.Reservations, func(r SchedulerReservation) bool {
			return slices.Contains(r.Instances, id) || i.PodUID != "" && slices.Contains(r.PodUIDs, i.PodUID) || slices.Contains(r.WorkloadUIDs, uid)
		})
	})
}

func (p *capacityPlanner) transitionConstraints(choice []int, route float64) (string, []SchedulerTransitionReserve) {
	for n, j := range choice {
		if plan := p.plans[n][j]; !plan.keep && route+plan.postMax > p.period {
			return schedulerTransitionHorizon, nil
		}
	}
	records := p.transitionReserve(choice)
	for _, record := range records {
		if !record.Allowed {
			return schedulerTransitionReserveInsufficient, records
		}
	}
	return "", records
}

// SchedulerTransitionReserve records actual supply, not future target capacity.
type SchedulerTransitionReserve struct {
	UID          string  `json:"uid"`
	Fraction     float64 `json:"fraction"`
	Demand       float64 `json:"demand"`
	Required     float64 `json:"requiredKappa"`
	BeforeRoute  float64 `json:"beforeRouteKappa"`
	AfterRoute   float64 `json:"afterRouteKappa"`
	Allowed      bool    `json:"allowed"`
	Reason       string  `json:"reason,omitempty"`
	CandidateKey string  `json:"candidateKey,omitempty"`
}

// transitionReserve checks the minimum surviving actual supply before and after
// route. A planned prepare is not supply; reservations cannot collateralize twice.
func (p *capacityPlanner) transitionReserve(choice []int) []SchedulerTransitionReserve {
	f := p.settings.TransitionReserveFraction
	if f == 0 {
		return nil
	}
	records := make([]SchedulerTransitionReserve, 0, len(p.ws))
	for w, ws := range p.ws {
		affected := false
		for n, j := range choice {
			plan := p.plans[n][j]
			if plan.keep {
				continue
			}
			for _, old := range p.snapshot.Instances {
				if old.Node != p.snapshot.Nodes[n].Node || old.WorkloadUID != ws.d.UID {
					continue
				}
				if plan.mode != p.snapshot.Nodes[n].PowerMode || !slices.ContainsFunc(plan.instances, func(i SchedulerInstance) bool {
					return i.ID == old.ID && i.MaxBatch == old.MaxBatch && i.MaxWait == old.MaxWait
				}) {
					affected = true
				}
			}
		}
		if !affected {
			continue
		}
		demand, _ := p.reductionDemand(ws, choice)
		record := SchedulerTransitionReserve{UID: ws.d.UID, Fraction: f, Demand: demand.rate, Required: demand.rate * (1 + f)}
		for n, j := range choice {
			plan := p.plans[n][j]
			if schedulerReservedNode(p.snapshot, p.snapshot.Nodes[n].Node) {
				continue
			}
			for _, item := range plan.retained[w] {
				if p.reserveAvailable(item.id, ws.d.UID) {
					record.BeforeRoute += item.kappa
				}
			}
			if plan.mode == p.snapshot.Nodes[n].PowerMode {
				for _, item := range plan.items[w] {
					if !item.prepare && p.reserveAvailable(item.id, ws.d.UID) {
						record.AfterRoute += item.kappa
					}
				}
			}
		}
		record.Allowed = record.BeforeRoute >= record.Required && record.AfterRoute >= record.Required
		if !record.Allowed {
			record.Reason = schedulerTransitionReserveInsufficient
		}
		records = append(records, record)
	}
	return records
}

// reserveRejections keeps one reproducible rejected candidate per Workload,
// rather than retaining every evaluated combination.
func reserveRejections(dst map[string]SchedulerTransitionReserve, records []SchedulerTransitionReserve, key string) map[string]SchedulerTransitionReserve {
	for _, record := range records {
		if record.Allowed {
			continue
		}
		if key != "" {
			record.CandidateKey = key
		}
		if dst == nil {
			dst = map[string]SchedulerTransitionReserve{}
		}
		if old, exists := dst[record.UID]; !exists || record.CandidateKey < old.CandidateKey {
			dst[record.UID] = record
		}
	}
	return dst
}
