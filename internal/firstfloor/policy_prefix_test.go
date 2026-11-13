package firstfloor

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"reflect"
	"testing"
	"time"
)

// Preserve the original repeated-prefix selector as the choice/benchmark oracle.
func originalPrefixChooseBatch(p Policy, requests []RequestState, gapDecision, gapUpper *int64, quantiles *QuantileCache) BatchChoice {
	choice := originalPrefixChoose(p, requests, gapDecision, gapUpper, quantiles)
	choice.DecisionIdleNanos = gapDecision
	choice.DecisionIdleApproximate = true
	return choice
}

func originalPrefixChoose(p Policy, requests []RequestState, gapDecision, gapUpper *int64, quantiles *QuantileCache) BatchChoice {
	if p.FixedBatchSize > 0 {
		return chooseFixedBatch(p.FixedBatchSize, requests)
	}
	head, deferred, active := queueHeads(requests)
	mode := ""
	if head >= 0 {
		mode = requestDeadlinePolicy(requests[head])
		if mode == "" {
			return BatchChoice{Reason: "admission_unknown"}
		}
	}
	costs := serviceCosts{quantiles: quantiles}
	if gapUpper != nil {
		if gapDecision == nil || *gapDecision < 0 || *gapUpper < *gapDecision {
			return BatchChoice{Reason: "admission_unknown"}
		}
		costs.gapWidth = *gapUpper - *gapDecision
	}
	if mode == DeadlineDrop && head >= 0 && SLOExpired(requests[head]) && !math.IsInf(minimumService(p, requests[head], gapDecision, &costs), 1) {
		r := requests[head]
		return BatchChoice{RequestID: r.Context.RequestID, WorkloadUID: r.Context.WorkloadUID, Definition: r.Context.Definition, Reason: OutcomeDeadlineDrop}
	}
	if active {
		return BatchChoice{Reason: "active"}
	}
	if head < 0 {
		head = deferred
	}
	if head < 0 {
		return BatchChoice{Reason: "empty"}
	}
	r := requests[head]
	choice := BatchChoice{RequestID: r.Context.RequestID, WorkloadUID: r.Context.WorkloadUID, Definition: r.Context.Definition}
	count := compatibleCount(requests, head)
	available, throughput := originalPrefixProfiles(p, requests, head, count, gapDecision, &costs)
	if available.Size == 0 {
		if r.Phase != phaseDeferred {
			choice.Reason = OutcomeDeadlineDrop
			if mode == DeadlineDefer {
				choice.Reason = ReasonDeadlineDefer
			}
			return choice
		}
		choice.Size, choice.Reason = throughput.Size, ReasonMaximumThroughput
		return choice
	}
	choice.Size, choice.Reason = available.Size, ReasonAvailableEnergy
	if math.IsInf(costs.cost(available, r.Context.SLO.Percentile, gapDecision), 1) {
		choice.Reason = ReasonMissingConditionalSupport
		return choice
	}
	return energyGainWait(p, requests, head, count, available, choice, gapDecision, &costs)
}

func originalPrefixProfiles(p Policy, requests []RequestState, head, count int, gapDecision *int64, costs *serviceCosts) (available, throughput BatchProfile) {
	r := requests[head]
	perItemCost := math.Inf(1)
	for _, b := range p.Batches {
		if b.Size < 1 || b.Size > count || !allowedBatch(p, b.Size) {
			continue
		}
		cost := costs.cost(b, r.Context.SLO.Percentile, gapDecision) / float64(b.Size)
		if throughput.Size == 0 || cost < perItemCost || cost == perItemCost && b.Size > throughput.Size {
			throughput, perItemCost = b, cost
		}
		if r.Phase != phaseDeferred && (math.IsInf(cost, 1) || originalPrefixFeasible(requests, head, b, gapDecision, costs)) && (available.Size == 0 || b.EnergyPerItemJ < available.EnergyPerItemJ || b.EnergyPerItemJ == available.EnergyPerItemJ && b.Size > available.Size) {
			available = b
		}
	}
	return available, throughput
}

func originalPrefixFeasible(requests []RequestState, head int, b BatchProfile, gap *int64, costs *serviceCosts) bool {
	previous := -1
	for range b.Size {
		previous = nextCompatible(requests, head, previous)
		if previous < 0 || requestSlack(requests[previous], b, gap, costs) < 0 {
			return false
		}
	}
	return true
}

func TestChooseBatchPrefixEquivalence(t *testing.T) {
	rng := rand.New(rand.NewPCG(73, 19))
	p := testPolicy()
	p.Batches = nil
	// Unsorted, duplicate and >32 sizes exercise reuse, ties and stack overflow.
	for _, size := range []int{8, 1, 64, 2, 4, 2, 0, -1} {
		p.Batches = append(p.Batches, testBatch(size, float64(rng.IntN(3)), startupSample("a", 2), startupSample("a", 20), pairedSample("a", 0, 3), pairedSample("a", 20, 30)))
	}
	gap, upper := int64(0), int64(20*time.Millisecond)
	check := func(label string, policy Policy, requests []RequestState, idle, bound *int64) {
		t.Helper()
		before := append([]RequestState(nil), requests...)
		var oldCache, newCache QuantileCache
		want := originalPrefixChooseBatch(policy, requests, idle, bound, &oldCache)
		got := ChooseBatchWithQuantiles(policy, requests, idle, bound, &newCache)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: got %+v, want %+v", label, got, want)
		}
		if len(requests) > 0 && !reflect.DeepEqual(requests, before) {
			t.Fatalf("%s: changed caller snapshot", label)
		}
	}
	for _, count := range []int{0, 1, 2, 8, 33, 64, 80} {
		requests := make([]RequestState, count)
		for i := range requests {
			requests[i] = testState("a", 100, 0)
			requests[i].Context.RequestID = fmt.Sprint(i)
		}
		for _, fixed := range []int{0, 2} {
			for _, phase := range []string{phaseQueued, phaseDeferred} {
				for i := range requests {
					requests[i].Phase = phase
				}
				policy := p
				policy.FixedBatchSize = fixed
				check(fmt.Sprintf("count%d/fixed%d/%s", count, fixed, phase), policy, requests, &gap, nil)
			}
		}
	}
	for trial := range 1000 {
		policy := p
		policy.MaxBatchSize = []int{-1, 0, 1, 4, 64}[rng.IntN(5)]
		if trial%7 == 0 {
			policy.FixedBatchSize = 2
		}
		policy.Batches = append([]BatchProfile(nil), p.Batches...)
		rng.Shuffle(len(policy.Batches), func(i, j int) { policy.Batches[i], policy.Batches[j] = policy.Batches[j], policy.Batches[i] })
		requests := make([]RequestState, rng.IntN(81))
		for i := range requests {
			q := testState(fmt.Sprint(rng.IntN(3)), float64(rng.IntN(12)*10), float64(rng.IntN(3)*100))
			q.Context.RequestID = fmt.Sprint(i)
			q.Context.Definition = fmt.Sprint(rng.IntN(2))
			q.Context.AdmissionPolicy = []string{AdmissionReject, AdmissionDelay, "unknown"}[rng.IntN(3)]
			q.Context.SLO.Percentile = []float64{0, 50, 95, 100}[rng.IntN(4)]
			q.Context.SenderElapsedMS = float64(rng.IntN(20))
			q.ElapsedMS = float64(rng.IntN(20))
			q.QueueMS = float64(rng.IntN(100))
			q.Context.CompletionMarginMS = float64(rng.IntN(30))
			q.Phase = []string{phaseQueued, phaseQueued, phaseQueued, phaseDeferred, phaseFinished}[rng.IntN(5)]
			if trial%13 == 0 && i == 0 {
				q.Phase = phaseRunning
			}
			requests[i] = q
		}
		idle, bound := &gap, (*int64)(nil)
		switch trial % 4 {
		case 0:
			idle = nil
		case 1:
			bound = &upper
		case 2:
			idle, bound = &upper, &gap // invalid bounded gap stays unknown
		}
		check(fmt.Sprint(trial), policy, requests, idle, bound)
	}
}

func TestChooseBatchPrefixStaysOnStack(t *testing.T) {
	p := testPolicy()
	requests := make([]RequestState, 32)
	for i := range requests {
		requests[i] = testState("a", 1e9, 0)
	}
	var quantiles QuantileCache
	gap := int64(0)
	if allocations := testing.AllocsPerRun(100, func() {
		ChooseBatchWithQuantiles(p, requests, &gap, nil, &quantiles)
	}); allocations != 0 {
		t.Fatalf("measured batch prefix allocated %g objects per choice", allocations)
	}
}

func BenchmarkChooseBatchPrefix(b *testing.B) {
	path := os.Getenv("CHILL_PREFIX_PROFILES")
	if path == "" {
		b.Skip("set CHILL_PREFIX_PROFILES to frozen stored-profiles.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	var stored struct {
		Profiles []struct {
			Batches []struct {
				Size    int             `json:"batch"`
				Energy  float64         `json:"energyPerInputJoules"`
				Samples []ServiceSample `json:"serviceSamples"`
			} `json:"batches"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		b.Fatal(err)
	}
	policies := make([]Policy, len(stored.Profiles))
	for i, profile := range stored.Profiles {
		for _, batch := range profile.Batches {
			distribution, err := NewServiceDistribution(batch.Samples)
			if err != nil {
				b.Fatal(err)
			}
			policies[i].Batches = append(policies[i].Batches, BatchProfile{Size: batch.Size, EnergyPerItemJ: batch.Energy, Distribution: distribution})
		}
	}
	if len(policies) == 0 {
		b.Fatal("frozen profiles are empty")
	}
	for _, count := range []int{32, 64} {
		requests := make([][]RequestState, len(policies))
		for i, p := range policies {
			requests[i] = retainedServiceRequests(p)
			for len(requests[i]) < count {
				requests[i] = append(requests[i], testState("retained", 1e9, 0))
			}
		}
		for _, original := range []bool{true, false} {
			name := "optimized"
			if original {
				name = "original"
			}
			b.Run(fmt.Sprintf("requests%d/%s", count, name), func(b *testing.B) {
				var quantiles QuantileCache
				gap := int64(0)
				b.ReportAllocs()
				for range b.N {
					for i, p := range policies {
						var choice BatchChoice
						if original {
							choice = originalPrefixChooseBatch(p, requests[i], &gap, nil, &quantiles)
						} else {
							choice = ChooseBatchWithQuantiles(p, requests[i], &gap, nil, &quantiles)
						}
						if choice.Size <= 0 {
							b.Fatalf("retained profile is not executable: %+v", choice)
						}
					}
				}
			})
		}
	}
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
