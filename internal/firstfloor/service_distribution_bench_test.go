package firstfloor

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

// fastModelProfile is one batch size of a fast model: 150,000 records with
// gaps log-uniform over 50 us to 5 s and service growing with the gap.
func fastModelProfile(tb testing.TB) *ServiceDistribution {
	tb.Helper()
	rng := rand.New(rand.NewPCG(3, 5))
	samples := make([]ServiceSample, 0, 150_000)
	for i := range 150_000 {
		gap := int64(50e3 * math.Pow(1e5, rng.Float64()))
		service := int64(8e6 + 2e6*math.Log10(float64(gap)/50e3) + rng.Float64()*4e6)
		samples = append(samples, ServiceSample{TrialID: fmt.Sprint("trial-", i%3), PrecedingIdleNanos: &gap, ServiceDurationNanos: service})
	}
	d, err := NewServiceDistribution(samples)
	if err != nil {
		tb.Fatal(err)
	}
	return d
}

func BenchmarkConditionalQuantile150k(b *testing.B) {
	gaps := []int64{int64(100 * time.Microsecond), int64(5 * time.Millisecond), int64(50 * time.Millisecond), int64(2 * time.Second)}
	d := fastModelProfile(b)
	for _, percentile := range []float64{95, 99} {
		n, _ := MinimumServiceSamples(percentile)
		b.Run(fmt.Sprintf("table-build-p%g", percentile), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				fresh := &ServiceDistribution{gaps: d.gaps, services: d.services, maxService: d.maxService}
				fresh.table(percentile, n)
			}
		})
	}
	if _, _, ok := d.QuantileNanos(&gaps[0], 95); !ok {
		b.Fatal("missing quantile")
	}
	b.Run("quantile-p95", func(b *testing.B) {
		b.ReportAllocs()
		for i := range b.N {
			if _, _, ok := d.QuantileNanos(&gaps[i%len(gaps)], 95); !ok {
				b.Fatal("missing quantile")
			}
		}
	})
	b.Run("maximum-1ms-p95", func(b *testing.B) {
		b.ReportAllocs()
		var uncached *QuantileCache
		for i := range b.N {
			gap := gaps[i%len(gaps)]
			if _, _, ok := uncached.MaximumQuantileNanos(d, gap, gap+int64(time.Millisecond), 95); !ok {
				b.Fatal("missing maximum quantile")
			}
		}
	})
	b.Run("residual-p95", func(b *testing.B) {
		b.ReportAllocs()
		for i := range b.N {
			if _, _, ok := d.ResidualQuantileNanos(&gaps[i%len(gaps)], int64(5*time.Millisecond), 95); !ok {
				b.Fatal("missing residual quantile")
			}
		}
	})
}
