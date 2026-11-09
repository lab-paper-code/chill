package firstfloor

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"testing"
)

// baselineQuantileNanos preserves the 02b521a0 query for exact comparisons,
// including floating-point trial summation and the positive-mass p0 rule.
func baselineQuantileNanos(d *ServiceDistribution, gap *int64, percentile float64) (int64, ConditionalSupport, bool) {
	support := d.support(gap)
	if support.TrialCount == 0 || math.IsNaN(percentile) || percentile < 0 || percentile > 100 {
		return 0, support, false
	}
	index := sort.Search(len(d.services), func(i int) bool {
		mass := 0.0
		for j := range d.trials {
			a, b, _, _ := d.trials[j].nearest(gap)
			if n := len(a) + len(b); n > 0 {
				mass += float64(serviceUpperBound(a, d.services[i])+serviceUpperBound(b, d.services[i])) / float64(n)
			}
		}
		return mass > 0 && mass/float64(support.TrialCount) >= percentile/100
	})
	if index == len(d.services) {
		return 0, support, false
	}
	return d.services[index], support, true
}

// This file also runs unchanged on 02b521a0. Retained profiles are external
// evidence, never synthetic replacements for missing paired service samples.
func retainedServicePolicies(tb testing.TB) ([]string, []Policy) {
	tb.Helper()
	path := os.Getenv("CHILL_NUMERICAL_PROFILES")
	if path == "" {
		tb.Skip("set CHILL_NUMERICAL_PROFILES to retained-profile-samples.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		tb.Fatal(err)
	}
	var profiles map[string]struct {
		Batches []struct {
			Size    int             `json:"batch"`
			Energy  float64         `json:"energyPerInputJoules"`
			Samples []ServiceSample `json:"serviceSamples"`
		} `json:"batches"`
	}
	if err := json.Unmarshal(data, &profiles); err != nil {
		tb.Fatal(err)
	}
	ids := make([]string, 0, len(profiles))
	for id := range profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	policies := make([]Policy, len(ids))
	for i, id := range ids {
		for _, batch := range profiles[id].Batches {
			d, err := NewServiceDistribution(batch.Samples)
			if err != nil {
				tb.Fatalf("profile %s batch %d: %v", id, batch.Size, err)
			}
			policies[i].Batches = append(policies[i].Batches, BatchProfile{Size: batch.Size, EnergyPerItemJ: batch.Energy, ServiceSamples: batch.Samples, Distribution: d})
		}
		if len(policies[i].Batches) == 0 {
			tb.Fatalf("profile %s lacks paired batch evidence", id)
		}
	}
	if len(ids) == 0 {
		tb.Fatal("retained profile evidence is empty")
	}
	return ids, policies
}

func retainedServiceRequests(p Policy) []RequestState {
	count := 0
	for _, batch := range p.Batches {
		count = max(count, batch.Size)
	}
	requests := make([]RequestState, count)
	for i := range requests {
		requests[i] = testState("retained", 1e9, 0)
		requests[i].Context.RequestID = fmt.Sprint(i)
		requests[i].Context.SLO.Percentile = 99
	}
	return requests
}

func TestRetainedServiceSamples(t *testing.T) {
	ids, policies := retainedServicePolicies(t)
	for i, p := range policies {
		for _, batch := range p.Batches {
			var lo, hi int64
			known := false
			for _, sample := range batch.ServiceSamples {
				if sample.PrecedingIdleNanos != nil {
					g := *sample.PrecedingIdleNanos
					if !known {
						lo, hi, known = g, g, true
					}
					lo, hi = min(lo, g), max(hi, g)
				}
			}
			zero, middle := int64(0), lo+(hi-lo)/2
			gaps := []*int64{nil, &zero}
			if known {
				gaps = append(gaps, &lo, &middle, &hi)
			}
			for _, gap := range gaps {
				for _, percentile := range []float64{0, 50, 99, 100} {
					want, support, valid := baselineQuantileNanos(batch.Distribution, gap, percentile)
					got, gotSupport, ok := batch.Distribution.QuantileNanos(gap, percentile)
					if got != want || gotSupport != support || ok != valid {
						t.Fatalf("profile %s batch %d p%g: got %d %+v %v, want %d %+v %v", ids[i], batch.Size, percentile, got, gotSupport, ok, want, support, valid)
					}
				}
			}
		}
		requests := retainedServiceRequests(p)
		zero := int64(0)
		startup, busy := ChooseBatch(p, requests, nil), ChooseBatch(p, requests, &zero)
		t.Logf("profile=%s startup=(%d,%s,%g) busy=(%d,%s,%g)", ids[i], startup.Size, startup.Reason, startup.WaitMS, busy.Size, busy.Reason, busy.WaitMS)
	}
}

func BenchmarkRetainedServiceSamples(b *testing.B) {
	_, policies := retainedServicePolicies(b)
	for i, p := range policies {
		b.Run(fmt.Sprintf("profile%03d", i), func(b *testing.B) {
			gap := int64(0)
			for _, batch := range p.Batches {
				b.Run(fmt.Sprintf("quantile/batch%d", batch.Size), func(b *testing.B) {
					b.ReportAllocs()
					for range b.N {
						if _, _, ok := batch.Distribution.QuantileNanos(&gap, 99); !ok {
							b.Fatal("retained batch lacks known-gap support")
						}
					}
				})
			}
			requests := retainedServiceRequests(p)
			b.Run("choose", func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					if choice := ChooseBatch(p, requests, &gap); choice.Size <= 0 {
						b.Fatalf("retained batch not executable: %+v", choice)
					}
				}
			})
		})
	}
}
