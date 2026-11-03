package serving

import (
	"context"
	"sort"
	"time"
)

const (
	MaximumPowerSeriesRange = time.Hour
	MaximumPowerBuckets     = 900
	powerSeriesInvalidQuery = "InvalidQuery"
)

type OperatingPowerSeries struct {
	From        time.Time              `json:"from"`
	To          time.Time              `json:"to"`
	BucketNanos time.Duration          `json:"bucketNanos"`
	Buckets     []OperatingPowerWindow `json:"buckets"`
}

// PowerBucketCount validates before either reading history or allocating reducers.
// Boundaries are UTC duration multiples, with the first/last bucket clipped to
// the requested range. Actual observations belong to [from,to), not both sides.
func PowerBucketCount(from, to time.Time, bucket time.Duration) (int, error) {
	if from.IsZero() || !from.Before(to) || to.Sub(from) > MaximumPowerSeriesRange || bucket < time.Second || bucket > MaximumPowerSeriesRange {
		return 0, failure(powerSeriesInvalidQuery, "power series requires a positive from/to range of at most 1h and bucket between 1s and 1h")
	}
	span := to.Sub(from.UTC().Truncate(bucket))
	count := int(span / bucket)
	if span%bucket != 0 {
		count++
	}
	if count > MaximumPowerBuckets {
		return 0, failure(powerSeriesInvalidQuery, "power series exceeds 900 buckets")
	}
	return count, nil
}

// OperatingPowerSeries shares one committed read and the period reducer's meter,
// coverage, trapezoid integration and physical-membership semantics.
func (l *ObservationLog) OperatingPowerSeries(ctx context.Context, from, to time.Time, bucket time.Duration, meters []OperatingPowerMeter, limit int) (OperatingPowerSeries, error) {
	count, err := PowerBucketCount(from, to, bucket)
	result := OperatingPowerSeries{From: from, To: to, BucketNanos: bucket, Buckets: []OperatingPowerWindow{}}
	if err != nil {
		return result, err
	}
	if limit <= 0 {
		return result, failure(powerSeriesInvalidQuery, "positive observation bound is required")
	}
	start := from.UTC().Truncate(bucket)
	reducers := make([]*operatingPowerReducer, count)
	for i := range reducers {
		left := start.Add(time.Duration(i) * bucket)
		reducers[i] = newOperatingPowerReducer(maxTime(from, left), minTime(to, left.Add(bucket)), meters, limit)
	}
	// Bound the entire query, not just each individual bucket.
	whole := newOperatingPowerReducer(from, to, meters, limit)
	coverage, err := l.readOperatingPower(ctx, from, to, func(records func(func(*SchedulerObservation) error) error) error {
		return records(func(event *SchedulerObservation) error {
			if err := whole.add(event); err != nil {
				return err
			}
			first, last := count, 0
			if event.Power != nil {
				left, right := event.Power.StartedAt.Add(-time.Minute), event.Power.EndedAt.Add(time.Minute)
				first = sort.Search(count, func(i int) bool { return !reducers[i].result.To.Before(left) })
				last = sort.Search(count, func(i int) bool { return reducers[i].result.From.After(right) })
			}
			if event.Snapshot != nil {
				first = min(first, sort.Search(count, func(i int) bool { return reducers[i].result.To.After(event.Snapshot.At) }))
				last = count
			}
			if first >= last {
				return nil
			}
			for _, reducer := range reducers[first:last] {
				if err := reducer.add(event); err != nil {
					return err
				}
			}
			return nil
		})
	})
	if err != nil {
		return result, err
	}
	whole.matched = mergeOperatingNeighbors(&whole.result, whole.history, whole.matched, limit)
	if whole.matched > limit {
		return result, failure(powerSeriesInvalidQuery, "power series raw observation count exceeds query bound")
	}
	result.Buckets = make([]OperatingPowerWindow, count)
	for i, reducer := range reducers {
		window := reducer.finish()
		for n := range window.Nodes {
			node := &window.Nodes[n]
			observed := 0
			for at := range reducer.history[n].samples {
				if at.Before(window.From) || !at.Before(window.To) {
					continue
				}
				observed++
				if node.FirstObservedAt == nil || at.Before(*node.FirstObservedAt) {
					first := at
					node.FirstObservedAt = &first
				}
				if node.LastObservedAt == nil || at.After(*node.LastObservedAt) {
					last := at
					node.LastObservedAt = &last
				}
			}
			node.ObservedSamples = &observed
			if observed == 0 {
				node.Joules, node.MeanWatts = nil, nil
				node.MeterJoules, node.MeterComplete = nil, false
				node.Reasons = append(node.Reasons, "no observed samples in bucket")
				window.WholePoolJoules, window.WholePoolMeanWatts = nil, nil
				window.ActivePoolJoules, window.ActivePoolMeanWatts = nil, nil
				window.Complete = false
				window.Reasons = appendUnique(window.Reasons, "no observed samples in bucket for Node "+node.Node.UID)
			}
		}
		applyOperatingCoverage(&window, coverage)
		result.Buckets[i] = window
	}
	return result, nil
}
