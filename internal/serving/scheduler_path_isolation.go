package serving

import (
	"math"
	"slices"
)

const (
	schedulerPathBudgetExceedsSLO  = "PathBudgetExceedsSLO"
	schedulerPathServiceExceedsSLO = "PathServiceExceedsSLO"
)

// pathInfeasibleReason requires a minimum-service proof for every compatible
// source variant. Search pruning, missing rows and grid zero are not proofs.
func (p *capacityPlanner) pathInfeasibleReason(w *Definition) string {
	budget := ResolvePathBudget(p.settings, p.snapshot.PathBudgets, w)
	if budget.TotalMS < 0 || math.IsNaN(budget.TotalMS) || math.IsInf(budget.TotalMS, 0) {
		return ""
	}
	found := false
	for _, option := range p.snapshot.Options {
		if !slices.Contains(option.CompatibleUIDs, w.UID) || !schedulerProfileMatches(w, option.Instance.Profile) {
			continue
		}
		variants := CapacityVariants(option)
		if len(variants) == 0 {
			return ""
		}
		for _, variant := range variants {
			key := NewCapacityInput(p.settings, p.snapshot.DispatchOccupancy, variant, w, budget).Key()
			row, ok := p.rows[key]
			if !ok || row.Status != CapacityBuilt || row.Kappa == nil || *row.Kappa != 0 || row.Reason != CapacityServiceExceedsSLO ||
				!(row.MinimumServiceNanos > 0) || math.IsInf(row.MinimumServiceNanos, 0) ||
				!(row.MinimumServiceNanos+budget.TotalMS*1e6 > float64(w.SLO.TargetLatency)) {
				return ""
			}
			found = true
		}
	}
	if !found {
		return ""
	}
	if budget.TotalMS*1e6 > float64(w.SLO.TargetLatency) {
		return schedulerPathBudgetExceedsSLO
	}
	return schedulerPathServiceExceedsSLO
}
