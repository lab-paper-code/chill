package serving

import (
	"context"
	"math"
	"math/rand/v2"
	"sort"
	"strings"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	"gonum.org/v1/gonum/stat"
)

// FeedbackWorkloadScore separates replayed finite outcomes from conditional
// compute potential. Null quantile means no successful completion.
type FeedbackWorkloadScore struct {
	UID                  string   `json:"uid"`
	Population           int      `json:"population"`
	ObservedArrivals     int      `json:"observedArrivals"`
	ReplayedArrivals     int      `json:"replayedArrivals"`
	MinimumDemandArrivals int     `json:"minimumDemandArrivals"`
	Completed            int      `json:"completed"`
	DeadlineDropped      int      `json:"deadlineDropped"`
	LifetimeExpired      int      `json:"lifetimeExpired"`
	Failed               int      `json:"failed"`
	Unfinished           int      `json:"unfinished"`
	FailureFraction      float64  `json:"failureFraction"`
	SuccessQuantileNanos *float64 `json:"successQuantileNanos"`
	PotentialExcess      *float64 `json:"potentialExcess"`
	PotentialUnavailableReasons []string `json:"potentialUnavailableReasons,omitempty"`
	Satisfied            bool     `json:"satisfied"`
}

// FeedbackCandidateResult owns scalars and reduced evidence, never replay state
// or raw request/profile populations. SLO shares the existing consumer reducer.
type FeedbackCandidateResult struct {
	Workloads          []FeedbackWorkloadScore      `json:"workloads"`
	InvalidReasons     []string                     `json:"invalidReasons"`
	SLO                []SchedulerEstimate          `json:"slo"`
	ConditionalService *ConditionalServiceEvidence `json:"conditionalService,omitempty"`
	Assumptions        []string                    `json:"assumptions,omitempty"`
	Sources            []string                    `json:"sources,omitempty"`
}

// EvaluateFeedbackCandidate drains the finite comparison-period arrival cohort
// through the actual native policy. Planner energy remains owned by its caller.
func EvaluateFeedbackCandidate(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot, candidate SchedulerCandidate, seed uint64) (FeedbackCandidateResult, error) {
	if err := ctx.Err(); err != nil {
		return FeedbackCandidateResult{}, err
	}
	var invalid []string
	for _, request := range snapshot.Outstanding {
		if request.Phase == phaseReturning && (request.NativeOutcome == firstfloor.OutcomeDeadlineDrop || request.NativeOutcome == firstfloor.OutcomePreprocessFailed || request.NativeOutcome == firstfloor.OutcomeBatchError) {
			continue // known terminal failure needs no new execution prediction
		}
		if request.Phase == phaseQueued || request.Phase == phaseDeferred || request.Phase == phaseRunning || request.Phase == phaseReturning {
			if request.NativeSenderElapsedMS == nil || request.NativeElapsedNanos == nil {
				invalid = appendUnique(invalid, "MissingNativeElapsedContext:"+request.ID)
			}
		}
	}
	if len(invalid) > 0 {
		return FeedbackCandidateResult{InvalidReasons: invalid}, nil
	}
	// Empty compared set disables node-energy estimation, not physical action,
	// service, ownership, readiness, occupancy, or routing behavior.
	replay, err := schedulerReplay(ctx, settings, snapshot, candidate, rand.New(rand.NewPCG(seed, 0)), map[NodeReference]bool{}, nil)
	if err != nil {
		return FeedbackCandidateResult{}, err
	}
	conditional := replay.conditional
	return FeedbackCandidateResult{
		Workloads: replay.feedback, InvalidReasons: appendUnique(replay.reasons, replay.invalidReasons...),
		SLO: feedbackReplayEstimates(snapshot, replay), ConditionalService: &conditional,
		Assumptions: replay.assumptions, Sources: replay.sources,
	}, nil
}

func (s *schedulerReplayState) feedbackFor(definition *Definition) PathFeedback {
	for _, feedback := range s.snap.PathFeedback {
		if feedback.WorkloadUID == definition.UID {
			return feedback
		}
	}
	return PathFeedback{WorkloadUID: definition.UID, TotalNanos: s.settings.CompletionMarginMS * 1e6, PostNanos: s.settings.CompletionMarginMS * 1e6, Source: "configured"}
}

func feedbackReplayEstimates(snapshot SchedulerSnapshot, replay schedulerReplayResult) []SchedulerEstimate {
	definitions := make(map[string]*Definition, len(replay.definitions)+len(snapshot.Workloads))
	for key, definition := range replay.definitions {
		definitions[key] = definition
	}
	for _, definition := range snapshot.Workloads {
		definitions[definition.Identity()] = definition
	}
	keys := make([]string, 0, len(definitions))
	for key := range definitions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	estimates := make([]SchedulerEstimate, 0, len(keys))
	for _, key := range keys {
		definition := definitions[key]
		samples := replay.samples[key]
		estimate := SchedulerEstimate{UID: definition.UID, Definition: key, Samples: len(samples), Percentile: definition.SLO.Percentile, TargetNanos: int64(definition.SLO.TargetLatency), PredictedOutcomes: replay.outcomes[key]}
		if len(samples) > 0 {
			sort.Float64s(samples)
			quantile := stat.Quantile(definition.SLO.Percentile/100, stat.Empirical, samples, nil)
			estimate.ExpectedNanos = &quantile
			estimate.NormalizedExceedance = max(0, quantile/float64(definition.SLO.TargetLatency)-1)
		}
		estimates = append(estimates, estimate)
	}
	return estimates
}

func (s *schedulerReplayState) finishFeedback() {
	type population struct {
		score      FeedbackWorkloadScore
		definition *Definition
		successes  []float64
		potentials []float64
		missing    bool
		fixedFailure bool
		sampleGroups int
	}
	populations := map[string]*population{}
	for _, definition := range s.snap.Workloads {
		populations[definition.UID] = &population{score: FeedbackWorkloadScore{UID: definition.UID}, definition: definition}
	}
	for _, request := range s.requests {
		definition := request.source.Definition
		population := populations[definition.UID]
		if population == nil {
			population = &population{score: FeedbackWorkloadScore{UID: definition.UID}, definition: definition}
			populations[definition.UID] = population
		}
		population.score.Population++
		if request.minimumDemand {
			population.score.MinimumDemandArrivals++
		} else if !request.observed {
			population.score.ReplayedArrivals++
		}
		switch {
		case request.dropped:
			population.score.DeadlineDropped++
		case request.failed:
			population.score.Failed++
		case request.expired:
			population.score.LifetimeExpired++
		case request.completed:
			population.score.Completed++
			continue
		default:
			population.score.Unfinished++
			if s.out.outcomes[request.identity] == nil {
				s.out.outcomes[request.identity] = map[string]int{}
			}
			s.out.outcomes[request.identity]["unfinished"]++
		}
		if request.observed && request.source.Phase == phaseReturning {
			population.fixedFailure = true
			continue // no remaining compute alternative for this terminal request
		}
		if !request.hasPotential && !request.dropped && !request.failed && !request.expired {
			s.capturePotential(request, s.byID[request.source.Instance])
		}
		if !request.hasPotential {
			if !population.missing {
				s.out.invalidReasons = appendUnique(s.out.invalidReasons, "MissingConditionalFailurePotential:"+definition.UID)
			}
			population.missing = true
		} else {
			population.potentials = append(population.potentials, request.potential)
		}
	}
	for key, samples := range s.out.samples {
		population := populations[s.out.definitions[key].UID]
		if population.sampleGroups == 0 {
			population.successes = samples // reuse single-definition population
		} else {
			if population.sampleGroups == 1 {
				combined := make([]float64, len(population.successes), population.score.Completed)
				copy(combined, population.successes)
				population.successes = combined
			}
			population.successes = append(population.successes, samples...)
		}
		population.sampleGroups++
	}
	for _, population := range populations {
		score := population.score
		score.ObservedArrivals = s.out.observedArrivals[score.UID]
		percentile := population.definition.SLO.Percentile / 100
		if score.Population > 0 {
			score.FailureFraction = float64(score.DeadlineDropped+score.Failed+score.LifetimeExpired+score.Unfinished) / float64(score.Population)
		}
		if len(population.successes) > 0 {
			sort.Float64s(population.successes)
			quantile := stat.Quantile(percentile, stat.Empirical, population.successes, nil)
			score.SuccessQuantileNanos = &quantile
		}
		if population.fixedFailure {
			score.PotentialUnavailableReasons = []string{"TerminalNativeOutcomeNoExecutionAlternative"}
		}
		if !population.missing && (!population.fixedFailure || len(population.potentials) > 0) {
			potential := 0.0
			if len(population.potentials) > 0 {
				sort.Float64s(population.potentials)
				potential = stat.Quantile(percentile, stat.Empirical, population.potentials, nil)
			}
			score.PotentialExcess = &potential
		}
		score.Satisfied = score.FailureFraction == 0 && (score.Population == 0 || score.SuccessQuantileNanos != nil && *score.SuccessQuantileNanos <= float64(population.definition.SLO.TargetLatency))
		s.out.feedback = append(s.out.feedback, score)
	}
	sort.Slice(s.out.feedback, func(a, b int) bool { return s.out.feedback[a].UID < s.out.feedback[b].UID })
}

// capturePotential queries only currently assemblable same-definition batches.
// It never samples again, predicts a dropped completion, or invents future fill.
func (s *schedulerReplayState) capturePotential(request *schedulerReplayRequest, assigned *schedulerReplayInstance) {
	if request.hasPotential || s.capacity != nil {
		return
	}
	best := math.Inf(1)
	for _, instance := range s.instances {
		if assigned != nil && instance != assigned {
			continue // accepted native work never teleports to another actor
		}
		if instance.config.Profile == nil || !schedulerProfileMatches(request.source.Definition, instance.config.Profile) || !schedulerOwnerAllows(instance.config.OwnerWorkloadUID, request.source.Definition.UID) {
			continue
		}
		wait, ready := s.potentialReady(instance, request)
		if !ready {
			continue
		}
		base := request.accountedAge + s.now - request.arrival + wait + request.marginNanos
		if request.returning {
			if request.source.NativeOutcome == "" || request.source.NativeOutcome == "completed" {
				best = min(best, request.accountedAge+request.completion-request.arrival)
			}
			continue
		}
		if request.running {
			// initializeRunning already drew conditioned residual; reuse it.
			best = min(best, base+max(0, instance.finish-s.now))
			continue
		}
		count, contains := 0, false
		if !request.dispatched || request.source.Phase == phaseAssigned {
			count = 1
		}
		for _, queued := range instance.queue {
			if queued.expired || queued.dropped || queued.deferred != request.deferred {
				continue
			}
			if queued.identity == request.identity {
				count++
				contains = contains || queued == request
			} else if request.deferred {
				if contains {
					break
				}
				count = 0
			}
		}
		gap := instance.idleNanos(s.now)
		if !instance.ready {
			gap = nil
		}
		for _, batch := range instance.config.Profile.Batches {
			if batch.Batch > instance.config.MaxBatch || batch.Batch > count || batch.ServiceDistribution == nil {
				continue
			}
			service, support, ok := batch.ServiceDistribution.QuantileNanos(gap, request.source.Definition.SLO.Percentile)
			s.out.conditional.record(gap, support)
			if ok {
				best = min(best, base+float64(service)+float64(batch.Batch)*instance.occupancy)
			}
		}
	}
	if !math.IsInf(best, 1) {
		request.potential = max(0, best/float64(request.source.Definition.SLO.TargetLatency)-1)
		request.hasPotential = true
	}
}

func (s *schedulerReplayState) potentialReady(instance *schedulerReplayInstance, request *schedulerReplayRequest) (float64, bool) {
	if request.dispatched {
		return 0, instance.loaded && instance.ready
	}
	if instance.config.WorkloadUID != request.source.Definition.UID {
		return 0, false
	}
	routing := s.snap.Routing
	if s.routingApplied {
		routing = s.applied.Routing
	}
	weight := instance.config.Weight
	if route, explicit := routing[request.source.Definition.UID]; explicit {
		weight = route[instance.config.ID]
	}
	if instance.loaded && instance.ready && !instance.stopped && weight > 0 {
		return 0, true
	}
	// Future potential needs both a sampled preparation and a fixed route DAG.
	// Unsampled operations and unresolved external prerequisites stay invalid.
	readyAt := math.Inf(1)
	for _, action := range s.actions {
		if action.action.Kind == schedulerActionPrepare && action.action.Instance == instance.config.ID && action.started && !action.done {
			readyAt = action.finish
		}
	}
	if !schedulerFinite(readyAt) {
		return 0, false
	}
	for _, action := range s.actions {
		if action.action.Kind != schedulerActionRoute || action.done {
			continue
		}
		target := s.goal
		if action.action.RouteTarget != nil {
			target = *action.action.RouteTarget
		}
		routed := false
		for _, wanted := range target.Instances {
			if s.byID[wanted.ID] != instance || wanted.WorkloadUID != request.source.Definition.UID {
				continue
			}
			weight := wanted.Weight
			if route, explicit := target.Routing[wanted.WorkloadUID]; explicit {
				weight = route[wanted.ID]
			}
			routed = weight > 0
		}
		at, known := readyAt, routed
		for _, id := range action.action.DependsOn {
			dependency := s.actionByID[id]
			if dependency == nil || !dependency.done && (!dependency.started || !schedulerFinite(dependency.finish)) {
				known = false
				break
			}
			if !dependency.done {
				at = max(at, dependency.finish)
			}
		}
		if known {
			return max(0, at-s.now), true
		}
	}
	return 0, false
}

// Preserve the existing failure vocabulary for consumers of Candidate.SLO.
func feedbackFailedOutcome(kind string) bool {
	return kind == "timeout" || kind == "unfinished" || kind == firstfloor.OutcomePreprocessFailed || kind == firstfloor.OutcomeBatchError || strings.HasPrefix(kind, "expired-")
}
