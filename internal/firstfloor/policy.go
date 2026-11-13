package firstfloor

import (
	"math"
	"time"
)

type serviceCostQuery struct {
	distribution *ServiceDistribution
	percentile   float64
	gap          int64
	unanchored   bool
}
type serviceCostEntry struct {
	query serviceCostQuery
	cost  float64
}
type serviceCosts struct {
	quantiles *QuantileCache
	gapWidth  int64
	local     [16]serviceCostEntry
	count     int
	extra     []serviceCostEntry
}

// Costs live for one ChooseBatch call. Gap values, not caller scratch pointers,
// identify queries; every request still checks its own remaining deadline.
// ponytail: linear lookup fits few distinct queries; index if mixed-query scans dominate.
func (costs *serviceCosts) cost(b BatchProfile, percentile float64, gap *int64) float64 {
	query := serviceCostQuery{distribution: b.Distribution, percentile: percentile, unanchored: gap == nil}
	if gap != nil {
		query.gap = *gap
	}
	for _, entry := range costs.local[:costs.count] {
		if entry.query == query {
			return entry.cost
		}
	}
	for _, entry := range costs.extra {
		if entry.query == query {
			return entry.cost
		}
	}
	var value int64
	var known bool
	if gap != nil && costs.gapWidth > 0 {
		value, _, known = costs.quantiles.MaximumQuantileNanos(b.Distribution, *gap, *gap+costs.gapWidth, percentile)
	} else {
		value, _, known = costs.quantiles.QuantileNanos(b.Distribution, gap, percentile)
	}
	cost := math.Inf(1)
	if known {
		cost = float64(value) / float64(time.Millisecond)
	}
	entry := serviceCostEntry{query, cost}
	if costs.count < len(costs.local) {
		costs.local[costs.count] = entry
		costs.count++
	} else {
		costs.extra = append(costs.extra, entry)
	}
	return cost
}

// RemainingSLOMS uses frozen sender and native elapsed spans, never remote UTC.
// Caller cancellation and explicit lifetime are separate from the SLO.
func RemainingSLOMS(r RequestState) float64 {
	return r.Context.SLO.TargetMS - r.Context.SenderElapsedMS - r.ElapsedMS
}
func SLOExpired(r RequestState) bool { return RemainingSLOMS(r) <= 0 }
func requestDeadlinePolicy(r RequestState) string {
	switch r.Context.AdmissionPolicy {
	case AdmissionReject:
		return DeadlineDrop
	case AdmissionDelay:
		return DeadlineDefer
	default:
		return ""
	}
}
func requestSlack(r RequestState, b BatchProfile, gap *int64, costs *serviceCosts) float64 {
	return RemainingSLOMS(r) - r.Context.CompletionMarginMS - costs.cost(b, r.Context.SLO.Percentile, gap)
}
func allowedBatch(p Policy, size int) bool {
	return p.MaxBatchSize >= 0 && (p.MaxBatchSize == 0 || size <= p.MaxBatchSize)
}
func sameQueue(a, b RequestState) bool {
	return a.Context.WorkloadUID == b.Context.WorkloadUID && a.Context.Definition == b.Context.Definition
}
func minimumService(p Policy, q RequestState, gap *int64, costs *serviceCosts) float64 {
	cost := math.Inf(1)
	for _, b := range p.Batches {
		if allowedBatch(p, b.Size) {
			cost = math.Min(cost, costs.cost(b, q.Context.SLO.Percentile, gap))
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
	// queueHeads already found the first compatible EDF/FIFO member.
	if previous < 0 {
		return head
	}
	deferred := requests[head].Phase == phaseDeferred
	best := -1
	for i := range requests {
		q := &requests[i]
		if deferred {
			if i < head || i <= previous || q.Phase != phaseDeferred {
				continue
			}
			if !sameQueue(*q, requests[head]) {
				return -1
			}
			return i
		}
		if !normalRequest(*q) || !sameQueue(*q, requests[head]) || !earlierEDF(requests, previous, i) {
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
func batchFeasible(requests []RequestState, head int, b BatchProfile, gap *int64, costs *serviceCosts, prefix []int) (bool, []int) {
	previous := -1
	for i := range b.Size {
		if i == len(prefix) {
			prefix = append(prefix, nextCompatible(requests, head, previous))
		}
		previous = prefix[i]
		if previous < 0 || requestSlack(requests[previous], b, gap, costs) < 0 {
			return false, prefix
		}
	}
	return true, prefix
}

// ChooseBatch is shared by the actor and replay. Normal work uses stable EDF;
// deferred work uses FIFO only after normal work is empty. A lifecycle choice
// has Size=0 and identifies the single rejected/demoted RequestID. Running work
// is never preempted; only expired queued drops can be selected while active.
// ponytail: bounded O(requests*b + profiles*b) scan; index only if measured CPU warrants it.
func ChooseBatch(p Policy, requests []RequestState, gapDecision *int64) BatchChoice {
	return ChooseBatchWithQuantiles(p, requests, gapDecision, nil, nil)
}

// ChooseBatchWithQuantiles shares exact conditional queries with its caller's
// dispatch. The actor uses ChooseBatch and keeps its call-local cost storage.
func ChooseBatchWithQuantiles(p Policy, requests []RequestState, gapDecision, gapUpper *int64, quantiles *QuantileCache) BatchChoice {
	choice := chooseBatch(p, requests, gapDecision, gapUpper, quantiles)
	choice.DecisionIdleNanos = gapDecision
	choice.DecisionIdleApproximate = true
	return choice
}

func chooseBatch(p Policy, requests []RequestState, gapDecision, gapUpper *int64, quantiles *QuantileCache) BatchChoice {
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
	// Missing conditional support is not evidence of infeasibility. Native can
	// still execute a measured batch; gateway prediction remains unknown.
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
	available, throughput := chooseBatchProfiles(&p, requests, head, count, gapDecision, &costs)
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

func chooseBatchProfiles(p *Policy, requests []RequestState, head, count int, gapDecision *int64, costs *serviceCosts) (available, throughput BatchProfile) {
	r := &requests[head]
	perItemCost := math.Inf(1)
	// Each profile checks its own slack, but all share the same EDF prefix.
	// Extend lazily so an early deadline failure does not scan unused members.
	// ponytail: measured batches through 32 use stack storage; larger batches grow the slice.
	var local [32]int
	prefix := local[:0]
	for _, b := range p.Batches {
		if b.Size < 1 || b.Size > count || !allowedBatch(*p, b.Size) {
			continue
		}
		cost := costs.cost(b, r.Context.SLO.Percentile, gapDecision) / float64(b.Size)
		if throughput.Size == 0 || cost < perItemCost || cost == perItemCost && b.Size > throughput.Size {
			throughput, perItemCost = b, cost
		}
		feasible := r.Phase != phaseDeferred
		if feasible && !math.IsInf(cost, 1) {
			feasible, prefix = batchFeasible(requests, head, b, gapDecision, costs, prefix)
		}
		if feasible && (available.Size == 0 || b.EnergyPerItemJ < available.EnergyPerItemJ || b.EnergyPerItemJ == available.EnergyPerItemJ && b.Size > available.Size) {
			available = b
		}
	}
	return available, throughput
}
func queueHeads(requests []RequestState) (head, deferred int, active bool) {
	head, deferred = -1, -1
	for i := range requests {
		r := &requests[i]
		if r.Phase == phaseRunning {
			active = true
			continue
		}
		if normalRequest(*r) {
			if head < 0 || earlierEDF(requests, i, head) {
				head = i
			}
		} else if r.Phase == phaseDeferred && deferred < 0 {
			deferred = i
		}
	}
	return head, deferred, active
}
func energyGainWait(p Policy, requests []RequestState, head, count int, available BatchProfile, choice BatchChoice, gapDecision *int64, costs *serviceCosts) BatchChoice {
	r := requests[head]
	if r.ArrivalRate <= 0 {
		return choice
	}
	// Follow-up service starts after this projected batch, so its gap is
	// known zero even when the first batch's physical idle gap is bounded.
	following := costs
	var followCosts serviceCosts
	if costs.gapWidth > 0 {
		followCosts.quantiles = costs.quantiles
		following = &followCosts
	}
	for _, b := range p.Batches {
		if b.Size <= count || b.EnergyPerItemJ >= available.EnergyPerItemJ || !allowedBatch(p, b.Size) {
			continue
		}
		fill := float64(b.Size-count) / r.ArrivalRate * 1000
		// Filling extends idle before this service; a competing queue's follow-up
		// starts at age zero after this batch finishes, not at today's idle age.
		var futureGap *int64
		if gapDecision != nil {
			n := *gapDecision + int64(fill*float64(time.Millisecond))
			futureGap = &n
		}
		allowed := p.MaxBatchWaitMS
		for _, q := range requests {
			if !normalRequest(q) {
				continue
			}
			slack := requestSlack(q, b, futureGap, costs)
			if sameQueue(q, r) {
				allowed = math.Min(allowed, p.MaxBatchWaitMS-q.QueueMS)
			} else {
				zero := int64(0)
				slack -= minimumService(p, q, &zero, following)
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
