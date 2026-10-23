package firstfloor

import (
	"math"
	"math/rand/v2"
	"reflect"
	"sort"
	"strconv"
	"testing"

	"gonum.org/v1/gonum/stat"
)

// TestChooseBatchMatchesReference compares ChooseBatch with the policy as
// deployed in the runtime image (gonum Quantile, nextCompatible count chain).
func TestChooseBatchMatchesReference(t *testing.T) {
	cases := 300000
	if testing.Short() {
		cases = 20000
	}
	rng := rand.New(rand.NewPCG(1, 2))
	phases := []string{"", phaseQueued, phaseQueued, phaseDeferred, phaseRunning, phaseFinished}
	deferredOnly := []string{phaseDeferred, phaseDeferred, phaseFinished}
	percentiles := []float64{0, 1, 50, 90, 95, 99, 100}
	for c := range cases {
		p := Policy{MaxBatchWaitMS: float64(rng.IntN(50)), DeadlinePolicy: []string{"", DeadlineDrop, DeadlineKeep, DeadlineDefer}[rng.IntN(4)], MaxBatchSize: rng.IntN(5)}
		if rng.IntN(8) == 0 {
			p.FixedBatchSize = 1 + rng.IntN(4)
		}
		for size := 1; size <= 4; size++ {
			if size > 1 && rng.IntN(3) == 0 {
				continue
			}
			samples := make([]float64, 1+rng.IntN(40))
			for i := range samples {
				samples[i] = float64(1 + rng.IntN(20*size))
			}
			sort.Float64s(samples)
			p.Batches = append(p.Batches, BatchProfile{Size: size, ServiceSamplesMS: samples, EnergyPerItemJ: float64(rng.IntN(4))})
		}
		requests, phases := make([]RequestState, rng.IntN(12)), phases
		if rng.IntN(4) == 0 {
			phases = deferredOnly
		}
		for i := range requests {
			w := strconv.Itoa(rng.IntN(3))
			requests[i] = RequestState{Context: RequestContext{RequestID: "r" + strconv.Itoa(i), WorkloadUID: w, Definition: w + "/" + strconv.Itoa(rng.IntN(2)), SenderElapsedMS: float64(rng.IntN(20)), CompletionMarginMS: float64(rng.IntN(5)), SLO: SLO{Percentile: percentiles[rng.IntN(len(percentiles))], TargetMS: float64(rng.IntN(150))}}, Phase: phases[rng.IntN(len(phases))], ElapsedMS: float64(rng.IntN(60)), QueueMS: float64(rng.IntN(60)), ArrivalRate: float64(rng.IntN(3) * 50)}
		}
		if got, want := ChooseBatch(p, requests), refChooseBatch(p, requests); !reflect.DeepEqual(got, want) {
			t.Fatalf("case %d: got %+v, want %+v", c, got, want)
		}
	}
}

func refServiceCost(b BatchProfile, percentile float64) float64 {
	if len(b.ServiceSamplesMS) == 0 {
		return math.Inf(1)
	}
	return stat.Quantile(percentile/100, stat.Empirical, b.ServiceSamplesMS, nil)
}

func refRequestSlack(r RequestState, b BatchProfile) float64 {
	return RemainingSLOMS(r) - r.Context.CompletionMarginMS - refServiceCost(b, r.Context.SLO.Percentile)
}
func refMinimumService(p Policy, q RequestState) float64 {
	cost := math.Inf(1)
	for _, b := range p.Batches {
		if allowedBatch(p, b.Size) {
			cost = math.Min(cost, refServiceCost(b, q.Context.SLO.Percentile))
		}
	}
	return cost
}

// nextCompatible walks normal requests in stable EDF without copying or sorting
func refNextCompatible(requests []RequestState, head, previous int) int {
	deferred := requests[head].Phase == phaseDeferred
	best := -1
	for i, q := range requests {
		if deferred {
			if i < head || i <= previous || q.Phase != phaseDeferred {
				continue
			}
			if !sameQueue(q, requests[head]) {
				return -1
			}
			return i
		}
		if !normalRequest(q) || !sameQueue(q, requests[head]) || previous >= 0 && !earlierEDF(requests, previous, i) {
			continue
		}
		if best < 0 || earlierEDF(requests, i, best) {
			best = i
		}
	}
	return best
}
func refBatchFeasible(requests []RequestState, head int, b BatchProfile) bool {
	previous := -1
	for range b.Size {
		previous = refNextCompatible(requests, head, previous)
		if previous < 0 || refRequestSlack(requests[previous], b) < 0 {
			return false
		}
	}
	return true
}

// ChooseBatch is shared by the actor and replay. Normal work uses stable EDF;
// deferred work uses FIFO only after normal work is empty. A lifecycle choice
// has Size=0 and identifies the single rejected/demoted RequestID. Running work
// is never preempted; only expired queued drops can be selected while active.
func refChooseBatch(p Policy, requests []RequestState) BatchChoice {
	if p.FixedBatchSize > 0 {
		return refChooseFixedBatch(p.FixedBatchSize, requests)
	}
	head, deferred, active := refQueueHeads(requests)
	mode := EffectiveDeadlinePolicy(p.DeadlinePolicy)
	if mode == DeadlineDrop && head >= 0 && SLOExpired(requests[head]) {
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
	count := 0
	for previous := -1; ; {
		previous = refNextCompatible(requests, head, previous)
		if previous < 0 {
			break
		}
		count++
	}
	var available, fastest BatchProfile
	fastestCost := math.Inf(1)
	for _, b := range p.Batches {
		if b.Size < 1 || b.Size > count || !allowedBatch(p, b.Size) {
			continue
		}
		cost := refServiceCost(b, r.Context.SLO.Percentile)
		if fastest.Size == 0 || cost < fastestCost || cost == fastestCost && b.Size > fastest.Size {
			fastest, fastestCost = b, cost
		}
		if r.Phase != phaseDeferred && refBatchFeasible(requests, head, b) && (available.Size == 0 || b.EnergyPerItemJ < available.EnergyPerItemJ || b.EnergyPerItemJ == available.EnergyPerItemJ && b.Size > available.Size) {
			available = b
		}
	}
	if available.Size == 0 {
		if r.Phase != phaseDeferred && mode != DeadlineKeep {
			choice.Reason = OutcomeDeadlineDrop
			if mode == DeadlineDefer {
				choice.Reason = ReasonDeadlineDefer
			}
			return choice
		}
		choice.Size, choice.Reason = fastest.Size, "minimum_service"
		return choice
	}
	choice.Size, choice.Reason = available.Size, "available_energy"
	return refEnergyGainWait(p, requests, head, count, available, choice)
}
func refQueueHeads(requests []RequestState) (head, deferred int, active bool) {
	head, deferred = -1, -1
	for i, r := range requests {
		if r.Phase == phaseRunning {
			active = true
			continue
		}
		if normalRequest(r) {
			if head < 0 || earlierEDF(requests, i, head) {
				head = i
			}
		} else if r.Phase == phaseDeferred && deferred < 0 {
			deferred = i
		}
	}
	return head, deferred, active
}
func refEnergyGainWait(p Policy, requests []RequestState, head, count int, available BatchProfile, choice BatchChoice) BatchChoice {
	r := requests[head]
	if r.ArrivalRate <= 0 {
		return choice
	}
	for _, b := range p.Batches {
		if b.Size <= count || b.EnergyPerItemJ >= available.EnergyPerItemJ || !allowedBatch(p, b.Size) {
			continue
		}
		fill := float64(b.Size-count) / r.ArrivalRate * 1000
		allowed := p.MaxBatchWaitMS
		for _, q := range requests {
			if !normalRequest(q) {
				continue
			}
			slack := refRequestSlack(q, b)
			if sameQueue(q, r) {
				allowed = math.Min(allowed, p.MaxBatchWaitMS-q.QueueMS)
			} else {
				slack -= refMinimumService(p, q)
			}
			allowed = math.Min(allowed, slack)
		}
		if fill > 0 && fill <= allowed {
			choice.Size = 0
			choice.WaitMS = fill
			choice.Reason = "measured_energy_gain_fill"
			return choice
		}
	}
	return choice
}

// chooseFixedBatch serves exactly size requests of the earliest EDF compatible
func refChooseFixedBatch(size int, requests []RequestState) BatchChoice {
	best := -1
	for i, r := range requests {
		if r.Phase == phaseRunning {
			return BatchChoice{Reason: "active"}
		}
		if !normalRequest(r) || best >= 0 && !earlierEDF(requests, i, best) {
			continue
		}
		count := 0
		for _, q := range requests {
			if normalRequest(q) && sameQueue(q, r) {
				count++
			}
		}
		if count >= size {
			best = i
		}
	}
	if best < 0 {
		return BatchChoice{Reason: "fixed_batch_filling"}
	}
	r := requests[best]
	return BatchChoice{RequestID: r.Context.RequestID, WorkloadUID: r.Context.WorkloadUID, Definition: r.Context.Definition, Size: size, Reason: "fixed_batch"}
}
