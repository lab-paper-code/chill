package serving

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"
)

// feedbackArrivalCohort contains candidate-invariant request templates only.
// Definition and captured context pointers refer to the immutable planner input.
type feedbackArrivalCohort struct {
	requests []*schedulerReplayRequest
	out      schedulerReplayResult
	ready    bool
}

func newFeedbackArrivalCohort(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot) (*feedbackArrivalCohort, error) {
	s := schedulerReplayState{
		settings: settings, snap: snapshot, horizon: float64(settings.ComparisonPeriod),
		out: schedulerReplayResult{definitions: map[string]*Definition{}},
	}
	ready := s.initializeArrivals(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &feedbackArrivalCohort{requests: s.requests, out: s.out, ready: ready}, nil
}

func (s *schedulerReplayState) initializeArrivalCohort(ctx context.Context, cohort *feedbackArrivalCohort) bool {
	// Copy mutable state into one replay-owned allocation. Never share RNG,
	// dispatch, lifetime, completion, or potential state between candidates.
	requests := make([]schedulerReplayRequest, len(cohort.requests))
	s.requests = slices.Grow(s.requests, len(requests))
	for index, template := range cohort.requests {
		if ctx.Err() != nil {
			return false
		}
		requests[index] = *template
		s.requests = append(s.requests, &requests[index])
	}
	for identity, definition := range cohort.out.definitions {
		s.out.definitions[identity] = definition
	}
	s.out.observedArrivals = cohort.out.observedArrivals
	s.out.reasons = appendUnique(s.out.reasons, cohort.out.reasons...)
	s.out.sources = appendUnique(s.out.sources, cohort.out.sources...)
	s.out.assumptions = appendUnique(s.out.assumptions, cohort.out.assumptions...)
	return cohort.ready
}

func (s *schedulerReplayState) feedbackArrivalRequest(source SchedulerRequest, identity string, at float64) *schedulerReplayRequest {
	feedback := s.feedbackFor(source.Definition)
	pre := feedback.TotalNanos - feedback.PostNanos
	return &schedulerReplayRequest{
		source: source, identity: identity, arrival: at,
		native: math.Inf(1), expires: math.Inf(1),
		preNanos: pre, accountedAge: pre, marginNanos: feedback.PostNanos,
	}
}

// addMinimumDemand preserves the repeated measured pattern and supplements only
// its configured demand deficit. Uniform supplement times are a model assumption,
// not reconstructed observations. Every candidate receives the same finite cohort.
func (s *schedulerReplayState) addMinimumDemand(ctx context.Context) bool {
	if s.capacity != nil || len(s.settings.MinimumDemand) == 0 || s.horizon <= 0 {
		return true // explicit Poisson rate experiments never acquire a demand floor
	}
	counts := make(map[string]int, len(s.snap.Workloads))
	for _, request := range s.requests {
		if !request.observed {
			counts[request.source.Definition.UID]++
		}
	}
	added := false
	for _, definition := range s.snap.Workloads {
		floor := int(math.Ceil(s.settings.MinimumDemand[definition.UID] * s.settings.ComparisonPeriod.Seconds()))
		deficit := floor - counts[definition.UID]
		if deficit <= 0 {
			continue
		}
		identity := definition.Identity()
		for index := range deficit {
			if ctx.Err() != nil {
				return false
			}
			at := float64(index) / float64(deficit) * s.horizon
			source := SchedulerRequest{
				ID: fmt.Sprintf("minimum-demand-%s-%d", definition.UID, index), Definition: definition,
				ReceivedAt: s.snap.At.Add(time.Duration(at)), Phase: phasePending,
			}
			request := s.feedbackArrivalRequest(source, identity, at)
			request.minimumDemand = true
			s.requests = append(s.requests, request)
		}
		s.out.definitions[identity] = definition
		added = true
	}
	if added {
		// Existing equal-time order remains intact; supplements follow it.
		sort.SliceStable(s.requests, func(a, b int) bool { return s.requests[a].arrival < s.requests[b].arrival })
		s.out.sources = appendUnique(s.out.sources, "minimum-demand")
		s.out.assumptions = appendUnique(s.out.assumptions, "minimum-demand: configured finite-cohort deficit uses uniformly spaced synthetic arrivals, not measured traffic")
	}
	return true
}
