package v2

import "github.com/lab-paper-code/chill/internal/scheduler/protocol"

// CapState describes the other nodes after the option move, including
// the move's retained supply and route time in demand (λ̃).
type CapState struct {
	KappaOthers  float64
	KappaEOthers float64
	Demand       float64
	Overload     bool
	Backlog      bool // B_w > 0; otherwise overload exceedance clamps at zero
}

type CapSelection struct {
	Candidates [2]int // table indices
	Count      int
	Preferred  int
}

// SelectCaps uses §4.1's two endpoints. The entry's variants are eligible and
// sorted by κ, then original plan index. Empty and keep entries have one cap.
// In overload, positive backlog makes the target exceedance strictly decrease
// with κ. With no backlog, satisfying caps tie at zero and energy still matters.
func SelectCaps(t *protocol.Table, entry protocol.Entry, s CapState) CapSelection {
	last := entry.Variants[len(entry.Variants)-1]
	out := CapSelection{Candidates: [2]int{last}, Count: 1, Preferred: last}
	if s.Overload && s.Backlog || entry.Workload < 0 {
		return out
	}
	w := entry.Workload
	minimum := -1
	for _, q := range entry.Variants {
		if t.Kappa[q][w] >= s.Demand-s.KappaOthers {
			minimum = q
			break
		}
	}
	if minimum < 0 {
		return out
	}
	if minimum != last {
		out.Candidates[1], out.Count = minimum, 2
	}
	if s.KappaOthers > 0 && t.Kappa[last][w] > 0 && t.KappaE[last][w]/t.Kappa[last][w] > s.KappaEOthers/s.KappaOthers {
		out.Preferred = minimum
	}
	return out
}
