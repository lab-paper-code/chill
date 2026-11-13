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
func fastModelProfile(tb testing.TB, trials int) *ServiceDistribution {
	tb.Helper()
	rng := rand.New(rand.NewPCG(3, 5))
	samples := make([]ServiceSample, 0, 150_000+trials)
	for i := range 150_000 {
		gap := int64(50e3 * math.Pow(1e5, rng.Float64()))
		service := int64(8e6 + 2e6*math.Log10(float64(gap)/50e3) + rng.Float64()*4e6)
		samples = append(samples, ServiceSample{TrialID: fmt.Sprint("trial-", i%trials), PrecedingIdleNanos: &gap, ServiceDurationNanos: service})
	}
	for trial := range trials {
		samples = append(samples, ServiceSample{TrialID: fmt.Sprint("trial-", trial), ServiceDurationNanos: 30e6})
	}
	d, err := NewServiceDistribution(samples)
	if err != nil {
		tb.Fatal(err)
	}
	return d
}

func BenchmarkConditionalQuantile150k(b *testing.B) {
	gaps := []int64{int64(100 * time.Microsecond), int64(5 * time.Millisecond), int64(50 * time.Millisecond), int64(2 * time.Second)}
	for _, trials := range []int{1, 3} {
		d := fastModelProfile(b, trials)
		b.Run(fmt.Sprintf("trials%d/quantile-p95", trials), func(b *testing.B) {
			b.ReportAllocs()
			for i := range b.N {
				if _, _, ok := d.QuantileNanos(&gaps[i%len(gaps)], 95); !ok {
					b.Fatal("missing quantile")
				}
			}
		})
		b.Run(fmt.Sprintf("trials%d/residual-p95", trials), func(b *testing.B) {
			b.ReportAllocs()
			for i := range b.N {
				if _, _, ok := d.ResidualQuantileNanos(&gaps[i%len(gaps)], int64(5*time.Millisecond), 95); !ok {
					b.Fatal("missing residual quantile")
				}
			}
		})
		b.Run(fmt.Sprintf("trials%d/maximum-1ms-p95", trials), func(b *testing.B) {
			b.ReportAllocs()
			var uncached *QuantileCache
			for i := range b.N {
				gap := gaps[i%len(gaps)]
				if _, _, ok := uncached.MaximumQuantileNanos(d, gap, gap+int64(time.Millisecond), 95); !ok {
					b.Fatal("missing maximum quantile")
				}
			}
		})
	}
}
