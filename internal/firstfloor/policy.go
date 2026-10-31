package firstfloor

import "math"

func serviceCost(b BatchProfile, percentile float64) float64 {
	if len(b.ServiceSamplesMS) == 0 {
		return math.Inf(1)
	}
	// stat.Quantile Empirical on sorted NaN-free samples (validatePolicy and
	// the replay sort them): the first i with i+1 >= p*n.
	x := b.ServiceSamplesMS
	return x[max(0, int(math.Ceil(percentile/100*float64(len(x))))-1)]
}

// RemainingSLOMS uses frozen sender and native elapsed spans, never remote UTC.
// Caller cancellation and explicit lifetime are separate from the SLO.
func RemainingSLOMS(r RequestState) float64 {
	return r.Context.SLO.TargetMS - r.Context.SenderElapsedMS - r.ElapsedMS
}
func SLOExpired(r RequestState) bool { return RemainingSLOMS(r) <= 0 }
func requestSlack(r RequestState, b BatchProfile) float64 {
	return RemainingSLOMS(r) - r.Context.CompletionMarginMS - serviceCost(b, r.Context.SLO.Percentile)
}
func allowedBatch(p Policy, size int) bool {
	return p.MaxBatchSize >= 0 && (p.MaxBatchSize == 0 || size <= p.MaxBatchSize)
}
func sameQueue(a, b RequestState) bool {
	return a.Context.WorkloadUID == b.Context.WorkloadUID && a.Context.Definition == b.Context.Definition
}
func minimumService(p Policy, q RequestState) float64 {
	cost := math.Inf(1)
	for _, b := range p.Batches {
		if allowedBatch(p, b.Size) {
			cost = math.Min(cost, serviceCost(b, q.Context.SLO.Percentile))
		}
	}
	return cost
}
func normalRequest(q RequestState) bool {
	return q.Phase != phaseRunning && q.Phase != phaseDeferred && q.Phase != phaseFinished
}
func earlierEDF(requests []RequestState, i, j int) bool {
	a, b := RemainingSLOMS(requests[i]), RemainingSLOMS(requests[j])
	return a < b || a == b && i < j
}

// nextCompatible walks normal requests in stable EDF without copying or sorting
// the caller's snapshot. Deferred work instead uses its contiguous FIFO prefix.
func nextCompatible(requests []RequestState, head, previous int) int {
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

// compatibleCount is the length of head's nextCompatible chain: every normal
// request of head's queue, or head's contiguous deferred FIFO run.
func compatibleCount(requests []RequestState, head int) int {
	r := &requests[head]
	count := 0
	for i := range requests {
		q := &requests[i]
		if r.Phase == phaseDeferred {
			if i < head || q.Phase != phaseDeferred {
				continue
			}
			if !sameQueue(*q, *r) {
				break
			}
		} else if !normalRequest(*q) || !sameQueue(*q, *r) {
			continue
		}
		count++
	}
	return count
}
func batchFeasible(requests []RequestState, head int, b BatchProfile) bool {
	previous := -1
	for range b.Size {
		previous = nextCompatible(requests, head, previous)
		if previous < 0 || requestSlack(requests[previous], b) < 0 {
			return false
		}
	}
	return true
}

// ChooseBatch is shared by the actor and replay. Normal work uses stable EDF;
// deferred work uses FIFO only after normal work is empty. A lifecycle choice
// has Size=0 and identifies the single rejected/demoted RequestID. Running work
// is never preempted; only expired queued drops can be selected while active.
// ponytail: bounded O(requests*profiles*b) scan; index only if measured CPU warrants it.
func ChooseBatch(p Policy, requests []RequestState) BatchChoice {
	if p.FixedBatchSize > 0 {
		return chooseFixedBatch(p.FixedBatchSize, requests)
	}
	head, deferred, active := queueHeads(requests)
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
	count := compatibleCount(requests, head)
	var available, throughput BatchProfile
	perItemCost := math.Inf(1)
	for _, b := range p.Batches {
		if b.Size < 1 || b.Size > count || !allowedBatch(p, b.Size) {
			continue
		}
		cost := serviceCost(b, r.Context.SLO.Percentile) / float64(b.Size)
		if throughput.Size == 0 || cost < perItemCost || cost == perItemCost && b.Size > throughput.Size {
			throughput, perItemCost = b, cost
		}
		if r.Phase != phaseDeferred && batchFeasible(requests, head, b) && (available.Size == 0 || b.EnergyPerItemJ < available.EnergyPerItemJ || b.EnergyPerItemJ == available.EnergyPerItemJ && b.Size > available.Size) {
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
		choice.Size, choice.Reason = throughput.Size, "maximum_throughput"
		return choice
	}
	choice.Size, choice.Reason = available.Size, "available_energy"
	return energyGainWait(p, requests, head, count, available, choice)
}
func queueHeads(requests []RequestState) (head, deferred int, active bool) {
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
func energyGainWait(p Policy, requests []RequestState, head, count int, available BatchProfile, choice BatchChoice) BatchChoice {
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
			slack := requestSlack(q, b)
			if sameQueue(q, r) {
				allowed = math.Min(allowed, p.MaxBatchWaitMS-q.QueueMS)
			} else {
				slack -= minimumService(p, q)
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
// group that has size queued; SLO, margin, energy and deadlinePolicy are unused.
func chooseFixedBatch(size int, requests []RequestState) BatchChoice {
	best := -1
	for i, r := range requests {
		if r.Phase == phaseRunning {
			return BatchChoice{Reason: "active"}
		}
		if !normalRequest(r) || best >= 0 && !earlierEDF(requests, i, best) {
			continue
		}
		if compatibleCount(requests, i) >= size {
			best = i
		}
	}
	if best < 0 {
		return BatchChoice{Reason: "fixed_batch_filling"}
	}
	r := requests[best]
	return BatchChoice{RequestID: r.Context.RequestID, WorkloadUID: r.Context.WorkloadUID, Definition: r.Context.Definition, Size: size, Reason: "fixed_batch"}
}
