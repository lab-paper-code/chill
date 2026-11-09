package serving

// initSearch records plan-local eligibility and physical workload effects.
func (t *planTable) initSearch(p *capacityPlanner) {
	if t.searchable != nil {
		return
	}
	t.searchable = make([]bool, len(t.index))
	t.keyParts = make([]string, len(t.index))
	t.excessIndex = make([]int, len(p.ws))
	assigned := make([]bool, len(p.ws))
	if p.settings.SLOPriorityStrategy == SLOPriorityWorkload {
		for _, uid := range p.settings.WorkloadPriority {
			if w, ok := p.index[uid]; ok {
				t.excessIndex[w] = t.priorityCount
				assigned[w] = true
				t.priorityCount++
			}
		}
	}
	next := t.priorityCount
	for w := range p.ws {
		if !assigned[w] {
			t.excessIndex[w] = next
			next++
		}
	}
	for q, n := range t.node {
		plan := p.plans[n][t.index[q]]
		t.keyParts[q] = p.snapshot.Nodes[n].Node.UID + "=" + plan.record.Key
		if n+1 < len(p.plans) {
			t.keyParts[q] += "|"
		}
		t.searchable[q] = t.eligible[q] && (t.keep[q] || p.compared[n])
	}
}

func (t *planTable) energy(q int, r, period float64) float64 {
	if t.keep[q] {
		return t.before[q] * period
	}
	// Preserve main's clamped durations and addition order bit for bit.
	return t.preJ[q] + t.mid[q]*max(0, min(r, period)-t.tauPre[q]) + t.postJ[q] + t.after[q]*max(0, period-r-t.tauPost[q])
}

// energyLowerBound minimizes each clamped duration separately over [low, high].
// With nonnegative watts, each term is <= its value at every route in the
// interval. Adding these minima cannot overestimate even across clamp bends.
func (t *planTable) energyLowerBound(q int, low, high, period float64) float64 {
	if t.keep[q] {
		return t.before[q] * period
	}
	return t.preJ[q] + t.mid[q]*max(0, min(low, period)-t.tauPre[q]) + t.postJ[q] + t.after[q]*max(0, period-high-t.tauPost[q])
}
