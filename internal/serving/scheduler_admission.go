package serving

// admits applies the same SLO and horizon-gain judgment to retirement as to
// serving changes. Overload recovery may expand or replace, never just retire.
func (p *capacityPlanner) admits(res *capacityResult) bool {
	if res.exclusion != "" || res.unroutedPrepare || res.unroutes {
		return false
	}
	for n, j := range res.choice {
		if p.plans[n][j].retires {
			if !res.feasible {
				return false
			}
			// A feasible target repairing infeasible Keep is recovery, including
			// a replacement whose source and destination are on different Nodes.
			if !p.keepFeasible {
				return true
			}
			if res.keepJoules <= res.joules {
				return false
			}
			keep := capacityResult{choice: make([]int, len(p.plans))}
			return res.keepJoules-res.joules > p.gainBound(keep, *res)
		}
	}
	return true
}
