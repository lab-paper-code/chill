package firstfloor

import (
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
)

// ConditionalServiceModel identifies the implemented physical-trial mixture.
const ConditionalServiceModel = "nearest-gap-uniform-physical-trial"

// ConditionalSupport describes the selected trial mixture, not a tail-coverage claim.
// DistanceNanos is the greatest nearest-record distance among eligible trials;
// OutsideSupport reports a query outside any eligible trial's observed gap range.
type ConditionalSupport struct {
	DistanceNanos  int64 `json:"distanceNanos"`
	TrialCount     int   `json:"trialCount"`
	OutsideSupport bool  `json:"outsideSupport"`
}

type serviceGroup struct {
	gap     int64
	samples []ServiceSample // sorted by service duration once, at hydration
}
type serviceTrial struct {
	startup []ServiceSample
	groups  []serviceGroup
}

type serviceNeighbors struct{ a, b []ServiceSample }

// ServiceDistribution is immutable after hydration. Quantile queries use local
// scratch; up to 32 physical trials need no heap-backed scratch.
type ServiceDistribution struct {
	trials []serviceTrial
}

func NewServiceDistribution(samples []ServiceSample) (*ServiceDistribution, error) {
	if len(samples) == 0 {
		return nil, errors.New("paired service samples required")
	}
	d := &ServiceDistribution{}
	byTrial := make(map[string][]ServiceSample)
	trialIDs := make([]string, 0)
	for _, s := range samples {
		if s.ServiceDurationNanos <= 0 || !identity(s.TrialID) || s.PrecedingIdleNanos != nil && *s.PrecedingIdleNanos < 0 {
			return nil, errors.New("positive service, nonnegative actual idle and physical trial ID required")
		}
		// Own the pointer values as well as the rows; callers may retain raw evidence.
		s = s.Clone()
		if _, ok := byTrial[s.TrialID]; !ok {
			trialIDs = append(trialIDs, s.TrialID)
		}
		byTrial[s.TrialID] = append(byTrial[s.TrialID], s)
	}
	sort.Strings(trialIDs)
	for _, id := range trialIDs {
		rows := byTrial[id]
		sort.Slice(rows, func(i, j int) bool {
			a, b := rows[i].PrecedingIdleNanos, rows[j].PrecedingIdleNanos
			if a == nil || b == nil {
				return a == nil && b != nil
			}
			if *a != *b {
				return *a < *b
			}
			return rows[i].ServiceDurationNanos < rows[j].ServiceDurationNanos
		})
		t := serviceTrial{}
		for i := 0; i < len(rows); {
			j := i + 1
			if rows[i].PrecedingIdleNanos == nil {
				for j < len(rows) && rows[j].PrecedingIdleNanos == nil {
					j++
				}
				t.startup = rows[i:j]
				sort.Slice(t.startup, func(i, j int) bool { return t.startup[i].ServiceDurationNanos < t.startup[j].ServiceDurationNanos })
			} else {
				gap := *rows[i].PrecedingIdleNanos
				for j < len(rows) && rows[j].PrecedingIdleNanos != nil && *rows[j].PrecedingIdleNanos == gap {
					j++
				}
				t.groups = append(t.groups, serviceGroup{gap: gap, samples: rows[i:j]})
			}
			i = j
		}
		d.trials = append(d.trials, t)
	}
	return d, nil
}

// nearest keeps both neighboring gap groups on an integer-distance tie.
func (t *serviceTrial) nearest(gap *int64) (a, b []ServiceSample, distance int64, outside bool) {
	if gap == nil {
		return t.startup, nil, 0, false
	}
	if len(t.groups) == 0 {
		return nil, nil, 0, false
	}
	g := *gap
	i := sort.Search(len(t.groups), func(i int) bool { return t.groups[i].gap >= g })
	outside = g < t.groups[0].gap || g > t.groups[len(t.groups)-1].gap
	if i == len(t.groups) {
		x := t.groups[i-1]
		return x.samples, nil, g - x.gap, outside
	}
	x := t.groups[i]
	if i == 0 || x.gap == g {
		return x.samples, nil, x.gap - g, outside
	}
	y := t.groups[i-1]
	left, right := g-y.gap, x.gap-g
	if left < right {
		return y.samples, nil, left, outside
	}
	if right < left {
		return x.samples, nil, right, outside
	}
	return y.samples, x.samples, left, outside
}
func (d *ServiceDistribution) support(gap *int64) ConditionalSupport {
	var out ConditionalSupport
	if d == nil || gap != nil && *gap < 0 {
		return out
	}
	for i := range d.trials {
		a, b, distance, outside := d.trials[i].nearest(gap)
		if len(a)+len(b) == 0 {
			continue
		}
		out.TrialCount++
		out.DistanceNanos = max(out.DistanceNanos, distance)
		out.OutsideSupport = out.OutsideSupport || outside
	}
	return out
}
func serviceUpperBound(samples []ServiceSample, value int64) int {
	return sort.Search(len(samples), func(i int) bool { return samples[i].ServiceDurationNanos > value })
}
func (d *ServiceDistribution) Sample(gap *int64, rng *rand.Rand) (ServiceSample, ConditionalSupport, bool) {
	support := d.support(gap)
	if support.TrialCount == 0 {
		return ServiceSample{}, support, false
	}
	trial := rng.IntN(support.TrialCount)
	for i := range d.trials {
		a, b, _, _ := d.trials[i].nearest(gap)
		if len(a)+len(b) == 0 {
			continue
		}
		if trial == 0 {
			n := rng.IntN(len(a) + len(b))
			if n < len(a) {
				return a[n], support, true
			}
			return b[n-len(a)], support, true
		}
		trial--
	}
	panic("eligible trial missing")
}

// SampleResidual conditions the same nearest/trial mixture on S > elapsed.
// Surviving trial mass is survivors/original-neighbors, not uniform surviving trials.
func (d *ServiceDistribution) SampleResidual(gap *int64, elapsed int64, rng *rand.Rand) (ServiceSample, ConditionalSupport, bool) {
	support := d.support(gap)
	if support.TrialCount == 0 || elapsed < 0 {
		return ServiceSample{}, support, false
	}
	total := 0.0
	for i := range d.trials {
		a, b, _, _ := d.trials[i].nearest(gap)
		if n := len(a) + len(b); n > 0 {
			total += float64(n-serviceUpperBound(a, elapsed)-serviceUpperBound(b, elapsed)) / float64(n)
		}
	}
	if total == 0 {
		return ServiceSample{}, support, false
	}
	pick := rng.Float64() * total
	for i := range d.trials {
		a, b, _, _ := d.trials[i].nearest(gap)
		n := len(a) + len(b)
		if n == 0 {
			continue
		}
		ai, bi := serviceUpperBound(a, elapsed), serviceUpperBound(b, elapsed)
		survivors := n - ai - bi
		if survivors == 0 {
			continue
		}
		mass := float64(survivors) / float64(n)
		pick -= mass
		if pick < 0 {
			j := rng.IntN(survivors)
			if j < len(a)-ai {
				return a[ai+j], support, true
			}
			return b[bi+j-(len(a)-ai)], support, true
		}
	}
	panic("surviving trial missing")
}

// QuantileNanos is the empirical inverse CDF of uniform physical trials and
// uniform actual nearest records within each trial; percentiles use [0,100].
func (d *ServiceDistribution) QuantileNanos(gap *int64, percentile float64) (int64, ConditionalSupport, bool) {
	var support ConditionalSupport
	if d == nil || gap != nil && *gap < 0 {
		return 0, support, false
	}
	var local [32]serviceNeighbors
	nearest := local[:0]
	selected := 0
	for j := range d.trials {
		a, b, distance, outside := d.trials[j].nearest(gap)
		if len(a)+len(b) == 0 {
			continue
		}
		if len(nearest) == cap(nearest) {
			all := make([]serviceNeighbors, len(nearest), len(d.trials))
			copy(all, nearest)
			nearest = all
		}
		nearest = append(nearest, serviceNeighbors{a, b})
		selected += len(a) + len(b)
		support.TrialCount++
		support.DistanceNanos = max(support.DistanceNanos, distance)
		support.OutsideSupport = support.OutsideSupport || outside
	}
	if support.TrialCount == 0 || math.IsNaN(percentile) || percentile < 0 || percentile > 100 {
		return 0, support, false
	}
	// Only selected records can increase the CDF. Keep the physical-trial
	// accumulation and comparison identical, including positive mass at p0.
	reaches := func(value int64) bool {
		mass := 0.0
		for _, trial := range nearest {
			n := len(trial.a) + len(trial.b)
			mass += float64(serviceUpperBound(trial.a, value)+serviceUpperBound(trial.b, value)) / float64(n)
		}
		return mass > 0 && mass/float64(support.TrialCount) >= percentile/100
	}
	var values [32]int64
	if selected <= len(values) {
		candidates := values[:0]
		for _, trial := range nearest {
			for _, group := range [2][]ServiceSample{trial.a, trial.b} {
				for _, sample := range group {
					candidates = append(candidates, sample.ServiceDurationNanos)
				}
			}
		}
		slices.Sort(candidates)
		index := sort.Search(len(candidates), func(i int) bool { return reaches(candidates[i]) })
		if index == len(candidates) {
			return 0, support, false
		}
		return candidates[index], support, true
	}
	// Large groups are already sorted. Find each group's first CDF crossing,
	// retaining the smallest, without copying or sorting their records.
	var value int64
	found := false
	for _, trial := range nearest {
		for _, group := range [2][]ServiceSample{trial.a, trial.b} {
			n := len(group)
			if found {
				n = sort.Search(n, func(i int) bool { return group[i].ServiceDurationNanos >= value })
			}
			index := sort.Search(n, func(i int) bool { return reaches(group[i].ServiceDurationNanos) })
			if index < n {
				value, found = group[index].ServiceDurationNanos, true
			}
		}
	}
	return value, support, found
}
