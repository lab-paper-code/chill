package serving

import (
	"context"
	"math"
	"slices"

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
		if i.WorkloadUID == r.source.Definition.UID && schedulerProfileMatches(r.source.Definition, i.Profile) {
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
		if r.observed && !r.expired && !r.dispatched && !r.completed && i.config.WorkloadUID == r.source.Definition.UID && schedulerProfileMatches(r.source.Definition, i.config.Profile) && !s.hasRoutedAlternative(r, i) {
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
		if other == retiring || !other.loaded || !other.ready || other.stopped || other.config.WorkloadUID != r.source.Definition.UID || !schedulerProfileMatches(r.source.Definition, other.config.Profile) {
			continue
		}
		weight := other.config.Weight
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
	for _, r := range s.live {
		if !r.completed {
			live = append(live, r)
		}
	}
	clear(s.live[len(live):])
	for ; s.arrived < len(s.requests) && s.requests[s.arrived].arrival <= s.now; s.arrived++ {
		if r := s.requests[s.arrived]; !r.completed {
			live = append(live, r)
		}
	}
	s.live = live
}

func (s *schedulerReplayState) expireRequests() {
	for _, r := range s.live {
		if r.completed || r.expired || r.dropped || r.expires > s.now {
			continue
		}
		r.expired = true
		kind := "expired-pending"
		if r.dispatched {
			kind = "expired-queued"
		}
		if r.running {
			kind = "expired-running"
		} else if r.returning {
			kind = "expired-returning"
		}
		key := r.identity
		if s.out.outcomes[key] == nil {
			s.out.outcomes[key] = map[string]int{}
		}
		s.out.outcomes[key][kind]++
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
				r.completion = s.now + r.returnMS*1e6
				if !math.IsInf(r.native, 1) {
					r.completion = math.Max(s.now, r.native) + r.returnMS*1e6
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
				r.completion = s.now + r.returnMS*1e6
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
			if !r.expired && !r.dropped {
				latency := float64(r.source.Elapsed) + r.completion - r.arrival
				s.out.samples[key] = append(s.out.samples[key], latency)
				if s.out.outcomes[key] == nil {
					s.out.outcomes[key] = map[string]int{}
				}
				s.out.outcomes[key]["completed"]++
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
	for _, r := range s.live {
		if r.completed || r.expired || r.dispatched || r.arrival > s.now {
			continue
		}
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
		for j, i := range s.instances {
			s.weights[j] = i.config.Weight
			routes := s.snap.Routing
			if s.routingApplied {
				routes = s.applied.Routing
			}
			if route, ok := routes[r.source.Definition.UID]; ok {
				s.weights[j] = route[i.config.ID]
			}
			if r.observed && !s.targetHandles(r) {
				s.weights[j] = i.config.Weight
			}
			s.available[j] = i.loaded && i.ready && !i.stopped && i.outstanding < i.config.Capacity && i.config.WorkloadUID == uid && schedulerProfileMatches(r.source.Definition, i.config.Profile)
		}
		selected := SmoothWeightedRoute(s.weights, credits, s.available)
		if selected < 0 {
			continue
		}
		i := s.instances[selected]
		r.dispatched = true
		r.source.Instance = i.config.ID
		r.senderMS = math.Max(0, float64(r.source.Elapsed)+s.now-r.arrival)/1e6 + r.senderReserveMS
		r.native = s.now
		i.outstanding++
	}
}

func (s *schedulerReplayState) receiveNative() {
	for _, r := range s.live {
		if r.dispatched && !r.returning && !r.completed && r.native <= s.now {
			r.native = math.Inf(1)
			r.queueAt = s.now
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
	return firstfloor.RequestState{Context: firstfloor.RequestContext{RequestID: r.source.ID, WorkloadUID: d.UID, Definition: r.identity, ReceivedAt: r.source.ReceivedAt, SenderElapsedMS: r.senderMS, CompletionMarginMS: r.postMS, SLO: firstfloor.SLO{Percentile: d.SLO.Percentile, TargetMS: float64(d.SLO.TargetLatency) / 1e6}}, Phase: phase, ElapsedMS: (s.now - r.queueAt) / 1e6, QueueMS: (s.now - r.queueAt) / 1e6, ArrivalRate: rate}
}

func (s *schedulerReplayState) chooseBatches(ctx context.Context) bool {
	for _, i := range s.instances {
		if !i.loaded || !i.ready || len(i.queue) == 0 {
			continue
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
				copy(i.queue[index:], i.queue[index+1:])
				i.queue[len(i.queue)-1] = nil
				i.queue = i.queue[:len(i.queue)-1]
				if choice.Reason == firstfloor.ReasonDeadlineDefer {
					r.deferred = true
					i.queue = append(i.queue, r)
				} else {
					r.dropped = true
					r.completed = true
					i.outstanding--
					key := r.identity
					if s.out.outcomes[key] == nil {
						s.out.outcomes[key] = map[string]int{}
					}
					s.out.outcomes[key]["timeout"]++
					if s.capacity != nil {
						s.capacity.failed = true // Any timeout already fails a capacity window.
						return false
					}
				}
				i.wake = s.now
				continue
			}
			if len(i.batch) > 0 || i.wake > s.now {
				break
			}
			if choice.WaitMS > 0 {
				i.wake = s.now + choice.WaitMS*1e6
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
			i.finish = i.nativeFinish + i.occupancy*float64(len(i.batch))
			s.addBatchEnergy(i, b, duration)
			break
		}
	}
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
