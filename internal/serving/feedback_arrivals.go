package serving

import (
	"context"
	"math"
	"slices"
)

// feedbackArrivalCohort contains candidate-invariant request templates only.
// Definition and captured context pointers refer to the immutable planner input.
type feedbackArrivalCohort struct {
	requests []*schedulerReplayRequest
	out      schedulerReplayResult
	ready    bool
}

func newFeedbackArrivalCohort(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot, loads []SchedulerLoad) (*feedbackArrivalCohort, error) {
	s := schedulerReplayState{
		settings: settings, snap: snapshot, horizon: float64(settings.ComparisonPeriod),
		out: schedulerReplayResult{definitions: map[string]*Definition{}},
	}
	ready := s.initializeArrivals(ctx, loads)
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
		preNanos: pre, accountedAgeNanos: pre,
	}
}

// marginNanos is r's POST reserve on instance i, as the runtime gateway uses
// it: frozen once routed, otherwise the path's own POST or the configured margin.
func (s *schedulerReplayState) marginNanos(r *schedulerReplayRequest, i *schedulerReplayInstance) float64 {
	if r.dispatched {
		return r.marginNanos
	}
	if path, ok := s.feedbackFor(r.source.Definition).pathPost(i.config.ID); ok {
		return path.PostNanos
	}
	return s.settings.CompletionMarginMS * 1e6
}
