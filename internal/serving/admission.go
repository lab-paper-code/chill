package serving

import (
	"math"
	"slices"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

// AdmissionInput is an executor snapshot plus assignments not yet present in
// that snapshot. Observed spans are measured at the snapshot; assignment and
// candidate spans are measured at the decision. Nil idle age means startup.
// The caller supplies the immutable measured distributions in Policy.Batches.
type AdmissionInput struct {
	Policy                 firstfloor.Policy
	Observed               []firstfloor.RequestState
	Assignments            []firstfloor.RequestState
	SnapshotAge            time.Duration
	LastServiceEndAgeNanos *int64
	Candidate              firstfloor.RequestState
}

// AdmissionEstimate is a deterministic decision rule made by chaining measured
// stage quantiles. It is not an end-to-end percentile guarantee. Completion is
// relative to the decision and includes the candidate's frozen POST reserve.
// FixedBatch preserves the strict-baseline exception without making a judgment.
type AdmissionEstimate struct {
	Completion     time.Duration
	OutsideSupport bool
	Known          bool
	FixedBatch     bool
}

type admissionScratch struct {
	states    []firstfloor.RequestState
	lifetimes []float64
	rates     map[string]float64
}

// Executor-level failures do not depend on a candidate's percentile. Check
// these once per dispatch, including whether any running service can survive.
func admissionExecutorKnown(in AdmissionInput) bool {
	p := in.Policy
	if p.FixedBatchSize > 0 {
		return true
	}
	if in.SnapshotAge < 0 || p.MaxBatchSize < 0 || p.MaxBatchWaitMS < 0 || math.IsNaN(p.MaxBatchWaitMS) || math.IsInf(p.MaxBatchWaitMS, 0) || len(p.Batches) == 0 || in.LastServiceEndAgeNanos != nil && *in.LastServiceEndAgeNanos < 0 {
		return false
	}
	for _, b := range p.Batches {
		if b.Size > 0 && (p.MaxBatchSize == 0 || b.Size <= p.MaxBatchSize) && b.Distribution == nil {
			return false
		}
	}
	var running uint64
	for _, q := range in.Observed {
		if q.Phase == phaseFinished || q.Phase == phaseReturning || admissionLifetimeExpired(q) {
			continue
		}
		if !admissionRequestKnown(q) {
			return false
		}
		if q.Phase != phaseRunning {
			continue
		}
		if q.BatchID == 0 || q.BatchSize <= 0 || q.ExchangeElapsedNanos == nil || running != 0 && running != q.BatchID {
			return false
		}
		if running != 0 {
			continue
		}
		b := admissionBatch(p, q.BatchSize)
		if b == nil {
			return false
		}
		if _, _, ok := b.Distribution.ResidualQuantileNanos(q.PrecedingIdleNanos, *q.ExchangeElapsedNanos, 100); !ok {
			return false
		}
		running = q.BatchID
	}
	if running == 0 {
		supported := false
		for _, b := range p.Batches {
			if b.Size < 1 || p.MaxBatchSize > 0 && b.Size > p.MaxBatchSize {
				continue
			}
			if _, _, ok := b.Distribution.QuantileNanos(in.LastServiceEndAgeNanos, 100); ok {
				supported = true
				break
			}
		}
		if !supported {
			return false
		}
	}
	for _, q := range in.Assignments {
		if !admissionLifetimeExpired(q) && !admissionRequestKnown(q) {
			return false
		}
	}
	return true
}

// EstimateAdmission advances the shared native batching policy, including EDF,
// deferred FIFO, fill wait, and survival-conditional running batch residuals.
// Missing state or empirical support leaves admission unknown, never unchecked.
func EstimateAdmission(in AdmissionInput) AdmissionEstimate {
	if !admissionExecutorKnown(in) {
		return AdmissionEstimate{}
	}
	return projectAdmission(in, &admissionScratch{})
}

// projectAdmission owns reusable projection storage; immutable native snapshots
// and their lifetime pointers are never modified. Runtime validates the common
// executor inputs once, before projecting any candidates.
func projectAdmission(in AdmissionInput, scratch *admissionScratch) AdmissionEstimate {
	if in.Policy.FixedBatchSize > 0 {
		return AdmissionEstimate{FixedBatch: true}
	}
	unknown := AdmissionEstimate{}
	if !admissionRequestKnown(in.Candidate) {
		return unknown
	}
	size := len(in.Observed) + len(in.Assignments) + 1
	if cap(scratch.states) < size {
		scratch.states = make([]firstfloor.RequestState, 0, max(size, 2*cap(scratch.states)))
	}
	if cap(scratch.lifetimes) < size {
		scratch.lifetimes = make([]float64, max(size, 2*cap(scratch.lifetimes)))
	}
	scratch.lifetimes = scratch.lifetimes[:size]
	if scratch.rates == nil {
		scratch.rates = make(map[string]float64)
	}
	clear(scratch.rates)
	defer clear(scratch.rates)
	defer clear(scratch.states[:cap(scratch.states)])
	states := scratch.states[:0]
	lifetime := 0
	appendState := func(q firstfloor.RequestState) {
		if q.RemainingLifetimeMS != nil {
			scratch.lifetimes[lifetime] = *q.RemainingLifetimeMS
			q.RemainingLifetimeMS = &scratch.lifetimes[lifetime]
			lifetime++
		}
		states = append(states, q)
	}
	for _, q := range in.Observed {
		if q.Phase != phaseFinished && q.Phase != phaseReturning && !admissionLifetimeExpired(q) {
			appendState(q)
		}
	}
	outsideSupport := false
	var idle *int64
	if in.LastServiceEndAgeNanos != nil {
		n := *in.LastServiceEndAgeNanos
		if n < 0 {
			return unknown
		}
		idle = &n
	}
	// Running batches are one physical exchange even when each member has a row.
	var activeBatch uint64
	var activeRemaining int64
	for _, q := range states {
		if q.Phase != phaseRunning {
			continue
		}
		if q.BatchID == 0 || q.BatchSize <= 0 || q.ExchangeElapsedNanos == nil || (activeBatch != 0 && activeBatch != q.BatchID) {
			return unknown
		}
		if activeBatch != 0 {
			continue
		}
		b := admissionBatch(in.Policy, q.BatchSize)
		if b == nil {
			return unknown
		}
		n, support, ok := b.Distribution.ResidualQuantileNanos(q.PrecedingIdleNanos, *q.ExchangeElapsedNanos, in.Candidate.Context.SLO.Percentile)
		if !ok {
			return unknown
		}
		outsideSupport = outsideSupport || support.OutsideSupport
		activeBatch, activeRemaining = q.BatchID, n
	}
	age := int64(in.SnapshotAge)
	clock := int64(0)
	inserted := false
	candidateID := in.Candidate.Context.RequestID
	// Each request can leave normal once and leave the set once. A fill wake
	// consumes its queue's remaining fill budget. No open-ended dispatch scan.
	limit := 8*(len(states)+len(in.Assignments)+1) + 16
	for range limit {
		if !inserted && clock >= age {
			for _, q := range in.Assignments {
				q.Phase = phaseQueued
				appendState(q)
				scratch.rates[q.Context.WorkloadUID] = q.ArrivalRate
			}
			appendState(in.Candidate)
			scratch.rates[in.Candidate.Context.WorkloadUID] = in.Candidate.ArrivalRate
			for j := range states {
				if rate, ok := scratch.rates[states[j].Context.WorkloadUID]; ok {
					states[j].ArrivalRate = rate
				}
			}
			inserted = true
		}
		candidateExpired := false
		states = slices.DeleteFunc(states, func(q firstfloor.RequestState) bool {
			expired := admissionLifetimeExpired(q)
			candidateExpired = candidateExpired || expired && q.Context.RequestID == candidateID
			return expired
		})
		if candidateExpired {
			return unknown
		}
		if activeRemaining > 0 {
			step := activeRemaining
			if !inserted && clock+step > age {
				step = age - clock
			}
			admissionAdvance(states, step)
			clock += step
			activeRemaining -= step
			if activeRemaining == 0 {
				states = slices.DeleteFunc(states, func(q firstfloor.RequestState) bool { return q.Phase == phaseRunning })
				n := int64(0)
				idle = &n
			}
			continue
		}
		if len(states) == 0 && !inserted {
			step := age - clock
			if idle != nil {
				*idle += step
			}
			clock = age
			continue
		}
		// The actor's queue is stable EDF followed by deferred FIFO.
		slices.SortStableFunc(states, func(a, b firstfloor.RequestState) int {
			if (a.Phase == phaseDeferred) != (b.Phase == phaseDeferred) {
				if a.Phase == phaseDeferred {
					return 1
				}
				return -1
			}
			if a.Phase == phaseDeferred {
				return 0
			}
			x, y := firstfloor.RemainingSLOMS(a), firstfloor.RemainingSLOMS(b)
			if x < y {
				return -1
			}
			if x > y {
				return 1
			}
			return 0
		})
		choice := firstfloor.ChooseBatch(in.Policy, states, idle)
		if choice.Reason == firstfloor.OutcomeDeadlineDrop || choice.Reason == firstfloor.ReasonDeadlineDefer {
			index := slices.IndexFunc(states, func(q firstfloor.RequestState) bool { return q.Context.RequestID == choice.RequestID })
			if index < 0 {
				return unknown
			}
			if choice.RequestID == candidateID || choice.Reason == firstfloor.ReasonDeadlineDefer {
				q := states[index]
				q.Phase = phaseDeferred
				states = append(slices.Delete(states, index, index+1), q)
			} else {
				states = slices.Delete(states, index, index+1)
			}
			continue
		}
		if choice.WaitMS > 0 {
			step := max(int64(1), int64(choice.WaitMS*float64(time.Millisecond)))
			if !inserted && clock+step > age {
				step = age - clock
			}
			admissionAdvance(states, step)
			if idle != nil {
				*idle += step
			}
			clock += step
			continue
		}
		if choice.Size <= 0 {
			return unknown
		}
		b := admissionBatch(in.Policy, choice.Size)
		if b == nil {
			return unknown
		}
		service, support, ok := b.Distribution.QuantileNanos(idle, in.Candidate.Context.SLO.Percentile)
		if !ok {
			return unknown
		}
		outsideSupport = outsideSupport || support.OutsideSupport
		selected := 0
		candidate := false
		deferred := false
		for _, q := range states {
			if q.Context.RequestID == choice.RequestID {
				deferred = q.Phase == phaseDeferred
				break
			}
		}
		for j := range states {
			q := &states[j]
			if selected < choice.Size && q.Context.WorkloadUID == choice.WorkloadUID && q.Context.Definition == choice.Definition && (q.Phase == phaseDeferred) == deferred {
				q.Phase = phaseRunning
				selected++
				candidate = candidate || q.Context.RequestID == candidateID
			}
		}
		if selected != choice.Size {
			return unknown
		}
		if candidate {
			completion := clock - age + service + int64(in.Candidate.Context.CompletionMarginMS*float64(time.Millisecond))
			return AdmissionEstimate{Completion: time.Duration(completion), Known: true, OutsideSupport: outsideSupport}
		}
		activeRemaining = service
	}
	return unknown
}

func admissionRequestKnown(q firstfloor.RequestState) bool {
	c := q.Context
	return c.RequestID != "" && c.WorkloadUID != "" && c.Definition != "" &&
		(c.AdmissionPolicy == string(AdmissionPolicyReject) || c.AdmissionPolicy == string(AdmissionPolicyDelay)) &&
		c.SLO.TargetMS > 0 && !math.IsInf(c.SLO.TargetMS, 0) && !math.IsNaN(c.SLO.Percentile) && c.SLO.Percentile > 0 && c.SLO.Percentile <= 100 &&
		q.ElapsedMS >= 0 && c.SenderElapsedMS >= 0 && c.CompletionMarginMS >= 0 &&
		!math.IsNaN(q.ElapsedMS+c.SenderElapsedMS+c.CompletionMarginMS) && !math.IsInf(q.ElapsedMS+c.SenderElapsedMS+c.CompletionMarginMS, 0) &&
		(q.Phase == phaseQueued || q.Phase == phaseDeferred || q.Phase == phaseRunning || q.Phase == phaseFinished || q.Phase == phaseReturning)
}

func admissionBatch(p firstfloor.Policy, size int) *firstfloor.BatchProfile {
	for j := range p.Batches {
		if p.Batches[j].Size == size && p.Batches[j].Distribution != nil {
			return &p.Batches[j]
		}
	}
	return nil
}

func admissionAdvance(states []firstfloor.RequestState, nanos int64) {
	ms := float64(nanos) / float64(time.Millisecond)
	for j := range states {
		states[j].ElapsedMS += ms
		if states[j].Phase != phaseRunning {
			states[j].QueueMS += ms
			if states[j].RemainingLifetimeMS != nil {
				*states[j].RemainingLifetimeMS -= ms
			}
		}
	}
}

func admissionLifetimeExpired(q firstfloor.RequestState) bool {
	if q.Phase == phaseRunning {
		return false
	}
	if q.RemainingLifetimeMS != nil {
		return *q.RemainingLifetimeMS <= 0
	}
	return q.Context.LifetimeMS > 0 && q.Context.LifetimeMS <= q.Context.SenderElapsedMS+q.ElapsedMS
}
