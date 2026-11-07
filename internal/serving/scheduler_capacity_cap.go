package serving

// capacityCapState describes the other nodes after the option move, including
// the move's retained supply and route time in demand (λ̃).
type capacityCapState struct {
	kappaOthers  float64
	kappaEOthers float64
	demand       float64
	overload     bool
	backlog      bool // B_w > 0; otherwise overload exceedance clamps at zero
}

type capacityCapSelection struct {
	candidates [2]int // table indices
	count      int
	preferred  int
}

// selectCaps uses §4.1's two endpoints. The entry's variants are eligible and
// sorted by κ, then original plan index. Empty and keep entries have one cap.
// In overload, positive backlog makes the target exceedance strictly decrease
// with κ. With no backlog, satisfying caps tie at zero and energy still matters.
func (t *planTable) selectCaps(entry capacityPlanEntry, s capacityCapState) capacityCapSelection {
	last := entry.variants[len(entry.variants)-1]
	out := capacityCapSelection{candidates: [2]int{last}, count: 1, preferred: last}
	if s.overload && s.backlog || entry.workload < 0 {
		return out
	}
	w := entry.workload
	minimum := -1
	for _, q := range entry.variants {
		if t.kappa[q][w] >= s.demand-s.kappaOthers {
			minimum = q
			break
		}
	}
	if minimum < 0 {
		return out
	}
	if minimum != last {
		out.candidates[1], out.count = minimum, 2
	}
	if s.kappaOthers > 0 && t.kappa[last][w] > 0 && t.kappaE[last][w]/t.kappa[last][w] > s.kappaEOthers/s.kappaOthers {
		out.preferred = minimum
	}
	return out
}
