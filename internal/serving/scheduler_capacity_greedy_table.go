package serving

import "slices"

// initSearch folds only plan-local timing exclusions. Set-dependent timing,
// reserves and bridge scheduling remain the exact funnel's responsibility.
func (t *planTable) initSearch(p *capacityPlanner) {
	if t.searchable != nil {
		return
	}
	t.searchable = make([]bool, len(t.index))
	t.slowWorkload, t.outage = make([][]bool, len(t.index)), make([][]bool, len(t.index))
	t.keyParts = make([]string, len(t.index))
	t.keepInsufficient = make([]bool, len(p.ws))
	t.unsearchable = map[string]int{}
	for w, ws := range p.ws {
		t.keepInsufficient[w] = p.keepInsufficient(ws)
	}
	choice := make([]int, len(p.plans))
	for q, n := range t.node {
		plan := p.plans[n][t.index[q]]
		t.keyParts[q] = p.snapshot.Nodes[n].Node.UID + "=" + plan.record.Key
		if n+1 < len(p.plans) {
			t.keyParts[q] += "|"
		}
		t.slowWorkload[q], t.outage[q] = make([]bool, len(p.ws)), make([]bool, len(p.ws))
		choice[n] = t.index[q]
		reason := ""
		for w, ws := range p.ws {
			_, slow := p.reductionDemand(ws, choice)
			t.slowWorkload[q][w] = slow
			t.outage[q][w] = plan.mode != p.snapshot.Nodes[n].PowerMode && slices.ContainsFunc(p.snapshot.Instances, func(i SchedulerInstance) bool {
				return i.Node == p.snapshot.Nodes[n].Node && i.WorkloadUID == ws.d.UID && schedulerInstanceRouted(p.keep.Routing, i.ID)
			})
			if !t.keepInsufficient[w] && (slices.Contains(t.recovery[q], ws.d.UID) || slow && (ws.growing || !p.snapshot.ScaleDown[ws.d.UID].Allowed)) {
				if ws.growing {
					reason = schedulerDemandIncreasing
				} else if reason == "" {
					reason = schedulerScaleDownStabilizing
				}
			}
		}
		choice[n] = 0
		t.searchable[q] = t.eligible[q] && t.dominated[q] == "" && reason == ""
		if reason != "" && t.eligible[q] && t.dominated[q] == "" {
			t.unsearchable[reason]++
		}
	}
}

func (t *planTable) energy(q int, r, period float64) float64 {
	if t.keep[q] {
		return t.before[q] * period
	}
	// Do not combine preJ+postJ or clamp either duration: v1 does neither.
	return t.preJ[q] + t.mid[q]*(r-t.tauPre[q]) + t.postJ[q] + t.after[q]*(period-r-t.tauPost[q])
}

func (t *planTable) sameWorkload(a, b, w int) bool {
	return t.slowWorkload[a][w] == t.slowWorkload[b][w] && t.outage[a][w] == t.outage[b][w] &&
		slices.Equal(t.items[a][w], t.items[b][w]) && slices.Equal(t.retained[a][w], t.retained[b][w])
}
