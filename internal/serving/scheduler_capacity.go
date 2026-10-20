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
	"time"
)

// Decision statuses and reasons of the capacity planner (analytic-planner §3, §4).
const (
	schedulerStatusKeep = "Keep"
	// SchedulerSharedInstance marks a captured instance routed to two or more
	// Workloads; the decision waits until the operator unshares it.
	SchedulerSharedInstance           = "SharedInstance"
	schedulerTransitionInProgress     = "TransitionInProgress"
	schedulerRouteWeightExceeds       = "RouteWeightExceedsCapacity"
	schedulerNoPlanMeetsSLO           = "NoPlanMeetsSLO"
	schedulerPlanMissingOperationCost = "MissingOperationCost"
	schedulerPlanInfeasibleResources  = "InfeasibleResources"
	schedulerPlanFixedWorkload        = "FixedWorkload"

	schedulerRouteUnits    = 1000  // route weights of one Workload sum to this
	schedulerMaxCells      = 50    // load cells per Workload
	schedulerMaxCellStates = 20000 // load-cell states of all Workloads

	// schedulerTargetBaselineDelta trials subtract a baseline the cost reports.
	schedulerTargetBaselineDelta = "target-baseline-delta"
	schedulerWholeWall           = "whole-wall"
)

// SchedulerCapacity records the capacity-sum judgment of a decision: the
// comparison period T, the chosen route time r, each controlled Workload in
// the chosen plan set and every node plan with its eligibility.
type SchedulerCapacity struct {
	Period    time.Duration               `json:"periodNanos"`
	Route     time.Duration               `json:"routeNanos"`
	Cells     int                         `json:"cells"`
	Workloads []SchedulerCapacityWorkload `json:"workloads"`
	Plans     []SchedulerNodePlan         `json:"plans"`
}

// SchedulerCapacityWorkload is one controlled Workload of the chosen plan set.
// Rate is λ (req/s), Backlog B (requests). A missing wait, drain or exceedance
// is unbounded: no capacity serves the Workload.
type SchedulerCapacityWorkload struct {
	UID                  string           `json:"uid"`
	Rate                 float64          `json:"rate"`
	Queued               int              `json:"queued"`
	KappaNew             float64          `json:"kappaNew"`
	KappaRetained        float64          `json:"kappaRetained"`
	Backlog              float64          `json:"backlog"`
	WaitSeconds          *float64         `json:"waitSeconds,omitempty"`
	DrainSeconds         *float64         `json:"drainSeconds,omitempty"`
	FinalExceedance      *float64         `json:"finalExceedance,omitempty"`
	TransitionExceedance *float64         `json:"transitionExceedance,omitempty"`
	Weights              map[string]int32 `json:"weights"`
	Rows                 []string         `json:"rows"`
}

// SchedulerNodePlan is one Node's plan: keep, or a set of option variants at
// one power mode. Kappa and KappaRetained are per Workload UID; Pre and Post
// are the longest chains before and after the route; Joules is the mean chain
// energy.
type SchedulerNodePlan struct {
	Node          NodeReference      `json:"node"`
	Key           string             `json:"key"`
	Eligible      bool               `json:"eligible"`
	Reason        string             `json:"reason,omitempty"`
	Kappa         map[string]float64 `json:"kappa,omitempty"`
	KappaRetained map[string]float64 `json:"kappaRetained,omitempty"`
	Pre           time.Duration      `json:"preNanos"`
	Post          time.Duration      `json:"postNanos"`
	Joules        float64            `json:"joules"`
}

// EvaluateScheduler plans with capacity sums: node plans, the transient and
// final SLO rule over exact κ sums, a Pareto DP per route time and analytic
// energy over one comparison period. capacity holds the rows CapacityTable.Rows
// returns.
func EvaluateScheduler(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot, capacity []CapacityRow) (SchedulerDecision, error) {
	if err := ctx.Err(); err != nil {
		return SchedulerDecision{}, err
	}
	snapshot = schedulerCanonical(snapshot)
	decision := SchedulerDecision{At: snapshot.At, Status: schedulerStatusPending, Settings: settings, Search: schedulerSearchPopulation(ctx, settings, snapshot), Workloads: schedulerWorkloadStates(settings, snapshot), References: slices.Clone(snapshot.References)}
	keep := SchedulerCandidate{ID: "keep", Keep: true, Nodes: snapshot.Nodes, Instances: slices.Clone(snapshot.Instances), Routing: schedulerInstanceRouting(ctx, snapshot.Workloads, snapshot.Instances)}
	for uid, routing := range snapshot.Routing {
		keep.Routing[uid] = routing
	}
	if err := settings.Validate(); err != nil {
		decision.Reasons, decision.Keep = []string{err.Error()}, keep
		return decision, ctx.Err()
	}
	// Lowering available Nodes is a fixed rule every plan, keep included, shares.
	keep.Nodes = schedulerCommonNodes(settings, snapshot)
	// Run status lives in snapshot.Actions; the goal keeps its planned copies,
	// so a goal action is finished only when its run reports it complete.
	transition := snapshot.InProgressTarget != nil && slices.ContainsFunc(snapshot.InProgressTarget.Actions, func(a Action) bool {
		return !a.Fixed && a.Status != schedulerActionCancel && a.Status != schedulerActionComplete && !slices.ContainsFunc(snapshot.Actions, func(run Action) bool {
			return run.ID == a.ID && run.Status == schedulerActionComplete
		})
	})
	var goal SchedulerCandidate
	if transition {
		goal = schedulerCurrentTarget(snapshot, *snapshot.InProgressTarget)
		keep.Actions = schedulerPlan(snapshot, goal)
	} else {
		p := schedulerPlanState{snap: snapshot, target: &keep, actions: []Action{}, nodeTail: map[string]string{}, prepares: []string{}, stops: map[string]string{}}
		p.changeNodes(keep)
		keep.Actions = p.actions
	}
	keep.Status = schedulerStatusPending
	decision.Keep = keep
	if shared := slices.DeleteFunc(slices.Clone(snapshot.Reasons), func(r string) bool { return !strings.HasPrefix(r, SchedulerSharedInstance) }); len(shared) != 0 {
		decision.Status, decision.Reasons = SchedulerSharedInstance, shared
		return decision, ctx.Err()
	}
	reasons := append(slices.Clone(snapshot.Reasons), schedulerValidateSnapshot(settings, snapshot)...)
	reasons = appendUnique(reasons, schedulerValidateResources(settings, snapshot.Nodes, snapshot.Instances)...)
	if len(reasons) != 0 {
		decision.Keep.Reasons, decision.Reasons = reasons, reasons
		return decision, ctx.Err()
	}
	if transition {
		// A plan assumes every chain starts now; the running transition ends first.
		target := keep
		target.ID, target.Nodes, target.Instances, target.Routing = goal.ID, goal.Nodes, goal.Instances, goal.Routing
		decision.Target, decision.Status, decision.Reasons = &target, schedulerStatusKeep, []string{schedulerTransitionInProgress}
		return decision, ctx.Err()
	}
	planner := newCapacityPlanner(ctx, settings, snapshot, keep, capacity, decision)
	if len(planner.ws) == 0 {
		zero := 0.0
		decision.Keep.Status, decision.Keep.ExpectedJoules = schedulerStatusFeasible, &zero
		target := decision.Keep
		decision.Target, decision.Status = &target, schedulerStatusKeep
		return decision, ctx.Err()
	}
	if pending := planner.pendingWorkloads(); len(pending) != 0 {
		decision.Status, decision.Reasons = CapacityPending, pending
		return decision, ctx.Err()
	}
	if err := planner.build(); err != nil {
		return SchedulerDecision{}, err
	}
	best, err := planner.solve()
	if err != nil {
		return SchedulerDecision{}, err
	}
	return planner.decide(decision, best), ctx.Err()
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
	queued  int     // q
	current int     // instances serving it now
	slack   float64 // L − m, seconds
	cells   int
	unit    float64 // req/s per load cell
}

// capacityItem is one instance serving a Workload: its κ, per-input energy e
// (batch 1) and its route weight now (a new instance: its option's weight).
type capacityItem struct {
	id     string
	kappa  float64
	e      float64
	weight int32
	row    string
}

type capacityPlan struct {
	record                             SchedulerNodePlan
	mode                               string
	keep                               bool
	instances                          []SchedulerInstance // the Node's instances after the plan
	items                              [][]capacityItem    // per Workload, by e then ID
	retained                           [][]capacityItem    // per Workload, current instances serving until the route
	vals                               []float64           // DP values (width per Workload, then −instances)
	others                             int                 // instances serving no controlled Workload
	serve                              [][]float64         // per Workload, request joules over T of y load cells
	preMean, preMax, postMean, postMax float64             // seconds
	preJ, postJ                        float64
	before, mid, after                 float64 // watts
}

// energy is the Node's joules over [0, t] when the route lands at r.
func (p *capacityPlan) energy(r, t float64) float64 {
	if p.keep {
		return p.before * t
	}
	return p.preJ + p.mid*(r-p.preMean) + p.postJ + p.after*(t-r-p.postMean)
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
	routes   []float64
	cells    int
	examined int
	// paretoMax is the largest Pareto set of one load-cell state, for tests and measurement.
	paretoMax int
	// free marks plans whose instance counts cannot pass maxReplicas or maxInstances.
	free     bool
	variants map[string]SchedulerOption // capacity variants by profile ID and batch limit
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
				w.rate = float64(load.ComputeInputs)
			}
		}
		for _, r := range snapshot.Outstanding {
			if r.Definition != nil && r.Definition.UID == state.UID {
				w.queued++
			}
		}
		w.rate = (w.rate + float64(w.queued)) / settings.ObservationWindow.Seconds()
		w.slack = w.d.SLO.TargetLatency.Seconds() - settings.CompletionMarginMS/1e3
		for _, i := range snapshot.Instances {
			if i.WorkloadUID == w.d.UID && !i.Stopped {
				w.current++
			}
		}
		p.index[w.d.UID] = len(p.ws)
		p.ws = append(p.ws, w)
	}
	// The largest cell count whose states over all Workloads stay within the bound.
	p.cells = schedulerMaxCells
	for p.cells > 1 && math.Pow(float64(p.cells+1), float64(len(p.ws))) > schedulerMaxCellStates {
		p.cells--
	}
	for i := range p.ws {
		if p.ws[i].rate > 0 {
			p.ws[i].cells, p.ws[i].unit = p.cells, p.ws[i].rate/float64(p.cells)
		}
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

// pendingWorkloads names each controlled Workload with no κ in any row.
func (p *capacityPlanner) pendingWorkloads() []string {
	built := map[string]bool{}
	for _, row := range p.rows {
		built[row.Input.WorkloadUID] = built[row.Input.WorkloadUID] || row.Kappa != nil
	}
	pending := []string{}
	for _, w := range p.ws {
		if !built[w.d.UID] {
			pending = append(pending, CapacityPending+":"+w.d.UID)
		}
	}
	return pending
}

// kappa looks up the row of option variant o for Workload w.
func (p *capacityPlanner) kappa(o SchedulerOption, w int) (float64, string, string) {
	key := NewCapacityInput(p.settings, o, p.ws[w].d).Key()
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

// build lists every Node's plans, the comparison period T and the route times.
func (p *capacityPlanner) build() error {
	common := p.keep.Nodes
	p.plans = make([][]*capacityPlan, len(p.snapshot.Nodes))
	p.compared = make([]bool, len(p.snapshot.Nodes))
	pre, post := 0.0, 0.0
	routes := []float64{0}
	for n := range p.snapshot.Nodes {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		p.plans[n] = p.nodePlans(n, common)
		for _, plan := range p.plans[n][1:] {
			if plan.record.Eligible {
				p.compared[n] = true
				pre, post = max(pre, plan.preMax), max(post, plan.postMax)
				routes = append(routes, plan.preMax)
			}
		}
		p.examined += len(p.plans[n])
	}
	p.elideCounts()
	p.period = max(p.settings.Horizon.Seconds(), pre+post)
	slices.Sort(routes)
	p.routes = slices.Compact(routes)
	for _, plans := range p.plans {
		for _, plan := range plans {
			p.serveTable(plan)
		}
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
	before, _, _, _, beforeReason := schedulerBaseline(p.snapshot.Baselines, at, current, p.snapshot.Options)
	keep.before = before
	plans := []*capacityPlan{keep}
	reason := ""
	switch {
	case !node.Ready:
		reason = "NodeUnavailable"
	case node.Measurement:
		reason = "MeasurementNode"
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
	if len(names) == 0 {
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
		for _, c := range groups[names[j]] {
			i := c.option.Instance
			if len(chosen) > 0 && (chosen[0].option.Instance.Profile.Condition.PowerMode != i.Profile.Condition.PowerMode || !p.sameModel(chosen[0].w, c.w)) {
				continue
			}
			replicas := 1
			for _, o := range chosen {
				if o.w == c.w {
					replicas++
				}
			}
			if replicas > p.settings.MaxReplicas || len(chosen)+1 > p.settings.MaxInstances {
				continue
			}
			next := append(slices.Clone(chosen), c)
			wanted := slices.Clone(allocations)
			for _, o := range next {
				wanted = append(wanted, o.option.Instance.Allocation)
			}
			if len(ValidateNodeBudget(node, wanted, nil)) != 0 {
				// Adding options never restores the budget, so the walk stops here.
				instances := make([]SchedulerInstance, len(next))
				for k, o := range next {
					instances[k] = o.option.Instance
					instances[k].WorkloadUID = p.ws[o.w].d.UID
				}
				plans = append(plans, &capacityPlan{record: SchedulerNodePlan{Node: node.Node, Key: planKey(instances), Reason: schedulerPlanInfeasibleResources}})
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

// sameModel reports whether two Workloads reference one ModelSpec; the
// instances on one Node share it.
func (p *capacityPlanner) sameModel(a, b int) bool {
	x, y := p.ws[a].d.Model, p.ws[b].d.Model
	return x.Name == y.Name && x.UID == y.UID
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
	chosenItems := map[string]capacityItem{}
	for _, c := range chosen {
		uid := p.ws[c.w].d.UID
		i := c.option.Instance
		i.ID, i.WorkloadUID, i.Ready, i.TargetID, i.OwnerWorkloadUID = fmt.Sprintf("target-%s-%s-%d", uid, i.Profile.ID, i.MaxBatch), uid, true, "", ""
		for _, old := range current {
			// The same physical executor serves on; a changed batch limit replaces
			// it. An unrouted one needs its prepared slot or owner as proof.
			if !used[old.ID] && mode == node.PowerMode && old.FixedBatchSize == 0 && schedulerState(old) == schedulerState(i) && old.MaxBatch == i.MaxBatch &&
				old.MaxWait == i.MaxWait && old.Capacity == i.Capacity && schedulerOwnerAllows(old.OwnerWorkloadUID, uid) &&
				(old.WorkloadUID == uid || old.WorkloadUID == "" && old.Ready && !old.Stopped && (old.TargetID != "" || old.OwnerWorkloadUID == uid)) {
				used[old.ID] = true
				old.WorkloadUID = uid
				i = old
				break
			}
		}
		plan.instances = append(plan.instances, i)
		chosenItems[i.ID] = capacityItem{kappa: c.kappa, row: c.row}
		if c.reason != "" && plan.record.Reason == "" {
			plan.record.Reason = c.reason
		}
	}
	plan.record.Key = planKey(plan.instances)
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
	}
	stopped, removedPre, reason := p.chain(n, plan)
	if reason != "" {
		plan.record.Reason = reason
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
	mid := slices.DeleteFunc(slices.Clone(current), func(i SchedulerInstance) bool { return removedPre[i.ID] })
	for _, i := range plan.instances {
		if !slices.ContainsFunc(current, func(old SchedulerInstance) bool { return old.ID == i.ID }) {
			mid = append(mid, i)
		}
	}
	var midReason, afterReason string
	plan.mid, _, _, _, midReason = schedulerBaseline(p.snapshot.Baselines, at, mid, p.snapshot.Options)
	plan.after, _, _, _, afterReason = schedulerBaseline(p.snapshot.Baselines, at, plan.instances, p.snapshot.Options)
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
		item.id, item.e, item.weight = i.ID, capacityInputEnergy(i), i.Weight
		if weight, current := p.keep.Routing[i.WorkloadUID][i.ID]; current || slices.ContainsFunc(p.snapshot.Instances, func(c SchedulerInstance) bool { return c.ID == i.ID }) {
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

// width is the number of DP values per Workload: κ_new, κ_ret, presence and
// −count, plus +count with replicas OFF so that counts compare by equality.
// The last value of a point is −(all instances). Every value compares as
// larger is better.
func (p *capacityPlanner) width() int {
	switch {
	case p.free:
		return 3
	case p.settings.Controls.Replicas:
		return 4
	}
	return 5
}

// elideCounts drops the count values when no plan set can pass maxReplicas
// or maxInstances (the largest plan of every Node summed stays within them,
// replicas ON): the counts then never decide a point.
func (p *capacityPlanner) elideCounts() {
	if !p.settings.Controls.Replicas {
		return
	}
	width, perWorkload, total := p.width(), make([]float64, len(p.ws)), 0.0
	for _, plans := range p.plans {
		most, mostTotal := make([]float64, len(p.ws)), 0.0
		for _, plan := range plans {
			if !plan.record.Eligible || plan.vals == nil {
				continue
			}
			for w := range p.ws {
				most[w] = max(most[w], -plan.vals[w*width+3])
			}
			mostTotal = max(mostTotal, -plan.vals[len(plan.vals)-1])
		}
		for w := range p.ws {
			perWorkload[w] += most[w]
		}
		total += mostTotal
	}
	if total > float64(p.settings.MaxInstances) || slices.ContainsFunc(perWorkload, func(n float64) bool { return n > float64(p.settings.MaxReplicas) }) {
		return
	}
	p.free = true
	for _, plans := range p.plans {
		for _, plan := range plans {
			if plan.vals != nil {
				p.sum(plan)
			}
		}
	}
}

// sum sets the plan's DP values and records its κ sums.
func (p *capacityPlanner) sum(plan *capacityPlan) {
	width := p.width()
	plan.vals = make([]float64, len(p.ws)*width+1)
	total := plan.others
	plan.record.Kappa, plan.record.KappaRetained = map[string]float64{}, map[string]float64{}
	for w, ws := range p.ws {
		v := plan.vals[w*width : (w+1)*width]
		for _, item := range plan.items[w] {
			v[0] += item.kappa
		}
		for _, item := range plan.retained[w] {
			v[1] += item.kappa
		}
		n := len(plan.items[w])
		if n > 0 {
			v[2] = 1
		}
		if width > 3 {
			v[3] = -float64(n)
		}
		if width == 5 {
			v[4] = float64(n)
		}
		total += n
		plan.record.Kappa[ws.d.UID], plan.record.KappaRetained[ws.d.UID] = v[0], v[1]
	}
	if !p.free {
		plan.vals[len(plan.vals)-1] = -float64(total)
	}
}

// bounded reports whether a point respects maxReplicas, maxInstances and,
// with replicas OFF, each Workload's current instance count (exactly once
// final); counts only grow, so a point past a bound stays past it.
func (p *capacityPlanner) bounded(vals []float64, final bool) bool {
	if p.free {
		return true
	}
	width := p.width()
	for w, ws := range p.ws {
		n := -vals[w*width+3]
		if n > float64(p.settings.MaxReplicas) || width == 5 && (n > float64(ws.current) || final && n != float64(ws.current)) {
			return false
		}
	}
	return -vals[len(vals)-1] <= float64(p.settings.MaxInstances)
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
		mean, longest, joules, ok := capacityOperationCost(schedulerCost(p.snapshot.Costs, a.CostID))
		if !ok {
			return nil, nil, schedulerPlanMissingOperationCost
		}
		if before[a.ID] {
			plan.preMean, plan.preMax, plan.preJ = plan.preMean+mean, plan.preMax+longest, plan.preJ+joules
		} else {
			plan.postMean, plan.postMax, plan.postJ = plan.postMean+mean, plan.postMax+longest, plan.postJ+joules
		}
	}
	plan.record.Pre, plan.record.Post = time.Duration(plan.preMax*1e9), time.Duration(plan.postMax*1e9)
	plan.record.Joules = plan.preJ + plan.postJ
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
		if d <= 0 || !schedulerFinite(t.Joules) {
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

// serveTable is the DP request energy of y load cells per Workload: the cells
// fill the cheapest instances up to their κ, and the last instance takes what
// rounding a plan's ⌈κ/cell⌉ cells adds. ponytail: it charges the whole period
// at the target assignment; ExpectedJoules splits the request energy at the route.
func (p *capacityPlanner) serveTable(plan *capacityPlan) {
	if !plan.record.Eligible {
		return
	}
	plan.serve = make([][]float64, len(p.ws))
	for w, ws := range p.ws {
		limit := 0
		if ws.cells > 0 && len(plan.items[w]) > 0 {
			limit = min(ws.cells, int(math.Ceil(plan.vals[w*p.width()]/ws.unit)))
		}
		plan.serve[w] = make([]float64, limit+1)
		for y := 1; y <= limit; y++ {
			plan.serve[w][y] = capacityServe(plan.items[w], float64(y)*ws.unit) * p.period
		}
	}
}

// capacityServe is the request power (W) of rate filled onto items by e.
func capacityServe(items []capacityItem, rate float64) float64 {
	watts := 0.0
	for k, item := range items {
		take := min(rate, item.kappa)
		if k == len(items)-1 {
			take = rate
		}
		watts += take * item.e
		rate -= take
	}
	return watts
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
	switch {
	case w.rate <= kret:
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
	feasible bool
	// weightsExceeded marks a route weight whose share of λ exceeds its κ.
	weightsExceeded bool
	judgments       []capacityJudgment
	weights         []map[string]int32
	rows            [][]string
	sums            [][2]float64
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
	res.feasible, res.worst = true, math.Inf(-1)
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
		// Under the transition rule the route also drains B within L − m.
		drain := 0.0
		if ws.rate > kret && ws.slack > 0 {
			drain = j.backlog / ws.slack
		}
		weights, y, ok := capacityWeights(ws.rate, drain, items[w])
		if !p.settings.Controls.Routing {
			weights, y, ok = capacityFixedWeights(ws.rate, items[w])
		}
		units := 0.0
		for _, weight := range weights {
			units += float64(weight)
		}
		routes := map[string]int32{}
		for k, item := range items[w] {
			if weights[k] > 0 {
				routes[item.id] = weights[k]
			}
		}
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
		res.worst = max(res.worst, worst)
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
	res.key = strings.Join(keys, "|")
	return res
}

// better orders feasible plan sets first, then the smaller worst exceedance,
// energy, keep and the logical key.
func (a capacityResult) better(b *capacityResult) bool {
	if b == nil {
		return true
	}
	if a.feasible != b.feasible {
		return a.feasible
	}
	if !a.feasible && a.worst != b.worst {
		return a.worst < b.worst
	}
	if a.joules != b.joules {
		return a.joules < b.joules
	}
	if a.keep != b.keep {
		return a.keep
	}
	return a.key < b.key
}

type capacityPoint struct {
	energy float64
	parent int32
	plan   int32
	keep   bool
}

// capacityStage holds the Pareto points of one DP step: per load-cell state
// the points no other point beats in energy and every κ sum at once.
type capacityStage struct {
	points []capacityPoint
	vals   []float64
	cells  [][]int32
	stride int
}

// reset empties the stage for states load-cell states, keeping its buffers.
func (s *capacityStage) reset(states, stride int) {
	s.points, s.vals, s.stride = s.points[:0], s.vals[:0], stride
	if len(s.cells) != states {
		s.cells = make([][]int32, states)
	}
	for state := range s.cells {
		s.cells[state] = s.cells[state][:0]
	}
}

func (s *capacityStage) values(index int32) []float64 {
	return s.vals[int(index)*s.stride : int(index+1)*s.stride]
}

func (s *capacityStage) insert(state int, point capacityPoint, vals []float64) {
	list, stride := s.cells[state], s.stride
	// Energy decides first, so the values of most points are never read.
	for _, index := range list {
		q := &s.points[index]
		if q.energy > point.energy {
			continue
		}
		qv := s.vals[int(index)*stride : int(index)*stride+stride]
		if capacityAtLeast(qv, vals) {
			if q.energy == point.energy && point.keep && !q.keep && slices.Equal(qv, vals) {
				break // exact tie: keep replaces it below
			}
			return
		}
	}
	kept := list[:0]
	for _, index := range list {
		if point.energy > s.points[index].energy || !capacityAtLeast(vals, s.vals[int(index)*stride:int(index)*stride+stride]) {
			kept = append(kept, index)
		}
	}
	s.points = append(s.points, point)
	s.vals = append(s.vals, vals...)
	s.cells[state] = append(kept, int32(len(s.points)-1))
}

// capacityAtLeast reports a[k] ≥ b[k] for every value.
func capacityAtLeast(a, b []float64) bool {
	for k, x := range b {
		if a[k] < x {
			return false
		}
	}
	return true
}

// solve runs the node DP once per route time r and keeps the best plan set.
// A state is the load cells assigned per Workload and whether some chosen
// chain ends exactly at r; feasible sets come from the fully assigned state.
//
//nolint:gocyclo // One DP pass per route time with its final judgment.
func (p *capacityPlanner) solve() (capacityResult, error) {
	k := len(p.ws)
	radix := make([]int, k)
	states := 1
	for w := range p.ws {
		radix[w] = states
		states *= p.ws[w].cells + 1
	}
	full := 0
	for w := range p.ws {
		full += p.ws[w].cells * radix[w]
	}
	nodes := []int{}
	for n, plans := range p.plans {
		if p.compared[n] || slices.ContainsFunc(plans[0].vals, func(v float64) bool { return v != 0 }) {
			nodes = append(nodes, n)
		}
	}
	var best *capacityResult
	var overload *capacityResult
	// The stages, the two per-Workload scratch stages and the value buffer are
	// reused for every route time.
	stages := make([]*capacityStage, len(nodes)+1)
	for s := range stages {
		stages[s] = &capacityStage{}
	}
	scratch := [2]*capacityStage{{}, {}}
	stride, width := len(p.ws)*p.width()+1, p.width()
	vals := make([]float64, stride)
	for _, r := range p.routes {
		if err := p.ctx.Err(); err != nil {
			return capacityResult{}, err
		}
		start := stages[0]
		start.reset(2*states, stride)
		hit := 0
		if r == 0 {
			hit = 1
		}
		start.insert(hit*states, capacityPoint{parent: -1, keep: true}, make([]float64, stride))
		for s, n := range nodes {
			prev, next := stages[s], stages[s+1]
			next.reset(2*states, stride)
			for _, j := range p.frontier(n, r) {
				plan := p.plans[n][j]
				// Load cells spread one Workload at a time: the plan's points of
				// Workload w feed Workload w+1, and the last ones enter next.
				target := next
				if k > 1 {
					target = scratch[0]
					target.reset(2*states, stride)
				}
				for state, list := range prev.cells {
					offset := state - state%states
					if !plan.keep && plan.preMax == r {
						offset = states
					}
					for _, index := range list {
						point := prev.points[index]
						energy := point.energy
						if p.compared[n] {
							energy += plan.energy(r, p.period)
						}
						for v, x := range prev.values(index) {
							vals[v] = x + plan.vals[v]
						}
						point = capacityPoint{parent: index, plan: int32(j), keep: point.keep && plan.keep}
						if !point.keep && !p.bounded(vals, false) {
							continue
						}
						p.spread(target, plan, 0, offset+state%states, point, energy, vals, radix[0])
					}
				}
				for w := 1; w < k; w++ {
					source := target
					if target = next; w < k-1 {
						target = scratch[w%2]
						target.reset(2*states, stride)
					}
					for state, list := range source.cells {
						for _, index := range list {
							p.spread(target, plan, w, state, source.points[index], source.points[index].energy, source.values(index), radix[w])
						}
					}
				}
			}
			for _, list := range next.cells {
				p.paretoMax = max(p.paretoMax, len(list))
			}
		}
		last := stages[len(stages)-1]
		for state := states; state < 2*states; state++ {
			for _, index := range last.cells[state] {
				vals := last.values(index)
				if !last.points[index].keep && !p.bounded(vals, true) {
					continue
				}
				feasible, worst := true, math.Inf(-1)
				for w, ws := range p.ws {
					j := capacityJudge(ws, vals[w*width], vals[w*width+1], vals[w*width+2] > 0, r)
					feasible = feasible && j.satisfied
					worst = max(worst, j.worst())
				}
				if feasible && state-states != full {
					continue
				}
				if !feasible && (best != nil || overload != nil && worst > overload.worst) {
					continue
				}
				choice := make([]int, len(p.plans))
				at := index
				for s := len(stages) - 1; s > 0; s-- {
					point := stages[s].points[at]
					choice[nodes[s-1]] = int(point.plan)
					at = point.parent
				}
				res := p.evaluate(choice, r)
				if res.feasible {
					if res.better(best) {
						best = &res
					}
				} else if res.better(overload) {
					overload = &res
				}
			}
		}
	}
	if best != nil {
		return *best, nil
	}
	return *overload, nil
}

// spread adds y = 0, 1, … load cells of Workload w (radix of its state digit)
// to the point's state at the plan's request energy, within the cell count.
func (p *capacityPlanner) spread(target *capacityStage, plan *capacityPlan, w, state int, point capacityPoint, energy float64, vals []float64, radix int) {
	used := state / radix % (p.ws[w].cells + 1)
	for y, joules := range plan.serve[w] {
		if used+y > p.ws[w].cells {
			return
		}
		point.energy = energy + joules
		target.insert(state+y*radix, point, vals)
	}
}

// frontier lists the plans of Node n eligible at route r that no other one
// beats: at most its energy, at least its κ sums and load cells at no higher
// request energy, and a chain ending at r when it has one. Every DP point of a
// beaten plan is beaten by the same point of the other plan; keep stays.
func (p *capacityPlanner) frontier(n int, r float64) []int {
	eligible := []int{}
	for j, plan := range p.plans[n] {
		if plan.record.Eligible && plan.preMax <= r {
			eligible = append(eligible, j)
		}
	}
	hit := func(plan *capacityPlan) bool { return !plan.keep && plan.preMax == r }
	beats := func(a, b *capacityPlan) bool {
		if b.keep || hit(b) && !hit(a) || a.energy(r, p.period) > b.energy(r, p.period) {
			return false
		}
		for v := range a.vals {
			if a.vals[v] < b.vals[v] {
				return false
			}
		}
		for w := range a.serve {
			if len(a.serve[w]) < len(b.serve[w]) {
				return false
			}
			for y, joules := range b.serve[w] {
				if a.serve[w][y] > joules {
					return false
				}
			}
		}
		return true
	}
	kept := []int{}
	for _, j := range eligible {
		if !slices.ContainsFunc(eligible, func(i int) bool {
			// Of two equal plans the earlier one stays.
			return i != j && beats(p.plans[n][i], p.plans[n][j]) && (i < j || !beats(p.plans[n][j], p.plans[n][i]))
		}) {
			kept = append(kept, j)
		}
	}
	return kept
}

// decide turns the chosen plan set into the decision: keep, a route-only
// target, or a target whose actions come from schedulerPlan.
func (p *capacityPlanner) decide(decision SchedulerDecision, best capacityResult) SchedulerDecision {
	keepChoice := make([]int, len(p.plans))
	keepResult := p.evaluate(keepChoice, 0)
	keep := &decision.Keep
	keep.ExpectedJoules, keep.WorstExceedance, keep.Status = &keepResult.joules, capacityWorst(keepResult.worst), schedulerStatusFeasible
	if !keepResult.feasible {
		keep.Status = schedulerStatusOverload
	}
	decision.Search.Examined = p.examined
	for n, compared := range p.compared {
		if compared {
			decision.Search.ComparedNodes = append(decision.Search.ComparedNodes, p.snapshot.Nodes[n].Node)
		}
	}
	record := &SchedulerCapacity{Period: time.Duration(p.period * 1e9), Route: time.Duration(best.route * 1e9), Cells: p.cells}
	for _, plans := range p.plans {
		for _, plan := range plans {
			record.Plans = append(record.Plans, plan.record)
		}
	}
	for w, ws := range p.ws {
		j := best.judgments[w]
		record.Workloads = append(record.Workloads, SchedulerCapacityWorkload{
			UID: ws.d.UID, Rate: ws.rate, Queued: ws.queued, KappaNew: best.sums[w][0], KappaRetained: best.sums[w][1], Backlog: j.backlog,
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
	for n, j := range best.choice {
		plan := p.plans[n][j]
		if !plan.keep {
			target.Nodes[n].PowerMode = plan.mode
		}
		target.Instances = append(target.Instances, plan.instances...)
	}
	if !best.feasible {
		target.Status = schedulerStatusOverload
		decision.Reasons = append(decision.Reasons, schedulerNoPlanMeetsSLO)
		if best.weightsExceeded {
			decision.Reasons = append(decision.Reasons, schedulerRouteWeightExceeds)
		}
	}
	if best.keep && p.sameRouting(target.Routing) {
		chosen := decision.Keep
		chosen.Status = target.Status
		decision.Target, decision.Status = &chosen, schedulerStatusKeep
		if !best.feasible {
			decision.Status = schedulerStatusOverload
		}
		return decision
	}
	sum := sha256.Sum256([]byte(best.key + fmt.Sprint(target.Routing)))
	target.ID = "plan-" + hex.EncodeToString(sum[:6])
	target.Actions = schedulerPlan(p.snapshot, target)
	decision.Target, decision.Status = &target, target.Status
	return decision
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
