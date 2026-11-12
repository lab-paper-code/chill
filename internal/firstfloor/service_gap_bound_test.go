package firstfloor

import "testing"

func TestMaximumConditionalQuantileMatchesEveryIntegerGap(t *testing.T) {
	rows := []struct {
		trial        string
		gap, service int64
	}{
		{"a", 0, 10}, {"a", 20, 50}, {"a", 40, 10},
		{"b", 0, 60}, {"b", 10, 20}, {"b", 40, 80},
		{"dead", 10, 5},
	}
	samples := make([]ServiceSample, 0, len(rows))
	for _, row := range rows {
		gap := row.gap
		samples = append(samples, ServiceSample{TrialID: row.trial, PrecedingIdleNanos: &gap, ServiceDurationNanos: row.service})
	}
	d, err := NewServiceDistribution(samples)
	if err != nil {
		t.Fatal(err)
	}
	var cache QuantileCache
	for lower := int64(0); lower <= 40; lower += 3 {
		for upper := lower; upper <= 50; upper += 5 {
			for _, percentile := range []float64{0, 50, 95, 100} {
				for _, elapsed := range []int64{-1, 0, 5, 20, 60, 100} {
					residual := elapsed >= 0
					at := func(gap int64) (int64, ConditionalSupport, bool) {
						if residual {
							return d.ResidualQuantileNanos(&gap, elapsed, percentile)
						}
						return d.QuantileNanos(&gap, percentile)
					}
					want, support, known := at(lower)
					maximizing := lower
					for gap := lower + 1; gap <= upper; gap++ {
						value, evidence, ok := at(gap)
						known = known && ok
						support.DistanceNanos = max(support.DistanceNanos, evidence.DistanceNanos)
						support.TrialCount = min(support.TrialCount, evidence.TrialCount)
						support.OutsideSupport = support.OutsideSupport || evidence.OutsideSupport
						if value > want {
							want, maximizing = value, gap
						}
					}
					if !known {
						want, maximizing = 0, 0
					}
					var got, arg int64
					var evidence ConditionalSupport
					var ok bool
					if residual {
						got, arg, evidence, ok = d.MaximumResidualQuantileNanos(lower, upper, elapsed, percentile)
					} else {
						got, arg, evidence, ok = d.MaximumQuantileNanos(lower, upper, percentile)
					}
					if got != want || arg != maximizing || evidence != support || ok != known {
						t.Fatalf("[%d,%d] p%g elapsed=%d: got (%d,%d,%+v,%v), want (%d,%d,%+v,%v)", lower, upper, percentile, elapsed, got, arg, evidence, ok, want, maximizing, support, known)
					}
					if residual {
						got, evidence, ok = cache.MaximumResidualQuantileNanos(d, lower, upper, elapsed, percentile)
					} else {
						got, evidence, ok = cache.MaximumQuantileNanos(d, lower, upper, percentile)
					}
					if got != want || evidence != support || ok != known {
						t.Fatal("cache changed exact gap bound")
					}
				}
			}
		}
	}
	for _, bounds := range [][2]int64{{-1, 0}, {5, 4}} {
		if _, _, _, ok := d.MaximumQuantileNanos(bounds[0], bounds[1], 95); ok {
			t.Fatal("invalid interval became a quantile")
		}
	}
}
