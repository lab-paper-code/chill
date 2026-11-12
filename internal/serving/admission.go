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
	Policy                      firstfloor.Policy
	Observed                    []firstfloor.RequestState
	Assignments                 []firstfloor.RequestState
	SnapshotAge                 time.Duration
	LastServiceEndAgeNanos      *int64
	LastServiceEndAgeUpperNanos *int64
	RunningGapUpperNanos        *int64
	Candidate                   firstfloor.RequestState
}

// AdmissionEstimate is a deterministic decision rule made by chaining measured
// stage quantiles. It is not an end-to-end percentile guarantee. Completion is
// relative to the decision and includes the candidate's frozen POST reserve.
// FixedBatch preserves the strict-baseline exception without making a judgment.
type AdmissionEstimate struct {
	Completion     time.Duration
	OutsideSupport bool
	Known          bool
	// UnknownReason identifies the actual failed input/support check.
	UnknownReason string
	// Infeasible proves a final-lifetime miss with otherwise known inputs.
	// Completion is zero when lifetime expires before any batch can be selected.
	Infeasible bool
	FixedBatch bool
}

type admissionScratch struct {
	quantiles     firstfloor.QuantileCache
	states        []firstfloor.RequestState
	lifetimes     []float64
	rates         map[string]float64
	usedLifetimes int
	idle          int64
	idleUpper     int64
}

// Executor-level failures do not depend on a candidate's percentile. Check
// these once per dispatch, including whether any running service can survive.
func admissionExecutorKnown(in AdmissionInput) bool {
	return admissionExecutorUnknownReason(in) == ""
}

func admissionExecutorUnknownReason(in AdmissionInput) string {
	if in.Policy.FixedBatchSize > 0 {
		return ""
	}
	if in.SnapshotAge < 0 || !admissionPolicyKnown(in.Policy, in.LastServiceEndAgeNanos) {
		return AdmissionMissingPolicyOrBatch
	}
	if upper := in.LastServiceEndAgeUpperNanos; upper != nil && (in.LastServiceEndAgeNanos == nil || *upper < *in.LastServiceEndAgeNanos) {
		return AdmissionResidualUnsupported
	}
	for _, q := range in.Observed {
		if q.Phase == phaseFinished || q.Phase == phaseReturning || admissionLifetimeExpired(q) {
			continue
		}
		if !admissionRequestKnown(q) {
			return AdmissionMissingPolicyOrBatch
		}
	}
	running, batch, ok := admissionRunningBatch(in.Policy, in.Observed)
	if !ok {
		for _, q := range in.Observed {
			if q.Phase == phaseRunning && q.ExchangeElapsedNanos == nil {
				return AdmissionPreIPCRunning
			}
		}
		return AdmissionMissingPolicyOrBatch
	}
	if running == nil {
		if !admissionIdleSupported(in.Policy, in.LastServiceEndAgeNanos) {
			return AdmissionResidualUnsupported
		}
	} else if !batch.Distribution.SupportsResidual(running.PrecedingIdleNanos, *running.ExchangeElapsedNanos) ||
		in.RunningGapUpperNanos != nil && (running.PrecedingIdleNanos == nil || *in.RunningGapUpperNanos < *running.PrecedingIdleNanos) {
		return AdmissionResidualUnsupported
	}
	for _, q := range in.Assignments {
		if !admissionLifetimeExpired(q) && !admissionRequestKnown(q) {
			return AdmissionMissingPolicyOrBatch
		}
	}
	return ""
}

func admissionPolicyKnown(p firstfloor.Policy, idle *int64) bool {
	if p.MaxBatchSize < 0 || p.MaxBatchWaitMS < 0 || math.IsNaN(p.MaxBatchWaitMS) || math.IsInf(p.MaxBatchWaitMS, 0) || len(p.Batches) == 0 {
		return false
	}
	if idle != nil && *idle < 0 {
		return false
	}
	for _, b := range p.Batches {
		if b.Size > 0 && (p.MaxBatchSize == 0 || b.Size <= p.MaxBatchSize) && b.Distribution == nil {
			return false
		}
	}
	return true
}

func admissionIdleSupported(p firstfloor.Policy, idle *int64) bool {
	for _, b := range p.Batches {
		if b.Size < 1 || p.MaxBatchSize > 0 && b.Size > p.MaxBatchSize {
			continue
		}
		if b.Distribution.SupportsGap(idle) {
			return true
		}
	}
	return false
}

func admissionQuantile(quantiles *firstfloor.QuantileCache, distribution *firstfloor.ServiceDistribution, idle, upper *int64, percentile float64) (int64, firstfloor.ConditionalSupport, bool) {
	if upper != nil {
		if idle == nil {
			return 0, firstfloor.ConditionalSupport{}, false
		}
		return quantiles.MaximumQuantileNanos(distribution, *idle, *upper, percentile)
	}
	return quantiles.QuantileNanos(distribution, idle, percentile)
}

// A running batch has one physical residual, regardless of its member rows.
func admissionRunningBatch(p firstfloor.Policy, states []firstfloor.RequestState) (*firstfloor.RequestState, *firstfloor.BatchProfile, bool) {
	var head *firstfloor.RequestState
	for j := range states {
		q := &states[j]
		if q.Phase != phaseRunning {
			continue
		}
		if q.BatchID == 0 || q.BatchSize <= 0 || q.ExchangeElapsedNanos == nil || head != nil && head.BatchID != q.BatchID {
			return nil, nil, false
		}
		if head == nil {
			head = q
		}
	}
	if head == nil {
		return nil, nil, true
	}
	batch := admissionBatch(p, head.BatchSize)
	return head, batch, batch != nil
}

func admissionRunningResidual(p firstfloor.Policy, states []firstfloor.RequestState, percentile float64, gapUpper *int64, quantiles *firstfloor.QuantileCache) (int64, bool, bool) {
	q, batch, ok := admissionRunningBatch(p, states)
	if !ok {
		return 0, false, false
	}
	if q == nil {
		return 0, false, true
	}
	var value int64
	var support firstfloor.ConditionalSupport
	if gapUpper != nil {
		if q.PrecedingIdleNanos == nil {
			return 0, false, false
		}
		value, support, ok = quantiles.MaximumResidualQuantileNanos(batch.Distribution, *q.PrecedingIdleNanos, *gapUpper, *q.ExchangeElapsedNanos, percentile)
	} else {
		value, support, ok = quantiles.ResidualQuantileNanos(batch.Distribution, q.PrecedingIdleNanos, *q.ExchangeElapsedNanos, percentile)
	}
	if !ok {
		return 0, false, false
	}
	return value, support.OutsideSupport, true
}

// EstimateAdmission advances the shared native batching policy, including EDF,
// deferred FIFO, fill wait, and survival-conditional running batch residuals.
// Missing state or empirical support leaves admission unknown, never unchecked.
func EstimateAdmission(in AdmissionInput) AdmissionEstimate {
	var scratch admissionScratch
	if reason := admissionExecutorUnknownReason(in); reason != "" {
		return AdmissionEstimate{UnknownReason: reason}
	}
	return projectAdmission(in, &scratch)
}

// projectAdmission owns reusable projection storage; immutable native snapshots
// and their lifetime pointers are never modified. Runtime validates the common
// executor inputs once, before projecting any candidates.
func projectAdmission(in AdmissionInput, scratch *admissionScratch) AdmissionEstimate {
	if in.Policy.FixedBatchSize > 0 {
		return AdmissionEstimate{FixedBatch: true}
	}
	unknown := AdmissionEstimate{UnknownReason: AdmissionMissingPolicyOrBatch}
	if !admissionRequestKnown(in.Candidate) {
		return unknown
	}
	scratch.reset(len(in.Observed) + len(in.Assignments) + 1)
	defer clear(scratch.rates)
	defer clear(scratch.states[:cap(scratch.states)])
	states := scratch.states[:0]
	for _, q := range in.Observed {
		if q.Phase != phaseFinished && q.Phase != phaseReturning && !admissionLifetimeExpired(q) {
			states = scratch.appendState(states, q)
		}
	}
	var idle, upper *int64
	if in.LastServiceEndAgeNanos != nil {
		scratch.idle = *in.LastServiceEndAgeNanos
		idle = &scratch.idle
	}
	if in.LastServiceEndAgeUpperNanos != nil {
		scratch.idleUpper = *in.LastServiceEndAgeUpperNanos
		upper = &scratch.idleUpper
	}
	activeRemaining, outsideSupport, ok := admissionRunningResidual(in.Policy, states, in.Candidate.Context.SLO.Percentile, in.RunningGapUpperNanos, &scratch.quantiles)
	if !ok {
		return AdmissionEstimate{UnknownReason: AdmissionResidualUnsupported}
	}
	age, clock := int64(in.SnapshotAge), int64(0)
	inserted := false
	candidateID := in.Candidate.Context.RequestID
	// Each request can leave normal once and leave the set once. Each fill wake
	// consumes its queue's fill budget; expiry wakes also remove queued members.
	limit := 8*(len(states)+len(in.Assignments)+1) + 16
	for range limit {
		if !inserted && clock >= age {
			states = scratch.insertCandidate(states, in)
			inserted = true
		}
		var expired bool
		states, expired = admissionExpire(states, candidateID)
		if expired {
			return AdmissionEstimate{Known: true, Infeasible: true, OutsideSupport: outsideSupport}
		}
		if activeRemaining > 0 {
			step := admissionWake(states, activeRemaining, clock, age, inserted)
			admissionAdvance(states, step)
			clock += step
			activeRemaining -= step
			if activeRemaining == 0 {
				states = slices.DeleteFunc(states, func(q firstfloor.RequestState) bool { return q.Phase == phaseRunning })
				scratch.idle = 0
				idle = &scratch.idle
				upper = nil
			}
			continue
		}
		if len(states) == 0 && !inserted {
			if idle != nil {
				*idle += age - clock
			}
			if upper != nil {
				*upper += age - clock
			}
			clock = age
			continue
		}
		slices.SortStableFunc(states, compareAdmissionQueue)
		choice := firstfloor.ChooseBatchWithQuantiles(in.Policy, states, idle, upper, &scratch.quantiles)
		if choice.Reason == firstfloor.OutcomeDeadlineDrop || choice.Reason == firstfloor.ReasonDeadlineDefer {
			states, ok = admissionTransition(states, choice, candidateID)
			if !ok {
				return unknown
			}
			continue
		}
		if choice.WaitMS > 0 {
			step := admissionWake(states, max(int64(1), int64(choice.WaitMS*float64(time.Millisecond))), clock, age, inserted)
			admissionAdvance(states, step)
			if idle != nil {
				*idle += step
			}
			if upper != nil {
				*upper += step
			}
			clock += step
			continue
		}
		service, candidate, outside, valid, reason := admissionProjectBatch(in, states, choice, idle, upper, &scratch.quantiles)
		if !valid {
			return AdmissionEstimate{UnknownReason: reason}
		}
		outsideSupport = outsideSupport || outside
		if candidate {
			completion := clock - age + service + int64(in.Candidate.Context.CompletionMarginMS*float64(time.Millisecond))
			remaining, limited := admissionRemainingLifetime(in.Candidate)
			// Native expires a request at its deadline (requestError uses >=), so
			// completing exactly at the remaining lifetime is already a timeout.
			return AdmissionEstimate{Completion: time.Duration(completion), Known: true, OutsideSupport: outsideSupport, Infeasible: limited && float64(completion)/float64(time.Millisecond) >= remaining}
		}
		activeRemaining = service
	}
	return AdmissionEstimate{}
}

func (s *admissionScratch) reset(size int) {
	if cap(s.states) < size {
		s.states = make([]firstfloor.RequestState, 0, max(size, 2*cap(s.states)))
	}
	if cap(s.lifetimes) < size {
		s.lifetimes = make([]float64, max(size, 2*cap(s.lifetimes)))
	}
	s.lifetimes = s.lifetimes[:size]
	s.usedLifetimes = 0
	if s.rates == nil {
		s.rates = make(map[string]float64)
	}
	clear(s.rates)
}

func (s *admissionScratch) appendState(states []firstfloor.RequestState, q firstfloor.RequestState) []firstfloor.RequestState {
	if q.RemainingLifetimeMS != nil {
		s.lifetimes[s.usedLifetimes] = *q.RemainingLifetimeMS
		q.RemainingLifetimeMS = &s.lifetimes[s.usedLifetimes]
		s.usedLifetimes++
	}
	return append(states, q)
}

func (s *admissionScratch) insertCandidate(states []firstfloor.RequestState, in AdmissionInput) []firstfloor.RequestState {
	for _, q := range in.Assignments {
		q.Phase = phaseQueued
		states = s.appendState(states, q)
		s.rates[q.Context.WorkloadUID] = q.ArrivalRate
	}
	states = s.appendState(states, in.Candidate)
	s.rates[in.Candidate.Context.WorkloadUID] = in.Candidate.ArrivalRate
	for j := range states {
		if rate, ok := s.rates[states[j].Context.WorkloadUID]; ok {
			states[j].ArrivalRate = rate
		}
	}
	return states
}

func admissionExpire(states []firstfloor.RequestState, candidateID string) ([]firstfloor.RequestState, bool) {
	kept := 0
	candidateExpired := false
	// Scan in place: a value predicate copies the large RequestState at every
	// virtual event, even when no request has a lifetime or needs removal.
	for j := range states {
		q := &states[j]
		if admissionLifetimeExpired(*q) {
			candidateExpired = candidateExpired || q.Context.RequestID == candidateID
			continue
		}
		if kept != j {
			states[kept] = *q
		}
		kept++
	}
	clear(states[kept:])
	return states[:kept], candidateExpired
}

func admissionRemainingLifetime(q firstfloor.RequestState) (float64, bool) {
	if q.RemainingLifetimeMS != nil {
		return *q.RemainingLifetimeMS, true
	}
	if q.Context.LifetimeMS > 0 {
		return q.Context.LifetimeMS - q.Context.SenderElapsedMS - q.ElapsedMS, true
	}
	return 0, false
}

func admissionWake(states []firstfloor.RequestState, step, clock, age int64, inserted bool) int64 {
	if !inserted {
		step = min(step, age-clock)
	}
	for j := range states {
		q := &states[j]
		if q.Phase == phaseRunning {
			continue
		}
		remaining, limited := admissionRemainingLifetime(*q)
		if limited && remaining > 0 {
			step = min(step, max(int64(1), int64(math.Ceil(remaining*float64(time.Millisecond)))))
		}
	}
	return step
}

func compareAdmissionQueue(a, b firstfloor.RequestState) int {
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
}

func admissionTransition(states []firstfloor.RequestState, choice firstfloor.BatchChoice, candidateID string) ([]firstfloor.RequestState, bool) {
	index := slices.IndexFunc(states, func(q firstfloor.RequestState) bool { return q.Context.RequestID == choice.RequestID })
	if index < 0 {
		return states, false
	}
	if choice.RequestID == candidateID || choice.Reason == firstfloor.ReasonDeadlineDefer {
		q := states[index]
		q.Phase = phaseDeferred
		return append(slices.Delete(states, index, index+1), q), true
	}
	return slices.Delete(states, index, index+1), true
}

func admissionProjectBatch(in AdmissionInput, states []firstfloor.RequestState, choice firstfloor.BatchChoice, idle, upper *int64, quantiles *firstfloor.QuantileCache) (int64, bool, bool, bool, string) {
	b := admissionBatch(in.Policy, choice.Size)
	if b == nil || choice.Size <= 0 {
		return 0, false, false, false, AdmissionMissingPolicyOrBatch
	}
	service, support, ok := admissionQuantile(quantiles, b.Distribution, idle, upper, in.Candidate.Context.SLO.Percentile)
	if !ok {
		return 0, false, false, false, AdmissionResidualUnsupported
	}
	selected, candidate, deferred := 0, false, false
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
			candidate = candidate || q.Context.RequestID == in.Candidate.Context.RequestID
		}
	}
	reason := ""
	if selected != choice.Size {
		reason = AdmissionMissingPolicyOrBatch
	}
	return service, candidate, support.OutsideSupport, selected == choice.Size, reason
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
