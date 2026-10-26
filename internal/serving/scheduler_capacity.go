package serving

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// Decision statuses and reasons of the capacity planner (analytic-planner §3, §4).
const (
	schedulerStatusKeep               = "Keep"
	schedulerRouteWeightExceeds       = "RouteWeightExceedsCapacity"
	schedulerNoPlanMeetsSLO           = "NoPlanMeetsSLO"
	schedulerPlanMissingOperationCost = "MissingOperationCost"
	schedulerPlanInfeasibleResources  = "InfeasibleResources"
	schedulerPlanFixedWorkload        = "FixedWorkload"
	schedulerPlanPreparationFailed    = "PreparationFailed"
	schedulerPlanHeld                 = "HeldAfterOverload"
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
	// schedulerGainWithinUncertainty keeps the current configuration when the
	// chosen transition saves no more than the selected evidence's conditional B.
	schedulerGainWithinUncertainty = "TransitionGainWithinUncertainty"
	// schedulerIdleInstanceRemoved names an instance unrouted for one
	// observation window that the target removes.
	schedulerIdleInstanceRemoved = "IdleInstanceRemoved"
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
}

// SchedulerPlanSearch is the scope of one plan search: the most Nodes one step
// changed in the last neighborhood it searched (k), the plan sets it
// evaluated, its time, and whether the time budget (half the control
// interval) ended it before its last step.
type SchedulerPlanSearch struct {
	Neighborhood      int                                   `json:"neighborhood"`
	Evaluated         int                                   `json:"evaluated"`
	Duration          time.Duration                         `json:"durationNanos"`
	BudgetCut         bool                                  `json:"budgetCut,omitempty"`
	Excluded          map[string]int                        `json:"excluded,omitempty"`
	ReserveRejections map[string]SchedulerTransitionReserve `json:"reserveRejections,omitempty"`
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
	Node                    NodeReference      `json:"node"`
	Key                     string             `json:"key"`
	Eligible                bool               `json:"eligible"`
	Reason                  string             `json:"reason,omitempty"`
	Tempo                   string             `json:"tempo,omitempty"`
	Kappa                   map[string]float64 `json:"kappa,omitempty"`
	KappaRetained           map[string]float64 `json:"kappaRetained,omitempty"`
	Pre                     time.Duration      `json:"preNanos"`
	Post                    time.Duration      `json:"postNanos"`
	Joules                  float64            `json:"joules"`
	EnergyUncertaintyJoules float64            `json:"energyUncertaintyJoules,omitempty"`
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
	// Lowering available Nodes is a fixed rule every plan, keep included, shares.
	keep.Nodes = schedulerCommonNodes(settings, snapshot)
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
	decision = planner.decide(decision, planner.minimumGain(planner.payback(best)))
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
			if i.Node != node.Node || i.Profile == nil || i.Profile.Condition.NodeUID != node.Node.UID || len(i.Allocation.CPUs) > settings.MaxCPUs {
				continue
			}
			if node.PowerMode != i.Profile.Condition.PowerMode && (!settings.Controls.Power || slices.Contains(node.Reasons, NodePowerModeUnavailable)) {
				continue
			}
			modeNode, known := node.AtMode(i.Profile.Condition.PowerMode)
			if !known || len(ValidateNodeBudget(modeNode, append(append([]SchedulerAllocation{node.Reserved}, node.NonServing...), i.Allocation), nil)) != 0 {
				continue
			}
			for w, workload := range p.ws {
				if !schedulerHas(option.CompatibleUIDs, workload.d.UID) || !schedulerProfileMatches(workload.d, i.Profile) || !p.controlAllows(w, i, nil) {
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

type capacityWorkload struct {
	d       *Definition
	rate    float64 // λ, req/s
	minRate float64 // the least arrival rate of one control interval in the observation window
	queued  int     // q
	current int     // instances serving it now
	slack   float64 // L − m, seconds
	growing bool    // recent observed rate exceeds the long-window rate
}

// capacityItem is one instance serving a Workload: its κ, per-input energy e
// (batch 1) and its route weight now (a new instance: its option's weight).
// prepare marks a new instance.
type capacityItem struct {
	id      string
	kappa   float64
	e       float64
	weight  int32
	row     string
	prepare bool
}

type capacityPlan struct {
	record                             SchedulerNodePlan
	mode                               string
	keep                               bool
	instances                          []SchedulerInstance // the Node's instances after the plan
	items                              [][]capacityItem    // per Workload, by e then ID
	retained                           [][]capacityItem    // per Workload, current instances serving until the route
	others                             int                 // instances serving no controlled Workload
	preMean, preMax, postMean, postMax float64             // seconds
	preJ, postJ                        float64
	before, mid, after                 float64 // watts
	operationBounds                    schedulerEnergyBounds
	beforeBound, midBound, afterBound  schedulerBaselineBound
	// replaces marks a plan that changes the batch policy of a current instance,
	// or removes one while adding another.
	replaces bool
}

// energy is the Node's joules over [0, t] when the route lands at r.
func (p *capacityPlan) energy(r, t float64) float64 {
	if p.keep {
		return p.before * t
	}
	return p.preJ + p.mid*(r-p.preMean) + p.postJ + p.after*(t-r-p.postMean)
}

// A trial's shift/gap error is deterministic: repeated uses add, shared uses
// cancel with their sign before distinct evidence bounds are summed.
type schedulerBoundSource struct {
	reference evidencestore.ObjectIdentity
	trial     int
}

type schedulerEnergyBounds map[schedulerBoundSource]float64

func (bounds schedulerEnergyBounds) halfWidth() float64 {
	total := 0.0
	for _, value := range bounds {
		total += math.Abs(value)
	}
	return total
}

type schedulerBaselineBound struct {
	reference evidencestore.ObjectIdentity
	watts     float64 // signed coefficient of the exact baseline reconstruction
}

func schedulerBaselineUncertainty(baselines []NodeBaseline, sources []string, instances int) schedulerBaselineBound {
	if len(sources) == 0 {
		return schedulerBaselineBound{}
	}
	for _, b := range baselines {
		if b.ID == sources[0] {
			coefficient := 1.0
			if len(b.State) == 0 {
				// unloaded + sum(loaded idle - unloaded), not n independent errors
				coefficient -= float64(instances)
			}
			return schedulerBaselineBound{reference: b.Reference, watts: coefficient * b.WattsUncertainty}
		}
	}
	return schedulerBaselineBound{}
}

func (plan *capacityPlan) addBounds(bounds schedulerEnergyBounds, r, t, sign float64) {
	add := func(b schedulerBaselineBound, seconds float64) {
		if b.watts != 0 {
			bounds[schedulerBoundSource{reference: b.reference, trial: -1}] += sign * seconds * b.watts
		}
	}
	if plan.keep {
		add(plan.beforeBound, t)
		return
	}
	for source, value := range plan.operationBounds {
		bounds[source] += sign * value
	}
	add(plan.midBound, r-plan.preMean)
	add(plan.afterBound, t-r-plan.postMean)
}

type capacityPlanner struct {
	ctx      context.Context
	settings SchedulerSettings
	snapshot SchedulerSnapshot
	keep     SchedulerCandidate
	rows     map[string]CapacityRow
	options  []SchedulerOption
	ws       []capacityWorkload
	index    map[string]int
	plans    [][]*capacityPlan // per Node; plans[n][0] is keep
	compared []bool
	period   float64
	examined int
	searched SchedulerPlanSearch
	variants map[string]SchedulerOption // capacity variants by profile ID and batch limit
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
			if r.Definition != nil && r.Definition.UID == state.UID {
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
	p.options = schedulerMeasuredOptions(ctx, settings, snapshot)
	// The capacity table keys rows by the variants of the snapshot options.
	for _, option := range snapshot.Options {
		for _, v := range CapacityVariants(option) {
			key := fmt.Sprint(v.Instance.Profile.ID, "/", v.Instance.MaxBatch)
			if _, seen := p.variants[key]; !seen {
				p.variants[key] = v
			}
		}
	}
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

// held reports an instance prepared under Overload still inside its hold.
func (p *capacityPlanner) held(i SchedulerInstance) bool {
	return i.HeldUntil != nil && i.HeldUntil.After(p.snapshot.At)
}

// idle reports an instance unrouted for at least one observation window once
// a whole window is observed.
func (p *capacityPlanner) idle(i SchedulerInstance) bool {
	return !p.partial && i.UnroutedSince != nil && p.snapshot.At.Sub(*i.UnroutedSince) >= p.settings.ObservationWindow && !p.held(i)
}

// kappa looks up the row of option variant o for Workload w.
func (p *capacityPlanner) kappa(o SchedulerOption, w int) (float64, string, string) {
	key := NewCapacityInput(p.settings, p.snapshot.DispatchOccupancy, o, p.ws[w].d).Key()
	row, ok := p.rows[key]
	switch {
	case !ok || row.Status == CapacityPending:
		return 0, key, CapacityPending
	case row.Kappa == nil:
		return 0, key, CapacityRejected
	}
	return *row.Kappa, key, ""
}

func capacityInputEnergy(i SchedulerInstance) float64 {
	if b := schedulerBatch(i.Profile, 1); b != nil {
		return b.EnergyPerInputJoules
	}
	return 0
}

// fixed reports an instance autonomous control never moves: a strict baseline,
// or one serving or owned by a current Workload outside control.
func (p *capacityPlanner) fixed(i SchedulerInstance) bool {
	current := func(uid string) bool {
		return slices.ContainsFunc(p.snapshot.Workloads, func(d *Definition) bool { return d != nil && d.UID == uid })
	}
	_, ownerControlled := p.index[i.OwnerWorkloadUID]
	_, servedControlled := p.index[i.WorkloadUID]
	return i.FixedBatchSize > 0 || !ownerControlled && current(i.OwnerWorkloadUID) || !servedControlled && current(i.WorkloadUID)
}

// build lists every Node's plans and the comparison period T.
func (p *capacityPlanner) build() error {
	common := p.keep.Nodes
	p.plans = make([][]*capacityPlan, len(p.snapshot.Nodes))
	p.compared = make([]bool, len(p.snapshot.Nodes))
	p.period = p.settings.ComparisonPeriod.Seconds()
	for n := range p.snapshot.Nodes {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		p.plans[n] = p.nodePlans(n, common)
		for _, plan := range p.plans[n][1:] {
			if plan.record.Eligible {
				if plan.preMax+plan.postMax > p.period {
					plan.record.Eligible, plan.record.Reason = false, schedulerTransitionHorizon
					continue
				}
				p.compared[n] = true
			}
		}
		p.examined += len(p.plans[n])
	}
	return nil
}

// planKey names a set of instances on one Node by option, batch and Workload.
func planKey(instances []SchedulerInstance) string {
	keys := make([]string, 0, len(instances))
	for _, i := range instances {
		profile := i.ProfileID
		if i.Profile != nil {
			profile = i.Profile.ID
		}
		keys = append(keys, fmt.Sprintf("%s/b%d/%s", profile, i.MaxBatch, i.WorkloadUID))
	}
	sort.Strings(keys)
	return strings.Join(keys, "+")
}

type capacityChoice struct {
	option SchedulerOption
	w      int
	kappa  float64
	row    string
	reason string
}

// Uncreated targets consume the shared instance limit, not current capacity or
// energy. Once their Pod/slot appears, physical occupancy already counts it.
func schedulerUncreatedReservations(snapshot SchedulerSnapshot, node NodeReference) int {
	seen := map[string]bool{}
	for _, reservation := range snapshot.Reservations {
		if reservation.Target == nil || !slices.Contains(reservation.Nodes, node) {
			continue
		}
		for _, wanted := range reservation.Target.Instances {
			if wanted.Node != node || seen[wanted.ID] {
				continue
			}
			if slices.ContainsFunc(snapshot.Instances, func(actual SchedulerInstance) bool {
				return actual.Node == node && (actual.ID == wanted.ID || actual.TargetID != "" && actual.TargetID == wanted.ID ||
					wanted.PodUID != "" && actual.PodUID == wanted.PodUID)
			}) {
				continue
			}
			seen[wanted.ID] = true
		}
	}
	return len(seen)
}

//nolint:gocyclo // One ordered pass applies the node rules, the control gates and the plan walk.
func (p *capacityPlanner) nodePlans(n int, common []SchedulerNode) []*capacityPlan {
	node := p.snapshot.Nodes[n]
	m0 := common[n].PowerMode
	for _, a := range p.snapshot.Actions {
		if a.Fixed && a.Node == node.Node && (a.Status == schedulerActionPlanned || a.Status == phaseRunning) {
			m0 = a.To
		}
	}
	current := []SchedulerInstance{}
	for _, i := range p.snapshot.Instances {
		if i.Node == node.Node {
			current = append(current, i)
		}
	}
	keep := &capacityPlan{mode: m0, keep: true, instances: current, record: SchedulerNodePlan{Node: node.Node, Key: "keep", Eligible: true}}
	p.annotate(keep, nil)
	p.sum(keep)
	at := node
	at.PowerMode = m0
	before, _, beforeSources, _, beforeReason := schedulerBaseline(p.snapshot.Baselines, at, current, p.snapshot.Options)
	keep.before = before
	keep.beforeBound = schedulerBaselineUncertainty(p.snapshot.Baselines, beforeSources, len(current))
	plans := []*capacityPlan{keep}
	reason := ""
	switch {
	case schedulerReservedNode(p.snapshot, node.Node):
		keep.others += schedulerUncreatedReservations(p.snapshot, node.Node)
		reason = "ReservedNode"
	case !node.Ready:
		reason = "NodeUnavailable"
	case node.Measurement:
		reason = "MeasurementNode"
	case slices.Contains(node.Reasons, NodeExcluded):
		reason = NodeExcluded
	case slices.Contains(node.Reasons, NodeInstanceUnavailable):
		reason = NodeInstanceUnavailable
	case slices.Contains(node.Reasons, NodePlacementInventoryMissing):
		reason = NodePlacementInventoryMissing
	case slices.Contains(node.Reasons, NodePlacementBudgetUnavailable):
		reason = NodePlacementBudgetUnavailable
	case slices.ContainsFunc(current, p.fixed):
		// Disabled Workloads keep their SLO: their Node takes no plan action.
		reason = schedulerPlanFixedWorkload
	case beforeReason != "":
		reason = beforeReason
	}
	groups, names := map[string][]capacityChoice{}, []string{}
	for _, o := range p.options {
		i := o.Instance
		if i.Node != node.Node || i.Profile == nil || i.Profile.Condition.NodeUID != node.Node.UID || len(i.Allocation.CPUs) > p.settings.MaxCPUs {
			continue
		}
		if node.PowerMode != i.Profile.Condition.PowerMode && (!p.settings.Controls.Power || slices.Contains(node.Reasons, NodePowerModeUnavailable)) {
			continue
		}
		for w := range p.ws {
			if !schedulerHas(o.CompatibleUIDs, p.ws[w].d.UID) || !schedulerProfileMatches(p.ws[w].d, i.Profile) || !p.controlAllows(w, i, current) {
				continue
			}
			c := capacityChoice{option: o, w: w}
			c.kappa, c.row, c.reason = p.kappa(o, w)
			name := fmt.Sprint(i.Profile.ID, i.Allocation, i.MaxWait)
			if groups[name] == nil {
				names = append(names, name)
			}
			groups[name] = append(groups[name], c)
		}
	}
	if len(names) == 0 && reason != NodeExcluded {
		return plans
	}
	if reason != "" {
		plans = append(plans, &capacityPlan{record: SchedulerNodePlan{Node: node.Node, Key: "*", Reason: reason}})
		return plans
	}
	currentKey := planKey(current)
	allocations := append([]SchedulerAllocation{node.Reserved}, node.NonServing...)
	var walk func(j int, chosen []capacityChoice)
	walk = func(j int, chosen []capacityChoice) {
		if j == len(names) {
			if len(chosen) == 0 && len(current) == 0 {
				return
			}
			if plan := p.plan(n, m0, current, chosen); plan != nil && plan.record.Key != currentKey {
				plans = append(plans, plan)
			}
			return
		}
		walk(j+1, chosen)
		// One serving instance per Node: κ of co-located instances does not add
		// (m4 r8 D-R8-1); a wider option is the larger instance.
		if len(chosen) > 0 {
			return
		}
		for _, c := range groups[names[j]] {
			i := c.option.Instance
			next := append(slices.Clone(chosen), c)
			wanted := slices.Clone(allocations)
			for _, o := range next {
				wanted = append(wanted, o.option.Instance.Allocation)
			}
			// A plan that changes the mode places on the CPUs that mode leaves online.
			modeNode, known := node.AtMode(i.Profile.Condition.PowerMode)
			if !known || len(ValidateNodeBudget(modeNode, wanted, nil)) != 0 {
				// Adding options never restores the budget, so the walk stops here.
				instances := make([]SchedulerInstance, len(next))
				for k, o := range next {
					instances[k] = o.option.Instance
					instances[k].WorkloadUID = p.ws[o.w].d.UID
				}
				reason := schedulerPlanInfeasibleResources
				if !known {
					reason = NodePlacementInventoryMissing
				}
				plans = append(plans, &capacityPlan{record: SchedulerNodePlan{Node: node.Node, Key: planKey(instances), Reason: reason}})
				continue
			}
			walk(j+1, next)
		}
	}
	walk(0, nil)
	return plans
}

// controlAllows applies the disabled controls the enumerator applied: with
// membership OFF a Workload stays on Nodes where it has an instance; with
// placement, resources or batching OFF the variant must match one current
// instance of the Workload in Node, resource choice or batch policy.
func (p *capacityPlanner) controlAllows(w int, i SchedulerInstance, current []SchedulerInstance) bool {
	s, uid := p.settings.Controls, p.ws[w].d.UID
	if !s.Membership && !slices.ContainsFunc(current, func(c SchedulerInstance) bool { return c.OwnerWorkloadUID == uid || c.WorkloadUID == uid }) {
		return false
	}
	if s.Placement && s.Resources && s.Batching {
		return true
	}
	return slices.ContainsFunc(p.snapshot.Instances, func(old SchedulerInstance) bool {
		return old.WorkloadUID == uid && (s.Placement || old.Node == i.Node) && (s.Resources || schedulerSameResourceChoice(old, i)) &&
			(s.Batching || old.MaxBatch == i.MaxBatch && old.MaxWait == i.MaxWait)
	})
}

// plan builds the node plan of chosen variants at their mode: physical reuse,
// resource validation, the schedulerPlan chain, chain costs and baselines.
//
//nolint:gocyclo // Reuse, validation, chain, retained κ and baselines of one node plan.
func (p *capacityPlanner) plan(n int, m0 string, current []SchedulerInstance, chosen []capacityChoice) *capacityPlan {
	node := p.snapshot.Nodes[n]
	mode := m0
	if len(chosen) > 0 {
		mode = chosen[0].option.Instance.Profile.Condition.PowerMode
	}
	plan := &capacityPlan{mode: mode, record: SchedulerNodePlan{Node: node.Node}}
	used := map[string]bool{}
	changed, added := map[string]bool{}, false
	chosenItems := map[string]capacityItem{}
	for _, c := range chosen {
		uid := p.ws[c.w].d.UID
		i := c.option.Instance
		i.ID, i.WorkloadUID, i.Ready, i.TargetID, i.OwnerWorkloadUID = fmt.Sprintf("target-%s-%s-%d", uid, i.Profile.ID, i.MaxBatch), uid, true, "", ""
		for _, old := range current {
			// The same physical executor serves on. Its batch limit and wait are
			// firstfloor policy: the route applies them without a restart, since
			// the Pod initializes every measured batch. An unrouted one needs its
			// prepared slot or owner as proof.
			if !used[old.ID] && mode == node.PowerMode && old.FixedBatchSize == 0 && schedulerState(old) == schedulerState(i) &&
				old.Capacity == i.Capacity && schedulerOwnerAllows(old.OwnerWorkloadUID, uid) &&
				(old.WorkloadUID == uid || old.WorkloadUID == "" && old.Ready && !old.Stopped && (old.TargetID != "" || old.OwnerWorkloadUID == uid)) {
				used[old.ID] = true
				changed[old.ID] = old.MaxBatch != i.MaxBatch || old.MaxWait != i.MaxWait
				old.WorkloadUID, old.MaxBatch, old.MaxWait = uid, i.MaxBatch, i.MaxWait
				i = old
				break
			}
		}
		added = added || !used[i.ID]
		plan.instances = append(plan.instances, i)
		chosenItems[i.ID] = capacityItem{kappa: c.kappa, row: c.row}
		if c.reason != "" && plan.record.Reason == "" {
			plan.record.Reason = c.reason
		}
	}
	plan.record.Key = planKey(plan.instances)
	removed := slices.ContainsFunc(current, func(old SchedulerInstance) bool { return !used[old.ID] })
	plan.replaces = removed && added
	for _, batch := range changed {
		plan.replaces = plan.replaces || batch
	}
	// An instance prepared under Overload is neither removed nor replaced
	// during its hold.
	if plan.record.Reason == "" && slices.ContainsFunc(current, func(old SchedulerInstance) bool {
		return p.held(old) && (!used[old.ID] || changed[old.ID])
	}) {
		plan.record.Reason = schedulerPlanHeld
	}
	if plan.record.Reason == "" && p.partial && removed {
		plan.record.Reason = schedulerPartialObservation
	}
	if plan.record.Reason == "" && slices.ContainsFunc(current, func(old SchedulerInstance) bool {
		w, controlled := p.index[old.WorkloadUID]
		return controlled && p.ws[w].growing && old.Ready && !old.Stopped && schedulerInstanceRouted(p.keep.Routing, old.ID) &&
			!used[old.ID]
	}) {
		plan.record.Reason = schedulerDemandIncreasing
	}
	if plan.record.Reason != "" {
		return plan
	}
	at := node
	at.PowerMode = mode
	for _, i := range plan.instances {
		if len(schedulerValidateInstance(p.settings, at, i, nil)) != 0 {
			plan.record.Reason = schedulerPlanInfeasibleResources
			return plan
		}
		// A failed preparation stays in the actions for one observation window.
		if !slices.ContainsFunc(current, func(old SchedulerInstance) bool { return old.ID == i.ID }) &&
			slices.ContainsFunc(p.snapshot.Actions, func(a Action) bool {
				return a.Kind == schedulerActionPrepare && a.Status == schedulerActionFailed && a.Node == node.Node && a.To == schedulerState(i)
			}) {
			plan.record.Reason = schedulerPlanPreparationFailed
			return plan
		}
	}
	stopped, removedPre, reason := p.chain(n, plan)
	if reason != "" {
		plan.record.Reason = reason
		return plan
	}
	// Make before break: a replacement on the Node's mode stops a routed
	// instance only after the route has moved its load to the prepared
	// capacity. A mode change stops every instance before the power action;
	// κ_ret and r carry that outage.
	if mode == node.PowerMode && slices.ContainsFunc(current, func(i SchedulerInstance) bool {
		return stopped[i.ID] && !i.Stopped && schedulerInstanceRouted(p.keep.Routing, i.ID)
	}) {
		plan.record.Reason = schedulerRoutedStopsFirst
		return plan
	}
	p.annotate(plan, chosenItems)
	for _, i := range current {
		w, ok := p.index[i.WorkloadUID]
		if weight := p.keep.Routing[i.WorkloadUID][i.ID]; ok && !stopped[i.ID] && i.Ready && !i.Stopped && weight > 0 {
			k, row := p.currentKappa(i)
			plan.retained[w] = append(plan.retained[w], capacityItem{id: i.ID, kappa: k, e: capacityInputEnergy(i), weight: weight, row: row})
		}
	}
	p.sum(plan)
	plan.record.Tempo = "fast"
	if slices.ContainsFunc(current, func(old SchedulerInstance) bool {
		return old.WorkloadUID == "" && old.OwnerWorkloadUID == "" && (!used[old.ID] || mode != node.PowerMode)
	}) {
		plan.record.Tempo = schedulerTempoSlow
		if !schedulerEmptySlowAllowed(p.snapshot) {
			plan.record.Reason = schedulerScaleDownStabilizing
			return plan
		}
	}
	for _, ws := range p.ws {
		affected := false
		policy := false
		oldKappa := 0.0
		for _, old := range current {
			if old.WorkloadUID == ws.d.UID || old.OwnerWorkloadUID == ws.d.UID {
				k, _ := p.currentKappa(old)
				oldKappa += k
				affected = affected || !used[old.ID] || mode != node.PowerMode
				policy = policy || changed[old.ID]
			}
		}
		hasCurrent := slices.ContainsFunc(current, func(old SchedulerInstance) bool {
			return old.WorkloadUID == ws.d.UID || old.OwnerWorkloadUID == ws.d.UID
		})
		slow := hasCurrent && (affected || policy && plan.record.Kappa[ws.d.UID] <= oldKappa)
		if slow {
			plan.record.Tempo = schedulerTempoSlow
			if p.partial {
				plan.record.Reason = schedulerPartialObservation
				return plan
			}
			if p.ws[p.index[ws.d.UID]].growing {
				plan.record.Reason = schedulerDemandIncreasing
				return plan
			}
			if !schedulerSlowAllowed(p.snapshot, ws.d.UID) {
				plan.record.Reason = schedulerScaleDownStabilizing
				return plan
			}
		}
	}
	mid := slices.DeleteFunc(slices.Clone(current), func(i SchedulerInstance) bool { return removedPre[i.ID] })
	for _, i := range plan.instances {
		if !slices.ContainsFunc(current, func(old SchedulerInstance) bool { return old.ID == i.ID }) {
			mid = append(mid, i)
		}
	}
	var midReason, afterReason string
	var midSources, afterSources []string
	plan.mid, _, midSources, _, midReason = schedulerBaseline(p.snapshot.Baselines, at, mid, p.snapshot.Options)
	plan.after, _, afterSources, _, afterReason = schedulerBaseline(p.snapshot.Baselines, at, plan.instances, p.snapshot.Options)
	plan.midBound = schedulerBaselineUncertainty(p.snapshot.Baselines, midSources, len(mid))
	plan.afterBound = schedulerBaselineUncertainty(p.snapshot.Baselines, afterSources, len(plan.instances))
	if reason := cmp.Or(midReason, afterReason); reason != "" {
		plan.record.Reason = reason
		return plan
	}
	plan.record.Eligible = true
	return plan
}

// currentKappa reads the row of a current instance through the option variant
// with its profile and batch limit; without one the instance counts no capacity.
func (p *capacityPlanner) currentKappa(i SchedulerInstance) (float64, string) {
	w, ok := p.index[i.WorkloadUID]
	if i.Profile == nil || !ok {
		return 0, ""
	}
	v, ok := p.variants[fmt.Sprint(i.Profile.ID, "/", i.MaxBatch)]
	if !ok {
		return 0, ""
	}
	k, row, _ := p.kappa(v, w)
	return k, row
}

// annotate fills the plan's per-Workload items from chosen (κ and row per
// instance ID). An item's weight is the instance's current route weight, or
// its option's weight for a new instance. Keep (chosen nil) reads the κ of
// its ready instances and counts the routed ones as retained; a stopped one
// serves nothing.
func (p *capacityPlanner) annotate(plan *capacityPlan, chosen map[string]capacityItem) {
	k := len(p.ws)
	plan.items, plan.retained = make([][]capacityItem, k), make([][]capacityItem, k)
	for _, i := range plan.instances {
		w, ok := p.index[i.WorkloadUID]
		if !ok || plan.keep && i.Stopped {
			plan.others++
			continue
		}
		item := chosen[i.ID]
		existing := slices.ContainsFunc(p.snapshot.Instances, func(c SchedulerInstance) bool { return c.ID == i.ID })
		item.id, item.e, item.weight, item.prepare = i.ID, capacityInputEnergy(i), i.Weight, !existing
		if weight, current := p.keep.Routing[i.WorkloadUID][i.ID]; current || existing {
			item.weight = weight
		}
		if plan.keep {
			if i.Ready {
				item.kappa, item.row = p.currentKappa(i)
			}
			if item.weight > 0 && i.Ready {
				plan.retained[w] = append(plan.retained[w], item)
			}
		}
		plan.items[w] = append(plan.items[w], item)
	}
	for w := range plan.items {
		slices.SortFunc(plan.items[w], func(a, b capacityItem) int { return cmp.Or(cmp.Compare(a.e, b.e), cmp.Compare(a.id, b.id)) })
	}
}

// sum records the plan's κ sums per Workload.
func (p *capacityPlanner) sum(plan *capacityPlan) {
	plan.record.Kappa, plan.record.KappaRetained = map[string]float64{}, map[string]float64{}
	for w, ws := range p.ws {
		knew, kret := 0.0, 0.0
		for _, item := range plan.items[w] {
			knew += item.kappa
		}
		for _, item := range plan.retained[w] {
			kret += item.kappa
		}
		plan.record.Kappa[ws.d.UID], plan.record.KappaRetained[ws.d.UID] = knew, kret
	}
}

// bounded reports whether the plan set respects maxReplicas, maxInstances
// and, with replicas OFF, each Workload's current instance count.
func (p *capacityPlanner) bounded(choice []int) bool {
	counts, total := make([]int, len(p.ws)), 0
	for n, j := range choice {
		plan := p.plans[n][j]
		total += plan.others
		for w := range p.ws {
			counts[w] += len(plan.items[w])
			total += len(plan.items[w])
		}
	}
	for w, ws := range p.ws {
		if counts[w] > p.settings.MaxReplicas || !p.settings.Controls.Replicas && counts[w] != ws.current {
			return false
		}
	}
	return total <= p.settings.MaxInstances
}

// chain plans the Node change with schedulerPlan and sums the measured costs
// of the operations before the route (those the route depends on) and after
// it. It returns the instances stopped before the route and those removed
// before it.
func (p *capacityPlanner) chain(n int, plan *capacityPlan) (map[string]bool, map[string]bool, string) {
	node := p.snapshot.Nodes[n].Node
	c := SchedulerCandidate{Nodes: slices.Clone(p.keep.Nodes), Routing: p.keep.Routing}
	c.Nodes[n].PowerMode = plan.mode
	for _, i := range p.snapshot.Instances {
		if i.Node != node {
			c.Instances = append(c.Instances, i)
		}
	}
	c.Instances = append(c.Instances, plan.instances...)
	actions := schedulerPlan(p.snapshot, c)
	byID := map[string]Action{}
	route := ""
	for _, a := range actions {
		byID[a.ID] = a
		if a.Kind == schedulerActionRoute {
			route = a.ID
		}
	}
	before := map[string]bool{}
	var visit func(id string)
	visit = func(id string) {
		for _, dep := range byID[id].DependsOn {
			if !before[dep] {
				before[dep] = true
				visit(dep)
			}
		}
	}
	visit(route)
	stopped, removed := map[string]bool{}, map[string]bool{}
	for _, a := range actions {
		if a.Node != node || a.Fixed || a.Status == schedulerActionComplete || a.Status == schedulerActionCancel {
			continue
		}
		switch a.Kind {
		case schedulerActionStop:
			stopped[a.Instance] = stopped[a.Instance] || before[a.ID]
			continue
		case schedulerActionRemove:
			removed[a.Instance] = before[a.ID]
		case schedulerActionPrepare, schedulerActionPower:
		default:
			continue
		}
		cost := schedulerCost(p.snapshot.Costs, a.CostID)
		mean, longest, joules, ok := capacityOperationCost(cost)
		if !ok {
			return nil, nil, schedulerPlanMissingOperationCost
		}
		for trial, measured := range cost.Trials {
			if measured.EnergyUncertaintyJoules != 0 {
				if plan.operationBounds == nil {
					plan.operationBounds = schedulerEnergyBounds{}
				}
				source := schedulerBoundSource{reference: cost.Reference, trial: trial}
				plan.operationBounds[source] += measured.EnergyUncertaintyJoules / float64(len(cost.Trials))
			}
		}
		if before[a.ID] {
			plan.preMean, plan.preMax, plan.preJ = plan.preMean+mean, plan.preMax+longest, plan.preJ+joules
		} else {
			plan.postMean, plan.postMax, plan.postJ = plan.postMean+mean, plan.postMax+longest, plan.postJ+joules
		}
	}
	plan.record.Pre, plan.record.Post = time.Duration(plan.preMax*1e9), time.Duration(plan.postMax*1e9)
	plan.record.Joules = plan.preJ + plan.postJ
	plan.record.EnergyUncertaintyJoules = plan.operationBounds.halfWidth()
	return stopped, removed, ""
}

// capacityOperationCost is a cost's mean and longest trial seconds and mean
// wall joules; a target-baseline-delta trial adds back its subtracted watts.
func capacityOperationCost(cost *OperationCost) (float64, float64, float64, bool) {
	if cost == nil || cost.Meter == "" || cost.Scope != schedulerNodeWallScope || len(cost.Trials) == 0 {
		return 0, 0, 0, false
	}
	if cost.EnergyScope == schedulerTargetBaselineDelta && (cost.SubtractedWatts == nil || !schedulerFinite(*cost.SubtractedWatts)) || cost.EnergyScope != schedulerWholeWall && cost.EnergyScope != schedulerTargetBaselineDelta {
		return 0, 0, 0, false
	}
	var mean, longest, joules float64
	for _, t := range cost.Trials {
		d := t.Duration.Seconds()
		if d <= 0 || !schedulerFinite(t.Joules) || !schedulerValidHalfWidth(t.EnergyUncertaintyJoules) {
			return 0, 0, 0, false
		}
		j := t.Joules
		if cost.EnergyScope == schedulerTargetBaselineDelta {
			j += *cost.SubtractedWatts * d
		}
		mean += d / float64(len(cost.Trials))
		joules += j / float64(len(cost.Trials))
		longest = max(longest, d)
	}
	return mean, longest, joules, true
}

// ActionDeadlineExceeded marks a running action past its deadline; its goal
// is abandoned and the next decision re-plans.
const ActionDeadlineExceeded = "ActionDeadlineExceeded"

// ActionDeadline is how long a running action may take: the longest measured
// trial of its cost (none for route, stop and drain) plus one observation
// window.
func ActionDeadline(window time.Duration, costs []OperationCost, a Action) time.Duration {
	longest := time.Duration(0)
	if cost := schedulerCost(costs, a.CostID); cost != nil {
		for _, t := range cost.Trials {
			longest = max(longest, t.Duration)
		}
	}
	return longest + window
}

// capacityJudgment is the §3 judgment of one Workload under κ sums and route r.
type capacityJudgment struct {
	backlog           float64
	wait, drain       *float64
	final, transition *float64
	satisfied         bool
}

func capacityJudge(w capacityWorkload, knew, kret float64, present bool, r float64) capacityJudgment {
	j := capacityJudgment{backlog: float64(w.queued) + max(0, w.rate-kret)*r}
	zero := 0.0
	switch {
	case !present:
	case knew > 0:
		final := w.rate/knew - 1
		j.final = &final
	case w.rate == 0:
		j.final = &zero
	}
	if knew > 0 {
		wait := j.backlog / knew
		j.wait = &wait
		if knew > w.rate {
			drain := j.backlog / (knew - w.rate)
			j.drain = &drain
		}
	}
	// Any backlog, the queue keep holds included, must drain within L − m.
	switch {
	case j.backlog == 0:
		j.transition = &zero
	case j.wait != nil && w.slack > 0:
		transition := *j.wait/w.slack - 1
		j.transition = &transition
	}
	j.satisfied = j.final != nil && *j.final <= 0 && j.transition != nil && *j.transition <= 0
	return j
}

// worst is the largest exceedance over Workloads and both judgments; +Inf when unbounded.
func (j capacityJudgment) worst() float64 {
	worst := math.Inf(-1)
	for _, x := range []*float64{j.final, j.transition} {
		if x == nil {
			return math.Inf(1)
		}
		worst = max(worst, *x)
	}
	return worst
}

// capacityFixedWeights keeps each item's current weight (an option's weight
// for a new instance) when routing is not controlled; ok is false when some
// instance's share of rate exceeds its κ or no weight is positive.
func capacityFixedWeights(rate float64, items []capacityItem) ([]int32, []float64, bool) {
	weights, y := make([]int32, len(items)), make([]float64, len(items))
	total := 0.0
	for k, item := range items {
		weights[k] = item.weight
		total += float64(item.weight)
	}
	ok := len(items) == 0 || total > 0
	for k, item := range items {
		if total > 0 {
			y[k] = rate * float64(weights[k]) / total
		}
		ok = ok && y[k] <= item.kappa*(1+1e-12)
	}
	return weights, y, ok
}

// capacityWeights fills rate plus the backlog drain rate onto items (sorted by
// e) up to each κ and rounds the shares to integers summing to
// schedulerRouteUnits by largest remainder. y is the fill of rate alone, for
// request energy. A weight whose share of rate exceeds its κ moves one unit at
// a time to the item with the most room; ok is false when no item has room.
// At zero load the weights are equal; past the κ sum they follow κ.
func capacityWeights(rate, drain float64, items []capacityItem) ([]int32, []float64, bool) {
	n := len(items)
	if n == 0 {
		return nil, nil, true
	}
	fill := rate + drain
	y, shares := make([]float64, n), make([]float64, n)
	total, left, routed := 0.0, rate, fill
	for k, item := range items {
		y[k] = min(left, item.kappa)
		left -= y[k]
		shares[k] = min(routed, item.kappa)
		routed -= shares[k]
		total += item.kappa
	}
	for k := range items {
		switch {
		case fill == 0 || total == 0:
			shares[k] = 1 / float64(n)
		case total < fill:
			shares[k] = items[k].kappa / total
		default:
			shares[k] /= fill
		}
	}
	weights := make([]int32, n)
	order := make([]int, n)
	sum := int32(0)
	for k, share := range shares {
		weights[k] = int32(math.Floor(share * schedulerRouteUnits))
		sum += weights[k]
		order[k] = k
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return cmp.Compare(shares[b]*schedulerRouteUnits-float64(weights[b]), shares[a]*schedulerRouteUnits-float64(weights[a]))
	})
	for k := 0; sum < schedulerRouteUnits; k = (k + 1) % n {
		weights[order[k]]++
		sum++
	}
	if rate == 0 || total < rate {
		return weights, y, true // the κ sum already decides the final judgment
	}
	// room is κ minus the load of one more unit; float slack keeps exact fills in.
	room := func(k int) float64 {
		return items[k].kappa*(1+1e-12) - rate*float64(weights[k]+1)/schedulerRouteUnits
	}
	for range schedulerRouteUnits {
		over := -1
		for k, item := range items {
			if rate*float64(weights[k])/schedulerRouteUnits > item.kappa*(1+1e-12) {
				over = k
				break
			}
		}
		if over < 0 {
			return weights, y, true
		}
		to := -1
		for k := range items {
			if k != over && room(k) >= 0 && (to < 0 || room(k) > room(to)) {
				to = k
			}
		}
		if to < 0 {
			return weights, y, false
		}
		weights[over]--
		weights[to]++
	}
	return weights, y, false
}

// capacityResult is one plan set evaluated exactly: per-Workload judgments,
// weights and the analytic energy over [0, T].
type capacityResult struct {
	choice   []int
	route    float64
	keep     bool
	key      string
	joules   float64
	worst    float64
	excesses []float64 // per-Workload worst exceedances, descending
	feasible bool
	// final marks a plan set whose final (steady-state) judgment holds for
	// every Workload, route weights within κ included.
	final bool
	// weightsExceeded marks a route weight whose share of λ exceeds its κ.
	weightsExceeded bool
	// unroutedPrepare marks a new instance its weights leave at 0; unroutes
	// a protected routed instance they leave at 0.
	unroutedPrepare, unroutes bool
	judgments                 []capacityJudgment
	weights                   []map[string]int32
	rows                      [][]string
	sums                      [][2]float64
	// unpaid names the Nodes whose replacement payback reverted to keep.
	unpaid []string
	// withinUncertainty marks keep chosen over a transition whose gain is
	// within the measurement uncertainty (minimumGain).
	withinUncertainty bool
	gainUncertainty   float64
	exclusion         string
	reserves          []SchedulerTransitionReserve
}

// evaluate judges the plan set (one plan index per Node) at route r and sums
// its compared Node energy and request energy: [0, r] by the retained
// instances up to κ_ret at their current weights, the overflow
// max(0, λ − κ_ret)·r and [r, T] by the target fill y.
func (p *capacityPlanner) evaluate(choice []int, r float64) capacityResult {
	t := p.period
	res := capacityResult{choice: choice, route: r, keep: true}
	keys := make([]string, len(choice))
	items, retained := make([][]capacityItem, len(p.ws)), make([][]capacityItem, len(p.ws))
	present := make([]bool, len(p.ws))
	for n, j := range choice {
		plan := p.plans[n][j]
		res.keep = res.keep && plan.keep
		keys[n] = p.snapshot.Nodes[n].Node.UID + "=" + plan.record.Key
		if p.compared[n] {
			res.joules += plan.energy(r, t)
		}
		for w := range p.ws {
			items[w] = append(items[w], plan.items[w]...)
			retained[w] = append(retained[w], plan.retained[w]...)
			present[w] = present[w] || len(plan.items[w]) > 0
		}
	}
	res.feasible, res.final, res.worst = true, true, math.Inf(-1)
	res.excesses = make([]float64, len(p.ws))
	for w, ws := range p.ws {
		slices.SortFunc(items[w], func(a, b capacityItem) int { return cmp.Or(cmp.Compare(a.e, b.e), cmp.Compare(a.id, b.id)) })
		knew, kret, weightSum := 0.0, 0.0, 0.0
		rows := []string{}
		for _, item := range items[w] {
			knew += item.kappa
			rows = append(rows, item.row)
		}
		for _, item := range retained[w] {
			kret += item.kappa
			weightSum += float64(item.weight)
		}
		j := capacityJudge(ws, knew, kret, present[w], r)
		// The route also drains B within L − m.
		drain := 0.0
		if j.backlog > 0 && ws.slack > 0 {
			drain = j.backlog / ws.slack
		}
		var weights []int32
		var y []float64
		var ok bool
		switch current, carries := p.currentWeights(ws.d.UID, items[w], ws.rate+drain); {
		case !p.settings.Controls.Routing:
			weights, y, ok = capacityFixedWeights(ws.rate, items[w])
		case carries:
			weights, y, ok = capacityFixedWeights(ws.rate, current)
		default:
			weights, y, ok = capacityWeights(ws.rate, drain, items[w])
		}
		units := 0.0
		for _, weight := range weights {
			units += float64(weight)
		}
		routes := p.weightRoutes(&res, ws, items[w], weights)
		worst := j.worst()
		if !ok {
			// A weight past its κ fails the final judgment by that excess.
			j.satisfied, res.weightsExceeded = false, true
			for k, item := range items[w] {
				if weights[k] == 0 {
					continue
				}
				excess := math.Inf(1)
				if item.kappa > 0 {
					excess = ws.rate*float64(weights[k])/units/item.kappa - 1
				}
				worst = max(worst, excess)
			}
		}
		res.feasible = res.feasible && j.satisfied
		res.final = res.final && ok && j.final != nil && *j.final <= 0
		res.worst = max(res.worst, worst)
		res.excesses[w] = worst
		served, mean := 0.0, 0.0
		for k, item := range items[w] {
			served += y[k]
			mean += y[k] * item.e
		}
		if served > 0 {
			mean /= served
		}
		initial := 0.0
		if weightSum > 0 {
			for _, item := range retained[w] {
				initial += min(ws.rate, kret) * float64(item.weight) / weightSum * item.e
			}
		}
		res.joules += initial*r + max(0, ws.rate-kret)*r*mean + (served*mean+max(0, ws.rate-served)*mean)*(t-r)
		res.judgments = append(res.judgments, j)
		res.weights = append(res.weights, routes)
		res.rows = append(res.rows, rows)
		res.sums = append(res.sums, [2]float64{knew, kret})
	}
	res.exclusion, res.reserves = p.transitionConstraints(choice, r)
	slices.SortFunc(res.excesses, func(a, b float64) int { return cmp.Compare(b, a) })
	res.key = strings.Join(keys, "|")
	return res
}

// weightRoutes maps each item with a positive weight to that weight and
// marks the result when the weights leave a new or protected routed instance at 0.
func (p *capacityPlanner) weightRoutes(res *capacityResult, w capacityWorkload, items []capacityItem, weights []int32) map[string]int32 {
	routes := map[string]int32{}
	for k, item := range items {
		if weights[k] > 0 {
			routes[item.id] = weights[k]
			continue
		}
		res.unroutedPrepare = res.unroutedPrepare || item.prepare
		res.unroutes = res.unroutes || (p.partial || w.growing) && schedulerInstanceRouted(p.keep.Routing, item.id)
	}
	return routes
}

// currentWeights returns items carrying the Workload's current route weights
// (a new instance 0). carries reports that every routed instance is among
// items, the weights use the 1,000 scale and each share of fill (λ plus the
// backlog drain) stays within its κ; the weights then stay as they are.
func (p *capacityPlanner) currentWeights(uid string, items []capacityItem, fill float64) ([]capacityItem, bool) {
	routes := p.keep.Routing[uid]
	current := slices.Clone(items)
	total, routed := int32(0), 0
	for k := range current {
		current[k].weight = max(0, routes[current[k].id])
		total += current[k].weight
		if current[k].weight > 0 {
			routed++
		}
	}
	positive := 0
	for _, weight := range routes {
		if weight > 0 {
			positive++
		}
	}
	if total != schedulerRouteUnits || routed != positive {
		return nil, false
	}
	_, _, carries := capacityFixedWeights(fill, current)
	return current, carries
}

// better orders feasible plan sets first; among the rest final-feasible first,
// then lexicographically smaller descending per-Workload exceedances.
// Only a complete tie reaches energy, keep and the logical key.
func (a capacityResult) better(b *capacityResult) bool {
	if b == nil {
		return true
	}
	if a.feasible != b.feasible {
		return a.feasible
	}
	if !a.feasible && a.final != b.final {
		return a.final
	}
	if !a.feasible {
		if order := slices.Compare(a.excesses, b.excesses); order != 0 {
			return order < 0
		}
	}
	if a.joules != b.joules {
		return a.joules < b.joules
	}
	if a.keep != b.keep {
		return a.keep
	}
	return a.key < b.key
}

// payback reverts each replacing node plan of a feasible result to keep when
// the result stays feasible without it and, at the least load of the
// observation window, the replacement does not repay its transition over T.
func (p *capacityPlanner) payback(best capacityResult) capacityResult {
	if !best.feasible {
		return best
	}
	for n, j := range best.choice {
		if !p.plans[n][j].replaces {
			continue
		}
		alt := slices.Clone(best.choice)
		alt[n] = 0
		r := p.routeOf(alt)
		kept := p.evaluate(alt, r)
		if !kept.feasible || !p.admits(&kept) || p.leastLoadJoules(alt, r) > p.leastLoadJoules(best.choice, best.route) {
			continue
		}
		kept.unpaid = append(slices.Clone(best.unpaid), p.snapshot.Nodes[n].Node.UID)
		best = kept
	}
	return best
}

// minimumGain requires nominal gain over T to exceed the deterministic
// operation and selected-baseline sensitivity bounds. Signed exposures to
// identical immutable evidence cancel before distinct residuals are summed.
// Profile statistical uncertainty, including BEO idle fallback, is outside
// this hurdle; it is not a joint confidence interval for predicted gain.
func (p *capacityPlanner) minimumGain(best capacityResult) capacityResult {
	if !best.feasible || best.keep {
		return best
	}
	keep := p.evaluate(make([]int, len(p.plans)), 0)
	if !keep.feasible {
		return best
	}
	bounds := schedulerEnergyBounds{}
	for n, j := range best.choice {
		if p.compared[n] {
			p.plans[n][0].addBounds(bounds, 0, p.period, 1)
			p.plans[n][j].addBounds(bounds, best.route, p.period, -1)
		}
	}
	best.gainUncertainty = bounds.halfWidth()
	if keep.joules-best.joules > best.gainUncertainty {
		return best
	}
	keep.withinUncertainty = true
	keep.gainUncertainty = best.gainUncertainty
	return keep
}

// routeOf is the route time of a plan set: its longest chain before the route.
func (p *capacityPlanner) routeOf(choice []int) float64 {
	r := 0.0
	for n, j := range choice {
		if plan := p.plans[n][j]; !plan.keep {
			r = max(r, plan.preMax)
		}
	}
	return r
}

// leastLoadJoules is the plan set's energy over T with every Workload at its
// least rate of the observation window.
func (p *capacityPlanner) leastLoadJoules(choice []int, r float64) float64 {
	rates := make([]float64, len(p.ws))
	for w := range p.ws {
		rates[w], p.ws[w].rate = p.ws[w].rate, p.ws[w].minRate
	}
	joules := p.evaluate(choice, r).joules
	for w := range p.ws {
		p.ws[w].rate = rates[w]
	}
	return joules
}

// search walks from two starts, keep and every Node emptied (its plan without
// instances, keep when it has none), so a change of many Nodes that each step
// from keep would refuse is still reached from the other side. From each
// start a step moves to the best admissible plan set that changes at most k
// Nodes, first with k = 2 until no step improves, then with k = 3 from there.
// The better end of the two starts is the result. At deadline the sweep
// stops; the best plan set found so far stays and p.searched records the cut.
func (p *capacityPlanner) search(deadline time.Time) (capacityResult, error) {
	start := time.Now()
	keep, empty := make([]int, len(p.plans)), make([]int, len(p.plans))
	for n, plans := range p.plans {
		empty[n] = max(0, slices.IndexFunc(plans, func(plan *capacityPlan) bool { return plan.record.Eligible && len(plan.instances) == 0 }))
	}
	starts := [][]int{keep}
	if !slices.Equal(empty, keep) {
		starts = append(starts, empty)
	}
	ends := make([]capacityResult, len(starts))
	for s, choice := range starts {
		ends[s] = p.evaluate(slices.Clone(choice), p.routeOf(choice))
	}
	for k := 2; k <= 3 && !p.searched.BudgetCut; k++ {
		p.searched.Neighborhood = k
		for s := range starts {
			for !p.searched.BudgetCut {
				step, err := p.sweep(ends[s].choice, k, deadline)
				if err != nil {
					return capacityResult{}, err
				}
				if step == nil || !step.better(&ends[s]) {
					break
				}
				ends[s] = *step
			}
		}
	}
	best := ends[0]
	for _, end := range ends[1:] {
		if p.bounded(end.choice) && p.admits(&end) && end.better(&best) {
			best = end
		}
	}
	p.searched.Duration = time.Since(start)
	return best, nil
}

// sweep evaluates the admissible plan sets that change 1 to k Nodes of choice
// and returns the best (nil when there is none). planSearchWorkers goroutines
// take the first changed Node plan in turn; better is a total order, so the
// result does not depend on their timing.
func (p *capacityPlanner) sweep(choice []int, k int, deadline time.Time) (*capacityResult, error) {
	type move struct{ n, j int }
	moves := []move{}
	for n, plans := range p.plans {
		for j, plan := range plans {
			if j != choice[n] && plan.record.Eligible {
				moves = append(moves, move{n, j})
			}
		}
	}
	workers := p.settings.PlanSearchWorkers
	bests, counts := make([]*capacityResult, workers), make([]int, workers)
	excluded := make([]map[string]int, workers)
	rejections := make([]map[string]SchedulerTransitionReserve, workers)
	for w := range excluded {
		excluded[w] = map[string]int{}
	}
	var next atomic.Int64
	var cut atomic.Bool
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := slices.Clone(choice)
			var walk func(i, left int)
			walk = func(i, left int) {
				m := moves[i]
				c[m.n] = m.j
				defer func() { c[m.n] = choice[m.n] }()
				if cut.Load() || time.Now().After(deadline) {
					cut.Store(true)
					return
				}
				if p.bounded(c) {
					counts[w]++
					res := p.evaluate(slices.Clone(c), p.routeOf(c))
					rejections[w] = reserveRejections(rejections[w], res.reserves, res.key)
					if res.exclusion != "" {
						excluded[w][res.exclusion]++
					}
					if p.admits(&res) && res.better(bests[w]) {
						bests[w] = &res
					}
				}
				for i2 := i + 1; left > 1 && i2 < len(moves); i2++ {
					if moves[i2].n != m.n {
						walk(i2, left-1)
					}
				}
			}
			for i := int(next.Add(1)) - 1; i < len(moves) && p.ctx.Err() == nil && !cut.Load(); i = int(next.Add(1)) - 1 {
				walk(i, k)
			}
		}()
	}
	wg.Wait()
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	if p.searched.Excluded == nil {
		p.searched.Excluded = map[string]int{}
	}
	for _, counts := range excluded {
		for reason, count := range counts {
			p.searched.Excluded[reason] += count
		}
	}
	for _, records := range rejections {
		for _, record := range records {
			p.searched.ReserveRejections = reserveRejections(p.searched.ReserveRejections, []SchedulerTransitionReserve{record}, "")
		}
	}
	var best *capacityResult
	for w, res := range bests {
		p.searched.Evaluated += counts[w]
		if res != nil && res.better(best) {
			best = res
		}
	}
	p.searched.BudgetCut = cut.Load()
	return best, nil
}

// admits reports whether a scored plan set may be chosen: every new instance
// routed, and every protected serving instance still routed.
func (p *capacityPlanner) admits(res *capacityResult) bool {
	return res.exclusion == "" && !res.unroutedPrepare && !res.unroutes
}

// decide turns the chosen plan set into the decision: keep, a route-only
// target, or a target whose actions come from schedulerPlan.
func (p *capacityPlanner) decide(decision SchedulerDecision, best capacityResult) SchedulerDecision {
	keepChoice := make([]int, len(p.plans))
	keepResult := p.evaluate(keepChoice, 0)
	keep := &decision.Keep
	keep.ExpectedJoules, keep.WorstExceedance, keep.Status = &keepResult.joules, capacityWorst(keepResult.worst), schedulerStatusFeasible
	if !keepResult.feasible {
		keep.Status = SchedulerStatusOverload
	}
	decision.Search.Examined = p.examined
	for n, compared := range p.compared {
		if compared {
			decision.Search.ComparedNodes = append(decision.Search.ComparedNodes, p.snapshot.Nodes[n].Node)
		}
	}
	search := p.searched
	record := &SchedulerCapacity{Period: time.Duration(p.period * 1e9), Route: time.Duration(best.route * 1e9), Search: &search, GainUncertaintyJoules: best.gainUncertainty}
	record.TransitionReserves = best.reserves
	for _, plans := range p.plans {
		for _, plan := range plans {
			record.Plans = append(record.Plans, plan.record)
		}
	}
	for w, ws := range p.ws {
		j := best.judgments[w]
		down := p.snapshot.ScaleDown[ws.d.UID]
		record.Workloads = append(record.Workloads, SchedulerCapacityWorkload{
			UID: ws.d.UID, Rate: ws.rate, MinimumDemand: p.settings.MinimumDemand[ws.d.UID], Queued: ws.queued, KappaNew: best.sums[w][0], KappaRetained: best.sums[w][1], Backlog: j.backlog,
			ScaleDown:   &down,
			WaitSeconds: j.wait, DrainSeconds: j.drain, FinalExceedance: j.final, TransitionExceedance: j.transition, Weights: best.weights[w], Rows: best.rows[w],
		})
	}
	decision.Capacity = record
	target := SchedulerCandidate{Nodes: slices.Clone(p.keep.Nodes), Routing: map[string]map[string]int32{}, ExpectedJoules: &best.joules, WorstExceedance: capacityWorst(best.worst), Status: schedulerStatusFeasible}
	for _, d := range p.snapshot.Workloads {
		if d == nil {
			continue
		}
		if w, controlled := p.index[d.UID]; controlled {
			target.Routing[d.UID] = best.weights[w]
		} else if routes := maps.Clone(p.keep.Routing[d.UID]); routes != nil {
			target.Routing[d.UID] = routes
		} else {
			target.Routing[d.UID] = map[string]int32{}
		}
	}
	removed := false
	idleRemoved := map[string]bool{}
	for n, j := range best.choice {
		plan := p.plans[n][j]
		if !plan.keep {
			target.Nodes[n].PowerMode = plan.mode
		}
		for _, i := range plan.instances {
			if p.idleRemovable(i, plan, best, target.Routing) {
				idleRemoved[i.ID] = true
				reason, reserves := p.idleTransition(best, idleRemoved)
				record.TransitionReserves = append(record.TransitionReserves, reserves...)
				if reason != "" {
					delete(idleRemoved, i.ID)
					target.Instances = append(target.Instances, i)
					decision.Reasons = appendUnique(decision.Reasons, reason)
					continue
				}
				decision.Reasons = append(decision.Reasons, schedulerIdleInstanceRemoved+":"+i.ID)
				removed = true
				continue
			}
			target.Instances = append(target.Instances, i)
		}
	}
	for _, node := range best.unpaid {
		decision.Reasons = append(decision.Reasons, schedulerReplacementUnpaid+":"+node)
	}
	if best.withinUncertainty {
		decision.Reasons = append(decision.Reasons, schedulerGainWithinUncertainty)
	}
	if !best.feasible {
		target.Status = SchedulerStatusOverload
		decision.Reasons = append(decision.Reasons, schedulerNoPlanMeetsSLO)
		if best.weightsExceeded {
			decision.Reasons = append(decision.Reasons, schedulerRouteWeightExceeds)
		}
	}
	// A protected Workload keeps its serving routes when keep is the fallback.
	if best.keep && !removed && (p.sameRouting(target.Routing) || best.unroutes) {
		chosen := decision.Keep
		chosen.Status = target.Status
		decision.Target, decision.Status = &chosen, schedulerStatusKeep
		if !best.feasible {
			decision.Status = SchedulerStatusOverload
		}
		return decision
	}
	sum := sha256.Sum256([]byte(best.key + fmt.Sprint(target.Routing)))
	target.ID = "plan-" + hex.EncodeToString(sum[:6])
	target.Actions = schedulerPlan(p.snapshot, target)
	decision.Target, decision.Status = &target, target.Status
	return decision
}

// idleRemovable reports an instance unrouted for one observation window that
// the chosen plan set leaves unrouted: owned by or serving a controlled
// Workload whose judgments still hold without its κ, or owned by nobody. The
// removal is for measurability and the lowest mode of an emptied Node, not
// energy.
func (p *capacityPlanner) idleRemovable(i SchedulerInstance, plan *capacityPlan, best capacityResult, routing map[string]map[string]int32) bool {
	if !p.idle(i) || schedulerInstanceRouted(routing, i.ID) {
		return false
	}
	// Measurement and excluded Nodes keep their instances.
	if slices.ContainsFunc(p.snapshot.Nodes, func(n SchedulerNode) bool {
		return n.Node == i.Node && (n.Measurement || slices.Contains(n.Reasons, NodeExcluded))
	}) {
		return false
	}
	uid := i.WorkloadUID
	if uid == "" {
		uid = i.OwnerWorkloadUID
	}
	if uid == "" && !schedulerEmptySlowAllowed(p.snapshot) || uid != "" && !schedulerSlowAllowed(p.snapshot, uid) {
		return false
	}
	// An instance owned by nobody and routed by no Workload serves nobody.
	w, controlled := p.index[uid]
	if uid == "" || !controlled {
		return uid == ""
	}
	if !best.feasible || i.WorkloadUID == "" {
		return true
	}
	knew := best.sums[w][0]
	for _, item := range plan.items[w] {
		if item.id == i.ID {
			knew -= item.kappa
		}
	}
	return capacityJudge(p.ws[w], knew, best.sums[w][1], knew > 0, best.route).satisfied
}

// sameRouting reports whether the controlled Workloads keep their current routes.
func (p *capacityPlanner) sameRouting(routing map[string]map[string]int32) bool {
	for _, w := range p.ws {
		current := maps.Clone(p.keep.Routing[w.d.UID])
		maps.DeleteFunc(current, func(_ string, weight int32) bool { return weight <= 0 })
		if !maps.Equal(current, routing[w.d.UID]) {
			return false
		}
	}
	return true
}

// capacityWorst is the recorded worst exceedance: never below zero, and the
// largest float for an unbounded one (no capacity), which JSON can carry.
func capacityWorst(worst float64) float64 {
	return min(max(0, worst), math.MaxFloat64)
}
