package firstfloor

import (
	"cmp"
	"errors"
	"math"
	"math/bits"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
)

// ConditionalServiceModel identifies the implemented estimator: the empirical
// percentile of the records nearest the queried preceding idle gap on a
// logarithmic gap scale, as many as the percentile's sample floor.
const ConditionalServiceModel = "log-gap-nearest-sample-floor"

// MinimumServiceSamples is the sample floor of a percentile p: the smallest
// record count whose maximum exceeds the p-quantile with probability at least
// p, ceil(ln(1-p)/ln(p)). It is 59 at the 95th percentile.
func MinimumServiceSamples(percentile float64) (int, error) {
	if math.IsNaN(percentile) || percentile <= 0 || percentile >= 100 {
		return 0, errors.New("Workload SLO percentile must be finite and in (0, 100) for finite sample coverage")
	}
	p := percentile / 100
	count := math.Ceil(math.Log1p(-p) / math.Log(p))
	if count > math.MaxInt32 {
		return 0, errors.New("Workload SLO sample count exceeds the supported range")
	}
	return int(count), nil
}

// ConditionalSupport describes the records one answer rests on. Records below
// MinimumServiceSamples means the percentile is not identified: the profile
// holds that many records in all, and the query is unknown. DistanceNanos is
// the largest gap distance between a queried gap and a selected record;
// OutsideSupport reports a queried gap outside the observed gap range.
type ConditionalSupport struct {
	Records        int   `json:"records"`
	DistanceNanos  int64 `json:"distanceNanos"`
	OutsideSupport bool  `json:"outsideSupport"`
}

// ServiceDistribution is immutable after hydration apart from its per-
// percentile window tables, built once on first use.
//
// The N records nearest a gap g are those with the smallest |ln(1+g)-ln(1+x)|
// over record gaps x. Gaps are integer nanoseconds of the native monotonic
// clock; adding that unit keeps a zero gap finite on the logarithmic scale.
// They form a window of N consecutive records in gap order; at equal distance
// the smaller gap is taken, and records sharing one gap are taken in stored
// order from the side facing g. Records of every physical trial are pooled:
// trial-to-trial spread is part of the service distribution the scheduler
// faces. A record without a preceding idle gap (the first execution after
// start) belongs to no gap and is dropped.
type ServiceDistribution struct {
	gaps          []int64 // ascending; equal gaps keep stored order
	services      []int64 // service of the record at the same index in gaps
	firstDropped  int
	maxService    int64
	build         sync.Mutex
	windowQueries atomic.Pointer[[]*windowTable]
}

// windowTable holds, for one percentile, its sample floor and a max segment
// tree whose leaves are the percentile of every window.
type windowTable struct {
	percentile float64
	n          int
	tree       []int64
}

func NewServiceDistribution(samples []ServiceSample) (*ServiceDistribution, error) {
	if len(samples) == 0 {
		return nil, errors.New("paired service samples required")
	}
	type record struct{ gap, service int64 }
	d := &ServiceDistribution{}
	known := make([]record, 0, len(samples))
	for _, s := range samples {
		if s.ServiceDurationNanos <= 0 || s.PrecedingIdleNanos != nil && *s.PrecedingIdleNanos < 0 {
			return nil, errors.New("positive service and nonnegative actual idle required")
		}
		if s.PrecedingIdleNanos == nil {
			d.firstDropped++
			continue
		}
		known = append(known, record{*s.PrecedingIdleNanos, s.ServiceDurationNanos})
		d.maxService = max(d.maxService, s.ServiceDurationNanos)
	}
	slices.SortStableFunc(known, func(a, b record) int { return cmp.Compare(a.gap, b.gap) })
	d.gaps, d.services = make([]int64, len(known)), make([]int64, len(known))
	for i, r := range known {
		d.gaps[i], d.services[i] = r.gap, r.service
	}
	return d, nil
}

// DroppedFirstExecutions counts records without a preceding idle gap.
func (d *ServiceDistribution) DroppedFirstExecutions() int { return d.firstDropped }

// QuantileNanos returns the empirical percentile of the window at gap. The
// sample floor makes the window's maximum a bound on the quantile at
// confidence p; its empirical percentile is a point estimate, not a bound.
// A nil gap (no execution anchor) is not measured.
func (d *ServiceDistribution) QuantileNanos(gap *int64, percentile float64) (int64, ConditionalSupport, bool) {
	if gap == nil {
		return 0, ConditionalSupport{}, false
	}
	return d.MaximumQuantileNanos(*gap, *gap, percentile)
}

// MaximumQuantileNanos returns the largest QuantileNanos over every integer
// gap in [lower, upper]: a binary search and a segment-tree range maximum.
func (d *ServiceDistribution) MaximumQuantileNanos(lower, upper int64, percentile float64) (int64, ConditionalSupport, bool) {
	first, last, support, ok := d.windows(lower, upper, percentile)
	if !ok {
		return 0, support, false
	}
	t := d.table(percentile, support.Records)
	value := int64(math.MinInt64)
	m := len(t.tree) / 2
	for l, r := first+m, last+m+1; l < r; l, r = l/2, r/2 {
		if l&1 == 1 {
			value = max(value, t.tree[l])
			l++
		}
		if r&1 == 1 {
			r--
			value = max(value, t.tree[r])
		}
	}
	return value, support, true
}

// ResidualQuantileNanos returns the percentile of S-elapsed over the window's
// records with S > elapsed. No survivor leaves the result unknown.
func (d *ServiceDistribution) ResidualQuantileNanos(gap *int64, elapsed int64, percentile float64) (int64, ConditionalSupport, bool) {
	if gap == nil {
		return 0, ConditionalSupport{}, false
	}
	return d.MaximumResidualQuantileNanos(*gap, *gap, elapsed, percentile)
}

// MaximumResidualQuantileNanos is MaximumQuantileNanos for residuals, used
// when a running batch's actual preceding gap is known only as a range.
func (d *ServiceDistribution) MaximumResidualQuantileNanos(lower, upper, elapsed int64, percentile float64) (int64, ConditionalSupport, bool) {
	if elapsed < 0 {
		return 0, ConditionalSupport{}, false
	}
	return d.slide(lower, upper, elapsed, percentile, percentile)
}

// Sample draws a service duration uniformly from the window at gap.
func (d *ServiceDistribution) Sample(gap *int64, percentile float64, rng *rand.Rand) (int64, ConditionalSupport, bool) {
	if gap == nil {
		return 0, ConditionalSupport{}, false
	}
	first, _, support, ok := d.windows(*gap, *gap, percentile)
	if !ok {
		return 0, support, false
	}
	return d.services[first+rng.IntN(support.Records)], support, true
}

// SampleResidual draws a service duration S > elapsed uniformly from the
// window's survivors.
func (d *ServiceDistribution) SampleResidual(gap *int64, elapsed int64, percentile float64, rng *rand.Rand) (int64, ConditionalSupport, bool) {
	if gap == nil || elapsed < 0 {
		return 0, ConditionalSupport{}, false
	}
	value, support, ok := d.slide(*gap, *gap, elapsed, percentile, 100*rng.Float64())
	return value + elapsed, support, ok
}

// SampleMaximum draws from the envelope MaximumQuantileNanos bounds: its
// inverse CDF at a uniform level.
func (d *ServiceDistribution) SampleMaximum(lower, upper int64, percentile float64, rng *rand.Rand) (int64, ConditionalSupport, bool) {
	return d.slide(lower, upper, 0, percentile, 100*rng.Float64())
}

// SupportsGap reports whether a known gap has records at all; whether a
// percentile is identified is the query's answer.
func (d *ServiceDistribution) SupportsGap(gap *int64) bool {
	return d.SupportsResidual(gap, 0)
}

// SupportsResidual reports whether any record survives elapsed at a known gap.
func (d *ServiceDistribution) SupportsResidual(gap *int64, elapsed int64) bool {
	return d != nil && gap != nil && *gap >= 0 && elapsed >= 0 && d.maxService > elapsed
}

// windows returns the first and last window an integer gap in [lower, upper]
// selects. Fewer records than the sample floor leave the percentile
// unidentified.
func (d *ServiceDistribution) windows(lower, upper int64, percentile float64) (first, last int, support ConditionalSupport, ok bool) {
	if d == nil || lower < 0 || upper < lower {
		return 0, 0, support, false
	}
	n, err := MinimumServiceSamples(percentile)
	if err != nil || len(d.gaps) < n {
		return 0, 0, ConditionalSupport{Records: len(d.gaps)}, false
	}
	first, last = d.window(lower, n), d.window(upper, n)
	return first, last, ConditionalSupport{
		Records:        n,
		DistanceNanos:  max(upper-d.gaps[first], d.gaps[last+n-1]-lower),
		OutsideSupport: lower < d.gaps[0] || upper > d.gaps[len(d.gaps)-1],
	}, true
}

// window is the start of the n-record window at gap g: the number of windows
// i whose successor record i+n is strictly nearer g than record i, that is
// (1+g)^2 > (1+gaps[i])(1+gaps[i+n]). The comparison is exact in 128 bits.
func (d *ServiceDistribution) window(g int64, n int) int {
	gh, gl := bits.Mul64(uint64(g)+1, uint64(g)+1)
	lo, hi := 0, len(d.gaps)-n
	for lo < hi {
		i := int(uint(lo+hi) >> 1)
		wh, wl := bits.Mul64(uint64(d.gaps[i])+1, uint64(d.gaps[i+n])+1)
		if gh > wh || gh == wh && gl > wl {
			lo = i + 1
		} else {
			hi = i
		}
	}
	return lo
}

// table returns the window tree for percentile, building it once.
func (d *ServiceDistribution) table(percentile float64, n int) *windowTable {
	if t := d.findTable(percentile); t != nil {
		return t
	}
	d.build.Lock()
	defer d.build.Unlock()
	if t := d.findTable(percentile); t != nil {
		return t
	}
	m := len(d.gaps) - n + 1
	t := &windowTable{percentile: percentile, n: n, tree: make([]int64, 2*m)}
	d.slideWindows(0, m-1, n, 0, percentile, t.tree[m:])
	for i := m - 1; i > 0; i-- {
		t.tree[i] = max(t.tree[2*i], t.tree[2*i+1])
	}
	var tables []*windowTable
	if old := d.windowQueries.Load(); old != nil {
		tables = append(tables, *old...)
	}
	tables = append(tables, t)
	d.windowQueries.Store(&tables)
	return t
}

func (d *ServiceDistribution) findTable(percentile float64) *windowTable {
	if tables := d.windowQueries.Load(); tables != nil {
		for _, t := range *tables {
			if t.percentile == percentile {
				return t
			}
		}
	}
	return nil
}

// slide answers residual and random-level queries, which no table covers,
// by walking every window the interval selects.
// ponytail: O(windows x floor) per interval query; a per-rank table if gap
// intervals spanning many windows become hot.
func (d *ServiceDistribution) slide(lower, upper, elapsed int64, percentile, level float64) (int64, ConditionalSupport, bool) {
	first, last, support, ok := d.windows(lower, upper, percentile)
	if !ok || math.IsNaN(level) || level < 0 || level > 100 {
		return 0, support, false
	}
	value, ok := d.slideWindows(first, last, support.Records, elapsed, level, nil)
	if !ok {
		return 0, ConditionalSupport{}, false
	}
	return value - elapsed, support, true
}

// slideWindows keeps windows first..last sorted while sliding and returns the
// largest level percentile over each window's records above elapsed, or false
// when a window has none. values, when given, receives every window's answer.
// Windows of up to 512 records need no heap.
func (d *ServiceDistribution) slideWindows(first, last, n int, elapsed int64, level float64, values []int64) (int64, bool) {
	var local [512]int64
	sorted := local[:0]
	if n > len(local) {
		sorted = make([]int64, 0, n)
	}
	sorted = append(sorted, d.services[first:first+n]...)
	slices.Sort(sorted)
	top := int64(math.MinInt64)
	for w := first; ; w++ {
		dead, _ := slices.BinarySearchFunc(sorted, elapsed, func(v, e int64) int {
			if v <= e {
				return -1
			}
			return 1
		})
		survivors := n - dead
		if survivors == 0 {
			return 0, false
		}
		k := min(max(int(math.Ceil(level/100*float64(survivors))), 1), survivors)
		value := sorted[dead+k-1]
		top = max(top, value)
		if values != nil {
			values[w-first] = value
		}
		if w == last {
			return top, true
		}
		out, _ := slices.BinarySearch(sorted, d.services[w])
		copy(sorted[out:], sorted[out+1:])
		sorted = sorted[:n-1]
		in, _ := slices.BinarySearch(sorted, d.services[w+n])
		sorted = slices.Insert(sorted, in, d.services[w+n])
	}
}
