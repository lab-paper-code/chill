package serving

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

// Decision statuses and reasons of the capacity planner (analytic-planner §3, §4).
const (
	schedulerStatusKeep              = "Keep"
	schedulerRouteWeightExceeds      = "RouteWeightExceedsCapacity"
	schedulerNoPlanMeetsSLO          = "NoPlanMeetsSLO"
	schedulerPlanInfeasibleResources = "InfeasibleResources"
	schedulerPlanFixedWorkload       = "FixedWorkload"
	schedulerPlanPreparationFailed   = "PreparationFailed"
	schedulerPlanHeld                = "HeldAfterOverload"
	// schedulerPartialObservation waits for one control interval of complete
	// arrival observation after the process starts; until one observation
	// window is observed it names the node plans that remove or replace.
	schedulerPartialObservation = "PartialObservationWindow"
	// schedulerReplacementUnpaid names a node whose replacement did not repay
	// its transition at the least load of the observation window.
	schedulerReplacementUnpaid = "ReplacementUnpaid"
	// schedulerGainWithinUncertainty keeps the current configuration when the
	// chosen transition saves no more than the selected evidence's conditional B.
	schedulerGainWithinUncertainty = "TransitionGainWithinUncertainty"
	// schedulerRoutedStopsFirst names a node plan that stops a routed
	// instance before its route.
	schedulerRoutedStopsFirst = "RoutedInstanceStopsBeforeRoute"

	schedulerRouteUnits = 1000 // route weights of one Workload sum to this

	// schedulerTargetBaselineDelta trials subtract a baseline the cost reports.
	schedulerTargetBaselineDelta = "target-baseline-delta"
	schedulerWholeWall           = "whole-wall"
)

// SchedulerCapacity records the capacity-sum judgment of a decision: the
// comparison period T, the chosen route time r, the plan search, each
// controlled Workload in the chosen plan set and every node plan with its
// eligibility.
type SchedulerCapacity struct {
	Period    time.Duration               `json:"periodNanos"`
	Route     time.Duration               `json:"routeNanos"`
	Search    *SchedulerPlanSearch        `json:"search,omitempty"`
	Workloads []SchedulerCapacityWorkload `json:"workloads"`
	Plans     []SchedulerNodePlan         `json:"plans"`
	// GainUncertaintyJoules is the selected-evidence conditional B threshold.
	// It excludes profile statistical uncertainty and leaves nominal energy unchanged.
	GainUncertaintyJoules float64                               `json:"gainUncertaintyJoules,omitempty"`
	TransitionReserves    []SchedulerTransitionReserve          `json:"transitionReserves,omitempty"`
	BridgeStages          []SchedulerBridgeStage                `json:"bridgeStages,omitempty"`
	EnergyComparison      SchedulerEnergyComparison             `json:"energyComparison"`
	MissingCells          []SchedulerMissingEnergyCell          `json:"missingCells,omitempty"`
	UndecidableCandidates []SchedulerUndecidableCandidateRecord `json:"undecidableCandidates,omitempty"`
}

// SchedulerPlanSearch is the scope of one plan search: the most Nodes one step
// changed in the last neighborhood it searched (k), the plan sets it
// evaluated, its time, and whether the time budget (half the control
// interval) ended it before its last step.
type SchedulerPlanSearch struct {
	Neighborhood int `json:"neighborhood"`
	// Evaluated counts plan-set candidates in neighborhood sweeps, excluding
	// initial keep/empty origins; it is not Search.Examined's node-plan count.
	Evaluated int           `json:"evaluated"`
	Duration  time.Duration `json:"durationNanos"`
	// SurrogateElapsed covers v2 Candidates only. ExactElapsed covers backend
	// exact search, keep seeding and filters, including v1 fallback; neither
	// includes scalar table/problem preparation or native diagnostic projection.
	SurrogateElapsed time.Duration `json:"surrogateElapsedNanos"`
	ExactElapsed     time.Duration `json:"exactElapsedNanos"`
	// StartedAt and FinishedAt are direct wall-clock boundaries of search,
	// excluding node-plan construction and final decision projection.
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	// BudgetCut marks wall-clock expiration in both v1 and v2.
	BudgetCut         bool                                  `json:"budgetCut,omitempty"`
	Excluded          map[string]int                        `json:"excluded,omitempty"`
	ReserveRejections map[string]SchedulerTransitionReserve `json:"reserveRejections,omitempty"`
	Steps             []SchedulerPlanSearchStep             `json:"steps,omitempty"`
	TrailingEvaluated int                                   `json:"trailingEvaluated,omitempty"`
	// TerminationReason is noImprovement/budgetCut in v1 and noImprovement/evaluationBudget/deadline in v2.
	TerminationReason string `json:"terminationReason,omitempty"`
	Strategy          string `json:"strategy,omitempty"`
	// Key and Joules identify the selected exact candidate before postfilters.
	Key                 string         `json:"key,omitempty"`
	Joules              float64        `json:"joules,omitempty"`
	Pruned              map[string]int `json:"pruned,omitempty"`
	SurrogateEvaluated  int            `json:"surrogateEvaluated,omitempty"`
	Finalists           int            `json:"finalists,omitempty"`
	ExcludedUndecidable int            `json:"excludedUndecidable"`
	ExactEvaluated      int            `json:"exactEvaluated,omitempty"`
	SurrogateGapJoules  float64        `json:"surrogateGapJoules,omitempty"`
	PostFilterReverts   int            `json:"postFilterReverts,omitempty"`
	Fallback            string         `json:"fallback,omitempty"`
	DualBound           float64        `json:"dualBound,omitempty"`
	Start               string         `json:"start,omitempty"`
}

// SchedulerPlanSearchStep records each start's origin (k = 0) and accepted moves
// in decision-time order. In v1, both origins precede k = 2 then k = 3;
// neighborhoods without accepted moves have no entry. Compare choices within
// the same Start.
// ChangedNodes is relative to that start's previous choice. Better means feasible,
// final, descending exceedances, then joules/keep/key: joules only cannot increase
// between feasible points, and may increase when recovering feasibility.
// Evaluated counts the accepting sweep; origins are 0 because the existing
// aggregate excludes initial evaluations. Across both starts, sum(Evaluated) +
// TrailingEvaluated = search.Evaluated; trailing counts every unaccepted sweep,
// including k = 2 local optima, not just the final sweep. Duration is measured
// on this goroutine around the initial evaluation or accepting sweep; their
// sum is <= search.Duration (unaccepted sweeps and bookkeeping are not steps).
// BudgetCut marks only the last recorded point when the search budget expires;
// that point need not be the selected endpoint. Final means final-capacity
// feasible, not the last step or the chosen end. Errors return no decision.
//
// V2 records keep, empty and optional priced origins, followed by each start's
// cover, polish and combine moves. K is the number of changed Nodes (1 through
// the Node count), not a neighborhood bound. Evaluated counts incremental
// evaluations in the accepting cover iteration, polish sweep or combine search;
// origins remain 0. Sum(Evaluated) + TrailingEvaluated = search.SurrogateEvaluated,
// including initial evaluations and all unaccepted sweeps in the trailing count.
// Step Joules is the incremental surrogate and can differ from the funnel's
// exact Joules. KeepJoules, Feasible and Final also come from that incremental key.
// Phase identifies cover, polish or combine moves; origins leave it empty.
// Cover can trade capacity for count-limit recovery from an unbounded point;
// its feasibility and energy need not improve. Polish/combine strictly improve.
type SchedulerPlanSearchStep struct {
	Start            string          `json:"start"`
	Phase            string          `json:"phase,omitempty"`
	K                int             `json:"k"`
	ChangedNodes     []NodeReference `json:"changedNodes,omitempty"`
	Joules           float64         `json:"joules"`
	Feasible         bool            `json:"feasible"`
	Final            bool            `json:"final"`
	ComparisonWindow time.Duration   `json:"comparisonWindowNanos,omitempty"`
	KeepJoules       float64         `json:"keepJoules,omitempty"`
	Evaluated        int             `json:"evaluated"`
	Duration         time.Duration   `json:"durationNanos"`
	BudgetCut        bool            `json:"budgetCut,omitempty"`
}

// SchedulerCapacityWorkload separates routing heuristics and current tested-rate evidence.
type SchedulerCapacityWorkload struct {
	UID                 string                  `json:"uid"`
	Rate                float64                 `json:"rate"`
	MinimumDemand       float64                 `json:"minimumDemand"`
	Queued              int                     `json:"queued"`
	RoutingRate         float64                 `json:"routingRate"`
	RetainedRoutingRate float64                 `json:"retainedRoutingRate"`
	Tests               []SchedulerFeedbackRate `json:"tests"`
	KeepTests           []SchedulerFeedbackRate `json:"keepTests"`
	Weights             map[string]int32        `json:"weights"`
}

// SchedulerFeedbackRate records tested queries, not a complete grid certificate.
type SchedulerFeedbackRate struct {
	InstanceID   string        `json:"instanceID"`
	Input        CapacityInput `json:"input"`
	PathFeedback PathFeedback  `json:"pathFeedback"`
	FeedbackCapacity
}

// SchedulerNodePlan records profile routing sums, transition timing and original energy.
type SchedulerNodePlan struct {
	Node               NodeReference       `json:"node"`
	Key                string              `json:"key"`
	Eligible           bool                `json:"eligible"`
	Reason             string              `json:"reason,omitempty"`
	PreparationFailure *PreparationFailure `json:"preparationFailure,omitempty"`
	// RequiresBridge is a conditional candidate, never permission to stop first.
	RequiresBridge          []string                       `json:"requiresBridge,omitempty"`
	RoutingRate             map[string]float64             `json:"routingRate,omitempty"`
	RetainedRoutingRate     map[string]float64             `json:"retainedRoutingRate,omitempty"`
	Pre                     time.Duration                  `json:"preNanos"`
	Post                    time.Duration                  `json:"postNanos"`
	Joules                  float64                        `json:"joules"`
	EnergyUncertaintyJoules float64                        `json:"energyUncertaintyJoules,omitempty"`
	ActionCosts             []SchedulerActionCost          `json:"actionCosts,omitempty"`
	IdleCosts               map[string]IdlePowerProvenance `json:"idleCosts,omitempty"`
}

// SchedulerMissingEnergyCell names the target measurement, never its transferred estimate.
type SchedulerMissingEnergyCell struct {
	Node      NodeReference `json:"node"`
	Phase     string        `json:"phase,omitempty"`
	Kind      string        `json:"kind,omitempty"`
	PowerMode string        `json:"powerMode,omitempty"`
	From      string        `json:"from,omitempty"`
	To        string        `json:"to,omitempty"`
	State     []string      `json:"state,omitempty"`
	Required  string        `json:"required"`
}

type SchedulerEnergyComparison struct {
	Status          string `json:"status"`
	MissingCellRefs []int  `json:"missingCellRefs,omitempty"`
}

type SchedulerUndecidableCandidate struct {
	Key           string                       `json:"key"`
	MissingCells  []SchedulerMissingEnergyCell `json:"missingCells"`
	Excluded      bool                         `json:"excluded"`
	ReplaySkipped bool                         `json:"replaySkipped,omitempty"`
}

// SchedulerUndecidableCandidateRecord references the decision's shared cell table.
type SchedulerUndecidableCandidateRecord struct {
	Key             string `json:"key"`
	MissingCellRefs []int  `json:"missingCellRefs"`
	Excluded        bool   `json:"excluded"`
	ReplaySkipped   bool   `json:"replaySkipped,omitempty"`
}

// EvaluateScheduler uses cheap profile heuristics and exact finite-demand finalists.
// Offline BuildCapacityRow/CapacityTable remain explicit full-grid analysis APIs.
func EvaluateScheduler(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot) (SchedulerDecision, error) {
	started := time.Now()
	return evaluateScheduler(ctx, settings, snapshot, started, started.Add(settings.ControlInterval/2))
}

func evaluateScheduler(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot, started, deadline time.Time) (decision SchedulerDecision, err error) {
	timing := SchedulerEvaluationTiming{}
	stage, stageStarted := &timing.Preparation, started
	defer func() {
		*stage += time.Since(stageStarted)
		decision.EvaluationTiming = timing
	}()
	if err := ctx.Err(); err != nil {
		return SchedulerDecision{}, err
	}
	settings = settings.Clone()
	snapshot = schedulerReplayScopedSnapshot(schedulerCanonical(snapshot))
	decision = SchedulerDecision{At: snapshot.At, Status: schedulerStatusPending, Settings: settings, Search: schedulerSearchPopulation(ctx, settings, snapshot), Workloads: schedulerWorkloadStates(settings, snapshot), References: slices.Clone(snapshot.References)}
	decision.Reasons = appendUnique(decision.Reasons, schedulerReplayInitialScope(snapshot).evidence...)
	fixSchedulerWorkloads(&decision, snapshot)
	keep := SchedulerCandidate{ID: "keep", Keep: true, Nodes: snapshot.Nodes, Instances: slices.Clone(snapshot.Instances), Routing: schedulerInstanceRouting(ctx, snapshot.Workloads, snapshot.Instances)}
	for uid, routing := range snapshot.Routing {
		keep.Routing[uid] = routing
	}
	if err := settings.Validate(); err != nil {
		decision.Reasons, decision.Keep = []string{err.Error()}, keep
		return decision, ctx.Err()
	}
	// Keep carries only already-authorized Fixed completion obligations.
	keep.Nodes = schedulerCommonNodes(snapshot)
	p := schedulerPlanState{snap: snapshot, target: &keep, actions: []Action{}, nodeTail: map[string]string{}, prepares: []string{}, stops: map[string]string{}}
	p.changeNodes(keep)
	keep.Actions = p.actions
	keep.Status = schedulerStatusPending
	decision.Keep = keep
	reasons := append(slices.Clone(snapshot.Reasons), schedulerValidateSnapshot(snapshot)...)
	reasons = appendUnique(reasons, schedulerValidateResources(settings, snapshot.Nodes, snapshot.Instances)...)
	if len(reasons) != 0 {
		decision.Keep.Reasons, decision.Reasons = reasons, reasons
		return decision, ctx.Err()
	}
	planner := newCapacityPlanner(ctx, settings, snapshot, keep, decision)
	planner.fixCapacityWorkloads(&decision)
	if len(planner.ws) == 0 {
		if len(snapshot.Instances) == 0 && slices.ContainsFunc(decision.Workloads, func(w SchedulerWorkloadState) bool {
			return strings.HasPrefix(w.Reason, CapacityPending+":") || strings.HasPrefix(w.Reason, CapacityRejected+":")
		}) {
			decision.Status = CapacityRejected
			if slices.ContainsFunc(decision.Workloads, func(w SchedulerWorkloadState) bool {
				return strings.HasPrefix(w.Reason, CapacityPending+":")
			}) {
				decision.Status = CapacityPending
			}
			decision.Keep.Actions = nil
			return decision, ctx.Err()
		}
		zero := 0.0
		decision.Keep.Status, decision.Keep.ExpectedJoules = schedulerStatusFeasible, &zero
		target := decision.Keep
		decision.Target, decision.Status = &target, schedulerStatusKeep
		markFixedEstimates(&decision)
		return decision, ctx.Err()
	}
	// λ needs one control interval of complete observation (after a restart),
	// and until a whole observation window is observed plans only expand.
	covered := decision.Search.WindowEnd.Sub(decision.Search.WindowStart)
	if covered < min(settings.ObservationWindow, settings.ControlInterval) {
		decision.Status, decision.Reasons = schedulerStatusPending, []string{schedulerPartialObservation}
		return decision, ctx.Err()
	}
	planner.partial = covered < settings.ObservationWindow
	if err := planner.build(); err != nil {
		return SchedulerDecision{}, err
	}
	timing.Preparation = time.Since(stageStarted)
	stage, stageStarted = &timing.Search, time.Now()
	best, err := planner.search(deadline)
	timing.Surrogate, timing.Exact = planner.searched.SurrogateElapsed, planner.searched.ExactElapsed
	if err != nil {
		return SchedulerDecision{}, err
	}
	timing.Search = time.Since(stageStarted)
	stage, stageStarted = &timing.Payback, time.Now()
	if settings.PlanSearchStrategy != protocol.V2 || !settings.PlanPostFilterFinalists {
		best = planner.minimumGain(planner.payback(best))
	}
	timing.Payback = time.Since(stageStarted)
	stage, stageStarted = &timing.Rates, time.Now()
	if err := planner.testDecisionRates(best); err != nil {
		return SchedulerDecision{}, err
	}
	timing.Rates = time.Since(stageStarted)
	stage, stageStarted = &timing.Projection, time.Now()
	decision = planner.decide(decision, best)
	markFixedEstimates(&decision)
	return decision, ctx.Err()
}

// schedulerCanonical orders the snapshot lists the planner iterates, so the
// decision does not depend on input order.
func schedulerCanonical(s SchedulerSnapshot) SchedulerSnapshot {
	s.Nodes = slices.Clone(s.Nodes)
	slices.SortFunc(s.Nodes, func(a, b SchedulerNode) int { return cmp.Compare(a.Node.UID, b.Node.UID) })
	s.Instances = slices.Clone(s.Instances)
	slices.SortFunc(s.Instances, func(a, b SchedulerInstance) int { return cmp.Compare(a.ID, b.ID) })
	s.Workloads = slices.Clone(s.Workloads)
	slices.SortStableFunc(s.Workloads, func(a, b *Definition) int {
		switch { // nil last; validation reports it
		case a == nil && b == nil:
			return 0
		case a == nil:
			return 1
		case b == nil:
			return -1
		}
		return cmp.Compare(a.UID, b.UID)
	})
	s.Options = slices.Clone(s.Options)
	slices.SortStableFunc(s.Options, func(a, b SchedulerOption) int {
		if schedulerOptionLess(a, b) {
			return -1
		}
		if schedulerOptionLess(b, a) {
			return 1
		}
		return 0
	})
	s.Costs = slices.Clone(s.Costs)
	slices.SortFunc(s.Costs, func(a, b OperationCost) int { return cmp.Compare(a.ID, b.ID) })
	s.Baselines = slices.Clone(s.Baselines)
	slices.SortFunc(s.Baselines, func(a, b NodeBaseline) int { return cmp.Compare(a.ID, b.ID) })
	s.Actions = slices.Clone(s.Actions)
	slices.SortFunc(s.Actions, func(a, b Action) int { return cmp.Compare(a.ID, b.ID) })
	return s
}

type capacityPlanner struct {
	ctx                      context.Context
	settings                 SchedulerSettings
	snapshot                 SchedulerSnapshot
	keep                     SchedulerCandidate
	feedbackCache            sync.Map            // Tick-local exact replay results; v1 workers share immutable values
	feedbackCohort           capacityArrivalMemo // Tick-local immutable arrivals; mutable requests belong to each replay
	options                  []SchedulerOption
	pending                  []SchedulerInstance // uncreated commitments, counted once without capacity
	ws                       []capacityWorkload
	keepRates, selectedRates [][]SchedulerFeedbackRate
	index                    map[string]int
	plans                    [][]*capacityPlan // per Node; plans[n][0] is keep
	compared                 []bool
	periodSeconds            float64
	keepWatts                float64 // keep energy is linear in the common comparison window
	keepEnergyOnce           sync.Once
	keepEnergy               capacityResult
	undecidable              []SchedulerUndecidableCandidate
	examined                 int
	searched                 SchedulerPlanSearch
	// Resolved once during construction; every plan and sweep worker only reads.
	optionCapacity  map[int][]capacityChoice
	currentCapacity map[capacityInstanceKey]capacityItem
	profileScalars  map[profileTermsKey]profileScalar
	// partial marks a window observed for less than its length since the
	// process started: arrivals there still reflect the restart, so plans
	// only add instances and keep every route.
	partial bool
}

func (p *capacityPlanner) currentKeep() capacityResult {
	p.keepEnergyOnce.Do(func() { p.keepEnergy = p.evaluateCurrentKeep(p.periodSeconds) })
	return p.keepEnergy
}

func newCapacityPlanner(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot, keep SchedulerCandidate, decision SchedulerDecision) *capacityPlanner {
	snapshot = schedulerReplayScopedSnapshot(snapshot)
	p := &capacityPlanner{ctx: ctx, settings: settings, snapshot: snapshot, keep: keep, index: map[string]int{}}
	for _, state := range decision.Workloads {
		if state.State != WorkloadControlled {
			continue
		}
		w := capacityWorkload{}
		for _, d := range snapshot.Workloads {
			if d != nil && d.UID == state.UID {
				w.d = d
			}
		}
		for _, load := range decision.Search.Loads {
			if load.UID == state.UID {
				w.rate = max(load.InputsPerSecond, load.RecentInputsPerSecond)
			}
		}
		for _, r := range snapshot.Outstanding {
			// Assignment reserves a slot before native dispatch, not service
			// start. Only observed running/returning work leaves backlog.
			if r.Definition != nil && r.Definition.UID == state.UID &&
				(r.Phase == phasePending || r.Phase == phaseAssigned || r.Phase == phaseQueued || r.Phase == phaseDeferred) &&
				(r.RemainingLifetime == nil || *r.RemainingLifetime > 0) {
				w.queued++
			}
		}
		covered := decision.Search.WindowEnd.Sub(decision.Search.WindowStart)
		if covered > 0 {
			w.rate += float64(w.queued) / covered.Seconds()
		}
		w.rate = max(w.rate, settings.MinimumDemand[w.d.UID])
		w.minRate = max(capacityMinimumRate(settings, decision.Search, snapshot.Arrivals, w.d.UID), settings.MinimumDemand[w.d.UID])
		w.totalNanos = p.pathFeedback(w.d).TotalNanos
		for _, i := range snapshot.Instances {
			if i.WorkloadUID == w.d.UID || i.OwnerWorkloadUID == w.d.UID {
				w.current++
			}
		}
		p.index[w.d.UID] = len(p.ws)
		p.ws = append(p.ws, w)
	}
	for _, node := range snapshot.Nodes {
		for _, instance := range schedulerUncreatedReservations(snapshot, node.Node) {
			p.pending = append(p.pending, instance)
			if w, ok := p.index[instance.WorkloadUID]; ok {
				p.ws[w].current++
			}
		}
	}
	p.options = schedulerMeasuredOptions(ctx, snapshot)
	p.resolveCapacity()
	return p
}

// fixCapacityWorkloads fixes only Workloads lacking valid raw profile support.
// Pending and zero-witness offline rows never gate control.
func (p *capacityPlanner) fixCapacityWorkloads(decision *SchedulerDecision) {
	kept := 0
	for w, ws := range p.ws {
		reason := CapacityRejected
		available := false
		for option, choices := range p.optionCapacity {
			o := p.options[option]
			if !slices.Contains(o.CompatibleUIDs, ws.d.UID) || !schedulerProfileMatches(ws.d, o.Instance.Profile) {
				continue
			}
			switch choices[w].reason {
			case "":
				available = true
			case CapacityPending:
				reason = CapacityPending
			}
		}
		if !available {
			delete(p.index, ws.d.UID)
			reason += ":" + ws.d.UID
			for k := range decision.Workloads {
				if decision.Workloads[k].UID == ws.d.UID {
					decision.Workloads[k].State, decision.Workloads[k].Reason = schedulerWorkloadFixed, reason
				}
			}
			decision.Reasons = appendUnique(decision.Reasons, reason)
			continue
		}
		p.ws[kept], p.index[ws.d.UID] = ws, kept
		for _, choices := range p.optionCapacity {
			choices[kept] = choices[w]
			choices[kept].w = kept
		}
		kept++
	}
	p.ws = p.ws[:kept]
	for option, choices := range p.optionCapacity {
		p.optionCapacity[option] = choices[:kept]
	}
}

// capacityMinimumRate is the least arrival rate of the Workload over the
// control intervals that tile the observed part of the observation window.
func capacityMinimumRate(settings SchedulerSettings, search SchedulerSearch, arrivals []SchedulerArrival, uid string) float64 {
	covered := search.WindowEnd.Sub(search.WindowStart)
	if covered <= 0 {
		return 0
	}
	buckets := max(1, int(covered/settings.ControlInterval))
	width := covered / time.Duration(buckets)
	start := search.WindowEnd.Add(-time.Duration(buckets) * width)
	counts := make([]int, buckets)
	for _, a := range arrivals {
		if (!a.Compute && !a.Accepted) || a.Definition == nil || a.Definition.UID != uid || a.At.Before(start) || !a.At.Before(search.WindowEnd) {
			continue
		}
		counts[min(buckets-1, int(a.At.Sub(start)/width))]++
	}
	return float64(slices.Min(counts)) / width.Seconds()
}

func (p *capacityPlanner) pathFeedback(d *Definition) PathFeedback {
	for _, feedback := range p.snapshot.PathFeedback {
		if feedback.WorkloadUID == d.UID {
			return feedback
		}
	}
	return BuildPathFeedback(d.UID, nil, d.SLO, time.Duration(p.settings.CompletionMarginMS*1e6))
}
