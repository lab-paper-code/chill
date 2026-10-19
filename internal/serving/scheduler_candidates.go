package serving

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
)

func EnumerateScheduler(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot) ([]SchedulerCandidate, SchedulerSearch, error) {
	if err := ctx.Err(); err != nil {
		return nil, SchedulerSearch{}, err
	}
	search := schedulerSearchPopulation(ctx, settings, snapshot)
	keep := SchedulerCandidate{ID: "keep", Keep: true, Nodes: append([]SchedulerNode(nil), snapshot.Nodes...), Instances: append([]SchedulerInstance(nil), snapshot.Instances...), Routing: schedulerInstanceRouting(ctx, snapshot.Workloads, snapshot.Instances)}
	for uid, routing := range snapshot.Routing {
		if err := ctx.Err(); err != nil {
			return nil, SchedulerSearch{}, err
		}
		keep.Routing[uid] = routing
	}
	if err := ctx.Err(); err != nil {
		return nil, SchedulerSearch{}, err
	}
	result := []SchedulerCandidate{keep}
	if settings.Validate() != nil {
		search.Examined = 1
		return result, search, ctx.Err()
	}
	// Lowering available Nodes is a fixed rule every candidate, keep included, shares.
	common := schedulerCommonNodes(settings, snapshot)
	keep.Nodes = common
	if snapshot.InProgressTarget != nil {
		keep.Actions = schedulerPlan(snapshot, *snapshot.InProgressTarget)
	} else {
		// Only the shared lowering of available Nodes.
		p := schedulerPlanState{snap: snapshot, target: &keep, actions: []Action{}, nodeTail: map[string]string{}, prepares: []string{}, stops: map[string]string{}}
		p.changeNodes(keep)
		keep.Actions = p.actions
	}
	result[0] = keep
	workloads := []*Definition{}
	controlled := map[string]bool{}
	for _, d := range snapshot.Workloads {
		if err := ctx.Err(); err != nil {
			return nil, SchedulerSearch{}, err
		}
		if d == nil {
			search.Examined = 1
			return result, search, ctx.Err()
		}
	}
	for _, state := range schedulerWorkloadStates(settings, snapshot) {
		if state.State == WorkloadControlled {
			controlled[state.UID] = true
		}
	}
	for _, d := range snapshot.Workloads {
		if controlled[d.UID] {
			workloads = append(workloads, d)
		}
	}
	if len(workloads) == 0 {
		search.Examined = 1
		return result, search, ctx.Err()
	}
	sort.Slice(workloads, func(i, j int) bool { return workloads[i].UID < workloads[j].UID })
	e := schedulerEnumeration{ctx: ctx, settings: settings, snapshot: snapshot, search: search, workloads: workloads, controlled: controlled, common: common, options: schedulerMeasuredOptions(ctx, settings, snapshot), seen: map[string]bool{schedulerConfigKey(keep): true}, result: result}
	e.controlBase = schedulerUnroutedTransition(ctx, snapshot)
	base := snapshot.Instances
	if e.controlBase != nil {
		base = e.controlBase.Instances
	}
	// Instances of a Workload outside autonomous control, and every instance in
	// the measurement POOL, stay as they are in every candidate.
	for _, i := range base {
		if e.fixedInstance(i) {
			e.fixed = append(e.fixed, i)
		} else {
			e.movable = append(e.movable, i)
		}
	}
	if !settings.Controls.Sharing {
		e.groups = schedulerExistingWorkloadGroups(ctx, workloads, e.controlInstances())
	}
	e.visit(0, slices.Clone(e.fixed))
	e.search.Examined = len(e.result)
	if err := ctx.Err(); err != nil {
		return nil, SchedulerSearch{}, err
	}
	return e.result, e.search, nil
}

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

func (e *schedulerEnumeration) fixedInstance(i SchedulerInstance) bool {
	if i.FixedBatchSize > 0 || slices.ContainsFunc(e.snapshot.Nodes, func(n SchedulerNode) bool { return n.Node == i.Node && n.Measurement }) {
		return true
	}
	// An owner that is no current Workload is ignored, like any unknown UID.
	if !e.controlled[i.OwnerWorkloadUID] && slices.ContainsFunc(e.snapshot.Workloads, func(d *Definition) bool { return d.UID == i.OwnerWorkloadUID }) {
		return true
	}
	return slices.ContainsFunc(i.WorkloadUIDs, func(uid string) bool { return !e.controlled[uid] })
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

// schedulerAvailable reports a Ready Node outside the measurement POOL without
// a CHILL serving instance whose power mode can change.
func schedulerAvailable(snapshot SchedulerSnapshot, n SchedulerNode) bool {
	return n.Ready && !n.Measurement && !slices.Contains(n.Reasons, NodePowerModeUnavailable) &&
		!slices.ContainsFunc(snapshot.Instances, func(i SchedulerInstance) bool { return i.Node == n.Node })
}

// schedulerCommonNodes places every available Node at its lowest power mode
// when power control is on; every candidate starts from this state.
func schedulerCommonNodes(settings SchedulerSettings, snapshot SchedulerSnapshot) []SchedulerNode {
	nodes := append([]SchedulerNode(nil), snapshot.Nodes...)
	if !settings.Controls.Power {
		return nodes
	}
	for index, n := range nodes {
		if lowest := SchedulerLowestMode(snapshot.Baselines, n.Node); lowest != "" && schedulerAvailable(snapshot, n) {
			nodes[index].PowerMode = lowest
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
			if schedulerHas(i.WorkloadUIDs, d.UID) && i.Weight > 0 {
				weights[i.ID] = i.Weight
			}
		}
		routing[d.UID] = weights
	}
	return routing
}

func schedulerSearchPopulation(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot) SchedulerSearch {
	search := SchedulerSearch{Bounds: settings, WindowStart: snapshot.ObservationEnd.Add(-settings.ObservationWindow), WindowEnd: snapshot.ObservationEnd, Population: "received events in [windowStart,windowEnd); validated compute arrivals (including capacity/evidence rejections) replayed; outstanding retained independently"}
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
		if settings.ObservationWindow > 0 {
			load.InputsPerSecond = float64(load.ComputeInputs) / settings.ObservationWindow.Seconds()
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
		if !settings.Controls.Batching {
			options = append(options, o)
			continue
		}
		if o.Instance.Profile == nil {
			continue
		}
		for _, b := range o.Instance.Profile.Batches {
			if ctx.Err() != nil {
				return nil
			}
			if b.Batch <= o.Instance.MaxBatch {
				variant := o
				variant.Instance.MaxBatch = b.Batch
				options = append(options, variant)
			}
		}
	}
	sort.Slice(options, func(i, j int) bool { return schedulerOptionKey(options[i]) < schedulerOptionKey(options[j]) })
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
	sort.Slice(options, func(i, j int) bool { return schedulerOptionKey(options[i]) < schedulerOptionKey(options[j]) })
	return options
}

// Sharing OFF preserves the existing physical UID cohorts, not singleton UIDs.
func schedulerExistingWorkloadGroups(ctx context.Context, workloads []*Definition, instances []SchedulerInstance) [][]*Definition {
	groups := make([][]*Definition, 0, len(instances))
	seen := map[string]bool{}
	assigned := map[string]bool{}
	for _, instance := range instances {
		if ctx.Err() != nil {
			return nil
		}
		group := []*Definition{}
		uids := []string{}
		for _, d := range workloads {
			if ctx.Err() != nil {
				return nil
			}
			if schedulerHas(instance.WorkloadUIDs, d.UID) {
				group = append(group, d)
				uids = append(uids, d.UID)
				assigned[d.UID] = true
			}
		}
		key := fmt.Sprint(uids)
		if len(group) > 0 && !seen[key] {
			groups = append(groups, group)
			seen[key] = true
		}
	}
	for _, d := range workloads {
		if ctx.Err() != nil {
			return nil
		}
		if !assigned[d.UID] {
			groups = append(groups, []*Definition{d})
		}
	}
	return groups
}

type schedulerEnumeration struct {
	ctx         context.Context
	settings    SchedulerSettings
	snapshot    SchedulerSnapshot
	search      SchedulerSearch
	controlBase *SchedulerCandidate
	workloads   []*Definition
	controlled  map[string]bool
	common      []SchedulerNode
	fixed       []SchedulerInstance
	movable     []SchedulerInstance
	groups      [][]*Definition
	sources     map[string]int
	options     []SchedulerOption
	seen        map[string]bool
	result      []SchedulerCandidate
}

// A drained routing gap does not change the selected cohort, replica count or
// per-UID weights. Resource occupancy and physical reuse still use the snapshot.
func schedulerUnroutedTransition(ctx context.Context, snapshot SchedulerSnapshot) *SchedulerCandidate {
	goal := snapshot.InProgressTarget
	if goal == nil || len(goal.Routing) != len(snapshot.Workloads) {
		return nil
	}
	missing := false
	for _, d := range snapshot.Workloads {
		if ctx.Err() != nil {
			return nil
		}
		if d == nil || !schedulerPositiveRouting(goal.Routing[d.UID]) {
			return nil
		}
		missing = missing || !schedulerPositiveRouting(snapshot.Routing[d.UID])
	}
	if missing {
		return goal
	}
	return nil
}

func schedulerPositiveRouting(weights map[string]int32) bool {
	for _, weight := range weights {
		if weight > 0 {
			return true
		}
	}
	return false
}

// controlInstances are the current instances autonomous control may change.
func (e *schedulerEnumeration) controlInstances() []SchedulerInstance {
	return e.movable
}

func (e *schedulerEnumeration) controlRouting() map[string]map[string]int32 {
	if e.controlBase != nil {
		return e.controlBase.Routing
	}
	return e.snapshot.Routing
}

func (e *schedulerEnumeration) visit(w int, chosen []SchedulerInstance) {
	if e.ctx.Err() != nil {
		return
	}
	if len(e.result) >= e.settings.MaxCandidates {
		e.search.Truncated = true
		return
	}
	countGroups := len(e.workloads)
	if e.groups != nil {
		countGroups = len(e.groups)
	}
	if w == countGroups {
		e.emit(chosen)
		return
	}
	group := e.group(w)
	d := group[0]
	// A sharing alternative attaches the UID to one compatible physical runner.
	if e.settings.Controls.Sharing {
		for j, i := range chosen {
			if e.ctx.Err() != nil {
				return
			}
			for _, o := range e.options {
				if e.ctx.Err() != nil {
					return
				}
				if i.OwnerWorkloadUID == "" && !schedulerHas(i.WorkloadUIDs, d.UID) && schedulerPlacementNode(e.snapshot.Nodes, i.Node) && schedulerOptionCondition(o) == schedulerOptionCondition(SchedulerOption{Instance: i}) && schedulerHas(o.CompatibleUIDs, d.UID) {
					next := append([]SchedulerInstance(nil), chosen...)
					next[j].WorkloadUIDs = append(append([]string(nil), next[j].WorkloadUIDs...), d.UID)
					e.visit(w+1, next)
					break
				}
			}
		}
	}
	count := 0
	for _, i := range e.controlInstances() {
		if e.ctx.Err() != nil {
			return
		}
		if e.sameGroup(i, group) {
			count++
		}
	}
	limit := e.replicaLimit(group, chosen)
	for n := 1; n <= limit; n++ {
		if e.ctx.Err() != nil {
			return
		}
		if !e.settings.Controls.Replicas && n != count {
			continue
		}
		e.place(w, 0, n, chosen)
	}
}

func (e *schedulerEnumeration) group(w int) []*Definition {
	if e.groups != nil {
		return e.groups[w]
	}
	return e.workloads[w : w+1]
}

func (e *schedulerEnumeration) sameGroup(instance SchedulerInstance, group []*Definition) bool {
	if e.groups == nil {
		return schedulerHas(instance.WorkloadUIDs, group[0].UID)
	}
	count := 0
	for _, d := range e.workloads {
		if e.ctx.Err() != nil {
			return false
		}
		if schedulerHas(instance.WorkloadUIDs, d.UID) {
			if !slices.Contains(group, d) {
				return false
			}
			count++
		}
	}
	return count == len(group)
}

func (e *schedulerEnumeration) replicaLimit(group []*Definition, chosen []SchedulerInstance) int {
	limit := e.settings.MaxReplicas
	for _, d := range group {
		if e.ctx.Err() != nil {
			return 0
		}
		count := 0
		for _, instance := range chosen {
			if e.ctx.Err() != nil {
				return 0
			}
			if schedulerHas(instance.WorkloadUIDs, d.UID) {
				count++
			}
		}
		limit = min(limit, e.settings.MaxReplicas-count)
	}
	return limit
}

func (e *schedulerEnumeration) sourceIndex(group []*Definition, chosen []SchedulerInstance) int {
	ordinal := 0
	for _, instance := range chosen {
		if e.ctx.Err() != nil {
			return -1
		}
		if e.sameGroup(instance, group) {
			ordinal++
		}
	}
	for index, instance := range e.controlInstances() {
		if e.ctx.Err() != nil {
			return -1
		}
		if e.sameGroup(instance, group) {
			if ordinal == 0 {
				return index
			}
			ordinal--
		}
	}
	return -1
}

func (e *schedulerEnumeration) emit(chosen []SchedulerInstance) {
	if e.ctx.Err() != nil {
		return
	}
	nodes := e.candidateNodes(chosen)
	if e.ctx.Err() != nil {
		return
	}
	c := SchedulerCandidate{Nodes: nodes, Instances: append([]SchedulerInstance(nil), chosen...)}
	// Preserve physical identity only when the complete measured allocation matches.
	// Fixed instances lead the chosen list and keep their own identity.
	used := map[string][]string{}
	for _, fixed := range e.fixed {
		used[fixed.ID] = fixed.WorkloadUIDs
	}
	for j := len(e.fixed); j < len(c.Instances); j++ {
		if e.ctx.Err() != nil {
			return
		}
		source, anchored := e.sources[c.Instances[j].ID]
		for _, old := range e.snapshot.Instances {
			if e.ctx.Err() != nil {
				return
			}
			if _, preserved := used[old.ID]; !preserved && e.canReuseSlot(old, source, anchored, c.Instances[j].WorkloadUIDs) && old.Node == c.Instances[j].Node && schedulerState(old) == schedulerState(c.Instances[j]) && schedulerNodeMode(e.snapshot.Nodes, old.Node) == schedulerNodeMode(nodes, old.Node) {
				c.Instances[j].ID = old.ID
				c.Instances[j].Ready = old.Ready
				c.Instances[j].TargetID, c.Instances[j].OwnerWorkloadUID = old.TargetID, old.OwnerWorkloadUID
				used[old.ID] = old.WorkloadUIDs
				break
			}
		}
	}
	c.Routing = e.routing(c.Instances, chosen, used)
	// Workloads outside autonomous control keep their current distribution.
	for _, d := range e.snapshot.Workloads {
		if !e.controlled[d.UID] {
			c.Routing[d.UID] = maps.Clone(e.controlRouting()[d.UID])
			if c.Routing[d.UID] == nil {
				c.Routing[d.UID] = map[string]int32{}
			}
		}
	}
	add := func(c SchedulerCandidate) {
		if e.ctx.Err() != nil {
			return
		}
		key := schedulerConfigKey(c)
		if e.seen[key] {
			return
		}
		if len(e.result) >= e.settings.MaxCandidates {
			e.search.Truncated = true
			return
		}
		e.seen[key] = true
		c.ID = fmt.Sprintf("candidate-%d", len(e.result))
		c.Actions = schedulerPlan(e.snapshot, c)
		e.result = append(e.result, c)
	}
	add(c)
	if e.settings.Controls.Routing {
		for _, d := range e.workloads {
			if e.ctx.Err() != nil {
				return
			}
			for _, i := range c.Instances {
				if e.ctx.Err() != nil {
					return
				}
				if !schedulerHas(i.WorkloadUIDs, d.UID) {
					continue
				}
				r := c
				r.Routing = maps.Clone(c.Routing)
				r.Routing[d.UID] = map[string]int32{i.ID: 1}
				add(r)
			}
		}
	}
}

func (e *schedulerEnumeration) candidateNodes(chosen []SchedulerInstance) []SchedulerNode {
	nodes := append([]SchedulerNode(nil), e.common...)
	modes := map[string]string{}
	for _, i := range chosen {
		if e.ctx.Err() != nil {
			return nil
		}
		if i.Profile != nil {
			modes[i.Node.UID] = i.Profile.Condition.PowerMode
		}
	}
	for n := range nodes {
		if m, ok := modes[nodes[n].Node.UID]; ok {
			nodes[n].PowerMode = m
		}
	}
	return nodes
}

func (e *schedulerEnumeration) canReuseSlot(old SchedulerInstance, source int, anchored bool, uids []string) bool {
	if !schedulerOwnerAllows(old.OwnerWorkloadUID, uids) {
		return false
	}
	if !anchored || old.ID == e.controlInstances()[source].ID {
		return true
	}
	// The routing origin is not a physical fence: an already prepared, unbound
	// matching executor is the replacement, not another replica to prepare.
	return old.Ready && !old.Stopped && len(old.WorkloadUIDs) == 0 && (old.TargetID != "" || old.OwnerWorkloadUID != "")
}

func (e *schedulerEnumeration) routing(instances, chosen []SchedulerInstance, used map[string][]string) map[string]map[string]int32 {
	routing := schedulerInstanceRouting(e.ctx, e.workloads, instances)
	if e.settings.Controls.Routing {
		return routing
	}
	for index, instance := range instances {
		if e.ctx.Err() != nil {
			return nil
		}
		oldUIDs, preserved := used[instance.ID]
		sourceID := instance.ID
		if source, anchored := e.sources[chosen[index].ID]; anchored {
			old := e.controlInstances()[source]
			oldUIDs, preserved, sourceID = old.WorkloadUIDs, true, old.ID
		}
		if !preserved {
			continue
		}
		for _, uid := range instance.WorkloadUIDs {
			if e.ctx.Err() != nil {
				return nil
			}
			if !schedulerHas(oldUIDs, uid) {
				continue
			}
			if route, ok := e.controlRouting()[uid]; ok {
				delete(routing[uid], instance.ID)
				if route[sourceID] > 0 {
					routing[uid][instance.ID] = route[sourceID]
				}
			}
		}
	}
	return routing
}

func (e *schedulerEnumeration) place(w, start, left int, xs []SchedulerInstance) {
	if e.ctx.Err() != nil {
		return
	}
	group := e.group(w)
	d := group[0]
	if left == 0 {
		e.visit(w+1, xs)
		return
	}
	if len(xs) >= e.settings.MaxInstances {
		return
	}
	source := -1
	if e.groups != nil {
		source = e.sourceIndex(group, xs)
	}
	for k := start; k < len(e.options); k++ {
		if e.ctx.Err() != nil {
			return
		}
		o := e.options[k]
		if slices.ContainsFunc(group, func(d *Definition) bool {
			return !schedulerHas(o.CompatibleUIDs, d.UID) || !e.optionAllowed(d, o, group, source)
		}) {
			continue
		}
		i := o.Instance
		i.WorkloadUIDs = make([]string, len(group))
		for index, d := range group {
			if e.ctx.Err() != nil {
				return
			}
			i.WorkloadUIDs[index] = d.UID
		}
		i.ID = fmt.Sprintf("target-%s-%d-%d", d.UID, k, len(xs))
		i.Ready = true
		if e.groups != nil {
			if e.sources == nil {
				e.sources = map[string]int{}
			}
			if source >= 0 {
				e.sources[i.ID] = source
			} else {
				delete(e.sources, i.ID)
			}
		}
		// One Node runs one power mode, and all its instances reference one ModelSpec.
		conflict := false
		for _, old := range xs {
			if e.ctx.Err() != nil {
				return
			}
			if old.Node.UID == i.Node.UID && (old.Profile == nil || old.Profile.Condition.PowerMode != i.Profile.Condition.PowerMode || !e.sameModel(old, i)) {
				conflict = true
			}
		}
		if conflict {
			continue
		}
		next := k
		if e.groups != nil {
			next = 0
		}
		e.place(w, next, left-1, append(append([]SchedulerInstance(nil), xs...), i))
	}
}

// sameModel reports whether two instances reference one ModelSpec: the same
// name, and the same UID when both reach a current Workload definition.
func (e *schedulerEnumeration) sameModel(a, b SchedulerInstance) bool {
	model := func(i SchedulerInstance) (string, string) {
		for _, d := range e.snapshot.Workloads {
			if d.UID == i.OwnerWorkloadUID || schedulerHas(i.WorkloadUIDs, d.UID) {
				return d.Model.Name, d.Model.UID
			}
		}
		return i.Profile.Condition.LogicalModel, ""
	}
	nameA, uidA := model(a)
	nameB, uidB := model(b)
	return nameA == nameB && (uidA == "" || uidB == "" || uidA == uidB)
}
func schedulerOptionKey(o SchedulerOption) string {
	return o.Instance.Node.UID + "/" + schedulerState(o.Instance) + fmt.Sprint(o.CompatibleUIDs)
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

// SchedulerLogicalConfigKey identifies configuration semantics, not a physical
// executor or a particular observation. Replica records retain joint routing.
func SchedulerLogicalConfigKey(snapshot SchedulerSnapshot, candidate SchedulerCandidate) (string, error) {
	nodes := make([]string, 0, len(candidate.Nodes))
	nodeIDs := map[string]bool{}
	for _, node := range candidate.Nodes {
		if node.Node.UID == "" || nodeIDs[node.Node.UID] {
			return "", fmt.Errorf("logical configuration has missing or duplicate Node UID %q", node.Node.UID)
		}
		nodeIDs[node.Node.UID] = true
		nodes = append(nodes, fmt.Sprintf("%q/%q", node.Node.UID, node.PowerMode))
	}
	definitions := map[string]*Definition{}
	for _, definition := range snapshot.Workloads {
		if definition == nil || definition.UID == "" || definitions[definition.UID] != nil {
			return "", fmt.Errorf("logical configuration requires unique immutable workload definitions")
		}
		definitions[definition.UID] = definition
	}
	instances := make([]string, 0, len(candidate.Instances))
	instanceIDs := map[string]bool{}
	for _, instance := range candidate.Instances {
		if instance.ID == "" || instanceIDs[instance.ID] || !nodeIDs[instance.Node.UID] || instance.Profile == nil || instance.Profile.ID == "" {
			return "", fmt.Errorf("logical configuration has unidentified instance %q", instance.ID)
		}
		instanceIDs[instance.ID] = true
		uids := slices.Clone(instance.WorkloadUIDs)
		sort.Strings(uids)
		for index, uid := range uids {
			if definitions[uid] == nil || (index > 0 && uids[index-1] == uid) {
				return "", fmt.Errorf("instance %q has unknown or duplicate workload %q", instance.ID, uid)
			}
		}
		deadlinePolicy := ""
		if instance.Profile.Condition.Firstfloor != nil {
			deadlinePolicy = instance.Profile.Condition.Firstfloor.DeadlinePolicy
		}
		routes := make([]string, 0, len(definitions))
		for uid := range definitions {
			weight := int32(0)
			if schedulerHas(instance.WorkloadUIDs, uid) {
				weight = instance.Weight
			}
			if routing, explicit := candidate.Routing[uid]; explicit {
				weight = routing[instance.ID]
			}
			routes = append(routes, fmt.Sprintf("%q/%d", uid, weight))
		}
		sort.Strings(routes)
		instances = append(instances, fmt.Sprintf("%q/%q/%q/%d/%t/%d/%d/%d/%q/%q", instance.Node.UID, schedulerState(instance), uids, instance.Allocation.CPUCount, instance.Allocation.CPUReserved, instance.Capacity, instance.MaxBatch, instance.MaxWait, deadlinePolicy, routes))
	}
	for uid, routing := range candidate.Routing {
		if definitions[uid] == nil {
			return "", fmt.Errorf("routing references unknown workload %q", uid)
		}
		for target := range routing {
			if !instanceIDs[target] {
				return "", fmt.Errorf("routing references unknown instance %q", target)
			}
		}
	}
	workloads := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		workloads = append(workloads, fmt.Sprintf("%q/%g/%d", definition.Identity(), definition.SLO.Percentile, definition.SLO.TargetLatency))
	}
	sort.Strings(nodes)
	sort.Strings(instances)
	sort.Strings(workloads)
	return fmt.Sprintf("%q", [][]string{nodes, instances, workloads}), nil
}

func schedulerConfigKey(c SchedulerCandidate) string {
	parts := []string{}
	for _, i := range c.Instances {
		uids := append([]string(nil), i.WorkloadUIDs...)
		sort.Strings(uids)
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

// schedulerPlacementNode reports a Node that accepts new placements.
func schedulerPlacementNode(nodes []SchedulerNode, node NodeReference) bool {
	for _, n := range nodes {
		if n.Node == node {
			return !n.Measurement && !slices.Contains(n.Reasons, NodePlacementInventoryMissing) && !slices.Contains(n.Reasons, NodePlacementBudgetUnavailable)
		}
	}
	return false
}

// inActivePool reports whether uid owns or is bound to an instance on node.
func (e *schedulerEnumeration) inActivePool(uid string, node NodeReference) bool {
	return slices.ContainsFunc(e.snapshot.Instances, func(i SchedulerInstance) bool {
		return i.Node == node && (i.OwnerWorkloadUID == uid || schedulerHas(i.WorkloadUIDs, uid))
	})
}
func (e *schedulerEnumeration) optionAllowed(d *Definition, o SchedulerOption, group []*Definition, source int) bool {
	s, snap := e.settings, e.snapshot
	i := o.Instance
	if len(i.Allocation.CPUs) > s.MaxCPUs {
		return false
	}
	if i.Profile == nil || i.Node.UID != i.Profile.Condition.NodeUID || !schedulerProfileMatches(d, i.Profile) {
		return false
	}
	if !schedulerPlacementNode(snap.Nodes, i.Node) {
		return false
	}
	for _, n := range snap.Nodes {
		if n.Node == i.Node && n.PowerMode != i.Profile.Condition.PowerMode && (!s.Controls.Power || slices.Contains(n.Reasons, NodePowerModeUnavailable)) {
			return false
		}
	}
	// Membership OFF keeps each Workload within its current active POOL.
	if !s.Controls.Membership && !e.inActivePool(d.UID, i.Node) {
		return false
	}
	for index, old := range e.controlInstances() {
		if !schedulerHas(old.WorkloadUIDs, d.UID) || (e.groups != nil && (!e.sameGroup(old, group) || (source >= 0 && index != source))) {
			continue
		}
		if !s.Controls.Placement && old.Node != i.Node {
			continue
		}
		if !s.Controls.Resources && !schedulerSameResourceChoice(old, i) {
			continue
		}
		if !s.Controls.Batching && (old.MaxBatch != i.MaxBatch || old.MaxWait != i.MaxWait) {
			continue
		}
		return true
	}
	return s.Controls.Placement && s.Controls.Resources && s.Controls.Batching
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

// Refresh forecast facts without mutating an executing operation's frozen target.
// Only a known ready slot supplies physical identity; missing proof stays missing.
func schedulerCurrentTarget(snapshot SchedulerSnapshot, target SchedulerCandidate) SchedulerCandidate {
	target.Instances = append([]SchedulerInstance(nil), target.Instances...)
	target.Routing = maps.Clone(target.Routing)
	for index, wanted := range target.Instances {
		current := wanted
		for _, actual := range snapshot.Instances {
			if !actual.Ready || actual.Stopped || !schedulerSamePhysicalTarget(actual, wanted) {
				continue
			}
			if !schedulerOwnerAllows(actual.OwnerWorkloadUID, wanted.WorkloadUIDs) {
				continue
			}
			current.ID, current.TargetID, current.OwnerWorkloadUID = actual.ID, actual.TargetID, actual.OwnerWorkloadUID
			current.Ready = true
			if current.ID != wanted.ID {
				for _, uid := range wanted.WorkloadUIDs {
					weights := maps.Clone(target.Routing[uid])
					if weight, ok := weights[wanted.ID]; ok {
						delete(weights, wanted.ID)
						weights[current.ID] = weight
						target.Routing[uid] = weights
					}
				}
			}
			break
		}
		target.Instances[index] = current
	}
	return target
}

func schedulerPlan(snap SchedulerSnapshot, c SchedulerCandidate) []Action {
	p := schedulerPlanState{snap: snap, target: &c, actions: []Action{}, nodeTail: map[string]string{}, prepares: []string{}, stops: map[string]string{}}
	p.changeNodes(c)
	p.prepareTargets(c)
	// Candidate ordinals and rolling witness text are not operation identity.
	route := p.add(schedulerActionRoute, NodeReference{}, "", "", schedulerConfigKey(c), p.prepares)
	p.retireOld(c, route)
	p.continueActions()
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
			if len(state) == 0 {
				for _, next := range p.actions[index+1:] {
					if next.Kind == schedulerActionPrepare && next.Node == a.Node && schedulerHas(next.DependsOn, a.ID) {
						state = []string{next.To}
					}
				}
			}
			matches = func(cost OperationCost) bool { return schedulerEqualState(state, cost.BeforeState) }
			modes[a.Node] = a.To
		default:
			continue
		}
		for _, cost := range p.snap.Costs {
			if a.CostID == "" && cost.Node == a.Node && cost.Kind == a.Kind && cost.From == a.From && cost.To == a.To && matches(cost) {
				a.CostID = cost.ID
			}
		}
	}
}

type schedulerPlanState struct {
	snap     SchedulerSnapshot
	target   *SchedulerCandidate
	actions  []Action
	nodeTail map[string]string
	prepares []string
	stops    map[string]string
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
		routing := old.Kind == schedulerActionRoute && (action.Kind == schedulerActionRoute || action.Kind == schedulerActionStop)
		if (sameNode || routing) && !schedulerHas(action.DependsOn, old.ID) {
			action.DependsOn = append(action.DependsOn, old.ID)
		}
	}
}

func (p *schedulerPlanState) completedEffectPresent(action Action) bool {
	switch action.Kind {
	case schedulerActionPrepare:
		return slices.ContainsFunc(p.snap.Instances, func(instance SchedulerInstance) bool {
			return instance.ID == action.Instance && instance.Node == action.Node &&
				instance.Ready && !instance.Stopped && schedulerState(instance) == action.To
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
		return !slices.ContainsFunc(p.snap.Instances, func(instance SchedulerInstance) bool { return instance.ID == action.Instance }) &&
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
			return schedulerSnapshotTargetMatches(actual, wanted)
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
	if !schedulerOwnerAllows(actual.OwnerWorkloadUID, wanted.WorkloadUIDs) {
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
		for _, oldNode := range p.snap.Nodes {
			if n.Node != oldNode.Node || n.PowerMode == oldNode.PowerMode {
				continue
			}
			from := oldNode.PowerMode
			// An available Node first drops to its lowest mode, the same in every
			// candidate; re-entering it raises the mode from there. While that
			// lowering runs the Node reports no mode, so the unfinished Fixed
			// action stays the common lowering rather than a compared change.
			if i := slices.IndexFunc(p.snap.Actions, func(a Action) bool {
				return a.Fixed && a.Node == n.Node && (a.Status == schedulerActionPlanned || a.Status == phaseRunning)
			}); i >= 0 {
				old := p.snap.Actions[i]
				p.add(schedulerActionPower, n.Node, "", old.From, old.To, nil)
				p.actions[len(p.actions)-1].Fixed = true
				from = old.To
			} else if lowest := SchedulerLowestMode(p.snap.Baselines, n.Node); lowest != "" && lowest != from && schedulerAvailable(p.snap, oldNode) {
				p.add(schedulerActionPower, n.Node, "", from, lowest, nil)
				p.actions[len(p.actions)-1].Fixed = true
				from = lowest
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
				deps = []string{dep}
			}
			for _, i := range p.snap.Instances {
				if i.Node == n.Node {
					p.add(schedulerActionRemove, n.Node, i.ID, schedulerState(i), "unloaded", deps)
				}
			}
			if tail := p.nodeTail[n.Node.UID]; tail != "" {
				p.prepares = append(p.prepares, tail)
			}
		}
	}
}

func (p *schedulerPlanState) prepareTargets(c SchedulerCandidate) {
	for _, wanted := range c.Instances {
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
func schedulerOwnerAllows(owner string, uids []string) bool {
	return owner == "" || !slices.ContainsFunc(uids, func(uid string) bool { return uid != owner })
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
			if actual.Node == wanted.Node && p.stops[actual.ID] == "" {
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
		return len(ValidateNodeBudget(node, allocations, nil)) == 0
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

func (p *schedulerPlanState) continueActions() {
	for _, old := range p.snap.Actions {
		found := false
		for _, a := range p.actions {
			if a.ID == old.ID {
				found = true
			}
		}
		if !found && old.Status != schedulerActionComplete && old.Status != schedulerActionFailed {
			if old.Cancelable && old.Status == schedulerActionPlanned {
				old.Status = schedulerActionCancel
				p.actions = append(p.actions, old)
			} else {
				p.actions = append(p.actions, old)
			}
		}
	}
}

// Costs for route/stop/drain are represented by the native queue and observed
// completion timeline. They are not assigned fictitious wall-energy zero trials.
