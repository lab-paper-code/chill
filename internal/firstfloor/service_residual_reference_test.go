package firstfloor

import (
	"fmt"
	"math"
	"sort"
	"testing"
)

// The pre-E2 implementation is a test oracle for trial summation order, retry,
// ties, support and failed queries. Production has only one implementation.
func originalResidualQuantile(d *ServiceDistribution, gap *int64, elapsed int64, percentile float64) (int64, ConditionalSupport, bool) {
	var support ConditionalSupport
	if d == nil || elapsed < 0 || gap != nil && *gap < 0 || math.IsNaN(percentile) || percentile < 0 || percentile > 100 {
		return 0, support, false
	}
	support = d.support(gap)
	total := 0.0
	for i := range d.trials {
		a, b, _, _ := d.trials[i].nearest(gap)
		if n := len(a) + len(b); n > 0 {
			total += float64(n-serviceUpperBound(a, elapsed)-serviceUpperBound(b, elapsed)) / float64(n)
		}
	}
	retry := total == 0
	if retry {
		support = ConditionalSupport{}
		for i := range d.trials {
			a, b, distance, outside := d.trials[i].nearestSurvivors(gap, elapsed)
			if len(a)+len(b) > 0 {
				total++
				support.TrialCount++
				support.DistanceNanos = max(support.DistanceNanos, distance)
				support.OutsideSupport = support.OutsideSupport || outside
			}
		}
	}
	if total == 0 {
		return 0, support, false
	}
	index := sort.Search(len(d.services), func(j int) bool {
		value := d.services[j]
		if value <= elapsed {
			return false
		}
		mass := 0.0
		for i := range d.trials {
			a, b, _, _ := d.trials[i].nearest(gap)
			if retry {
				a, b, _, _ = d.trials[i].nearestSurvivors(gap, elapsed)
			}
			if n := len(a) + len(b); n > 0 {
				mass += float64(serviceUpperBound(a, value)+serviceUpperBound(b, value)-serviceUpperBound(a, elapsed)-serviceUpperBound(b, elapsed)) / float64(n)
			}
		}
		return mass > 0 && mass/total >= percentile/100
	})
	if index == len(d.services) {
		return 0, support, false
	}
	return d.services[index] - elapsed, support, true
}

func TestResidualQuantileMatchesOriginalTrialSummation(t *testing.T) {
	for _, trials := range []int{3, 33} {
		var samples []ServiceSample
		for trial := range trials {
			for _, gap := range []int64{0, 10, 20, 80} {
				for j := range trial%4 + 1 {
					samples = append(samples, ServiceSample{TrialID: fmt.Sprint(trial), PrecedingIdleNanos: &gap, ServiceDurationNanos: int64(5+trial+j) + gap})
				}
			}
			samples = append(samples, ServiceSample{TrialID: fmt.Sprint(trial), ServiceDurationNanos: int64(7 + trial)})
		}
		d, err := NewServiceDistribution(samples)
		if err != nil {
			t.Fatal(err)
		}
		gaps := []*int64{nil}
		for _, gap := range []int64{-1, 0, 1, 5, 10, 15, 20, 50, 80, 100} {
			gaps = append(gaps, &gap)
		}
		for _, gap := range gaps {
			for _, elapsed := range []int64{-1, 0, 4, 5, 7, 20, 60, 100, 200} {
				for _, percentile := range []float64{-1, 0, 0.1, 50, 95, 99.999999, 100, 101, math.NaN()} {
					want, support, known := originalResidualQuantile(d, gap, elapsed, percentile)
					got, gotSupport, gotKnown := d.ResidualQuantileNanos(gap, elapsed, percentile)
					if got != want || gotSupport != support || gotKnown != known {
						t.Fatalf("trials=%d elapsed=%d percentile=%g: got (%d,%+v,%v), want (%d,%+v,%v)", trials, elapsed, percentile, got, gotSupport, gotKnown, want, support, known)
					}
					if percentile == 100 && d.SupportsResidual(gap, elapsed) != known {
						t.Fatal("support-only survival changed the original executor-known result")
					}
				}
			}
		}
	}
}
