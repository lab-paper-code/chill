package serving

import "slices"

// SchedulerRebaseRouting combines one owner's optimizer weights with the
// currently applied route. Newer owners keep their committed shares; the owner
// takes its target share within that fence, and older/unowned destinations share
// the remainder in their applied ratio. A zero delta explicitly withdraws a
// touched destination. nil means no Ready composition can be applied.
func SchedulerRebaseRouting(current, delta map[string]int32, ready, newer map[string]bool) map[string]int32 {
	weights := make(map[string]int32, len(current)+len(delta))
	var fixed int64
	var own, older []string
	var ownTotal, olderTotal int64
	for id, weight := range current {
		if !ready[id] || weight <= 0 {
			continue
		}
		if newer[id] {
			weights[id] = weight
			fixed += int64(weight)
		} else if _, touched := delta[id]; !touched {
			weights[id] = weight
			older = append(older, id)
			olderTotal += int64(weight)
		}
	}
	for id, weight := range delta {
		if ready[id] && !newer[id] && weight > 0 {
			weights[id] = weight
			own = append(own, id)
			ownTotal += int64(weight)
		}
	}
	budget := int64(schedulerRouteUnits) - fixed
	ownBudget := min(ownTotal, budget)
	if olderTotal == 0 && ownTotal > 0 {
		ownBudget = budget // Same rule as the existing partial-Ready subset.
	}
	if olderTotal == 0 && ownTotal == 0 && budget != 0 {
		return nil
	}
	normalize := func(ids []string, total, share int64) {
		slices.Sort(ids)
		var cumulative, assigned int64
		for _, id := range ids {
			cumulative += int64(weights[id])
			next := cumulative * share / total
			weights[id] = int32(next - assigned)
			assigned = next
			if weights[id] == 0 {
				delete(weights, id)
			}
		}
	}
	normalize(own, ownTotal, ownBudget)
	normalize(older, olderTotal, budget-ownBudget)
	return weights
}
