package serving

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
)

func EnumerateScheduler(settings SchedulerSettings, snapshot SchedulerSnapshot) ([]SchedulerCandidate, SchedulerSearch) {
	search := schedulerSearchPopulation(settings, snapshot)
	keep := SchedulerCandidate{ID: "keep", Keep: true, Nodes: append([]SchedulerNode(nil), snapshot.Nodes...), Instances: append([]SchedulerInstance(nil), snapshot.Instances...), Routing: schedulerInstanceRouting(snapshot.Workloads, snapshot.Instances)}
	maps.Copy(keep.Routing, snapshot.Routing)
	result := []SchedulerCandidate{keep}
	if settings.Validate() != nil || !settings.Controls.Enabled {
		search.Examined = 1
		return result, search
	}
	if snapshot.InProgressTarget != nil {
		keep.Actions = schedulerPlan(snapshot, *snapshot.InProgressTarget)
		result[0] = keep
	}
	workloads := append([]*Definition(nil), snapshot.Workloads...)
	for _, d := range workloads {
		if d == nil {
			search.Examined = 1
			return result, search
		}
	}
	sort.Slice(workloads, func(i, j int) bool { return workloads[i].UID < workloads[j].UID })
	e := schedulerEnumeration{settings: settings, snapshot: snapshot, search: search, workloads: workloads, options: schedulerMeasuredOptions(settings, snapshot), seen: map[string]bool{schedulerConfigKey(keep, true): true}, result: result}
	e.controlBase = schedulerUnroutedTransition(snapshot)
	if !settings.Controls.Sharing {
		e.groups = schedulerExistingWorkloadGroups(workloads, e.controlInstances())
	}
	e.visit(0, nil)
	e.search.Examined = len(e.result)
	return e.result, e.search
}

func schedulerInstanceRouting(workloads []*Definition, instances []SchedulerInstance) map[string]map[string]int32 {
	routing := make(map[string]map[string]int32, len(workloads))
	for _, d := range workloads {
		if d == nil {
			continue
		}
		weights := map[string]int32{}
		for _, i := range instances {
			if schedulerHas(i.WorkloadUIDs, d.UID) && i.Weight > 0 {
				weights[i.ID] = i.Weight
			}
		}
		routing[d.UID] = weights
	}
	return routing
}

func schedulerSearchPopulation(settings SchedulerSettings, snapshot SchedulerSnapshot) SchedulerSearch {
	search := SchedulerSearch{Bounds: settings, WindowStart: snapshot.At.Add(-settings.ObservationWindow), WindowEnd: snapshot.At, Population: "received events in [windowStart,windowEnd); validated compute arrivals (including capacity/evidence rejections) replayed; outstanding retained independently"}
	loads := map[string]*SchedulerLoad{}
	for _, a := range snapshot.Arrivals {
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
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		load := *loads[key]
		if settings.ObservationWindow > 0 {
			load.InputsPerSecond = float64(load.ComputeInputs) / settings.ObservationWindow.Seconds()
		}
		search.Loads = append(search.Loads, load)
	}
	return search
}

func schedulerMeasuredOptions(settings SchedulerSettings, snapshot SchedulerSnapshot) []SchedulerOption {
	options := []SchedulerOption{}
	for _, o := range snapshot.Options {
		if !settings.Controls.Batching {
			options = append(options, o)
			continue
		}
		if o.Instance.Profile == nil {
			continue
		}
		for _, b := range o.Instance.Profile.Batches {
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
		options = append(options, o)
	}
	sort.Slice(options, func(i, j int) bool { return schedulerOptionKey(options[i]) < schedulerOptionKey(options[j]) })
	return options
}

// Sharing OFF preserves the existing physical UID cohorts, not singleton UIDs.
func schedulerExistingWorkloadGroups(workloads []*Definition, instances []SchedulerInstance) [][]*Definition {
	groups := make([][]*Definition, 0, len(instances))
	seen := map[string]bool{}
	assigned := map[string]bool{}
	for _, instance := range instances {
		group := []*Definition{}
		uids := []string{}
		for _, d := range workloads {
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
		if !assigned[d.UID] {
			groups = append(groups, []*Definition{d})
		}
	}
	return groups
}

type schedulerEnumeration struct {
	settings    SchedulerSettings
	snapshot    SchedulerSnapshot
	search      SchedulerSearch
	controlBase *SchedulerCandidate
	workloads   []*Definition
	groups      [][]*Definition
	sources     map[string]int
	options     []SchedulerOption
	seen        map[string]bool
	result      []SchedulerCandidate
}

// A drained routing gap does not change the selected cohort, replica count or
// per-UID weights. Resource occupancy and physical reuse still use the snapshot.
func schedulerUnroutedTransition(snapshot SchedulerSnapshot) *SchedulerCandidate {
	goal := snapshot.InProgressTarget
	if goal == nil || len(goal.Routing) != len(snapshot.Workloads) {
		return nil
	}
	missing := false
	for _, d := range snapshot.Workloads {
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

func (e *schedulerEnumeration) controlInstances() []SchedulerInstance {
	if e.controlBase != nil {
		return e.controlBase.Instances
	}
	return e.snapshot.Instances
}

func (e *schedulerEnumeration) controlRouting() map[string]map[string]int32 {
	if e.controlBase != nil {
		return e.controlBase.Routing
	}
	return e.snapshot.Routing
}

func (e *schedulerEnumeration) visit(w int, chosen []SchedulerInstance) {
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
			for _, o := range e.options {
				if schedulerOptionCondition(o) == schedulerOptionCondition(SchedulerOption{Instance: i}) && schedulerHas(o.CompatibleUIDs, d.UID) {
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
		if e.sameGroup(i, group) {
			count++
		}
	}
	limit := e.replicaLimit(group, chosen)
	for n := 1; n <= limit; n++ {
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
		count := 0
		for _, instance := range chosen {
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
		if e.sameGroup(instance, group) {
			ordinal++
		}
	}
	for index, instance := range e.controlInstances() {
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
	nodes := append([]SchedulerNode(nil), e.snapshot.Nodes...)
	modes := map[string]string{}
	for _, i := range chosen {
		if i.Profile != nil {
			modes[i.Node.UID] = i.Profile.Condition.PowerMode
		}
	}
	for n := range nodes {
		if m, ok := modes[nodes[n].Node.UID]; ok {
			nodes[n].PowerMode = m
		}
		for _, i := range chosen {
			if i.Node.UID == nodes[n].Node.UID {
				nodes[n].Pool = i.Pool
			}
		}
	}
	c := SchedulerCandidate{Nodes: nodes, Instances: append([]SchedulerInstance(nil), chosen...)}
	// Preserve physical identity only when the complete measured allocation matches.
	used := map[string][]string{}
	for j := range c.Instances {
		source, anchored := e.sources[c.Instances[j].ID]
		for _, old := range e.snapshot.Instances {
			if _, preserved := used[old.ID]; !preserved && e.canReuseSlot(old, source, anchored, c.Instances[j].WorkloadUIDs) && old.Node == c.Instances[j].Node && old.Pool == c.Instances[j].Pool && schedulerState(old) == schedulerState(c.Instances[j]) && schedulerNodeMode(e.snapshot.Nodes, old.Node) == schedulerNodeMode(nodes, old.Node) {
				c.Instances[j].ID = old.ID
				c.Instances[j].Ready = old.Ready
				c.Instances[j].TargetID, c.Instances[j].OwnerWorkloadUID = old.TargetID, old.OwnerWorkloadUID
				if old.CompletionObserved && old.CompletionMeasurement.Valid() && old.CompletionOverhead == old.CompletionMeasurement.Completion && old.CompletionMeasurement.Matches(c.Instances[j]) {
					schedulerCopyCompletion(&c.Instances[j], old)
				}
				used[old.ID] = old.WorkloadUIDs
				break
			}
		}
	}
	c.Routing = e.routing(c.Instances, chosen, used)
	add := func(c SchedulerCandidate) {
		key := schedulerConfigKey(c, true)
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
			for _, i := range c.Instances {
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

func (e *schedulerEnumeration) canReuseSlot(old SchedulerInstance, source int, anchored bool, uids []string) bool {
	if old.OwnerWorkloadUID != "" && (len(uids) != 1 || uids[0] != old.OwnerWorkloadUID) {
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
	routing := schedulerInstanceRouting(e.workloads, instances)
	if e.settings.Controls.Routing {
		return routing
	}
	for index, instance := range instances {
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
		o := e.options[k]
		if slices.ContainsFunc(group, func(d *Definition) bool {
			return !schedulerHas(o.CompatibleUIDs, d.UID) || !e.optionAllowed(d, o, group, source)
		}) {
			continue
		}
		i := o.Instance
		i.WorkloadUIDs = make([]string, len(group))
		for index, d := range group {
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
		conflict := false
		for _, old := range xs {
			if old.Node.UID == i.Node.UID && (old.Pool != i.Pool || old.Profile.Condition.PowerMode != i.Profile.Condition.PowerMode) {
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
func schedulerOptionKey(o SchedulerOption) string {
	return o.Instance.Node.UID + "/" + o.Instance.Pool + "/" + schedulerState(o.Instance) + fmt.Sprint(o.CompatibleUIDs)
}
func schedulerOptionCondition(o SchedulerOption) string {
	i := o.Instance
	if i.Profile == nil {
		return ""
	}
	p := i.Profile
	return fmt.Sprint(i.Node, i.Pool, p.Condition.Artifact, p.Condition.RuntimeFamily, p.Condition.Backend, p.Condition.RuntimeImage, p.Condition.Tensor, p.Condition.PowerMode, p.Condition.CPU, p.Condition.Firstfloor, p.Condition.RuntimeAccess, p.Condition.PlannedBatchSizes, i.Allocation, i.MaxBatch, i.MaxWait, i.CompletionOverhead, i.EnvelopeSource, i.CompletionObserved)
}
func schedulerConfigKey(c SchedulerCandidate, includeObservation bool) string {
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
		part := fmt.Sprint(i.ID, i.Node.UID, i.Pool, schedulerState(i), uids, weights, i.Capacity, i.MaxBatch, i.MaxWait, i.CompletionOverhead, deadlinePolicy, i.CompletionObserved)
		if includeObservation {
			part += i.EnvelopeSource
		}
		parts = append(parts, part)
	}
	sort.Strings(parts)
	for _, n := range c.Nodes {
		parts = append(parts, n.Node.UID+"/"+n.Pool+"/"+n.PowerMode)
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
func (e *schedulerEnumeration) optionAllowed(d *Definition, o SchedulerOption, group []*Definition, source int) bool {
	s, snap := e.settings, e.snapshot
	i := o.Instance
	if len(i.Allocation.CPUs) > s.MaxCPUs {
		return false
	}
	if i.Profile == nil || i.Node.UID != i.Profile.Condition.NodeUID || !schedulerProfileMatches(d, i.Profile) {
		return false
	}
	for _, n := range snap.Nodes {
		if n.Node == i.Node {
			if !s.Controls.Power && n.PowerMode != i.Profile.Condition.PowerMode {
				return false
			}
			if !s.Controls.Membership && n.Pool != i.Pool {
				return false
			}
		}
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
func schedulerRequestMatches(d *Definition, path int, p *Profile) bool {
	return d != nil && p != nil && path >= 0 && path < len(d.Model.Paths) && sameInputs(d.Model.Paths[path].Tensors, p.Condition.Tensor.Tensors) && schedulerProfileMatches(d, p)
}

// Refresh forecast facts without mutating an executing operation's frozen target.
// Only a known ready slot supplies physical identity; missing proof stays missing.
func schedulerCurrentTarget(snapshot SchedulerSnapshot, target SchedulerCandidate) SchedulerCandidate {
	target.Instances = append([]SchedulerInstance(nil), target.Instances...)
	target.Routing = maps.Clone(target.Routing)
	for index, wanted := range target.Instances {
		current := wanted
		current.CompletionObserved, current.CompletionMeasurement = false, nil
		for _, option := range snapshot.Options {
			observed := option.Instance
			if observed.Node == wanted.Node && observed.Pool == wanted.Pool && schedulerState(observed) == schedulerState(wanted) {
				schedulerCopyCompletion(&current, observed)
				break
			}
		}
		for _, actual := range snapshot.Instances {
			if !actual.Ready || actual.Stopped || !schedulerSamePhysicalTarget(actual, wanted) {
				continue
			}
			if actual.OwnerWorkloadUID != "" && (len(wanted.WorkloadUIDs) != 1 || wanted.WorkloadUIDs[0] != actual.OwnerWorkloadUID) {
				continue
			}
			current.ID, current.TargetID, current.OwnerWorkloadUID = actual.ID, actual.TargetID, actual.OwnerWorkloadUID
			current.Ready = true
			schedulerCopyCompletion(&current, actual)
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

func schedulerCopyCompletion(target *SchedulerInstance, observed SchedulerInstance) {
	target.CompletionOverhead = observed.CompletionOverhead
	target.CompletionObserved = observed.CompletionObserved
	target.CompletionMeasurement = observed.CompletionMeasurement
	target.EnvelopeSource = observed.EnvelopeSource
}

func schedulerPlan(snap SchedulerSnapshot, c SchedulerCandidate) []Action {
	p := schedulerPlanState{snap: snap, target: &c, actions: []Action{}, nodeTail: map[string]string{}, prepares: []string{}, stops: map[string]string{}}
	p.changeNodes(c)
	p.prepareTargets(c)
	// Candidate ordinals and rolling witness text are not operation identity.
	route := p.add(schedulerActionRoute, NodeReference{}, "", "", schedulerConfigKey(c, false), p.prepares)
	p.retireOld(c, route)
	p.continueActions()
	return p.actions
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
	if kind == schedulerActionPrepare || kind == schedulerActionRemove || kind == schedulerActionPower || kind == schedulerActionMembership {
		if tail := p.nodeTail[node.UID]; tail != "" {
			a.DependsOn = append(a.DependsOn, tail)
		}
		for _, cost := range p.snap.Costs {
			if cost.Node == node && cost.Kind == kind && cost.From == from && cost.To == to {
				a.CostID = cost.ID
				break
			}
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
	if kind == schedulerActionPrepare || kind == schedulerActionRemove || kind == schedulerActionPower || kind == schedulerActionMembership {
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
	case schedulerActionPower, schedulerActionMembership:
		for _, node := range p.snap.Nodes {
			if node.Node == action.Node {
				if action.Kind == schedulerActionPower {
					return node.PowerMode == action.To
				}
				return node.Pool == action.To
			}
		}
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
		actual.ID != wanted.ID || actual.Node != wanted.Node || actual.Pool != wanted.Pool ||
		schedulerState(actual) != schedulerState(wanted) {
		return false
	}
	if actual.OwnerWorkloadUID != "" && (len(wanted.WorkloadUIDs) != 1 || wanted.WorkloadUIDs[0] != actual.OwnerWorkloadUID) {
		return false
	}
	return actual.Capacity == wanted.Capacity && actual.MaxBatch == wanted.MaxBatch &&
		actual.MaxWait == wanted.MaxWait && actual.CompletionOverhead == wanted.CompletionOverhead &&
		reflect.DeepEqual(actual.Profile.Condition.Firstfloor, wanted.Profile.Condition.Firstfloor) &&
		schedulerCompletionMatches(actual) && schedulerCompletionMatches(wanted)
}

func schedulerCompletionMatches(instance SchedulerInstance) bool {
	return instance.CompletionObserved && instance.CompletionMeasurement != nil &&
		instance.CompletionOverhead == instance.CompletionMeasurement.Completion &&
		instance.CompletionMeasurement.Matches(instance)
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
	// Power and membership affect every runner on the node, never only one UID.
	for _, n := range c.Nodes {
		for _, oldNode := range p.snap.Nodes {
			if n.Node != oldNode.Node {
				continue
			}
			if n.PowerMode == oldNode.PowerMode && n.Pool == oldNode.Pool {
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
			if n.PowerMode != oldNode.PowerMode {
				dep := p.add(schedulerActionPower, n.Node, "", oldNode.PowerMode, n.PowerMode, deps)
				deps = []string{dep}
			}
			removed := []string{}
			for _, i := range p.snap.Instances {
				if i.Node == n.Node {
					rmDeps := deps
					if n.PowerMode == oldNode.PowerMode {
						rmDeps = []string{drains[i.ID]}
					}
					rm := p.add(schedulerActionRemove, n.Node, i.ID, schedulerState(i), "unloaded", rmDeps)
					removed = append(removed, rm)
				}
			}
			if n.Pool != oldNode.Pool {
				p.add(schedulerActionMembership, n.Node, "", oldNode.Pool, n.Pool, removed)
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
		actual.Node == wanted.Node && actual.Pool == wanted.Pool && schedulerState(actual) == schedulerState(wanted)
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
		return len(schedulerValidateNodeBudget(node, allocations, nil)) == 0
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
