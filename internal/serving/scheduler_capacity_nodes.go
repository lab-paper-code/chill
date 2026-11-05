package serving

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"strings"
)

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
	pendingReplicas                    []int               // per-workload occupancy excluded from capacity items
	preMean, preMax, postMean, postMax float64             // seconds
	preJ, postJ                        float64
	before, mid, after                 float64 // watts
	operationBounds                    schedulerEnergyBounds
	beforeBound, midBound, afterBound  schedulerBaselineBound
	// replaces marks a plan that changes the batch policy of a current instance,
	// or removes one while adding another.
	replaces bool
	// retires removes occupancy without preparing replacement capacity.
	retires bool
}

// held reports an instance prepared under Overload still inside its hold.
func (p *capacityPlanner) held(i SchedulerInstance) bool {
	return !i.Terminated && i.HeldUntil != nil && i.HeldUntil.After(p.snapshot.At)
}

// resolveCapacity freezes all option/workload and current-instance results for
// this planner, including missing and rejected rows, before any sweep workers.
func (p *capacityPlanner) resolveCapacity() {
	p.optionCapacity = make(map[int][]capacityChoice, len(p.options))
	for option, o := range p.options {
		choices := make([]capacityChoice, len(p.ws))
		for w := range p.ws {
			c := capacityChoice{option: o, w: w}
			c.kappa, c.row, c.reason = p.kappa(o, w)
			choices[w] = c
		}
		p.optionCapacity[option] = choices
	}
	p.currentCapacity = make(map[capacityInstanceKey]capacityItem)
	resolve := func(instance SchedulerInstance) {
		key := currentCapacityKey(instance)
		if _, ok := p.currentCapacity[key]; !ok {
			k, row := p.resolveCurrentKappa(instance)
			p.currentCapacity[key] = capacityItem{kappa: k, row: row}
		}
	}
	for _, o := range p.options {
		for _, w := range p.ws {
			instance := o.Instance
			instance.WorkloadUID = w.d.UID
			resolve(instance)
		}
	}
	for _, instance := range p.snapshot.Instances {
		resolve(instance)
	}
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
				if plan.preMax+plan.postMax > p.settings.ComparisonPeriod.Seconds() {
					plan.record.Reason = schedulerTransitionHorizon
				}
				p.compared[n] = true
			}
		}
		p.examined += len(p.plans[n])
	}
	keep := p.evaluateAtWindow(make([]int, len(p.plans)), 0, nil, 1)
	p.keepWatts, p.keepFeasible = keep.joules, keep.feasible
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
func schedulerUncreatedReservations(snapshot SchedulerSnapshot, node NodeReference) []SchedulerInstance {
	seen := map[string]bool{}
	var pending []SchedulerInstance
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
			pending = append(pending, wanted)
		}
	}
	return pending
}

//nolint:gocyclo // One ordered pass applies the node rules, the control gates and the plan walk.
func (p *capacityPlanner) nodePlans(n int, common []SchedulerNode) []*capacityPlan {
	node := p.snapshot.Nodes[n]
	m0 := common[n].PowerMode
	current := []SchedulerInstance{}
	for _, i := range p.snapshot.Instances {
		if i.Node == node.Node {
			current = append(current, i)
		}
	}
	keep := &capacityPlan{mode: m0, keep: true, instances: current, record: SchedulerNodePlan{Node: node.Node, Key: "keep", Eligible: true}}
	p.annotate(keep, nil)
	p.sum(keep)
	before, _, beforeSources, _ := schedulerBaseline(p.settings, p.snapshot.Baselines, common[n], current, p.snapshot.Options)
	keep.before = before
	keep.beforeBound = schedulerBaselineUncertainty(p.snapshot.Baselines, beforeSources, len(current))
	keep.record.IdleCosts = map[string]IdlePowerProvenance{"before": beforeSources}
	for _, instance := range p.pending {
		if instance.Node != node.Node {
			continue
		}
		keep.others++
		if w, ok := p.index[instance.WorkloadUID]; ok {
			keep.pendingReplicas[w]++
		}
	}
	plans := []*capacityPlan{keep}
	reason := ""
	physicalReason := schedulerPhysicalChangeReason(p.snapshot, node.Node)
	switch {
	case slices.Contains(common[n].Reasons, NodePlacementInventoryMissing):
		reason = NodePlacementInventoryMissing
	case schedulerReservedNode(p.snapshot, node.Node):
		reason = "ReservedNode"
	case physicalReason == schedulerNodeUnavailable:
		reason = schedulerNodeUnavailable
	case node.Measurement:
		reason = "MeasurementNode"
	case slices.Contains(node.Reasons, NodeExcluded):
		reason = NodeExcluded
	case physicalReason != "":
		reason = physicalReason
	case slices.Contains(node.Reasons, NodePlacementBudgetUnavailable):
		reason = NodePlacementBudgetUnavailable
	case slices.ContainsFunc(current, p.fixed):
		// Disabled Workloads keep their SLO: their Node takes no plan action.
		reason = schedulerPlanFixedWorkload
	}
	groups, names := map[string][]capacityChoice{}, []string{}
	for option, o := range p.options {
		i := o.Instance
		if i.Node != node.Node || i.Profile == nil || i.Profile.Condition.NodeUID != node.Node.UID {
			continue
		}
		if node.PowerMode != i.Profile.Condition.PowerMode && (!p.settings.Controls.Power || slices.Contains(node.Reasons, NodePowerModeUnavailable)) {
			continue
		}
		for w := range p.ws {
			if !slices.Contains(o.CompatibleUIDs, p.ws[w].d.UID) || !schedulerProfileMatches(p.ws[w].d, i.Profile) || !p.controlAllows(w, i, current) {
				continue
			}
			c := p.optionCapacity[option][w]
			name := fmt.Sprint(i.Profile.ID, i.Allocation, i.MaxWait)
			if groups[name] == nil {
				names = append(names, name)
			}
			groups[name] = append(groups[name], c)
		}
	}
	if len(names) == 0 && len(current) > 0 && reason != NodeExcluded {
		return plans
	}
	if reason != "" {
		plans = append(plans, &capacityPlan{record: SchedulerNodePlan{Node: node.Node, Key: "*", Reason: reason}})
		return plans
	}
	currentKey := planKey(current)
	hasTerminal := slices.ContainsFunc(current, func(i SchedulerInstance) bool { return i.Terminated })
	allocations := append([]SchedulerAllocation{node.Reserved}, node.NonServing...)
	var walk func(j int, chosen []capacityChoice)
	walk = func(j int, chosen []capacityChoice) {
		if j == len(names) {
			if plan := p.plan(n, m0, current, chosen); plan != nil && (plan.record.Key != currentKey || plan.mode != m0 || hasTerminal) {
				plan.before, plan.beforeBound = keep.before, keep.beforeBound
				if plan.record.IdleCosts == nil {
					plan.record.IdleCosts = map[string]IdlePowerProvenance{}
				}
				plan.record.IdleCosts["before"] = keep.record.IdleCosts["before"]
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
				reason := schedulerPlanInfeasibleResources
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
	} else if len(current) == 0 {
		mode = SchedulerLowestMode(p.snapshot.Baselines, node.Node)
		if !p.settings.Controls.Power || !schedulerAvailable(p.snapshot, node) || mode == "" || mode == m0 {
			return nil
		}
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
			if !used[old.ID] && !old.Terminated &&
				mode == node.PowerMode && old.FixedBatchSize == 0 && schedulerState(old) == schedulerState(i) &&
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
	plan.retires = !added && slices.ContainsFunc(current, func(old SchedulerInstance) bool {
		return !old.Terminated && !used[old.ID]
	})
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
		if !slices.ContainsFunc(current, func(old SchedulerInstance) bool { return old.ID == i.ID }) {
			for _, action := range p.snapshot.Actions {
				if action.Kind != schedulerActionPrepare || action.Node != node.Node {
					continue
				}
				failure := action.PreparationFailure
				if failure != nil && failure.Matches(node, i) {
					if plan.record.PreparationFailure == nil || failure.RepeatCount > plan.record.PreparationFailure.RepeatCount ||
						failure.Classification == PreparationDeterministic || failure.Classification == PreparationPromoted {
						plan.record.PreparationFailure = failure
					}
					if failure.Classification == PreparationDeterministic || failure.Classification == PreparationPromoted {
						plan.record.Reason = schedulerPlanPreparationFailed
						return plan
					}
				}
			}
		}
	}
	stopped, removedPre := p.chain(n, plan)
	// A same-mode routed blocker remains a veto unless a globally compiled
	// Ready/ACK handoff can precede its stop. Power outages keep their existing
	// κ_ret accounting and are never treated as bridge supply.
	if mode == node.PowerMode {
		for _, i := range current {
			if stopped[i.ID] && !i.Stopped && schedulerInstanceRouted(p.keep.Routing, i.ID) {
				plan.record.RequiresBridge = append(plan.record.RequiresBridge, i.ID)
			}
		}
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
	plan.record.Tempo = schedulerTempoFast
	if len(current) == 0 && len(chosen) == 0 || slices.ContainsFunc(current, func(old SchedulerInstance) bool {
		return old.WorkloadUID == "" && old.OwnerWorkloadUID == "" && (!used[old.ID] || mode != node.PowerMode)
	}) {
		plan.record.Tempo = schedulerTempoSlow
		if !p.snapshot.EmptyNodeScaleDownAllowed {
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
			return !old.Terminated && (old.WorkloadUID == ws.d.UID || old.OwnerWorkloadUID == ws.d.UID)
		})
		slow := hasCurrent && (affected || policy && plan.record.Kappa[ws.d.UID] <= oldKappa)
		if slow {
			plan.record.Tempo = schedulerTempoSlow
			if p.partial {
				plan.record.Reason = schedulerPartialObservation
				return plan
			}
			if ws.growing || !p.snapshot.ScaleDown[ws.d.UID].Allowed {
				plan.record.RequiresRecovery = append(plan.record.RequiresRecovery, ws.d.UID)
			}
		}
	}
	mid := slices.DeleteFunc(slices.Clone(current), func(i SchedulerInstance) bool { return removedPre[i.ID] })
	for _, i := range plan.instances {
		if !slices.ContainsFunc(current, func(old SchedulerInstance) bool { return old.ID == i.ID }) {
			mid = append(mid, i)
		}
	}
	var midSources, afterSources IdlePowerProvenance
	plan.mid, _, midSources, _ = schedulerBaseline(p.settings, p.snapshot.Baselines, at, mid, p.snapshot.Options)
	plan.after, _, afterSources, _ = schedulerBaseline(p.settings, p.snapshot.Baselines, at, plan.instances, p.snapshot.Options)
	plan.midBound = schedulerBaselineUncertainty(p.snapshot.Baselines, midSources, len(mid))
	plan.afterBound = schedulerBaselineUncertainty(p.snapshot.Baselines, afterSources, len(plan.instances))
	plan.record.IdleCosts = map[string]IdlePowerProvenance{
		"mid":   midSources,
		"after": afterSources,
	}
	plan.record.Eligible = true
	return plan
}

// Only these instance fields affect current-instance variant resolution.
type capacityInstanceKey struct {
	profile    *Profile
	node       NodeReference
	workload   string
	maxBatch   int
	terminated bool
}

func currentCapacityKey(i SchedulerInstance) capacityInstanceKey {
	return capacityInstanceKey{i.Profile, i.Node, i.WorkloadUID, i.MaxBatch, i.Terminated}
}

// currentKappa reads the tick's resolved capacity, including unresolved instances.
func (p *capacityPlanner) currentKappa(i SchedulerInstance) (float64, string) {
	item := p.currentCapacity[currentCapacityKey(i)]
	return item.kappa, item.row
}

// resolveCurrentKappa reads the row through the current profile and batch
// variant; without one the instance counts no capacity.
func (p *capacityPlanner) resolveCurrentKappa(i SchedulerInstance) (float64, string) {
	w, ok := p.index[i.WorkloadUID]
	if i.Terminated || i.Profile == nil || !ok {
		return 0, ""
	}
	v, ok := p.variants[fmt.Sprint(i.Profile.ID, "/", i.MaxBatch)]
	if !ok {
		cell, mode := MeasurementProfileIdentity(i.Profile), schedulerNodeMode(p.snapshot.Nodes, i.Node)
		for _, option := range p.options {
			j := option.Instance
			if j.Profile != nil && j.Node.UID == i.Node.UID && j.MaxBatch == i.MaxBatch &&
				j.Profile.Condition.PowerMode == mode && MeasurementProfileIdentity(j.Profile) == cell {
				v, ok = option, true
				break
			}
		}
	}
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
	plan.pendingReplicas = make([]int, k)
	for _, i := range plan.instances {
		w, ok := p.index[i.WorkloadUID]
		if owner, controlled := p.index[i.OwnerWorkloadUID]; controlled && (!ok || owner != w) {
			plan.pendingReplicas[owner]++
		}
		if !ok || i.Terminated || plan.keep && (!i.Ready || i.Stopped) {
			plan.others++
			if ok {
				plan.pendingReplicas[w]++
			}
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
