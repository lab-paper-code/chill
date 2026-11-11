package firstfloor

import "testing"

func TestQuantileCachePreservesValuesSupportAndUnknown(t *testing.T) {
	zero, gap := int64(0), int64(10)
	outside := int64(100)
	samples := []ServiceSample{
		{TrialID: "a", ServiceDurationNanos: 5},
		{TrialID: "a", PrecedingIdleNanos: &zero, ServiceDurationNanos: 20},
		{TrialID: "a", PrecedingIdleNanos: &gap, ServiceDurationNanos: 30},
	}
	a, err := NewServiceDistribution(samples)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewServiceDistribution(samples[:1])
	if err != nil {
		t.Fatal(err)
	}
	var cache QuantileCache
	for range 2 {
		for _, d := range []*ServiceDistribution{a, b, nil} {
			for _, idle := range []*int64{nil, &zero, &gap, &outside} {
				for _, percentile := range []float64{0, 50, 100} {
					want, support, known := d.QuantileNanos(idle, percentile)
					got, gotSupport, gotKnown := cache.QuantileNanos(d, idle, percentile)
					if got != want || gotSupport != support || gotKnown != known {
						t.Fatal("quantile cache changed result")
					}
					if percentile == 100 && d.SupportsGap(idle) != known {
						t.Fatal("support-only availability differs from the original quantile query")
					}
					for _, elapsed := range []int64{0, 5, 100} {
						want, support, known := d.ResidualQuantileNanos(idle, elapsed, percentile)
						got, gotSupport, gotKnown := cache.ResidualQuantileNanos(d, idle, elapsed, percentile)
						if got != want || gotSupport != support || gotKnown != known {
							t.Fatal("residual cache changed result")
						}
						if percentile == 100 && d.SupportsResidual(idle, elapsed) != known {
							t.Fatal("support-only survival differs from the original residual query")
						}
					}
				}
			}
		}
	}
	if len(cache.entries) != 144 {
		t.Fatalf("queries were not reused: %d", len(cache.entries))
	}
	cache.Reset()
	if len(cache.entries) != 0 {
		t.Fatal("dispatch retained query identities")
	}
}
