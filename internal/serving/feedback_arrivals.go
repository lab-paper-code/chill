package serving

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

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
