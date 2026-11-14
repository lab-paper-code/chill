package firstfloor

import (
	"fmt"
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

func TestBatchServiceCostsUseExactQueryKeys(t *testing.T) {
	batch := testBatch(1, 1, pairedSample("a", 0, 1), pairedSample("a", 20, 9))
	other := testBatch(1, 1, pairedSample("a", 0, 7))
	var costs serviceCosts
	gap, same := int64(0), int64(0)
	// A nil gap has no anchor and p100 no sample floor: both are unmeasured,
	// reported as not known rather than as a cost.
	for _, query := range []struct {
		batch BatchProfile
		gap   *int64
		p     float64
		want  float64
		known bool
	}{{batch, &gap, 50, 1, true}, {batch, &same, 50, 1, true}, {batch, nil, 50, 0, false}, {other, &gap, 50, 7, true}, {batch, &gap, 100, 0, false}} {
		if got, known := costs.cost(query.batch, query.p, query.gap); got != query.want || known != query.known {
			t.Fatalf("exact query p%g: got %g/%v want %g/%v", query.p, got, known, query.want, query.known)
		}
	}
	if costs.count != 4 || len(costs.extra) != 0 {
		t.Fatalf("same-valued gap pointers did not reuse cost: %+v", costs)
	}
	gap = int64(20 * time.Millisecond)
	if got, known := costs.cost(batch, 50, &gap); got != 9 || !known {
		t.Fatalf("reused caller scratch pointer kept old gap: %g", got)
	}
	var overflow serviceCosts
	for range 2 {
		for percentile := 1; percentile <= 20; percentile++ {
			if _, known := overflow.cost(batch, float64(percentile), nil); known {
				t.Fatalf("overflow percentile %d answered without an anchor", percentile)
			}
		}
	}
	if overflow.count != 16 || len(overflow.extra) != 4 {
		t.Fatalf("overflow queries were lost or repeated: local=%d extra=%d", overflow.count, len(overflow.extra))
	}
}

func TestBatchServiceReuseRetainsEachRequestDeadline(t *testing.T) {
	p := testPolicy()
	// Batch 2 at gap 0: the p50 window is its first record, the p70 window
	// all four, whose 70th percentile is 80 ms.
	p.Batches[0] = testBatch(1, 2, pairedSample("a", 0, 2))
	p.Batches[1] = testBatch(2, 1, pairedSample("a", 0, 2), pairedSample("a", 0, 80), pairedSample("a", 0, 80), pairedSample("a", 0, 80))
	zero := int64(0)
	requests := []RequestState{testState("a", 50, 0), testState("a", 100, 0)}
	requests[0].Context.SenderElapsedMS, requests[0].ElapsedMS = 30, 10
	requests[1].Context.SenderElapsedMS, requests[1].ElapsedMS = 70, 10
	for i := range requests {
		requests[i].Context.SLO.Percentile = 50
	}
	for _, tc := range []struct {
		margin, percentile float64
		size               int
	}{{19, 50, 1}, {18, 50, 2}, {18, 70, 1}, {18, 50, 2}} {
		requests[1].Context.CompletionMarginMS = tc.margin
		requests[1].Context.SLO.Percentile = tc.percentile
		choice := ChooseBatch(p, requests, &zero)
		if choice.Size != tc.size {
			t.Fatalf("individual margin=%g p=%g: %+v", tc.margin, tc.percentile, choice)
		}
	}
	var costs serviceCosts
	costs.cost(p.Batches[1], 50, &zero) // throughput and feasibility share this query
	var prefix []int
	if feasible, _ := batchFeasible(requests, 0, p.Batches[1], &zero, &costs, prefix); !feasible || costs.count != 1 {
		t.Fatalf("compatible requests repeated identical quantile: queries=%d", costs.count)
	}
}

func TestCompatibleChainStartsAtSelectedQueueHead(t *testing.T) {
	requests := []RequestState{testState("a", 40, 0), testState("b", 30, 0), testState("a", 20, 0)}
	head, _, _ := queueHeads(requests)
	if head != 2 || nextCompatible(requests, head, -1) != head || nextCompatible(requests, head, head) != 0 {
		t.Fatal("compatible normal chain lost the selected EDF head or its successor")
	}
	requests[0].Phase, requests[2].Phase = phaseDeferred, phaseDeferred
	_, deferred, _ := queueHeads(requests)
	if deferred != 0 || nextCompatible(requests, deferred, -1) != deferred || nextCompatible(requests, deferred, deferred) != 2 {
		t.Fatal("compatible deferred chain lost its FIFO head or successor")
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
