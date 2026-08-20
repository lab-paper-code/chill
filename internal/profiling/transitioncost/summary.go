package transitioncost

import (
	"errors"
	"fmt"
	"math"
	"time"

	"gonum.org/v1/gonum/stat"
)

// DirectedSummaryMethod is the fixed descriptive-statistics method used by
// this first directed transition-cost table.
const DirectedSummaryMethod = "arithmetic-mean-sample-standard-deviation-range-v1"

// DirectedStatePair is one ordered transition between two exact states.
type DirectedStatePair struct {
	Source StateIdentity `json:"source"`
	Target StateIdentity `json:"target"`
}

// DistributionSummary retains descriptive statistics for one measured
// quantity. SampleStandardDeviation uses the unbiased sample estimator.
type DistributionSummary struct {
	Mean                    float64 `json:"mean"`
	SampleStandardDeviation float64 `json:"sampleStandardDeviation"`
	Minimum                 float64 `json:"minimum"`
	Maximum                 float64 `json:"maximum"`
}

// VerdictCounts retains every attempted transition classification.
type VerdictCounts struct {
	Accepted     int `json:"accepted"`
	Invalid      int `json:"invalid"`
	Failed       int `json:"failed"`
	Insufficient int `json:"insufficient"`
}

// DirectedEntry is one exact directed state-pair row of the transition-cost
// table.
type DirectedEntry struct {
	SourceState StateIdentity `json:"sourceState"`
	TargetState StateIdentity `json:"targetState"`

	TargetIdleBaseline IdleBaseline  `json:"targetIdleBaseline"`
	AttemptCount       int           `json:"attemptCount"`
	VerdictCounts      VerdictCounts `json:"verdictCounts"`
	AcceptedCount      int           `json:"acceptedCount"`

	TauMilliseconds DistributionSummary `json:"tauMilliseconds"`
	EtaJoules       DistributionSummary `json:"etaJoules"`

	MaximumBoundaryMappingUncertainty time.Duration `json:"maximumBoundaryMappingUncertainty"`
	ServingSurvivedTrueCount          int           `json:"servingSurvivedTrueCount"`
	ServingSurvivedFalseCount         int           `json:"servingSurvivedFalseCount"`
}

// DirectedTableRecord is the stable factual projection consumed by a later
// evidence codec.
type DirectedTableRecord struct {
	SummaryMethod string          `json:"summaryMethod"`
	Entries       []DirectedEntry `json:"entries"`
}

// DirectedTable is an opaque, complete two-direction transition-cost table.
type DirectedTable struct {
	record DirectedTableRecord
}

// Record returns an isolated copy of the complete directed table.
func (t DirectedTable) Record() DirectedTableRecord {
	return cloneDirectedTableRecord(t.record)
}

// DirectionProgress exposes non-numeric progress when a complete table cannot
// be constructed.
type DirectionProgress struct {
	Direction     DirectedStatePair `json:"direction"`
	AttemptCount  int               `json:"attemptCount"`
	VerdictCounts VerdictCounts     `json:"verdictCounts"`
}

// InsufficientAcceptedCountError reports that at least one direction did not
// reach the caller's frozen accepted-count target. No partial table accompanies
// this error.
type InsufficientAcceptedCountError struct {
	AcceptedCountTarget int                 `json:"acceptedCountTarget"`
	Directions          []DirectionProgress `json:"directions"`
}

func (e *InsufficientAcceptedCountError) Error() string {
	if e == nil {
		return "transition-cost accepted count is insufficient"
	}
	return fmt.Sprintf(
		"transition-cost accepted count is insufficient: target=%d, directions=%d",
		e.AcceptedCountTarget,
		len(e.Directions),
	)
}

// Summarize constructs exactly two directed state-pair entries in the
// caller-provided order. Only accepted results contribute numeric values; all
// terminal results contribute attempt and verdict counts.
func Summarize(
	directions []DirectedStatePair,
	acceptedCountTarget int,
	results []FinalizedObservation,
) (DirectedTable, error) {
	frozenDirections, err := validateDirections(directions, acceptedCountTarget)
	if err != nil {
		return DirectedTable{}, err
	}

	accumulators := [2]directionAccumulator{
		{direction: frozenDirections[0]},
		{direction: frozenDirections[1]},
	}
	for index, result := range results {
		frozen := result.record.FrozenContract
		directionIndex := directionIndex(frozenDirections, DirectedStatePair{
			Source: frozen.SourceState,
			Target: frozen.TargetState,
		})
		if directionIndex < 0 {
			return DirectedTable{}, fmt.Errorf("transition result %d has an undeclared direction", index)
		}
		if err := accumulators[directionIndex].add(result); err != nil {
			return DirectedTable{}, fmt.Errorf("add transition result %d: %w", index, err)
		}
	}

	progress := make([]DirectionProgress, 2)
	insufficient := false
	for index := range accumulators {
		progress[index] = accumulators[index].progress()
		if progress[index].VerdictCounts.Accepted < acceptedCountTarget {
			insufficient = true
		}
	}
	if insufficient {
		return DirectedTable{}, &InsufficientAcceptedCountError{
			AcceptedCountTarget: acceptedCountTarget,
			Directions:          progress,
		}
	}

	entries := make([]DirectedEntry, 2)
	for index := range accumulators {
		entry, err := accumulators[index].entry()
		if err != nil {
			return DirectedTable{}, fmt.Errorf("summarize direction %d: %w", index, err)
		}
		entries[index] = entry
	}
	return DirectedTable{record: DirectedTableRecord{
		SummaryMethod: DirectedSummaryMethod,
		Entries:       entries,
	}}, nil
}

type directionAccumulator struct {
	direction             DirectedStatePair
	targetIdleBaseline    IdleBaseline
	hasTargetIdleBaseline bool
	verdictCounts         VerdictCounts
	tauMilliseconds       []float64
	etaJoules             []float64
	maximumUncertainty    time.Duration
	servingSurvivedTrue   int
	servingSurvivedFalse  int
}

func (a *directionAccumulator) add(result FinalizedObservation) error {
	baseline := result.record.FrozenContract.TargetIdleBaseline
	if !a.hasTargetIdleBaseline {
		a.targetIdleBaseline = baseline
		a.hasTargetIdleBaseline = true
	} else if baseline != a.targetIdleBaseline {
		return errors.New("target idle baseline changed within one direction")
	}

	switch result.record.Verdict {
	case VerdictAccepted:
		a.verdictCounts.Accepted++
		observation := result.record.AcceptedObservation
		a.tauMilliseconds = append(a.tauMilliseconds, observation.TauMilliseconds)
		a.etaJoules = append(a.etaJoules, observation.EtaJoules)
		uncertainty := time.Duration(observation.BoundaryMappingUncertaintyMilliseconds) * time.Millisecond
		if uncertainty > a.maximumUncertainty {
			a.maximumUncertainty = uncertainty
		}
		if observation.ServingSurvived {
			a.servingSurvivedTrue++
		} else {
			a.servingSurvivedFalse++
		}
	case VerdictInvalid:
		a.verdictCounts.Invalid++
	case VerdictFailed:
		a.verdictCounts.Failed++
	case VerdictInsufficient:
		a.verdictCounts.Insufficient++
	default:
		return errors.New("transition result has an unknown verdict")
	}
	return nil
}

func (a directionAccumulator) progress() DirectionProgress {
	return DirectionProgress{
		Direction:     a.direction,
		AttemptCount:  totalVerdicts(a.verdictCounts),
		VerdictCounts: a.verdictCounts,
	}
}

func (a directionAccumulator) entry() (DirectedEntry, error) {
	tau, err := summarizeDistribution(a.tauMilliseconds)
	if err != nil {
		return DirectedEntry{}, fmt.Errorf("summarize tau: %w", err)
	}
	eta, err := summarizeDistribution(a.etaJoules)
	if err != nil {
		return DirectedEntry{}, fmt.Errorf("summarize eta: %w", err)
	}
	return DirectedEntry{
		SourceState:                       a.direction.Source,
		TargetState:                       a.direction.Target,
		TargetIdleBaseline:                a.targetIdleBaseline,
		AttemptCount:                      totalVerdicts(a.verdictCounts),
		VerdictCounts:                     a.verdictCounts,
		AcceptedCount:                     a.verdictCounts.Accepted,
		TauMilliseconds:                   tau,
		EtaJoules:                         eta,
		MaximumBoundaryMappingUncertainty: a.maximumUncertainty,
		ServingSurvivedTrueCount:          a.servingSurvivedTrue,
		ServingSurvivedFalseCount:         a.servingSurvivedFalse,
	}, nil
}

func validateDirections(directions []DirectedStatePair, acceptedCountTarget int) ([2]DirectedStatePair, error) {
	var frozen [2]DirectedStatePair
	if len(directions) != len(frozen) {
		return frozen, errors.New("exactly two directed state pairs are required")
	}
	if acceptedCountTarget < 5 {
		return frozen, errors.New("accepted-count target must be at least five per direction")
	}
	copy(frozen[:], directions)
	for _, direction := range frozen {
		if !validState(direction.Source) || !validState(direction.Target) {
			return [2]DirectedStatePair{}, errors.New("directed state pair contains an invalid state identity")
		}
		if direction.Source.DeviceType != direction.Target.DeviceType ||
			direction.Source.Model != direction.Target.Model ||
			direction.Source.PowerMode == direction.Target.PowerMode {
			return [2]DirectedStatePair{}, errors.New(
				"each direction must join distinct power modes of one exact device type and model",
			)
		}
	}
	if frozen[1].Source != frozen[0].Target || frozen[1].Target != frozen[0].Source {
		return [2]DirectedStatePair{}, errors.New("the second direction must exactly reverse the first")
	}
	return frozen, nil
}

func directionIndex(directions [2]DirectedStatePair, candidate DirectedStatePair) int {
	for index, direction := range directions {
		if candidate == direction {
			return index
		}
	}
	return -1
}

func summarizeDistribution(values []float64) (DistributionSummary, error) {
	mean, standardDeviation := stat.MeanStdDev(values, nil)
	minimum, maximum := values[0], values[0]
	for _, value := range values[1:] {
		minimum = math.Min(minimum, value)
		maximum = math.Max(maximum, value)
	}
	if !finite(mean) || !finite(standardDeviation) || !finite(minimum) || !finite(maximum) {
		return DistributionSummary{}, errors.New("descriptive statistic is non-finite")
	}
	return DistributionSummary{
		Mean:                    mean,
		SampleStandardDeviation: standardDeviation,
		Minimum:                 minimum,
		Maximum:                 maximum,
	}, nil
}

func totalVerdicts(counts VerdictCounts) int {
	return counts.Accepted + counts.Invalid + counts.Failed + counts.Insufficient
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func cloneDirectedTableRecord(record DirectedTableRecord) DirectedTableRecord {
	clone := record
	clone.Entries = append([]DirectedEntry(nil), record.Entries...)
	return clone
}
