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
		if other == retiring || !replayExecutable(other) || other.stopped || !schedulerProfileMatches(r.source.Definition, other.config.Profile) {
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
func (s *schedulerReplayState) admitArrivals(ctx context.Context) bool {
	live := s.live[:0]
	if s.queuedCounts == nil {
		s.queuedCounts = make(map[string]int)
	}
	queued := s.queuedCounts
	clear(queued)
	for _, r := range s.live {
		if !r.completed {
			live = append(live, r)
			if !r.dispatched && !r.expired && r.expires > s.now {
				queued[r.source.Definition.UID]++
			}
		}
	}
	clear(s.live[len(live):])
	s.live = live
	for ; s.arrived < len(s.requests) && s.requests[s.arrived].arrival <= s.now; s.arrived++ {
		if ctx.Err() != nil {
			return false
		}
		r := s.requests[s.arrived]
		if r.completed || r.admitted {
			continue
		}
		if !r.dispatched && r.expires > s.now {
			uid := r.source.Definition.UID
			if !r.observed && queued[uid] >= int(r.source.Definition.Admission.MaxQueuedRequests) {
				s.rejectGateway(r)
				continue
			}
			queued[uid]++
		}
		s.live = append(s.live, r)
		r.admitted = true
		// Runtime Accept enqueues and dispatches each arrival before the next
		// request checks the gateway bound, even at the same virtual timestamp.
		if !r.observed && !s.routeRequests(ctx) {
			return false
		}
	}
	return true
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
		if i := s.byID[r.source.Instance]; i != nil && i.excluded && r.dispatched && !r.returning {
			continue // client expiry is not physical completion of an unmodelable executor
		}
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
			i.hasNativeFinishBounds = false
			i.hasBatchIdleBounds = false
			i.idleUnknown = false
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

func (s *schedulerReplayState) routeRequests(ctx context.Context) bool {
	if s.gatewayQueueEmpty() {
		return ctx.Err() == nil
	}
	if !s.beginAdmission(ctx) {
		s.endAdmission()
		return false
	}
	defer s.endAdmission()
	if !s.admissionReady {
		return true
	}
	// Each bounded pass visits each Workload queue once, not the whole live
	// population once per pending request, executor, or Workload.
	for _, delayed := range [...]bool{false, true} {
		for _, uid := range s.dispatchUIDs {
			for _, r := range s.admissionPending[uid] {
				if ctx.Err() != nil {
					return false
				}
				if r.completed || r.dispatched || r.gatewayDeferred != delayed {
					continue
				}
				selected, state := s.chooseAdmission(ctx, r)
				if selected < 0 {
					if s.admissionBlocked {
						break
					}
					if state == CodeDeadlineInfeasible && !r.gatewayDeferred {
						s.finishNormalAdmission(r)
						if r.source.Definition.Admission.Policy == AdmissionPolicyDelay {
							r.gatewayDeferred = true
						} else {
							s.rejectGateway(r)
							if s.queuedCounts[uid] > 0 {
								s.queuedCounts[uid]--
							}
						}
					}
					continue
				}
				i := s.instances[selected]
				s.finishNormalAdmission(r)
				candidate := s.gatewayRequestState(i, r)
				i.admissionAssignments = append(i.admissionAssignments, candidate)
				i.admissionArrivals[uid] = i.admissionArrivals[uid].accepted(s.snap.At.Add(time.Duration(s.now)))
				r.dispatched = true
				r.deferred = false // native admission starts normal and rechecks its frozen policy
				r.source.Instance = i.config.ID
				r.senderMS = math.Max(0, float64(r.source.Elapsed)+r.preNanos+s.now-r.arrival) / 1e6
				r.native = s.now
				i.outstanding++
				if s.queuedCounts[uid] > 0 {
					s.queuedCounts[uid]--
				}
			}
		}
	}
	return ctx.Err() == nil
}

func (s *schedulerReplayState) gatewayQueueEmpty() bool {
	if s.queuedCounts == nil {
		return false // no indexed queue count yet
	}
	for _, count := range s.queuedCounts {
		if count > 0 {
			return false
		}
	}
	return true
}

func (s *schedulerReplayState) routeWeight(r *schedulerReplayRequest, i *schedulerReplayInstance) int32 {
	routes := s.snap.Routing
	if s.routingApplied {
		routes = s.applied.Routing
	}
	handled := r.admissionTarget
	if !s.admissionPrepared {
		handled = s.targetHandles(r)
	}
	if r.observed && !handled {
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

func (s *schedulerReplayState) finishNormalAdmission(r *schedulerReplayRequest) {
	if r.gatewayDeferred {
		return
	}
	for _, i := range s.instances {
		if !i.admissionUnknown && s.routeWeight(r, i) > 0 && s.potentialProfileMatches(r.source.Definition, i.config.Profile) {
			i.admissionNormal--
		}
	}
}

func replayExecutable(i *schedulerReplayInstance) bool {
	return !i.excluded && i.loaded && i.ready
}

func replayHasCapacity(i *schedulerReplayInstance) bool {
	return replayExecutable(i) && !i.stopped && !i.config.Terminated && i.outstanding < i.config.Capacity
}

func (s *schedulerReplayState) replayRouteCredits(uid string) []int64 {
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
	return credits
}

func (s *schedulerReplayState) chooseAdmission(ctx context.Context, r *schedulerReplayRequest) (int, string) {
	if !s.admissionPrepared {
		if !s.beginAdmission(ctx) {
			s.endAdmission()
			return -1, AdmissionUnknown
		}
		defer s.endAdmission()
	}
	if r.observed && !s.admissionReady {
		r.admissionTarget = s.targetHandles(r)
	}
	credits := s.replayRouteCredits(r.source.Definition.UID)
	known, unknown := false, false
	s.admissionBlocked = true
	selected := -1
	var earliest time.Duration
	for j, i := range s.instances {
		if ctx.Err() != nil {
			return -1, AdmissionUnknown
		}
		s.weights[j], s.available[j] = s.routeWeight(r, i), false
		if s.weights[j] <= 0 || !replayHasCapacity(i) {
			continue
		}
		if i.admissionUnknown {
			unknown = true
			continue
		}
		s.admissionBlocked = false
		if !s.potentialProfileMatches(r.source.Definition, i.config.Profile) || r.gatewayDeferred && i.admissionNormal > 0 {
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
		s.available[j] = !estimate.Infeasible && (r.gatewayDeferred || float64(estimate.Completion) <= remaining)
		if r.gatewayDeferred && s.available[j] && (selected < 0 || estimate.Completion < earliest) {
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
	return -1, AdmissionNoCapacity
}

func (s *schedulerReplayState) estimateAdmission(i *schedulerReplayInstance, r *schedulerReplayRequest) AdmissionEstimate {
	if i.admissionUnknown {
		return AdmissionEstimate{}
	}
	in := AdmissionInput{Policy: i.policy, Observed: i.admissionObserved, Assignments: i.admissionAssignments, LastServiceEndAgeNanos: i.idleNanos(s.now), LastServiceEndAgeUpperNanos: i.idleUpperNanos(s.now), RunningGapUpperNanos: i.runningIdleUpper(), Candidate: s.gatewayRequestState(i, r)}
	return projectAdmission(in, &s.admissionScratch)
}

func replayAdmissionArrival(at time.Time, history []float64) admissionArrival {
	var arrival admissionArrival
	if len(history) > 0 {
		arrival.last = at.Add(time.Duration(history[0]))
	}
	if len(history) > 1 {
		arrival.previous = at.Add(time.Duration(history[1]))
	}
	return arrival
}

func (s *schedulerReplayState) gatewayRequestState(i *schedulerReplayInstance, r *schedulerReplayRequest) firstfloor.RequestState {
	d := r.source.Definition
	phase := phaseQueued
	if r.gatewayDeferred {
		phase = phaseDeferred
	}
	arrival := i.admissionArrivals[d.UID]
	if !s.admissionPrepared {
		arrival = replayAdmissionArrival(s.snap.At, i.arrivalTimes[d.UID])
	}
	rate := arrival.accepted(s.snap.At.Add(time.Duration(s.now))).rate()
	state := firstfloor.RequestState{Context: firstfloor.RequestContext{RequestID: r.source.ID, WorkloadUID: d.UID, Definition: r.identity, AdmissionPolicy: string(d.Admission.Policy), ReceivedAt: r.source.ReceivedAt, SenderElapsedMS: math.Max(0, r.accountedAgeNanos+s.now-r.arrival) / 1e6, CompletionMarginMS: r.marginNanos / 1e6, SLO: firstfloor.SLO{Percentile: d.SLO.Percentile, TargetMS: float64(d.SLO.TargetLatency) / 1e6}}, Phase: phase, ArrivalRate: rate}
	return s.requestLifetimeState(r, state)
}

func (s *schedulerReplayState) prepareAdmissionInstance(i *schedulerReplayInstance) {
	clear(i.admissionObserved)
	i.admissionObserved = i.admissionObserved[:0]
	clear(i.admissionAssignments)
	i.admissionAssignments = i.admissionAssignments[:0]
	clear(i.admissionCalls)
	i.admissionCalls = i.admissionCalls[:0]
	clear(i.admissionArrivals)
	i.admissionNormal = 0
	i.admissionUnknown = !replayHasCapacity(i) || i.config.Profile == nil || (!i.arrivalsKnown || i.idleUnknown) && i.policy.FixedBatchSize == 0
	if i.admissionUnknown {
		return
	}
	for _, r := range i.queue {
		i.admissionObserved = append(i.admissionObserved, s.nativeRequestState(i, r))
	}
	for _, r := range i.batch {
		i.admissionObserved = append(i.admissionObserved, s.nativeRequestState(i, r))
	}
	i.admissionUnknown = !admissionExecutorKnown(AdmissionInput{Policy: i.policy, Observed: i.admissionObserved, LastServiceEndAgeNanos: i.idleNanos(s.now), LastServiceEndAgeUpperNanos: i.idleUpperNanos(s.now), RunningGapUpperNanos: i.runningIdleUpper()})
	if i.admissionUnknown {
		return
	}
	if i.admissionArrivals == nil {
		i.admissionArrivals = make(map[string]admissionArrival)
	}
	for uid, history := range i.arrivalTimes {
		i.admissionArrivals[uid] = replayAdmissionArrival(s.snap.At, history)
	}
}

func (s *schedulerReplayState) beginAdmission(ctx context.Context) bool {
	s.admissionPrepared, s.admissionReady = true, false
	s.admissionScratch.quantiles.Reset()
	s.dispatchUIDs = s.dispatchUIDs[:0]
	if s.admissionPending == nil {
		s.admissionPending = make(map[string][]*schedulerReplayRequest)
		s.admissionTargets = make(map[*Definition]bool)
	}
	if s.routeCredits == nil {
		s.routeCredits = make(map[string][]int64)
	}
	if len(s.weights) != len(s.instances) {
		s.weights, s.available = make([]int32, len(s.instances)), make([]bool, len(s.instances))
	}
	for _, i := range s.instances {
		if ctx.Err() != nil {
			return false
		}
		s.prepareAdmissionInstance(i)
		s.admissionReady = s.admissionReady || !i.admissionUnknown
	}
	if !s.admissionReady || !s.indexAdmissionRequests(ctx) {
		return ctx.Err() == nil
	}
	s.admissionReady = false
	for _, i := range s.instances {
		if i.admissionUnknown {
			continue
		}
		slices.SortFunc(i.admissionCalls, func(a, b *schedulerReplayRequest) int {
			return strings.Compare(a.source.ID, b.source.ID)
		})
		for _, r := range i.admissionCalls {
			q := s.gatewayRequestState(i, r)
			i.admissionArrivals[r.source.Definition.UID] = i.admissionArrivals[r.source.Definition.UID].accepted(s.snap.At.Add(time.Duration(r.native)))
			q.ArrivalRate = i.admissionArrivals[r.source.Definition.UID].rate()
			i.admissionAssignments = append(i.admissionAssignments, q)
		}
		for _, q := range i.admissionAssignments {
			if i.policy.FixedBatchSize == 0 && !admissionLifetimeExpired(q) && !admissionRequestKnown(q) {
				i.admissionUnknown = true
				break
			}
		}
		s.admissionReady = s.admissionReady || !i.admissionUnknown
	}
	slices.Sort(s.dispatchUIDs)
	return ctx.Err() == nil
}

func (s *schedulerReplayState) indexAdmissionRequests(ctx context.Context) bool {
	for _, r := range s.live {
		if ctx.Err() != nil {
			return false
		}
		if r.completed || r.expired || r.expires <= s.now || r.arrival > s.now {
			continue
		}
		if r.dispatched {
			if i := s.byID[r.source.Instance]; i != nil && !i.admissionUnknown && !r.returning && !math.IsInf(r.native, 1) {
				i.admissionCalls = append(i.admissionCalls, r)
			}
			continue
		}
		d := r.source.Definition
		handled, found := s.admissionTargets[d]
		if !found {
			handled = s.targetHandles(r)
			s.admissionTargets[d] = handled
		}
		r.admissionTarget = handled
		if len(s.admissionPending[d.UID]) == 0 {
			s.dispatchUIDs = append(s.dispatchUIDs, d.UID)
		}
		s.admissionPending[d.UID] = append(s.admissionPending[d.UID], r)
		if !r.gatewayDeferred {
			for _, i := range s.instances {
				if !i.admissionUnknown && s.routeWeight(r, i) > 0 && s.potentialProfileMatches(d, i.config.Profile) {
					i.admissionNormal++
				}
			}
		}
	}
	return true
}

func (s *schedulerReplayState) endAdmission() {
	s.admissionPrepared = false
	s.admissionScratch.quantiles.Reset()
	for _, i := range s.instances {
		clear(i.admissionObserved)
		i.admissionObserved = i.admissionObserved[:0]
		clear(i.admissionAssignments)
		i.admissionAssignments = i.admissionAssignments[:0]
		clear(i.admissionCalls)
		i.admissionCalls = i.admissionCalls[:0]
		clear(i.admissionArrivals)
	}
	for uid, queue := range s.admissionPending {
		clear(queue)
		s.admissionPending[uid] = queue[:0]
	}
	clear(s.admissionTargets)
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
		if !replayExecutable(i) || len(i.queue) == 0 {
			continue
		}
		if i.config.Profile == nil {
			s.out.reasons = appendUnique(s.out.reasons, "MissingFeedbackProfile:"+i.config.ID)
			return false
		}
		if (i.idleUnknown || !i.hasNativeFinish) && len(i.batch) == 0 {
			s.out.reasons = appendUnique(s.out.reasons, "UnknownNativeServiceEnd:"+i.config.ID)
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
			choice := firstfloor.ChooseBatchWithQuantiles(i.policy, states, gapDecision, i.idleUpperNanos(s.now), nil)
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
			if choice.Reason == "fixed_batch_filling" {
				break // Native fixed-B waits for an arrival/lifetime event, without a timer.
			}
			if choice.Size <= 0 {
				s.out.reasons = append(s.out.reasons, "BatchPolicyNoExecutableChoice:"+i.config.ID)
				return false
			}
			if !s.startChosenBatch(i, choice) {
				return false
			}
			break
		}
	}
	return true
}

func (s *schedulerReplayState) startChosenBatch(i *schedulerReplayInstance, choice firstfloor.BatchChoice) bool {
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
	var durationNanos int64
	var support firstfloor.ConditionalSupport
	var ok bool
	percentile := i.batch[0].source.Definition.SLO.Percentile
	if upper := i.idleUpperNanos(s.now); upper != nil && gap != nil {
		i.hasBatchIdleBounds, i.batchIdleUpper = true, *upper
		durationNanos, support, ok = b.ServiceDistribution.SampleMaximum(*gap, *upper, percentile, s.rng)
		s.out.assumptions = appendUnique(s.out.assumptions, "gateway-completion gap interval uses the exact maximum conditional inverse-CDF envelope until the next simulated service completion")
	} else {
		durationNanos, support, ok = b.ServiceDistribution.Sample(gap, percentile, s.rng)
	}
	s.out.conditional.record(support)
	if !ok {
		s.out.reasons = append(s.out.reasons, "MissingConditionalServiceSupport:"+i.config.ID)
		return false
	}
	duration := float64(durationNanos)
	// Trace gap selects S; arrivals/queue already supplied the gap.
	// Retain the current occupancy model without treating it as native S.
	i.nativeFinish = s.now + duration
	i.finish = i.nativeFinish + i.occupancyNanos*float64(len(i.batch))
	s.addBatchEnergy(i, b, duration)
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

// addBatchEnergy counts a started batch's inputs within the horizon; the
// replay charges them as node load energy at its end (addLoadEnergy).
func (s *schedulerReplayState) addBatchEnergy(i *schedulerReplayInstance, _ *BatchProfile, duration float64) {
	if s.now < s.horizon {
		i.inputs += float64(len(i.batch)) * math.Min(duration, s.horizon-s.now) / duration
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
