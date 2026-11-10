package firstfloor

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func numericDistributionFixture(tb testing.TB, trials, groups int) *ServiceDistribution {
	tb.Helper()
	var rows []ServiceSample
	for trial := range trials {
		id := fmt.Sprintf("trial-%03d", trial)
		if trial%3 != 0 {
			rows = append(rows, ServiceSample{TrialID: id, ServiceDurationNanos: int64(1000 + trial)})
		}
		if trial%5 == 1 {
			continue // startup-only physical trials are ineligible at known gaps
		}
		for group := range groups {
			gap := int64(2*group + trial%2)
			for record := 0; record <= trial%4; record++ {
				rows = append(rows, ServiceSample{TrialID: id, PrecedingIdleNanos: &gap, ServiceDurationNanos: int64(1 + (trial*7+group*3+record)%101)})
			}
		}
	}
	d, err := NewServiceDistribution(rows)
	if err != nil {
		tb.Fatal(err)
	}
	return d
}

func TestQuantileMatchesBaselineTrialMixture(t *testing.T) {
	for _, trials := range []int{1, 32, 33, 64} {
		d := numericDistributionFixture(t, trials, 8)
		services := baselineServiceValues(d)
		gaps := []*int64{nil}
		for _, value := range []int64{-1, 0, 1, 2, 7, 8, 15, 16, math.MaxInt64} {
			gap := value
			gaps = append(gaps, &gap)
		}
		for _, gap := range gaps {
			for _, percentile := range []float64{math.NaN(), -1, 0, math.SmallestNonzeroFloat64, 12.5, 37.5, 50, math.Nextafter(50, 100), 87.5, 95, 99.9, 100, 101, math.Inf(1)} {
				want, support, valid := baselineQuantileNanos(d, services, gap, percentile)
				got, gotSupport, ok := d.QuantileNanos(gap, percentile)
				if got != want || gotSupport != support || ok != valid {
					t.Fatalf("trials=%d gap=%v p=%g: got %d %+v %v, want %d %+v %v", trials, gap, percentile, got, gotSupport, ok, want, support, valid)
				}
			}
		}
	}
	var absent *ServiceDistribution
	if _, support, ok := absent.QuantileNanos(nil, 50); ok || support != (ConditionalSupport{}) {
		t.Fatalf("nil distribution gained support: %+v %v", support, ok)
	}
}

func TestQuantileIntegerNeighborTieAndSupport(t *testing.T) {
	left, right := int64(math.MaxInt64-2), int64(math.MaxInt64)
	d, err := NewServiceDistribution([]ServiceSample{
		{TrialID: "a", PrecedingIdleNanos: &left, ServiceDurationNanos: 2},
		{TrialID: "a", PrecedingIdleNanos: &left, ServiceDurationNanos: 2},
		{TrialID: "a", PrecedingIdleNanos: &left, ServiceDurationNanos: 2},
		{TrialID: "a", PrecedingIdleNanos: &right, ServiceDurationNanos: 200},
		{TrialID: "b", PrecedingIdleNanos: &left, ServiceDurationNanos: 80},
		{TrialID: "startup", ServiceDurationNanos: 123},
	})
	if err != nil {
		t.Fatal(err)
	}
	gap := left + 1
	for _, tc := range []struct {
		percentile float64
		want       int64
	}{{0, 2}, {37.5, 2}, {math.Nextafter(37.5, 100), 80}, {87.5, 80}, {math.Nextafter(87.5, 100), 200}, {100, 200}} {
		got, support, ok := d.QuantileNanos(&gap, tc.percentile)
		if !ok || got != tc.want || support != (ConditionalSupport{DistanceNanos: 1, TrialCount: 2, OutsideSupport: true}) {
			t.Fatalf("integer tie p%g: got %d %+v %v", tc.percentile, got, support, ok)
		}
	}
	if got, support, ok := d.QuantileNanos(nil, 100); !ok || got != 123 || support != (ConditionalSupport{TrialCount: 1}) {
		t.Fatalf("startup mixture: got %d %+v %v", got, support, ok)
	}
}

// Repeated selected gaps exercise large pre-sorted groups. A third physical
// trial lacks the right gap, so ties must not turn into uniform record weights.
func selectedGroupFixture(tb testing.TB, records int) *ServiceDistribution {
	tb.Helper()
	rows := make([]ServiceSample, 0, 8*records+3)
	left, right, unrelated := int64(0), int64(2), int64(100_000)
	for trial, id := range []string{"c", "a", "b"} {
		for record := range records {
			service := int64(2 + (record*7+trial*13)%257)
			if trial == 2 && record == records-1 {
				service = math.MaxInt64
			}
			rows = append(rows,
				ServiceSample{TrialID: id, ServiceDurationNanos: service},
				ServiceSample{TrialID: id, PrecedingIdleNanos: &left, ServiceDurationNanos: service},
			)
			if trial != 2 {
				rows = append(rows, ServiceSample{TrialID: id, PrecedingIdleNanos: &right, ServiceDurationNanos: service + 1})
			}
		}
		rows = append(rows, ServiceSample{TrialID: id, PrecedingIdleNanos: &unrelated, ServiceDurationNanos: 1})
	}
	d, err := NewServiceDistribution(rows)
	if err != nil {
		tb.Fatal(err)
	}
	return d
}

func TestQuantileSelectedGroupBoundaries(t *testing.T) {
	for _, records := range []int{1, 6, 4096} {
		d := selectedGroupFixture(t, records)
		services := baselineServiceValues(d)
		gaps := []*int64{nil}
		for _, gap := range []int64{-1, 0, 1, 2, 3, math.MaxInt64} {
			gaps = append(gaps, &gap)
		}
		for _, gap := range gaps {
			percentiles := []float64{math.NaN(), -1, 0, math.SmallestNonzeroFloat64, 50, 99, 100, 101}
			// Check both sides of floating CDF jumps, not just round percentiles.
			support := d.support(gap)
			if support.TrialCount > 0 {
				for _, value := range []int64{2, 33, 128, 258, math.MaxInt64} {
					mass := 0.0
					for i := range d.trials {
						a, b, _, _ := d.trials[i].nearest(gap)
						if n := len(a) + len(b); n > 0 {
							mass += float64(serviceUpperBound(a, value)+serviceUpperBound(b, value)) / float64(n)
						}
					}
					p := mass / float64(support.TrialCount) * 100
					percentiles = append(percentiles, math.Nextafter(p, 0), p, math.Nextafter(p, math.Inf(1)))
				}
			}
			for _, percentile := range percentiles {
				want, support, valid := baselineQuantileNanos(d, services, gap, percentile)
				got, gotSupport, ok := d.QuantileNanos(gap, percentile)
				if got != want || gotSupport != support || ok != valid {
					t.Fatalf("records=%d gap=%v p=%.17g: got %d %+v %v, want %d %+v %v", records, gap, percentile, got, gotSupport, ok, want, support, valid)
				}
			}
		}
	}
	empty := &ServiceDistribution{}
	if value, support, ok := empty.QuantileNanos(nil, 0); value != 0 || support != (ConditionalSupport{}) || ok {
		t.Fatalf("empty distribution gained support: %d %+v %v", value, support, ok)
	}
}

func BenchmarkConditionalQuantileGroupSize(b *testing.B) {
	for _, records := range []int{1, 32, 4096, 65536} {
		d := selectedGroupFixture(b, records)
		for _, gap := range []int64{0, 1} {
			b.Run(fmt.Sprintf("records%d/gap%d", records, gap), func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					if _, _, ok := d.QuantileNanos(&gap, 99); !ok {
						b.Fatal("missing quantile")
					}
				}
			})
		}
	}
}

func TestBatchServiceCostsUseExactQueryKeys(t *testing.T) {
	batch := testBatch(1, 1, startupSample("a", 11), pairedSample("a", 0, 1), pairedSample("a", 20, 9))
	other := testBatch(1, 1, pairedSample("a", 0, 7))
	var costs serviceCosts
	gap, same := int64(0), int64(0)
	for _, query := range []struct {
		batch BatchProfile
		gap   *int64
		p     float64
		want  float64
	}{{batch, &gap, 50, 1}, {batch, &same, 50, 1}, {batch, nil, 50, 11}, {other, &gap, 50, 7}, {batch, &gap, 100, 1}} {
		if got := costs.cost(query.batch, query.p, query.gap); got != query.want {
			t.Fatalf("exact query p%g: got %g want %g", query.p, got, query.want)
		}
	}
	if costs.count != 4 || len(costs.extra) != 0 {
		t.Fatalf("same-valued gap pointers did not reuse cost: %+v", costs)
	}
	gap = int64(20 * time.Millisecond)
	if got := costs.cost(batch, 50, &gap); got != 9 {
		t.Fatalf("reused caller scratch pointer kept old gap: %g", got)
	}
	var overflow serviceCosts
	for range 2 {
		for percentile := 1; percentile <= 20; percentile++ {
			if got := overflow.cost(batch, float64(percentile), nil); got != 11 {
				t.Fatalf("overflow percentile %d: %g", percentile, got)
			}
		}
	}
	if overflow.count != 16 || len(overflow.extra) != 4 {
		t.Fatalf("overflow queries were lost or repeated: local=%d extra=%d", overflow.count, len(overflow.extra))
	}
}

func TestBatchServiceReuseRetainsEachRequestDeadline(t *testing.T) {
	p := testPolicy()
	p.Batches[0] = testBatch(1, 2, startupSample("a", 2))
	p.Batches[1] = testBatch(2, 1, startupSample("a", 2), startupSample("a", 80))
	requests := []RequestState{testState("a", 50, 0), testState("a", 100, 0)}
	requests[0].Context.SenderElapsedMS, requests[0].ElapsedMS = 30, 10
	requests[1].Context.SenderElapsedMS, requests[1].ElapsedMS = 70, 10
	for i := range requests {
		requests[i].Context.SLO.Percentile = 50
	}
	for _, tc := range []struct {
		margin, percentile float64
		size               int
	}{{19, 50, 1}, {18, 50, 2}, {18, 100, 1}, {18, 50, 2}} {
		requests[1].Context.CompletionMarginMS = tc.margin
		requests[1].Context.SLO.Percentile = tc.percentile
		choice := ChooseBatch(p, requests, nil)
		if choice.Size != tc.size {
			t.Fatalf("individual margin=%g p=%g: %+v", tc.margin, tc.percentile, choice)
		}
	}
	var costs serviceCosts
	costs.cost(p.Batches[1], 50, nil) // throughput and feasibility share this query
	if !batchFeasible(requests, 0, p.Batches[1], nil, &costs) || costs.count != 1 {
		t.Fatalf("compatible requests repeated identical quantile: queries=%d", costs.count)
	}
}

func BenchmarkConditionalQuantile(b *testing.B) {
	for _, trials := range []int{8, 32, 64} {
		b.Run(fmt.Sprintf("trials%d", trials), func(b *testing.B) {
			d := numericDistributionFixture(b, trials, 64)
			gap := int64(63)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, _, ok := d.QuantileNanos(&gap, 99); !ok {
					b.Fatal("missing quantile")
				}
			}
		})
	}
}

func BenchmarkChooseBatchServiceReuse(b *testing.B) {
	for _, count := range []int{8, 64} {
		b.Run(fmt.Sprintf("requests%d", count), func(b *testing.B) {
			d := numericDistributionFixture(b, 8, 64)
			p := Policy{}
			for _, size := range []int{1, 2, 4, 8} {
				p.Batches = append(p.Batches, BatchProfile{Size: size, EnergyPerItemJ: 1 / float64(size), Distribution: d})
			}
			requests := make([]RequestState, count)
			for i := range requests {
				requests[i] = testState("a", 100, 0)
			}
			gap := int64(63)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if choice := ChooseBatch(p, requests, &gap); choice.Size != 8 {
					b.Fatalf("changed selected batch: %+v", choice)
				}
			}
		})
	}
}
