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
	known bool
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
// known=false is an unidentified quantile: callers decide explicitly what an
// unknown cost means; it never enters arithmetic.
// ponytail: linear lookup fits few distinct queries; index if mixed-query scans dominate.
func (costs *serviceCosts) cost(b BatchProfile, percentile float64, gap *int64) (float64, bool) {
	query := serviceCostQuery{distribution: b.Distribution, percentile: percentile, unanchored: gap == nil}
	if gap != nil {
		query.gap = *gap
	}
	for _, entry := range costs.local[:costs.count] {
		if entry.query == query {
			return entry.cost, entry.known
		}
	}
	for _, entry := range costs.extra {
		if entry.query == query {
			return entry.cost, entry.known
		}
	}
	var value int64
	var known bool
	if gap != nil && costs.gapWidth > 0 {
		value, _, known = costs.quantiles.MaximumQuantileNanos(b.Distribution, *gap, *gap+costs.gapWidth, percentile)
	} else {
		value, _, known = costs.quantiles.QuantileNanos(b.Distribution, gap, percentile)
	}
	entry := serviceCostEntry{query: query, known: known}
	if known {
		entry.cost = float64(value) / float64(time.Millisecond)
	}
	if costs.count < len(costs.local) {
		costs.local[costs.count] = entry
		costs.count++
	} else {
		costs.extra = append(costs.extra, entry)
	}
	return entry.cost, entry.known
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
// requestSlack is unknown when the batch's service quantile is not identified.
func requestSlack(r RequestState, b BatchProfile, gap *int64, costs *serviceCosts) (float64, bool) {
	cost, known := costs.cost(b, r.Context.SLO.Percentile, gap)
	return RemainingSLOMS(r) - r.Context.CompletionMarginMS - cost, known
}
func allowedBatch(p Policy, size int) bool {
	return p.MaxBatchSize >= 0 && (p.MaxBatchSize == 0 || size <= p.MaxBatchSize)
}
func sameQueue(a, b RequestState) bool {
	return a.Context.WorkloadUID == b.Context.WorkloadUID && a.Context.Definition == b.Context.Definition
}
// minimumService is the least identified service quantile over allowed
// batches; known=false when no allowed batch's quantile is identified.
func minimumService(p Policy, q RequestState, gap *int64, costs *serviceCosts) (minimum float64, known bool) {
	for _, b := range p.Batches {
		if !allowedBatch(p, b.Size) {
			continue
		}
		if cost, ok := costs.cost(b, q.Context.SLO.Percentile, gap); ok && (!known || cost < minimum) {
			minimum, known = cost, true
		}
	}
	return minimum, known
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
		if previous < 0 {
			return false, prefix
		}
		// A member whose own quantile is unidentified cannot be shown to fit.
		if slack, known := requestSlack(requests[previous], b, gap, costs); !known || slack < 0 {
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
	if mode == DeadlineDrop && head >= 0 && SLOExpired(requests[head]) && serviceIdentified(p, requests[head], gapDecision, &costs) {
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
	if _, known := costs.cost(available, r.Context.SLO.Percentile, gapDecision); !known {
		choice.Reason = ReasonMissingConditionalSupport
		return choice
	}
	return energyGainWait(p, requests, head, count, available, choice, gapDecision, &costs)
}

// serviceIdentified reports whether any allowed batch's quantile is identified.
func serviceIdentified(p Policy, q RequestState, gap *int64, costs *serviceCosts) bool {
	_, known := minimumService(p, q, gap, costs)
	return known
}

func chooseBatchProfiles(p *Policy, requests []RequestState, head, count int, gapDecision *int64, costs *serviceCosts) (available, throughput BatchProfile) {
	r := &requests[head]
	var perItemCost float64
	throughputKnown := false
	// Each profile checks its own slack, but all share the same EDF prefix.
	// Extend lazily so an early deadline failure does not scan unused members.
	// ponytail: measured batches through 32 use stack storage; larger batches grow the slice.
	var local [32]int
	prefix := local[:0]
	for _, b := range p.Batches {
		if b.Size < 1 || b.Size > count || !allowedBatch(*p, b.Size) {
			continue
		}
		cost, known := costs.cost(b, r.Context.SLO.Percentile, gapDecision)
		cost /= float64(b.Size)
		// Identified per-item cost ranks first; among equal (or all unknown)
		// costs the larger batch wins.
		better := known && (!throughputKnown || cost < perItemCost)
		tied := known == throughputKnown && (!known || cost == perItemCost) && b.Size > throughput.Size
		if throughput.Size == 0 || better || tied {
			throughput, perItemCost, throughputKnown = b, cost, known
		}
		// Missing conditional support is not evidence of infeasibility.
		feasible := r.Phase != phaseDeferred
		if feasible && known {
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
batches:
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
			// An unidentified deadline cannot justify waiting for this batch.
			slack, known := requestSlack(q, b, futureGap, costs)
			if !known {
				continue batches
			}
			if sameQueue(q, r) {
				allowed = math.Min(allowed, p.MaxBatchWaitMS-q.QueueMS)
			} else {
				zero := int64(0)
				service, known := minimumService(p, q, &zero, following)
				if !known {
					continue batches
				}
				slack -= service
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
