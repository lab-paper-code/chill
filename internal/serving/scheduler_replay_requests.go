package serving

import (
	"context"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

type schedulerReplayQueued struct {
	r   *schedulerReplayRequest
	slo float64
}

// compareReplayQueued puts normal requests first by EDF key and keeps deferred
// FIFO. Plain '<' leaves NaN unordered, as the actor's EDF does.
func compareReplayQueued(left, right schedulerReplayQueued) int {
	switch {
	case left.r.deferred != right.r.deferred:
		if left.r.deferred {
			return 1
		}
		return -1
	case left.r.deferred:
		return 0
	case left.slo < right.slo:
		return -1
	case right.slo < left.slo:
		return 1
	}
	return 0
}

func (s *schedulerReplayState) targetHandles(r *schedulerReplayRequest) bool {
	for _, i := range s.goal.Instances {
		if (i.WorkloadUID == r.source.Definition.UID || s.goal.Routing[r.source.Definition.UID][i.ID] > 0) && schedulerProfileMatches(r.source.Definition, i.Profile) {
			return true
		}
	}
	return false
}

func (s *schedulerReplayState) retainedIngress(i *schedulerReplayInstance) bool {
	if i == nil {
		return false
	}
	for _, r := range s.live {
		if r.observed && !r.expired && !r.dispatched && !r.completed && (i.config.WorkloadUID == r.source.Definition.UID || s.snap.Routing[r.source.Definition.UID][i.config.ID] > 0) && schedulerProfileMatches(r.source.Definition, i.config.Profile) && !s.hasRoutedAlternative(r, i) {
			return true
		}
	}
	return false
}

func (s *schedulerReplayState) hasRoutedAlternative(r *schedulerReplayRequest, retiring *schedulerReplayInstance) bool {
	routing := s.snap.Routing
	if s.routingApplied {
		routing = s.applied.Routing
	}
	for _, other := range s.instances {
		if other == retiring || !other.loaded || !other.ready || other.stopped || !schedulerProfileMatches(r.source.Definition, other.config.Profile) {
			continue
		}
		var weight int32
		if other.config.WorkloadUID == r.source.Definition.UID {
			weight = other.config.Weight
		}
		if weights, explicit := routing[r.source.Definition.UID]; explicit {
			weight = weights[other.config.ID]
		}
		if weight > 0 {
			return true
		}
	}
	return false
}

// admitArrivals keeps s.live to the requests a step can touch, so a step costs
// the in-flight population rather than the whole window.
func (s *schedulerReplayState) admitArrivals() {
	live := s.live[:0]
	if s.queuedCounts == nil {
		s.queuedCounts = make(map[string]int)
	}
	queued := s.queuedCounts
	clear(queued)
	for _, r := range s.live {
		if !r.completed {
			live = append(live, r)
			if !r.dispatched && !r.expired {
				queued[r.source.Definition.UID]++
			}
		}
	}
	clear(s.live[len(live):])
	for ; s.arrived < len(s.requests) && s.requests[s.arrived].arrival <= s.now; s.arrived++ {
		r := s.requests[s.arrived]
		if r.completed {
			continue
		}
		if !r.observed && !r.dispatched {
			uid := r.source.Definition.UID
			if queued[uid] >= int(r.source.Definition.Admission.MaxQueuedRequests) {
				s.rejectGateway(r)
				continue
			}
			queued[uid]++
		}
		live = append(live, r)
	}
	s.live = live
}

func (s *schedulerReplayState) expireRequests() {
	for _, r := range s.live {
		if r.completed || r.expired || r.dropped || r.failed || r.expires > s.now {
			continue
		}
		s.capturePotential(r, s.byID[r.source.Instance])
		r.expired = true
		// A final lifetime timeout is one terminal outcome even while physical
		// execution or response return continues to hold the executor.
		key := r.identity
		if s.out.outcomes[key] == nil {
			s.out.outcomes[key] = map[string]int{}
		}
		s.out.outcomes[key][schedulerOutcomeTimeout]++
		if !r.running && !r.returning {
			r.returning = true
			r.completion = s.now
			if r.dispatched {
				i := s.byID[r.source.Instance]
				kept := i.queue[:0]
				for _, q := range i.queue {
					if q != r {
						kept = append(kept, q)
					}
				}
				i.queue = kept
				r.completion = s.now + r.marginNanos
				if !math.IsInf(r.native, 1) {
					r.completion = math.Max(s.now, r.native) + r.marginNanos
				}
				r.native = math.Inf(1)
			}
		}
	}
}

func (s *schedulerReplayState) completeExecution() {
	for _, i := range s.instances {
		if i.finish <= s.now {
			i.lastNativeFinish, i.hasNativeFinish = i.nativeFinish, true
			for _, r := range i.batch {
				r.running = false
				r.returning = true
				r.completion = s.now + r.marginNanos
			}
			i.batch = nil
			i.finish = math.Inf(1)
			i.wake = s.now
		}
	}
	for _, r := range s.live {
		if r.returning && !r.completed && r.completion <= s.now {
			r.completed = true
			i := s.byID[r.source.Instance]
			if i != nil && r.dispatched {
				i.outstanding--
			}
			key := r.identity
			if !r.expired && !r.dropped && !r.failed {
				latency := r.accountedAgeNanos + r.completion - r.arrival
				s.out.samples[key] = append(s.out.samples[key], latency)
				if s.out.outcomes[key] == nil {
					s.out.outcomes[key] = map[string]int{}
				}
				outcome := schedulerOutcomeOnTime
				if latency > float64(r.source.Definition.SLO.TargetLatency) {
					outcome = schedulerOutcomeLate
				}
				s.out.outcomes[key][outcome]++
			}
		}
	}
}

// Runtime retains a route entry's credit only when its physical ID and weight
// are unchanged. Apply the same rule to existing and lazily seeded replay state.
func (s *schedulerReplayState) retainedRouteCredit(uid string, i SchedulerInstance, credit int64) int64 {
	before, after := i.Weight, i.Weight
	if route, ok := s.previousRoute[uid]; ok {
		before = route[i.ID]
	}
	if route, ok := s.applied.Routing[uid]; ok {
		after = route[i.ID]
	}
	if before != after {
		return 0
	}
	return credit
}

func (s *schedulerReplayState) routeRequests() {
	// Match runtime's UID order and two bounded passes across all Workloads.
	uids := s.dispatchUIDs[:0]
	for _, r := range s.live {
		if !r.completed && !r.expired && !r.dispatched {
			uids = append(uids, r.source.Definition.UID)
		}
	}
	slices.Sort(uids)
	uids = slices.Compact(uids)
	s.dispatchUIDs = uids
	for _, delayed := range [...]bool{false, true} {
		for _, uid := range uids {
			for _, r := range s.live {
				if r.completed || r.expired || r.dispatched || r.arrival > s.now || r.source.Definition.UID != uid || r.gatewayDeferred != delayed {
					continue
				}
				selected, state := s.chooseAdmission(r)
				if selected < 0 {
					if state == CodeDeadlineInfeasible && !r.gatewayDeferred {
						if r.source.Definition.Admission.Policy == AdmissionPolicyDelay {
							r.gatewayDeferred = true
						} else {
							s.rejectGateway(r)
						}
					}
					continue
				}
				i := s.instances[selected]
				r.dispatched = true
				r.deferred = false // native admission starts normal and rechecks its frozen policy
				r.source.Instance = i.config.ID
				r.senderMS = math.Max(0, float64(r.source.Elapsed)+r.preNanos+s.now-r.arrival) / 1e6
				r.native = s.now
				i.outstanding++
			}
		}
	}
}

func (s *schedulerReplayState) routeWeight(r *schedulerReplayRequest, i *schedulerReplayInstance) int32 {
	routes := s.snap.Routing
	if s.routingApplied {
		routes = s.applied.Routing
	}
	if r.observed && !s.targetHandles(r) {
		if weight := s.snap.Routing[r.source.Definition.UID][i.config.ID]; weight > 0 {
			return weight
		}
		if i.config.WorkloadUID == r.source.Definition.UID {
			return i.config.Weight
		}
		return 0
	}
	if route, ok := routes[r.source.Definition.UID]; ok {
		return route[i.config.ID]
	}
	if i.config.WorkloadUID == r.source.Definition.UID {
		return i.config.Weight
	}
	return 0
}

func (s *schedulerReplayState) normalUsesInstance(i *schedulerReplayInstance) bool {
	for _, r := range s.live {
		if !r.completed && !r.expired && !r.dispatched && !r.gatewayDeferred && s.routeWeight(r, i) > 0 && schedulerProfileMatches(r.source.Definition, i.config.Profile) {
			return true
		}
	}
	return false
}

func (s *schedulerReplayState) chooseAdmission(r *schedulerReplayRequest) (int, string) {
	uid := r.source.Definition.UID
	credits := s.routeCredits[uid]
	if len(credits) != len(s.instances) {
		credits = make([]int64, len(s.instances))
		for j, i := range s.instances {
			credits[j] = s.snap.RoutingCredits[uid][i.config.ID]
			if s.creditInvalid[uid][i.config.ID] {
				credits[j] = 0
			} else if s.routingApplied {
				credits[j] = s.retainedRouteCredit(uid, i.config, credits[j])
			}
		}
		s.routeCredits[uid] = credits
	}
	known, unknown := false, false
	selected := -1
	var earliest time.Duration
	for j, i := range s.instances {
		s.weights[j], s.available[j] = s.routeWeight(r, i), false
		if s.weights[j] <= 0 || !i.loaded || !i.ready || i.stopped || i.config.Terminated || i.outstanding >= i.config.Capacity {
			continue
		}
		if i.config.Profile == nil {
			unknown = true
			continue
		}
		if !schedulerProfileMatches(r.source.Definition, i.config.Profile) {
			continue
		}
		if r.gatewayDeferred && s.normalUsesInstance(i) {
			continue
		}
		estimate := s.estimateAdmission(i, r)
		if estimate.FixedBatch {
			s.available[j] = true
			if r.gatewayDeferred && selected < 0 {
				selected = j
			}
			continue
		}
		if !estimate.Known {
			unknown = true
			continue
		}
		known = true
		remaining := float64(r.source.Definition.SLO.TargetLatency) - r.accountedAgeNanos - s.now + r.arrival
		s.available[j] = r.gatewayDeferred || float64(estimate.Completion) <= remaining
		if r.gatewayDeferred && (selected < 0 || estimate.Completion < earliest) {
			selected, earliest = j, estimate.Completion
		}
	}
	if !r.gatewayDeferred {
		selected = SmoothWeightedRoute(s.weights, credits, s.available)
	}
	if selected >= 0 {
		return selected, "Feasible"
	}
	if unknown {
		return -1, AdmissionUnknown
	}
	if known {
		return -1, CodeDeadlineInfeasible
	}
	return -1, "NoCapacity"
}

func (s *schedulerReplayState) estimateAdmission(i *schedulerReplayInstance, r *schedulerReplayRequest) AdmissionEstimate {
	if !i.arrivalsKnown && i.policy.FixedBatchSize == 0 {
		return AdmissionEstimate{}
	}
	observed := s.states[:0]
	for _, q := range i.queue {
		observed = append(observed, s.nativeRequestState(i, q))
	}
	for _, q := range i.batch {
		observed = append(observed, s.nativeRequestState(i, q))
	}
	s.states = observed
	assignments := s.admissionAssignments[:0]
	for _, q := range s.live {
		if q.dispatched && !q.completed && !q.returning && q.source.Instance == i.config.ID && !math.IsInf(q.native, 1) {
			assignments = append(assignments, s.gatewayRequestState(i, q))
		}
	}
	// Replay's zero-transit assignments share this dispatch timestamp. Runtime
	// breaks equal dispatch times by request ID, independently of receipt age.
	slices.SortStableFunc(assignments, func(a, b firstfloor.RequestState) int {
		return strings.Compare(a.Context.RequestID, b.Context.RequestID)
	})
	s.admissionAssignments = assignments
	in := AdmissionInput{Policy: i.policy, Observed: observed, Assignments: assignments, LastServiceEndAgeNanos: i.idleNanos(s.now), Candidate: s.gatewayRequestState(i, r)}
	if !admissionExecutorKnown(in) {
		return AdmissionEstimate{}
	}
	return projectAdmission(in, &s.admissionScratch)
}

func (s *schedulerReplayState) gatewayRequestState(i *schedulerReplayInstance, r *schedulerReplayRequest) firstfloor.RequestState {
	d := r.source.Definition
	phase := phaseQueued
	if r.gatewayDeferred {
		phase = phaseDeferred
	}
	state := firstfloor.RequestState{Context: firstfloor.RequestContext{RequestID: r.source.ID, WorkloadUID: d.UID, Definition: r.identity, AdmissionPolicy: string(d.Admission.Policy), ReceivedAt: r.source.ReceivedAt, SenderElapsedMS: math.Max(0, r.accountedAgeNanos+s.now-r.arrival) / 1e6, CompletionMarginMS: r.marginNanos / 1e6, SLO: firstfloor.SLO{Percentile: d.SLO.Percentile, TargetMS: float64(d.SLO.TargetLatency) / 1e6}}, Phase: phase}
	last, known := 0.0, false
	if history := i.arrivalTimes[d.UID]; len(history) > 0 {
		last, known = history[0], true
	}
	for _, q := range s.live {
		if q != r && q.dispatched && !q.completed && !q.returning && q.source.Instance == i.config.ID && q.source.Definition.UID == d.UID && !math.IsInf(q.native, 1) {
			last, known = max(last, q.native), true
		}
	}
	if known && s.now > last {
		state.ArrivalRate = 1e9 / (s.now - last)
	}
	return s.requestLifetimeState(r, state)
}

func (s *schedulerReplayState) requestLifetimeState(r *schedulerReplayRequest, state firstfloor.RequestState) firstfloor.RequestState {
	if !math.IsInf(r.expires, 1) {
		r.remainingLifetimeMS = max(0, r.expires-s.now) / 1e6
		state.RemainingLifetimeMS = &r.remainingLifetimeMS
		state.Context.LifetimeMS = state.Context.SenderElapsedMS + state.ElapsedMS + r.remainingLifetimeMS
	}
	return state
}

func (s *schedulerReplayState) rejectGateway(r *schedulerReplayRequest) {
	s.capturePotential(r, nil)
	r.dropped, r.completed, r.completion = true, true, s.now
	if s.out.outcomes[r.identity] == nil {
		s.out.outcomes[r.identity] = map[string]int{}
	}
	s.out.outcomes[r.identity][schedulerOutcomeRejected]++
	if s.capacity != nil && !r.capacityMiss {
		r.capacityMiss = true
		s.capacity.missed++
	}
}

func (s *schedulerReplayState) receiveNative() {
	for _, r := range s.live {
		if r.dispatched && !r.returning && !r.completed && r.native <= s.now {
			r.native = math.Inf(1)
			r.queueAt = s.now
			r.nativeAgeNanos = -s.now
			i := s.byID[r.source.Instance]
			i.queue = append(i.queue, r)
			uid := r.source.Definition.UID
			if _, known := i.arrivalTimes[uid]; !known && len(i.arrivalTimes) >= i.config.Capacity {
				oldest := ""
				at := math.Inf(1)
				for key, h := range i.arrivalTimes {
					if len(h) > 0 && (h[0] < at || h[0] == at && key < oldest) {
						oldest = key
						at = h[0]
					}
				}
				delete(i.arrivalTimes, oldest)
			}
			history := i.arrivalTimes[uid]
			if cap(history) < 2 {
				h := make([]float64, len(history), 2)
				copy(h, history)
				history = h
			}
			if len(history) == 0 {
				history = append(history, s.now)
			} else {
				if len(history) == 1 {
					history = append(history, history[0])
				} else {
					history[1] = history[0]
				}
				history[0] = s.now
			}
			i.arrivalTimes[uid] = history
			i.wake = s.now
		}
	}
}

func (s *schedulerReplayState) nativeRequestState(i *schedulerReplayInstance, r *schedulerReplayRequest) firstfloor.RequestState {
	d := r.source.Definition
	rate := r.source.ArrivalRate
	if history := i.arrivalTimes[d.UID]; len(history) == 2 {
		gap := history[0] - history[1]
		if gap > 0 {
			rate = 1e9 / gap
		} else {
			rate = 0
		}
	}
	phase := phaseQueued
	if r.running {
		phase = phaseRunning
	} else if r.deferred {
		phase = phaseDeferred
	}
	state := firstfloor.RequestState{Context: firstfloor.RequestContext{RequestID: r.source.ID, WorkloadUID: d.UID, Definition: r.identity, AdmissionPolicy: string(d.Admission.Policy), ReceivedAt: r.source.ReceivedAt, SenderElapsedMS: r.senderMS, CompletionMarginMS: r.marginNanos / 1e6, SLO: firstfloor.SLO{Percentile: d.SLO.Percentile, TargetMS: float64(d.SLO.TargetLatency) / 1e6}}, Phase: phase, ElapsedMS: (s.now + r.nativeAgeNanos) / 1e6, QueueMS: (s.now - r.queueAt) / 1e6, ArrivalRate: rate}
	if r.running {
		state.BatchID, state.BatchSize = 1, len(i.batch)
		if i.exchangeKnown {
			i.exchangeElapsed = i.batchElapsed + int64(s.now-i.batchStarted)
			state.ExchangeElapsedNanos = &i.exchangeElapsed
			if i.hasBatchIdle {
				state.PrecedingIdleNanos = &i.batchIdle
			}
		}
	}
	return s.requestLifetimeState(r, state)
}

func (s *schedulerReplayState) chooseBatches(ctx context.Context) bool {
	for _, i := range s.instances {
		if !i.loaded || !i.ready || len(i.queue) == 0 {
			continue
		}
		if i.config.Profile == nil {
			s.out.reasons = appendUnique(s.out.reasons, "MissingFeedbackProfile:"+i.config.ID)
			return false
		}
		for _, batch := range i.config.Profile.Batches {
			if batch.Batch <= i.config.MaxBatch && batch.ServiceDistribution == nil {
				s.out.reasons = appendUnique(s.out.reasons, "MissingConditionalServiceSupport:"+i.config.ID)
				return false
			}
		}
		entries, states := s.entries[:0], s.states[:0]
		for len(i.queue) > 0 {
			if ctx.Err() != nil {
				return false
			}
			// EDF keys are fixed at s.now: compute each once, then sort.
			entries = entries[:0]
			for _, r := range i.queue {
				entries = append(entries, schedulerReplayQueued{r: r, slo: firstfloor.RemainingSLOMS(s.nativeRequestState(i, r))})
			}
			// Match the actor's stable EDF normal queue and contiguous deferred FIFO.
			slices.SortStableFunc(entries, compareReplayQueued)
			states = states[:0]
			for j, e := range entries {
				i.queue[j] = e.r
				states = append(states, s.nativeRequestState(i, e.r))
			}
			for _, r := range i.batch {
				states = append(states, s.nativeRequestState(i, r))
			}
			s.entries, s.states = entries, states
			gapDecision := i.idleNanos(s.now)
			choice := firstfloor.ChooseBatch(i.policy, states, gapDecision)
			if choice.Reason == firstfloor.OutcomeDeadlineDrop || choice.Reason == firstfloor.ReasonDeadlineDefer {
				if !s.transitionQueuedChoice(i, choice) {
					return false
				}
				continue
			}
			if len(i.batch) > 0 || i.wake > s.now {
				break
			}
			if choice.WaitMS > 0 {
				i.wake = s.now + math.Max(1, math.Ceil(choice.WaitMS*1e6))
				break
			}
			if choice.Size <= 0 {
				s.out.reasons = append(s.out.reasons, "BatchPolicyNoExecutableChoice:"+i.config.ID)
				return false
			}
			deferred := i.queue[0].deferred
			rest := i.queue[:0]
			for _, r := range i.queue {
				if len(i.batch) < choice.Size && r.deferred == deferred && r.source.Definition.UID == choice.WorkloadUID && r.identity == choice.Definition {
					i.batch = append(i.batch, r)
				} else {
					rest = append(rest, r)
				}
			}
			i.queue = rest
			for _, r := range i.batch {
				r.running = true
			}
			b := schedulerBatch(i.config.Profile, len(i.batch))
			if b == nil {
				s.out.reasons = append(s.out.reasons, "BatchPolicyOutsideProfile:"+i.config.ID)
				return false
			}
			gap := i.idleNanos(s.now)
			i.batchStarted, i.batchElapsed, i.exchangeKnown = s.now, 0, true
			i.hasBatchIdle = gap != nil
			if gap != nil {
				i.batchIdle = *gap
			}
			sample, support, ok := b.ServiceDistribution.Sample(gap, s.rng)
			s.out.conditional.record(gap, support)
			if !ok {
				s.out.reasons = append(s.out.reasons, "MissingConditionalServiceSupport:"+i.config.ID)
				return false
			}
			duration := float64(sample.ServiceDurationNanos)
			// Trace gap selects S; arrivals/queue already supplied the gap.
			// Retain the current occupancy model without treating it as native S.
			i.nativeFinish = s.now + duration
			i.finish = i.nativeFinish + i.occupancyNanos*float64(len(i.batch))
			s.addBatchEnergy(i, b, duration)
			break
		}
	}
	return true
}

func (s *schedulerReplayState) transitionQueuedChoice(i *schedulerReplayInstance, choice firstfloor.BatchChoice) bool {
	index := -1
	for j, r := range i.queue {
		if r.source.ID == choice.RequestID {
			index = j
			break
		}
	}
	if index < 0 {
		s.out.reasons = append(s.out.reasons, "BatchPolicyUnknownTransitionRequest:"+i.config.ID)
		return false
	}
	r := i.queue[index]
	if choice.Reason == firstfloor.OutcomeDeadlineDrop {
		s.capturePotential(r, i)
	}
	copy(i.queue[index:], i.queue[index+1:])
	i.queue[len(i.queue)-1] = nil
	i.queue = i.queue[:len(i.queue)-1]
	if choice.Reason == firstfloor.ReasonDeadlineDefer {
		r.deferred = true
		i.queue = append(i.queue, r)
	} else {
		r.dropped, r.completed, r.completion = true, true, s.now
		i.outstanding--
		if s.out.outcomes[r.identity] == nil {
			s.out.outcomes[r.identity] = map[string]int{}
		}
		s.out.outcomes[r.identity][schedulerOutcomeRejected]++
		if s.capacityWindowFailed() {
			return false
		}
	}
	i.wake = s.now
	return true
}

// addBatchEnergy attributes a started batch's incremental joules within the
// horizon; total energy counts only compared Nodes.
func (s *schedulerReplayState) addBatchEnergy(i *schedulerReplayInstance, b *BatchProfile, duration float64) {
	if s.now < s.horizon {
		joules := b.EnergyPerInputJoules * float64(len(i.batch)) * math.Min(duration, s.horizon-s.now) / duration
		s.out.requestJoules += joules
		if s.counted(i.config.Node) {
			s.out.energy += joules
		}
	}
}

func schedulerBatch(p *Profile, size int) *BatchProfile {
	if p != nil {
		for j := range p.Batches {
			if p.Batches[j].Batch == size {
				return &p.Batches[j]
			}
		}
	}
	return nil
}
