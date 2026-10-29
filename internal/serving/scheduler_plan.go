package serving

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// schedulerWorkloadStates classifies every current Workload for this decision.
func schedulerWorkloadStates(settings SchedulerSettings, snapshot SchedulerSnapshot) []SchedulerWorkloadState {
	states := []SchedulerWorkloadState{}
	for _, d := range snapshot.Workloads {
		if d == nil {
			continue
		}
		state := SchedulerWorkloadState{UID: d.UID, State: WorkloadControlled}
		switch {
		case !slices.Contains(settings.AutonomousWorkloads, d.UID):
			state.State, state.Reason = WorkloadDisabled, "AutonomousControlOff"
		case !slices.ContainsFunc(snapshot.Options, func(o SchedulerOption) bool { return schedulerHas(o.CompatibleUIDs, d.UID) }):
			state.State, state.Reason = WorkloadMeasurementEvidenceMissing, "NoCompletedMeasurementMatches"
		}
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].UID < states[j].UID })
	return states
}

// SchedulerLowestMode is the power mode of the Node's lowest unloaded baseline,
// ties broken by name; "" when the Node has no unloaded baseline.
func SchedulerLowestMode(baselines []NodeBaseline, node NodeReference) string {
	latest := map[string]*NodeBaseline{}
	for j := range baselines {
		b := &baselines[j]
		if b.Node != node || len(b.State) != 0 || b.Scope != schedulerNodeWallScope || b.Meter == "" || !schedulerFinite(b.Watts) || b.Watts < 0 || b.ID == "" || b.Reference.Validate() != nil {
			continue
		}
		if previous := latest[b.PowerMode]; previous == nil || b.MeasuredAt.After(previous.MeasuredAt) {
			latest[b.PowerMode] = b
		}
	}
	lowest := ""
	for mode, b := range latest {
		if lowest == "" || b.Watts < latest[lowest].Watts || b.Watts == latest[lowest].Watts && mode < lowest {
			lowest = mode
		}
	}
	return lowest
}

// schedulerAvailable reports a Ready Node outside the measurement POOL and the
// excluded Nodes without a CHILL serving instance whose power mode can change.
func schedulerAvailable(snapshot SchedulerSnapshot, n SchedulerNode) bool {
	return n.Ready && !schedulerReservedNode(snapshot, n.Node) && !n.Measurement && !slices.Contains(n.Reasons, NodeExcluded) && !slices.Contains(n.Reasons, NodePowerModeUnavailable) &&
		!slices.Contains(n.Reasons, NodeInstanceUnavailable) &&
		!slices.ContainsFunc(snapshot.Instances, func(i SchedulerInstance) bool { return i.Node == n.Node })
}

// schedulerCommonNodes preserves already-authorized Fixed power changes. New
// empty-Node savings are comparison candidates, never part of keep.
func schedulerCommonNodes(snapshot SchedulerSnapshot) []SchedulerNode {
	nodes := slices.Clone(snapshot.Nodes)
	for index, n := range nodes {
		for _, a := range snapshot.Actions {
			if a.Fixed && a.Node == n.Node && (a.Status == schedulerActionPlanned || a.Status == phaseRunning) {
				nodes[index].PowerMode = a.To
			}
		}
	}
	return nodes
}

func schedulerInstanceRouting(ctx context.Context, workloads []*Definition, instances []SchedulerInstance) map[string]map[string]int32 {
	routing := make(map[string]map[string]int32, len(workloads))
	for _, d := range workloads {
		if ctx.Err() != nil {
			return nil
		}
		if d == nil {
			continue
		}
		weights := map[string]int32{}
		for _, i := range instances {
			if ctx.Err() != nil {
				return nil
			}
			if i.WorkloadUID == d.UID && i.Weight > 0 {
				weights[i.ID] = i.Weight
			}
		}
		routing[d.UID] = weights
	}
	return routing
}

func schedulerSearchPopulation(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot) SchedulerSearch {
	search := SchedulerSearch{Bounds: settings, WindowStart: snapshot.ObservationEnd.Add(-settings.ObservationWindow), WindowEnd: snapshot.ObservationEnd, Population: "received events in [windowStart,windowEnd); validated compute arrivals (including capacity/evidence rejections) replayed; outstanding retained independently"}
	if snapshot.ObservedSince.After(search.WindowStart) {
		search.WindowStart = snapshot.ObservedSince
	}
	search.RecentWindowStart = maxTime(search.WindowStart, search.WindowEnd.Add(-settings.ControlInterval))
	loads := map[string]*SchedulerLoad{}
	for _, a := range snapshot.Arrivals {
		if ctx.Err() != nil {
			return search
		}
		if a.At.Before(search.WindowStart) || !a.At.Before(search.WindowEnd) {
			continue
		}
		uid := a.WorkloadUID
		if a.Definition != nil {
			uid = a.Definition.UID
		}
		if loads[uid] == nil {
			loads[uid] = &SchedulerLoad{UID: uid}
		}
		load := loads[uid]
		search.Received++
		load.Received++
		if a.Accepted {
			search.Accepted++
			load.Accepted++
		}
		if (a.Compute || a.Accepted) && a.Definition != nil {
			search.ComputeInputs++
			load.ComputeInputs++
			if !a.At.Before(search.RecentWindowStart) {
				load.RecentComputeInputs++
			}
		}
	}
	keys := []string{}
	for key := range loads {
		if ctx.Err() != nil {
			return search
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if ctx.Err() != nil {
			return search
		}
		load := *loads[key]
		if covered := search.WindowEnd.Sub(search.WindowStart); covered > 0 {
			load.InputsPerSecond = float64(load.ComputeInputs) / covered.Seconds()
		}
		if covered := search.WindowEnd.Sub(search.RecentWindowStart); covered > 0 {
			load.RecentInputsPerSecond = float64(load.RecentComputeInputs) / covered.Seconds()
		}
		search.Loads = append(search.Loads, load)
	}
	return search
}

func schedulerMeasuredOptions(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot) []SchedulerOption {
	options := []SchedulerOption{}
	for _, o := range snapshot.Options {
		if ctx.Err() != nil {
			return nil
		}
		for _, variant := range CapacityVariants(o) {
			if ctx.Err() != nil {
				return nil
			}
			options = append(options, variant)
		}
	}
	sort.SliceStable(options, func(i, j int) bool { return schedulerOptionLess(options[i], options[j]) })
	// Latest successful evidence wins for otherwise identical execution conditions.
	latest := map[string]SchedulerOption{}
	for _, o := range options {
		if ctx.Err() != nil {
			return nil
		}
		if o.Instance.Profile == nil {
			continue
		}
		key := schedulerOptionCondition(o)
		prev, ok := latest[key]
		compatible := appendUnique(append([]string(nil), prev.CompatibleUIDs...), o.CompatibleUIDs...)
		sort.Strings(compatible)
		if !ok || o.Instance.Profile.MeasuredAt.After(prev.Instance.Profile.MeasuredAt) {
			o.CompatibleUIDs = compatible
			latest[key] = o
		} else {
			prev.CompatibleUIDs = compatible
			latest[key] = prev
		}
	}
	options = nil
	for _, o := range latest {
		if ctx.Err() != nil {
			return nil
		}
		options = append(options, o)
	}
	sort.Slice(options, func(i, j int) bool { return schedulerOptionLess(options[i], options[j]) })
	return options
}

func schedulerOptionKey(o SchedulerOption) string {
	return o.Instance.Node.UID + "/" + schedulerState(o.Instance) + fmt.Sprint(o.Instance.MaxBatch, o.Instance.MaxWait, o.CompatibleUIDs)
}

// schedulerOptionLess is a total order on distinct execution conditions, so the
// node plans do not depend on map iteration or input order.
func schedulerOptionLess(a, b SchedulerOption) bool {
	ka, kb := schedulerOptionKey(a), schedulerOptionKey(b)
	return ka < kb || ka == kb && schedulerOptionCondition(a) < schedulerOptionCondition(b)
}
func schedulerOptionCondition(o SchedulerOption) string {
	i := o.Instance
	if i.Profile == nil {
		return ""
	}
	p := i.Profile
	// A top-level *CurveDatasetContract prints its value (&{name uid}), not its address.
	return fmt.Sprint(i.Node, p.Condition.Artifact, p.Condition.RuntimeFamily, p.Condition.Backend, p.Condition.RuntimeImage, p.Condition.Preprocessor, p.Condition.ActiveProtocol.Dataset, p.Condition.PowerMode, p.Condition.CPU, p.Condition.Firstfloor, p.Condition.RuntimeAccess, p.Condition.PlannedBatchSizes, i.Allocation, i.MaxBatch, i.MaxWait)
}

func schedulerConfigKey(c SchedulerCandidate) string {
	parts := []string{}
	for _, i := range c.Instances {
		uids := []string{}
		if i.WorkloadUID != "" {
			uids = append(uids, i.WorkloadUID)
		}
		weights := []int32{}
		for _, uid := range uids {
			weight := i.Weight
			if route, ok := c.Routing[uid]; ok {
				weight = route[i.ID]
			}
			weights = append(weights, weight)
		}
		deadlinePolicy := ""
		if i.Profile != nil && i.Profile.Condition.Firstfloor != nil {
			deadlinePolicy = i.Profile.Condition.Firstfloor.DeadlinePolicy
		}
		parts = append(parts, fmt.Sprint(i.ID, i.Node.UID, schedulerState(i), uids, weights, i.Capacity, i.MaxBatch, i.MaxWait, deadlinePolicy))
	}
	sort.Strings(parts)
	for _, n := range c.Nodes {
		parts = append(parts, n.Node.UID+"/"+n.PowerMode)
	}
	return fmt.Sprint(parts)
}
func schedulerNodeMode(nodes []SchedulerNode, node NodeReference) string {
	for _, n := range nodes {
		if n.Node == node {
			return n.PowerMode
		}
	}
	return ""
}

func schedulerSameResourceChoice(old, target SchedulerInstance) bool {
	a, b := old.Allocation, target.Allocation
	// Memory is a measured placement constraint, not a resource control knob.
	a.MemoryBytes, b.MemoryBytes = 0, 0
	if old.Node != target.Node {
		// Physical CPU IDs and GPU UUIDs are scoped to the selected Node.
		if len(a.CPUs) != len(b.CPUs) || (a.GPUUUID == "") != (b.GPUUUID == "") {
			return false
		}
		b.CPUs, b.GPUUUID = a.CPUs, a.GPUUUID
	}
	return reflect.DeepEqual(a, b)
}
func schedulerProfileMatches(d *Definition, p *Profile) bool {
	return d != nil && profilePath(d, p) != nil
}

func schedulerPlan(snap SchedulerSnapshot, c SchedulerCandidate) []Action {
	p := schedulerPlanState{snap: snap, target: &c, actions: []Action{}, nodeTail: map[string]string{}, prepares: []string{}, stops: map[string]string{}}
	p.changeNodes(c)
	p.prepareTargets(c)
	// Candidate ordinals and rolling witness text are not operation identity.
	route := p.add(schedulerActionRoute, NodeReference{}, "", "", schedulerConfigKey(c), p.prepares)
	if action := &p.actions[len(p.actions)-1]; action.Status == schedulerActionPlanned &&
		(len(p.reasons) != 0 || strings.HasPrefix(action.Reason, NodeInstanceUnavailable+":")) {
		action.Reason = strings.Join(p.reasons, ";")
	}
	if len(p.reasons) == 0 {
		p.retireOld(c, route)
	}
	p.selectCosts()
	return p.actions
}

// selectCosts picks each operation cost by the key replay checks: the Node
// power mode for prepare and remove, the loaded state for power.
func (p *schedulerPlanState) selectCosts() {
	modes := map[NodeReference]string{}
	loaded := map[NodeReference][]string{}
	for _, n := range p.snap.Nodes {
		modes[n.Node] = n.PowerMode
	}
	for _, i := range p.snap.Instances {
		loaded[i.Node] = append(loaded[i.Node], schedulerState(i))
	}
	for index := range p.actions {
		a := &p.actions[index]
		if a.Status == schedulerActionPlanned || a.Status == "" {
			a.CostID = ""
			a.ExpectedSeconds, a.ExpectedSource = nil, nil
		}
		mode := modes[a.Node]
		matches := func(cost OperationCost) bool { return cost.PowerMode == mode }
		switch a.Kind {
		case schedulerActionPrepare:
			loaded[a.Node] = append(loaded[a.Node], a.To)
		case schedulerActionRemove:
			if i := slices.Index(loaded[a.Node], a.From); i >= 0 {
				loaded[a.Node] = slices.Delete(loaded[a.Node], i, i+1)
			}
		case schedulerActionPower:
			state := slices.Clone(loaded[a.Node])
			matches = func(cost OperationCost) bool { return schedulerEqualState(state, cost.BeforeState) }
			modes[a.Node] = a.To
		default:
			continue
		}
		for _, cost := range p.snap.Costs {
			if a.CostID == "" && cost.Node == a.Node && cost.Kind == a.Kind && schedulerCostState(cost.From) == schedulerCostState(a.From) &&
				schedulerCostState(cost.To) == schedulerCostState(a.To) && matches(cost) {
				a.CostID = cost.ID
			}
		}
		if a.Status == schedulerActionPlanned || a.Status == "" {
			cost := schedulerCost(p.snap.Costs, a.CostID)
			if mean, _, _, ok := capacityOperationCost(cost); ok {
				a.ExpectedSeconds = &mean
				a.ExpectedSource = &ActionExpectedSource{CostID: cost.ID, Reference: cost.Reference, Statistic: "mean", TrialCount: len(cost.Trials)}
			}
		}
	}
}

// EstimateAction selects a duration using the same measured conditions as planning.
func EstimateAction(snapshot SchedulerSnapshot, action Action) Action {
	p := schedulerPlanState{snap: snapshot, actions: []Action{action}}
	p.selectCosts()
	return p.actions[0]
}

type schedulerPlanState struct {
	snap     SchedulerSnapshot
	target   *SchedulerCandidate
	actions  []Action
	nodeTail map[string]string
	prepares []string
	stops    map[string]string
	reasons  []string
}

func (p *schedulerPlanState) add(kind string, node NodeReference, id, from, to string, deps []string) string {
	a := Action{ID: fmt.Sprintf("%s-%s-%s-%d-%d", kind, node.UID, id, p.snap.At.UnixNano(), len(p.actions)), Kind: kind, Node: node, Instance: id, From: from, To: to, DependsOn: append([]string(nil), deps...), Status: schedulerActionPlanned}
	if kind == schedulerActionPrepare || kind == schedulerActionRemove || kind == schedulerActionPower {
		if tail := p.nodeTail[node.UID]; tail != "" {
			a.DependsOn = append(a.DependsOn, tail)
		}
	}
	p.addRunningPrerequisites(&a)
	for _, old := range p.snap.Actions {
		if old.Kind == kind && old.Node == node && old.Instance == id && old.From == from && old.To == to && (old.Status == schedulerActionPlanned || old.Status == phaseRunning || old.Status == schedulerActionComplete) {
			if old.Status == schedulerActionComplete && !p.completedEffectPresent(old) {
				continue
			}
			a.ID = old.ID
			a.Status = old.Status
			a.Elapsed = old.Elapsed
			a.Cancelable = old.Cancelable
			a.Reason = old.Reason
			a.ExpectedSeconds, a.ExpectedSource = old.ExpectedSeconds, old.ExpectedSource
			if old.CostID != "" {
				a.CostID = old.CostID
			}
			break
		}
	}
	if kind == schedulerActionPrepare || kind == schedulerActionRemove || kind == schedulerActionPower {
		p.nodeTail[node.UID] = a.ID
	}
	p.actions = append(p.actions, a)
	return a.ID
}

func (p *schedulerPlanState) addRunningPrerequisites(action *Action) {
	for _, old := range p.snap.Actions {
		if old.Status != phaseRunning || (old.Kind == action.Kind && old.Node == action.Node && old.Instance == action.Instance && old.From == action.From && old.To == action.To) {
			continue
		}
		sameNode := action.Node.UID != "" && old.Node == action.Node
		if sameNode && !schedulerHas(action.DependsOn, old.ID) {
			action.DependsOn = append(action.DependsOn, old.ID)
		}
	}
}

func (p *schedulerPlanState) completedEffectPresent(action Action) bool {
	switch action.Kind {
	case schedulerActionPrepare:
		return slices.ContainsFunc(p.snap.Instances, func(instance SchedulerInstance) bool {
			return instance.ID == action.Instance && instance.Node == action.Node &&
				!instance.Stopped && schedulerState(instance) == action.To
		})
	case schedulerActionRoute:
		return p.routeEffectPresent()
	case schedulerActionStop, schedulerActionDrain:
		if schedulerInstanceRouted(p.snap.Routing, action.Instance) {
			return false
		}
		for _, instance := range p.snap.Instances {
			if instance.ID == action.Instance && (instance.Node != action.Node || !instance.Stopped) {
				return false
			}
		}
		return action.Kind == schedulerActionStop ||
			!slices.ContainsFunc(p.snap.Outstanding, func(request SchedulerRequest) bool { return request.Instance == action.Instance })
	case schedulerActionRemove:
		return action.Status == schedulerActionComplete && !slices.ContainsFunc(p.snap.Instances, func(instance SchedulerInstance) bool { return instance.ID == action.Instance }) &&
			!slices.ContainsFunc(p.snap.Outstanding, func(request SchedulerRequest) bool { return request.Instance == action.Instance }) &&
			!schedulerInstanceRouted(p.snap.Routing, action.Instance)
	case schedulerActionPower:
		return schedulerNodeMode(p.snap.Nodes, action.Node) == action.To
	}
	return false
}

func (p *schedulerPlanState) routeEffectPresent() bool {
	if p.target == nil || !maps.EqualFunc(p.snap.Routing, p.target.Routing, func(actual, wanted map[string]int32) bool {
		return maps.Equal(actual, wanted)
	}) {
		return false
	}
	for _, wanted := range p.target.Instances {
		if !slices.ContainsFunc(p.snap.Instances, func(actual SchedulerInstance) bool {
			return schedulerSnapshotTargetMatches(actual, wanted) ||
				!actual.Ready && !actual.Stopped && schedulerSamePhysicalTarget(actual, wanted)
		}) {
			return false
		}
	}
	return true
}

func schedulerSnapshotTargetMatches(actual, wanted SchedulerInstance) bool {
	if actual.Profile == nil || wanted.Profile == nil || !actual.Ready || actual.Stopped ||
		actual.ID != wanted.ID || actual.Node != wanted.Node ||
		schedulerState(actual) != schedulerState(wanted) {
		return false
	}
	if !schedulerOwnerAllows(actual.OwnerWorkloadUID, wanted.WorkloadUID) {
		return false
	}
	return actual.Capacity == wanted.Capacity && actual.MaxBatch == wanted.MaxBatch && actual.MaxWait == wanted.MaxWait &&
		reflect.DeepEqual(actual.Profile.Condition.Firstfloor, wanted.Profile.Condition.Firstfloor)
}

func schedulerInstanceRouted(routing map[string]map[string]int32, id string) bool {
	for _, weights := range routing {
		if weights[id] > 0 {
			return true
		}
	}
	return false
}

func (p *schedulerPlanState) changeNodes(c SchedulerCandidate) {
	// Power affects every runner on the node, never only one UID.
	for _, n := range c.Nodes {
		if schedulerReservedNode(p.snap, n.Node) {
			continue
		}
		for _, oldNode := range p.snap.Nodes {
			if n.Node != oldNode.Node || n.PowerMode == oldNode.PowerMode {
				continue
			}
			if slices.ContainsFunc(p.snap.Instances, func(i SchedulerInstance) bool {
				return i.Node == n.Node && !i.Ready && !i.Stopped
			}) {
				p.reasons = appendUnique(p.reasons, fmt.Sprintf("%s:%s:%s", NodeInstanceUnavailable, n.Node.Name, n.Node.UID))
				continue
			}
			from := oldNode.PowerMode
			// Preserve an already-authorized lowering before any new change.
			if i := slices.IndexFunc(p.snap.Actions, func(a Action) bool {
				return a.Fixed && a.Node == n.Node && (a.Status == schedulerActionPlanned || a.Status == phaseRunning)
			}); i >= 0 {
				old := p.snap.Actions[i]
				p.add(schedulerActionPower, n.Node, "", old.From, old.To, nil)
				p.actions[len(p.actions)-1].Fixed = true
				from = old.To
			}
			emptySaving := schedulerAvailable(p.snap, oldNode) &&
				!slices.ContainsFunc(c.Instances, func(i SchedulerInstance) bool { return i.Node == n.Node }) &&
				n.PowerMode == SchedulerLowestMode(p.snap.Baselines, n.Node)
			if emptySaving && from != n.PowerMode && p.snap.ScaleDown != nil && !p.snap.EmptyNodeScaleDownAllowed {
				p.reasons = appendUnique(p.reasons, schedulerScaleDownStabilizing)
				continue
			}
			deps := []string{}
			drains := map[string]string{}
			for _, i := range p.snap.Instances {
				if i.Node == n.Node {
					st := p.add(schedulerActionStop, n.Node, i.ID, "", "", nil)
					dr := p.add(schedulerActionDrain, n.Node, i.ID, "", "", []string{st})
					p.stops[i.ID] = st
					drains[i.ID] = dr
					deps = append(deps, dr)
				}
			}
			// Directed power trials retain their loaded process witness. Immutable native
			// bundles are removed only after actual mode/readiness proof, then rebuilt.
			if n.PowerMode != from {
				dep := p.add(schedulerActionPower, n.Node, "", from, n.PowerMode, deps)
				p.actions[len(p.actions)-1].Fixed = emptySaving
				deps = []string{dep}
			}
			for _, i := range p.snap.Instances {
				if i.Node == n.Node {
					p.add(schedulerActionRemove, n.Node, i.ID, schedulerState(i), "unloaded", deps)
				}
			}
			// Authorized empty lowering runs independently of the target's route.
			needed := slices.ContainsFunc(c.Instances, func(i SchedulerInstance) bool { return i.Node == n.Node })
			if tail := p.nodeTail[n.Node.UID]; tail != "" && (needed || len(drains) != 0 || !p.actions[len(p.actions)-1].Fixed) {
				p.prepares = append(p.prepares, tail)
			}
		}
	}
}

func (p *schedulerPlanState) prepareTargets(c SchedulerCandidate) {
	for _, wanted := range c.Instances {
		if schedulerReservedNode(p.snap, wanted.Node) {
			continue
		}
		if slices.ContainsFunc(p.snap.Instances, func(actual SchedulerInstance) bool {
			return actual.Node == wanted.Node && !actual.Ready && !actual.Stopped
		}) {
			p.reasons = appendUnique(p.reasons, fmt.Sprintf("%s:%s:%s", NodeInstanceUnavailable, wanted.Node.Name, wanted.Node.UID))
			continue
		}
		if running := p.runningPreparation(wanted); running != "" {
			p.prepares = append(p.prepares, running)
			continue
		}
		exists := false
		for _, actual := range p.snap.Instances {
			if schedulerSamePhysicalTarget(actual, wanted) {
				exists = true
			}
		}
		if exists {
			continue
		}
		p.freePreparationResources(wanted)
		id := p.add(schedulerActionPrepare, wanted.Node, wanted.ID, "unloaded", schedulerState(wanted), nil)
		p.prepares = append(p.prepares, id)
	}
}

func (p *schedulerPlanState) runningPreparation(wanted SchedulerInstance) string {
	for _, action := range p.snap.Actions {
		if action.Kind != schedulerActionPrepare || action.Status != phaseRunning || action.Node != wanted.Node || action.To != schedulerState(wanted) {
			continue
		}
		if action.Instance == wanted.ID || slices.ContainsFunc(p.snap.Instances, func(actual SchedulerInstance) bool {
			return actual.ID == wanted.ID && actual.TargetID == action.Instance
		}) {
			return action.ID
		}
	}
	return ""
}

func schedulerSamePhysicalTarget(actual, wanted SchedulerInstance) bool {
	return (actual.ID == wanted.ID || (actual.TargetID != "" && actual.TargetID == wanted.ID)) &&
		actual.Node == wanted.Node && schedulerState(actual) == schedulerState(wanted)
}

// schedulerOwnerAllows reports whether an owned instance serves only its owner.
func schedulerOwnerAllows(owner, uid string) bool {
	return owner == "" || uid == "" || uid == owner
}

// Keep make-before-break whenever it fits. Otherwise release only obsolete
// allocations on this Node through the existing stop/drain/remove primitives.
func (p *schedulerPlanState) freePreparationResources(wanted SchedulerInstance) {
	if p.preparationFits(wanted) {
		return
	}
	for phase := range 2 {
		for _, actual := range p.snap.Instances {
			if actual.Node != wanted.Node || p.stops[actual.ID] != "" || slices.ContainsFunc(p.target.Instances, func(target SchedulerInstance) bool { return schedulerSamePhysicalTarget(actual, target) }) {
				continue
			}
			overlap := (actual.Allocation.GPUUUID != "" && actual.Allocation.GPUUUID == wanted.Allocation.GPUUUID) ||
				slices.ContainsFunc(actual.Allocation.CPUs, func(cpu int) bool { return slices.Contains(wanted.Allocation.CPUs, cpu) })
			if phase == 0 && !overlap {
				continue
			}
			p.retirePreparationBlocker(actual)
			if p.preparationFits(wanted) {
				return
			}
		}
	}
}

func (p *schedulerPlanState) preparationFits(wanted SchedulerInstance) bool {
	for _, node := range p.snap.Nodes {
		if node.Node != wanted.Node {
			continue
		}
		allocations := append([]SchedulerAllocation{node.Reserved}, node.NonServing...)
		for _, actual := range p.snap.Instances {
			removedBeforePrepare := slices.ContainsFunc(p.actions, func(action Action) bool {
				return action.Kind == schedulerActionRemove && action.Instance == actual.ID && action.Node == actual.Node
			})
			if actual.Node == wanted.Node && !removedBeforePrepare {
				allocations = append(allocations, actual.Allocation)
			}
		}
		for _, action := range p.actions {
			if action.Kind != schedulerActionPrepare || action.Node != wanted.Node {
				continue
			}
			for _, prepared := range p.target.Instances {
				if prepared.ID == action.Instance {
					allocations = append(allocations, prepared.Allocation)
				}
			}
		}
		allocations = append(allocations, wanted.Allocation)
		mode := node.PowerMode
		if wanted.Profile != nil {
			mode = wanted.Profile.Condition.PowerMode
		}
		modeNode, known := node.AtMode(mode)
		return known && len(ValidateNodeBudget(modeNode, allocations, nil)) == 0
	}
	return false
}

func (p *schedulerPlanState) retirePreparationBlocker(actual SchedulerInstance) {
	for _, old := range p.snap.Actions {
		if old.Kind == schedulerActionRemove && old.Instance == actual.ID && old.Node == actual.Node && old.Status == phaseRunning {
			p.stops[actual.ID] = old.ID
			p.add(old.Kind, old.Node, old.Instance, old.From, old.To, old.DependsOn)
			return
		}
	}
	stop := p.add(schedulerActionStop, actual.Node, actual.ID, "", "", nil)
	drain := p.add(schedulerActionDrain, actual.Node, actual.ID, "", "", []string{stop})
	p.add(schedulerActionRemove, actual.Node, actual.ID, schedulerState(actual), "unloaded", []string{drain})
	p.stops[actual.ID] = stop
}

func (p *schedulerPlanState) retireOld(c SchedulerCandidate, route string) {
	for _, i := range p.snap.Instances {
		if schedulerReservedNode(p.snap, i.Node) {
			continue
		}
		exists := false
		for _, target := range c.Instances {
			if schedulerSamePhysicalTarget(i, target) {
				exists = true
			}
		}
		if !exists && p.stops[i.ID] == "" {
			st := p.add(schedulerActionStop, i.Node, i.ID, "", "", []string{route})
			dr := p.add(schedulerActionDrain, i.Node, i.ID, "", "", []string{st})
			p.add(schedulerActionRemove, i.Node, i.ID, schedulerState(i), "unloaded", []string{dr})
		}
	}
}

// Costs for route/stop/drain are represented by the native queue and observed
// completion timeline. They are not assigned fictitious wall-energy zero trials.

func schedulerReservedNode(snapshot SchedulerSnapshot, node NodeReference) bool {
	return slices.ContainsFunc(snapshot.Reservations, func(reservation SchedulerReservation) bool {
		return slices.Contains(reservation.Nodes, node)
	})
}

func schedulerFixedReason(snapshot SchedulerSnapshot, uid string) string {
	for _, reservation := range snapshot.Reservations {
		if slices.Contains(reservation.WorkloadUIDs, uid) && len(reservation.RouteOnly[uid]) == 0 {
			return "ReservedScope:" + reservation.Owner
		}
	}
	for _, instance := range snapshot.Instances {
		if (instance.WorkloadUID == uid || instance.OwnerWorkloadUID == uid) && !instance.Ready && !instance.Stopped {
			if slices.ContainsFunc(snapshot.Reservations, func(r SchedulerReservation) bool {
				return len(r.RouteOnly[uid]) > 0 && r.Target != nil &&
					slices.ContainsFunc(r.Target.Actions, func(a Action) bool {
						return a.Kind == schedulerActionStop && a.Instance == instance.ID && a.Node == instance.Node
					})
			}) {
				continue // An unconfirmed retiree remains owned, never route capacity.
			}
			return "UnconfirmedInstance:" + instance.ID
		}
	}
	return ""
}

func fixSchedulerWorkloads(decision *SchedulerDecision, snapshot SchedulerSnapshot, rows []CapacityRow) {
	for index, state := range decision.Workloads {
		if state.State != WorkloadControlled {
			continue
		}
		reason := schedulerFixedReason(snapshot, state.UID)
		if reason == "" && !slices.ContainsFunc(rows, func(row CapacityRow) bool { return row.Input.WorkloadUID == state.UID && row.Kappa != nil }) {
			reason = CapacityPending + ":" + state.UID
		}
		if reason != "" {
			decision.Workloads[index].State, decision.Workloads[index].Reason = schedulerWorkloadFixed, reason
			decision.Reasons = appendUnique(decision.Reasons, reason)
		}
	}
}

const schedulerWorkloadFixed = "Fixed"

func markFixedEstimates(decision *SchedulerDecision) {
	if !slices.ContainsFunc(decision.Workloads, func(state SchedulerWorkloadState) bool { return state.State == schedulerWorkloadFixed }) {
		return
	}
	decision.Keep.ExpectedJoules = nil
	decision.Keep.Assumptions = appendUnique(decision.Keep.Assumptions, "FixedScopeUnverified: overall SLO and energy are unknown; only independent changes are compared")
	if decision.Target != nil {
		decision.Target.ExpectedJoules = nil
		decision.Target.Assumptions = appendUnique(decision.Target.Assumptions, "FixedScopeUnverified: overall SLO and energy are unknown; only independent changes are compared")
	}
}
