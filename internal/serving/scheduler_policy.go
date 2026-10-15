package serving

import (
	"fmt"
	"math"
	"time"
)

// SchedulerWorkHistory stores relative projected work through one receipt cutoff.
// Initialize the actual logical key to zero at the declared start. An absent key
// is unevaluable (infinite work), never a free newly discovered configuration.
type SchedulerWorkHistory struct {
	Through time.Time          `json:"through"`
	Values  map[string]float64 `json:"values"`
}

// AdvanceSchedulerWorkHistory consumes exactly the next disjoint receipt interval.
// Kernels are directed source-to-destination costs over that entire interval, not
// overlapping prospective horizon costs. This is not a classical WFA guarantee.
func AdvanceSchedulerWorkHistory(previous SchedulerWorkHistory, from, to time.Time, kernels map[string]map[string]SchedulerTransitionProjection) (SchedulerWorkHistory, error) {
	if !from.Equal(previous.Through) || !to.After(from) {
		return previous, fmt.Errorf("work history requires the next nonempty interval starting at %s", previous.Through.Format(time.RFC3339Nano))
	}
	next := SchedulerWorkHistory{Through: to, Values: map[string]float64{}}
	minimum := math.Inf(1)
	for source, work := range previous.Values {
		if !schedulerFiniteWork(work) {
			return previous, fmt.Errorf("work history has invalid value for %q", source)
		}
		for destination, edge := range kernels[source] {
			if !edge.TransitionComplete || edge.Status != schedulerStatusFeasible || edge.ExpectedJoules == nil || !schedulerFiniteWork(*edge.ExpectedJoules) {
				continue
			}
			value := work + *edge.ExpectedJoules
			if !schedulerFiniteWork(value) {
				continue
			}
			if old, exists := next.Values[destination]; !exists || value < old {
				next.Values[destination] = value
				minimum = math.Min(minimum, value)
			}
		}
	}
	if len(next.Values) == 0 {
		return previous, fmt.Errorf("work history interval has no finite completed projected path")
	}
	for key, value := range next.Values {
		next.Values[key] = value - minimum
	}
	return next, nil
}

// SelectSchedulerWorkCandidate ranks actual SLO-feasible candidates by historical
// projected work plus actual prospective energy. History never buys an SLO
// violation. Without feasible candidates, retain the existing overload ordering.
// The returned string is a pending reason when no candidate can be selected.
func SelectSchedulerWorkCandidate(history SchedulerWorkHistory, snapshot SchedulerSnapshot, candidates []SchedulerCandidate) (int, string) {
	best, fallback := -1, -1
	feasible := false
	bestScore, bestEnergy, bestKey := 0.0, 0.0, ""
	for index, candidate := range candidates {
		// Match EvaluateScheduler's eligibility before using schedulerBetter.
		if candidate.ExpectedJoules == nil || candidate.Status == schedulerStatusLifetime || !schedulerFiniteWork(*candidate.ExpectedJoules) {
			continue
		}
		if fallback < 0 || schedulerBetter(candidate, candidates[fallback]) {
			fallback = index
		}
		if candidate.Status != schedulerStatusFeasible {
			continue
		}
		feasible = true
		key, err := SchedulerLogicalConfigKey(snapshot, candidate)
		if err != nil {
			continue
		}
		work, exists := history.Values[key]
		if !exists || !schedulerFiniteWork(work) {
			continue
		}
		energy := *candidate.ExpectedJoules
		score := work + energy
		if !schedulerFiniteWork(score) {
			continue
		}
		better := best < 0 || score < bestScore
		if best >= 0 && score == bestScore {
			if candidate.Keep != candidates[best].Keep {
				better = candidate.Keep
			} else if energy != bestEnergy {
				better = energy < bestEnergy
			} else {
				better = key < bestKey
			}
		}
		if better {
			best, bestScore, bestEnergy, bestKey = index, score, energy, key
		}
	}
	if feasible {
		if best < 0 {
			return -1, "Pending:NoActualSLOFeasibleCandidateWithFiniteWorkHistory"
		}
		return best, ""
	}
	if fallback < 0 {
		return -1, "Pending:NoFullyEvaluableCandidateWithinReportedBounds"
	}
	return fallback, ""
}

func schedulerFiniteWork(value float64) bool {
	return value >= 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}
