package firstfloor

import (
	"math/rand/v2"
	"testing"
)

// The interval maximum must equal the largest answer at every integer gap,
// including repeated gaps, equal-distance ties and survivors of elapsed, on
// both the table path and the sliding path.
func TestMaximumConditionalQuantileMatchesEveryIntegerGap(t *testing.T) {
	rows := [][2]int64{
		{0, 10}, {0, 60}, {3, 5}, {4, 70}, {4, 20}, {4, 90}, {9, 50}, {10, 20}, {12, 15},
		{20, 50}, {21, 8}, {25, 65}, {33, 40}, {40, 10}, {40, 80}, {41, 30}, {47, 55},
	}
	samples := make([]ServiceSample, 0, len(rows))
	for _, row := range rows {
		gap := row[0]
		samples = append(samples, ServiceSample{TrialID: "a", PrecedingIdleNanos: &gap, ServiceDurationNanos: row[1]})
	}
	d, err := NewServiceDistribution(samples)
	if err != nil {
		t.Fatal(err)
	}
	var cache QuantileCache
	rng := rand.New(rand.NewPCG(1, 2))
	for lower := int64(0); lower <= 50; lower += 3 {
		for upper := lower; upper <= 55; upper += 4 {
			// Floors 1, 4 and 8 records; 22 records exceed the profile.
			for _, percentile := range []float64{50, 70, 80, 90} {
				for _, elapsed := range []int64{0, 9, 20, 60, 100} {
					want, _, known := d.ResidualQuantileNanos(&lower, elapsed, percentile)
					for gap := lower + 1; gap <= upper; gap++ {
						value, _, ok := d.ResidualQuantileNanos(&gap, elapsed, percentile)
						known = known && ok
						want = max(want, value)
					}
					if !known {
						want = 0
					}
					got, _, ok := d.MaximumResidualQuantileNanos(lower, upper, elapsed, percentile)
					if got != want || ok != known {
						t.Fatalf("[%d,%d] p%g elapsed=%d: got (%d,%v), want (%d,%v)", lower, upper, percentile, elapsed, got, ok, want, known)
					}
					if got, _, ok = cache.MaximumResidualQuantileNanos(d, lower, upper, elapsed, percentile); got != want || ok != known {
						t.Fatal("cache changed exact gap bound")
					}
					if elapsed > 0 {
						continue
					}
					if got, _, ok = d.MaximumQuantileNanos(lower, upper, percentile); got != want || ok != known {
						t.Fatalf("[%d,%d] p%g table: got (%d,%v), want (%d,%v)", lower, upper, percentile, got, ok, want, known)
					}
					if _, _, ok = d.SampleMaximum(lower, upper, percentile, rng); ok != known {
						t.Fatal("envelope draw lost its support")
					}
				}
			}
		}
	}
	for _, bounds := range [][2]int64{{-1, 0}, {5, 4}} {
		if _, _, ok := d.MaximumQuantileNanos(bounds[0], bounds[1], 50); ok {
			t.Fatal("invalid interval became a quantile")
		}
	}
}
