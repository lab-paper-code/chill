package serving

import (
	"context"
	"math/rand/v2"
)

// FeedbackTrace exposes the same finite-cohort replay used by feedback admission.
// Successful request latencies and physically started batch services remain
// separate populations. Outcomes retain rejected, failed and unfinished requests.
// This diagnostic does not select plans or impose scientific pass/fail criteria.
type FeedbackTrace struct {
	LatencyNanos       map[string][]float64       `json:"latencyNanos"`
	BatchServiceNanos  []int64                    `json:"batchServiceNanos"`
	Outcomes           map[string]map[string]int  `json:"outcomes"`
	InvalidReasons     []string                   `json:"invalidReasons"`
	ConditionalService ConditionalServiceEvidence `json:"conditionalService"`
}

// ReplayFeedbackTrace repeats the observed arrival pattern over ComparisonPeriod,
// exactly as EvaluateFeedbackCandidate. Set ObservationWindow = ComparisonPeriod
// for one finite trace, and ObservationEnd to the end of its arrival window.
// Snapshot.At anchors simulation time, not the original cross-host wall clock.
func ReplayFeedbackTrace(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot, candidate SchedulerCandidate, seed uint64) (FeedbackTrace, error) {
	var services []int64
	replay, err := schedulerReplayWithTrace(ctx, settings, snapshot, candidate, rand.New(rand.NewPCG(seed, 0)), map[NodeReference]bool{}, nil, nil, &services)
	return FeedbackTrace{LatencyNanos: replay.samples, BatchServiceNanos: services, Outcomes: replay.outcomes,
		InvalidReasons: replay.reasons, ConditionalService: replay.conditional}, err
}
