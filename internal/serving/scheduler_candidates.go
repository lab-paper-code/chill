package serving

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
)

func EnumerateScheduler(settings SchedulerSettings, snapshot SchedulerSnapshot) ([]SchedulerCandidate, SchedulerSearch) {
	search := schedulerSearchPopulation(settings, snapshot)
	keep := SchedulerCandidate{ID: "keep", Keep: true, Nodes: append([]SchedulerNode(nil), snapshot.Nodes...), Instances: append([]SchedulerInstance(nil), snapshot.Instances...), Routing: snapshot.Routing}
	result := []SchedulerCandidate{keep}
	if settings.Validate() != nil || !settings.Controls.Enabled {
		search.Examined = 1
		return result, search
	}
	workloads := append([]*Definition(nil), snapshot.Workloads...)
	for _, d := range workloads {
		if d == nil {
			search.Examined = 1
			return result, search
		}
	}
	sort.Slice(workloads, func(i, j int) bool { return workloads[i].UID < workloads[j].UID })
	e := schedulerEnumeration{settings: settings, snapshot: snapshot, search: search, workloads: workloads, options: schedulerMeasuredOptions(settings, snapshot), seen: map[string]bool{schedulerConfigKey(keep): true}, result: result}
	e.visit(0, nil)
	e.search.Examined = len(e.result)
	return e.result, e.search
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

type schedulerEnumeration struct {
	settings  SchedulerSettings
	snapshot  SchedulerSnapshot
	search    SchedulerSearch
	workloads []*Definition
	options   []SchedulerOption
	seen      map[string]bool
	result    []SchedulerCandidate
}

func (e *schedulerEnumeration) visit(w int, chosen []SchedulerInstance) {
	if len(e.result) >= e.settings.MaxCandidates {
		e.search.Truncated = true
		return
	}
	if w == len(e.workloads) {
		e.emit(chosen)
		return
	}
	d := e.workloads[w]
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
	for _, i := range e.snapshot.Instances {
		if schedulerHas(i.WorkloadUIDs, d.UID) {
			count++
		}
	}
	for n := 1; n <= e.settings.MaxReplicas; n++ {
		if !e.settings.Controls.Replicas && n != count {
			continue
		}
		e.place(w, 0, n, chosen)
	}
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
	used := map[string]bool{}
	for j := range c.Instances {
		for _, old := range e.snapshot.Instances {
			if !used[old.ID] && old.Node == c.Instances[j].Node && old.Pool == c.Instances[j].Pool && schedulerState(old) == schedulerState(c.Instances[j]) && schedulerNodeMode(e.snapshot.Nodes, old.Node) == schedulerNodeMode(nodes, old.Node) {
				c.Instances[j].ID = old.ID
				c.Instances[j].Ready = old.Ready
				if old.CompletionObserved && old.CompletionMeasurement.Valid() && !slices.ContainsFunc(c.Instances[j].WorkloadUIDs, func(uid string) bool { return !schedulerHas(old.WorkloadUIDs, uid) }) {
					c.Instances[j].CompletionOverhead, c.Instances[j].EnvelopeSource, c.Instances[j].CompletionObserved = old.CompletionOverhead, old.EnvelopeSource, true
					c.Instances[j].CompletionMeasurement = old.CompletionMeasurement
				}
				used[old.ID] = true
				break
			}
		}
	}
	add := func(c SchedulerCandidate) {
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
			for _, i := range c.Instances {
				if !schedulerHas(i.WorkloadUIDs, d.UID) {
					continue
				}
				r := c
				r.Routing = map[string]map[string]int32{d.UID: {i.ID: 1}}
				add(r)
			}
		}
	}
}

func (e *schedulerEnumeration) place(w, start, left int, xs []SchedulerInstance) {
	d := e.workloads[w]
	if left == 0 {
		e.visit(w+1, xs)
		return
	}
	if len(xs) >= e.settings.MaxInstances {
		return
	}
	for k := start; k < len(e.options); k++ {
		o := e.options[k]
		if !schedulerHas(o.CompatibleUIDs, d.UID) || !schedulerOptionAllowed(e.settings, e.snapshot, d, o) {
			continue
		}
		i := o.Instance
		i.WorkloadUIDs = []string{d.UID}
		i.ID = fmt.Sprintf("target-%s-%d-%d", d.UID, k, len(xs))
		i.Ready = true
		conflict := false
		for _, old := range xs {
			if old.Node.UID == i.Node.UID && (old.Pool != i.Pool || old.Profile.Condition.PowerMode != i.Profile.Condition.PowerMode) {
				conflict = true
			}
		}
		if conflict {
			continue
		}
		e.place(w, k, left-1, append(append([]SchedulerInstance(nil), xs...), i))
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
		parts = append(parts, fmt.Sprint(i.Node.UID, i.Pool, schedulerState(i), uids, weights, i.MaxBatch, i.MaxWait, i.CompletionOverhead, i.EnvelopeSource, i.CompletionObserved))
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
func schedulerOptionAllowed(s SchedulerSettings, snap SchedulerSnapshot, d *Definition, o SchedulerOption) bool {
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
	current := []SchedulerInstance{}
	for _, old := range snap.Instances {
		if schedulerHas(old.WorkloadUIDs, d.UID) {
			current = append(current, old)
		}
	}
	for _, old := range current {
		if !s.Controls.Placement && old.Node != i.Node {
			continue
		}
		if !s.Controls.Resources && !reflect.DeepEqual(old.Allocation, i.Allocation) {
			continue
		}
		if !s.Controls.Batching && (old.MaxBatch != i.MaxBatch || old.MaxWait != i.MaxWait) {
			continue
		}
		return true
	}
	return s.Controls.Placement && s.Controls.Resources && s.Controls.Batching
}
func schedulerProfileMatches(d *Definition, p *Profile) bool {
	return d != nil && profilePath(d, p) != nil
}
func schedulerRequestMatches(d *Definition, path int, p *Profile) bool {
	return d != nil && p != nil && path >= 0 && path < len(d.Model.Paths) && sameInputs(d.Model.Paths[path].Tensors, p.Condition.Tensor.Tensors) && schedulerProfileMatches(d, p)
}
func schedulerPlan(snap SchedulerSnapshot, c SchedulerCandidate) []Action {
	p := schedulerPlanState{snap: snap, actions: []Action{}, nodeTail: map[string]string{}, prepares: []string{}, stops: map[string]string{}}
	p.changeNodes(c)
	p.prepareTargets(c)
	route := p.add(schedulerActionRoute, NodeReference{}, "", "", c.ID, p.prepares)
	p.retireOld(c, route)
	p.continueActions()
	return p.actions
}

type schedulerPlanState struct {
	snap     SchedulerSnapshot
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
	for _, old := range p.snap.Actions {
		if old.Kind == kind && old.Node == node && old.Instance == id && old.From == from && old.To == to && (old.Status == schedulerActionPlanned || old.Status == phaseRunning) {
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
	for _, i := range c.Instances {
		exists := false
		for _, old := range p.snap.Instances {
			if old.ID == i.ID {
				exists = true
			}
		}
		if !exists {
			id := p.add(schedulerActionPrepare, i.Node, i.ID, "unloaded", schedulerState(i), nil)
			p.prepares = append(p.prepares, id)
		}
	}
}

func (p *schedulerPlanState) retireOld(c SchedulerCandidate, route string) {
	for _, i := range p.snap.Instances {
		exists := false
		for _, target := range c.Instances {
			if i.ID == target.ID {
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
