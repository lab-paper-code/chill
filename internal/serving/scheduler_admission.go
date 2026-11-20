package serving

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
