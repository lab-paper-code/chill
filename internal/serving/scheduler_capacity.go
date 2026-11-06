package serving

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
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
	// schedulerDemandIncreasing protects serving capacity while recent arrivals
	// exceed the long-window rate.
	schedulerDemandIncreasing = "DemandIncreasing"
	// schedulerReplacementUnpaid names a node whose replacement did not repay
	// its transition at the least load of the observation window.
	schedulerReplacementUnpaid = "ReplacementUnpaid"
	schedulerReplacementDwell  = "ReplacementDwell"
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
	GainUncertaintyJoules float64                      `json:"gainUncertaintyJoules,omitempty"`
	TransitionReserves    []SchedulerTransitionReserve `json:"transitionReserves,omitempty"`
	BridgeStages          []SchedulerBridgeStage       `json:"bridgeStages,omitempty"`
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
	// StartedAt and FinishedAt are direct wall-clock boundaries of search,
	// excluding node-plan construction and final decision projection.
	StartedAt         time.Time                             `json:"startedAt"`
	FinishedAt        time.Time                             `json:"finishedAt"`
	BudgetCut         bool                                  `json:"budgetCut,omitempty"`
	Excluded          map[string]int                        `json:"excluded,omitempty"`
	ReserveRejections map[string]SchedulerTransitionReserve `json:"reserveRejections,omitempty"`
	Steps             []SchedulerPlanSearchStep             `json:"steps,omitempty"`
	TrailingEvaluated int                                   `json:"trailingEvaluated,omitempty"`
	TerminationReason string                                `json:"terminationReason,omitempty"`
	Strategy           string                     `json:"strategy,omitempty"`
	Pruned             map[string]int             `json:"pruned,omitempty"`
	SurrogateEvaluated int                        `json:"surrogateEvaluated,omitempty"`
	Finalists          int                        `json:"finalists,omitempty"`
	ExactEvaluated     int                        `json:"exactEvaluated,omitempty"`
	SurrogateGapJoules float64                    `json:"surrogateGapJoules,omitempty"`
	PostFilterReverts  int                        `json:"postFilterReverts,omitempty"`
	Fallback           string                     `json:"fallback,omitempty"`
	DualBound          float64                    `json:"dualBound,omitempty"`
	Shadow             *SchedulerPlanSearchShadow `json:"shadow,omitempty"`
}

// SchedulerPlanSearchStep records each start's origin (k = 0) and accepted moves
// in decision-time order. Both origins precede k = 2 then k = 3; neighborhoods
// without accepted moves have no entry. Compare choices within the same Start.
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
type SchedulerPlanSearchStep struct {
	Start            string          `json:"start"`
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

// SchedulerPlanSearchShadow records the independent shadow planner.
type SchedulerPlanSearchShadow struct {
	Key       string        `json:"key"`
	Joules    float64       `json:"joules"`
	Evaluated int           `json:"evaluated"`
	Duration  time.Duration `json:"durationNanos"`
	Fallback  string        `json:"fallback,omitempty"`
}

// SchedulerCapacityWorkload is one controlled Workload of the chosen plan set.
// Rate is λ (req/s), Backlog B (requests). A missing wait, drain or exceedance
// is unbounded: no capacity serves the Workload.
type SchedulerCapacityWorkload struct {
	UID                  string              `json:"uid"`
	Rate                 float64             `json:"rate"`
	MinimumDemand        float64             `json:"minimumDemand"`
	Queued               int                 `json:"queued"`
	KappaNew             float64             `json:"kappaNew"`
	KappaRetained        float64             `json:"kappaRetained"`
	Backlog              float64             `json:"backlog"`
	WaitSeconds          *float64            `json:"waitSeconds,omitempty"`
	DrainSeconds         *float64            `json:"drainSeconds,omitempty"`
	FinalExceedance      *float64            `json:"finalExceedance,omitempty"`
	TransitionExceedance *float64            `json:"transitionExceedance,omitempty"`
	Weights              map[string]int32    `json:"weights"`
	Rows                 []string            `json:"rows"`
	ScaleDown            *SchedulerDownState `json:"scaleDown,omitempty"`
}

// SchedulerNodePlan is one Node's plan: keep, or a set of option variants at
// one power mode. Kappa and KappaRetained are per Workload UID; Pre and Post
// are the longest chains before and after the route; Joules is the mean chain
// energy.
type SchedulerNodePlan struct {
	Node               NodeReference       `json:"node"`
	Key                string              `json:"key"`
	Eligible           bool                `json:"eligible"`
	Reason             string              `json:"reason,omitempty"`
	PreparationFailure *PreparationFailure `json:"preparationFailure,omitempty"`
	Dominated          string              `json:"dominated,omitempty"`
	// RequiresBridge is a conditional candidate, never permission to stop first.
	RequiresBridge          []string                       `json:"requiresBridge,omitempty"`
	RequiresRecovery        []string                       `json:"requiresRecovery,omitempty"`
	Tempo                   string                         `json:"tempo,omitempty"`
	Kappa                   map[string]float64             `json:"kappa,omitempty"`
	KappaRetained           map[string]float64             `json:"kappaRetained,omitempty"`
	Pre                     time.Duration                  `json:"preNanos"`
	Post                    time.Duration                  `json:"postNanos"`
	Joules                  float64                        `json:"joules"`
	EnergyUncertaintyJoules float64                        `json:"energyUncertaintyJoules,omitempty"`
	ActionCosts             []SchedulerActionCost          `json:"actionCosts,omitempty"`
	IdleCosts               map[string]IdlePowerProvenance `json:"idleCosts,omitempty"`
}

// EvaluateScheduler plans with capacity sums: node plans, the transient and
// final SLO rule over exact κ sums, a neighborhood search over plan sets and
// analytic energy over one comparison period. capacity holds the rows
// CapacityTable.Rows returns.
func EvaluateScheduler(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot, capacity []CapacityRow) (decision SchedulerDecision, err error) {
	started := time.Now()
	if err := ctx.Err(); err != nil {
		return SchedulerDecision{}, err
	}
	settings.AutonomousWorkloads = slices.Clone(settings.AutonomousWorkloads)
	settings.ExcludedNodes = slices.Clone(settings.ExcludedNodes)
	settings.MinimumDemand = maps.Clone(settings.MinimumDemand)
	settings.DefaultOperationCost = maps.Clone(settings.DefaultOperationCost)
	if settings.DefaultIdlePower != nil {
		idle := *settings.DefaultIdlePower
		settings.DefaultIdlePower = &idle
	}
	snapshot = schedulerCanonical(snapshot)
	if settings.ScaleDownStabilization > 0 && snapshot.ScaleDown == nil {
		snapshot.ScaleDown = map[string]SchedulerDownState{}
	}
	decision = SchedulerDecision{At: snapshot.At, Status: schedulerStatusPending, Settings: settings, Search: schedulerSearchPopulation(ctx, settings, snapshot), Workloads: schedulerWorkloadStates(settings, snapshot), References: slices.Clone(snapshot.References)}
	defer func() {
		if decision.Capacity == nil && !decision.At.IsZero() {
			decision.Capacity = schedulerDownCapacity(settings, snapshot)
		}
	}()
	fixSchedulerWorkloads(&decision, snapshot, capacity)
	keep := SchedulerCandidate{ID: "keep", Keep: true, Nodes: snapshot.Nodes, Instances: slices.Clone(snapshot.Instances), Routing: schedulerInstanceRouting(ctx, snapshot.Workloads, snapshot.Instances)}
	for uid, routing := range snapshot.Routing {
		keep.Routing[uid] = routing
	}
	if err := settings.Validate(); err != nil {
		decision.Reasons, decision.Keep = []string{err.Error()}, keep
		return decision, ctx.Err()
	}
	if settings.ScaleDownStabilization == 0 && snapshot.ScaleDown == nil {
		var tempo SchedulerTempo
		tempo.Observe(settings, &snapshot, capacity)
	}
	// Keep carries only already-authorized Fixed completion obligations.
	keep.Nodes = schedulerCommonNodes(snapshot)
	p := schedulerPlanState{snap: snapshot, target: &keep, actions: []Action{}, nodeTail: map[string]string{}, prepares: []string{}, stops: map[string]string{}}
	p.changeNodes(keep)
	keep.Actions = p.actions
	keep.Status = schedulerStatusPending
	decision.Keep = keep
	reasons := append(slices.Clone(snapshot.Reasons), schedulerValidateSnapshot(settings, snapshot)...)
	reasons = appendUnique(reasons, schedulerValidateResources(settings, snapshot.Nodes, snapshot.Instances)...)
	if len(reasons) != 0 {
		decision.Keep.Reasons, decision.Reasons = reasons, reasons
		return decision, ctx.Err()
	}
	decision.Bootstrap = schedulerBootstrap(ctx, settings, snapshot, capacity, decision)
	if decision.Bootstrap != nil && len(decision.Bootstrap.PendingRows) > 0 && !decision.Bootstrap.TimedOut {
		decision.Status, decision.Reasons = CapacityPending, []string{"BootstrapCapacityPending"}
		decision.Keep.Actions = nil
		return decision, ctx.Err()
	}
	planner := newCapacityPlanner(ctx, settings, snapshot, keep, capacity, decision)
	if len(planner.ws) == 0 {
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
	best, err := planner.search(started.Add(settings.ControlInterval / 2))
	if err != nil {
		return SchedulerDecision{}, err
	}
	if settings.PlanSearchStrategy != schedulerPlanSearchV2 || !settings.PlanPostFilterFinalists {
		best = planner.minimumGain(planner.payback(best))
	}
	decision = planner.decide(decision, best)
	markFixedEstimates(&decision)
	return decision, ctx.Err()
}

// schedulerBootstrap waits only for rows of candidates that can occupy an empty
// pool. Rejected rows are terminal; they need not and may never have a κ.
//
//nolint:gocyclo // Empty-pool candidate eligibility mirrors nodePlans without requiring κ.
func schedulerBootstrap(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot, rows []CapacityRow, decision SchedulerDecision) *SchedulerBootstrap {
	if settings.BootstrapCapacityWait == 0 || len(snapshot.Instances) != 0 || len(snapshot.Reservations) != 0 || len(settings.AutonomousWorkloads) == 0 {
		return nil
	}
	started := snapshot.BootstrapStartedAt
	if started.IsZero() {
		started = snapshot.At
	}
	result := &SchedulerBootstrap{StartedAt: started, Deadline: started.Add(settings.BootstrapCapacityWait), PendingRows: []string{}}
	// Restore only capacity-pending UIDs for candidate eligibility; reservation
	// and observation exclusions retain their normal meaning.
	decision.Workloads = slices.Clone(decision.Workloads)
	for k := range decision.Workloads {
		if strings.HasPrefix(decision.Workloads[k].Reason, CapacityPending+":") {
			decision.Workloads[k].State, decision.Workloads[k].Reason = WorkloadControlled, ""
		}
	}
	p := newCapacityPlanner(ctx, settings, snapshot, decision.Keep, rows, decision)
	for _, node := range snapshot.Nodes {
		if !node.Ready || node.Measurement || slices.Contains(settings.ExcludedNodes, node.Node.Name) ||
			slices.ContainsFunc(node.Reasons, func(reason string) bool {
				return reason == NodeExcluded || reason == NodeInstanceUnavailable || reason == NodePlacementInventoryMissing || reason == NodePlacementBudgetUnavailable
			}) {
			continue
		}
		for _, option := range p.options {
			i := option.Instance
			if i.Node != node.Node || i.Profile == nil || i.Profile.Condition.NodeUID != node.Node.UID {
				continue
			}
			if node.PowerMode != i.Profile.Condition.PowerMode && (!settings.Controls.Power || slices.Contains(node.Reasons, NodePowerModeUnavailable)) {
				continue
			}
			if len(ValidateNodeBudget(node, append(append([]SchedulerAllocation{node.Reserved}, node.NonServing...), i.Allocation), nil)) != 0 {
				continue
			}
			for w, workload := range p.ws {
				if !slices.Contains(option.CompatibleUIDs, workload.d.UID) || !schedulerProfileMatches(workload.d, i.Profile) || !p.controlAllows(w, i, nil) {
					continue
				}
				key := NewCapacityInput(settings, snapshot.DispatchOccupancy, option, workload.d).Key()
				if row, found := p.rows[key]; !found || row.Status == CapacityPending {
					result.PendingRows = appendUnique(result.PendingRows, key)
				}
			}
		}
	}
	slices.Sort(result.PendingRows)
	result.TimedOut = len(result.PendingRows) > 0 && !snapshot.At.Before(result.Deadline)
	return result
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
	ctx          context.Context
	settings     SchedulerSettings
	snapshot     SchedulerSnapshot
	keep         SchedulerCandidate
	rows         map[string]CapacityRow
	options      []SchedulerOption
	pending      []SchedulerInstance // uncreated commitments, counted once without capacity
	ws           []capacityWorkload
	index        map[string]int
	plans        [][]*capacityPlan // per Node; plans[n][0] is keep
	compared     []bool
	period       float64
	keepWatts    float64 // keep energy is linear in the common comparison window
	keepFeasible bool    // full Keep judgment, shared by admission and recovery
	examined     int
	searched     SchedulerPlanSearch
	variants     map[string]SchedulerOption // capacity variants by profile ID and batch limit
	// Resolved once during construction; every plan and sweep worker only reads.
	optionCapacity  map[int][]capacityChoice
	currentCapacity map[capacityInstanceKey]capacityItem
	// partial marks a window observed for less than its length since the
	// process started: arrivals there still reflect the restart, so plans
	// only add instances and keep every route.
	partial bool
}

func newCapacityPlanner(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot, keep SchedulerCandidate, rows []CapacityRow, decision SchedulerDecision) *capacityPlanner {
	p := &capacityPlanner{ctx: ctx, settings: settings, snapshot: snapshot, keep: keep, rows: map[string]CapacityRow{}, index: map[string]int{}, variants: map[string]SchedulerOption{}}
	for _, row := range rows {
		p.rows[row.Key] = row
	}
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
				w.growing = load.RecentInputsPerSecond > load.InputsPerSecond
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
		w.slack = w.d.SLO.TargetLatency.Seconds() - settings.CompletionMarginMS/1e3
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
	// The capacity table keys rows by the variants of the snapshot options.
	for _, option := range snapshot.Options {
		for _, v := range CapacityVariants(option) {
			key := fmt.Sprint(v.Instance.Profile.ID, "/", v.Instance.MaxBatch)
			if _, seen := p.variants[key]; !seen {
				p.variants[key] = v
			}
		}
	}
	p.resolveCapacity()
	return p
}

// capacityMinimumRate is the least arrival rate of the Workload over the
// control intervals that tile the observed part of the observation window.
func capacityMinimumRate(settings SchedulerSettings, search SchedulerSearch, arrivals []SchedulerArrival, uid string) float64 {
	covered := search.WindowEnd.Sub(search.WindowStart)
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


