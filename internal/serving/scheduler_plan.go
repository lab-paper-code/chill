package serving

import (
	"context"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"gonum.org/v1/gonum/mathext"
	"gonum.org/v1/gonum/stat/distuv"
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
		case !slices.ContainsFunc(snapshot.Options, func(o SchedulerOption) bool { return slices.Contains(o.CompatibleUIDs, d.UID) }):
			state.State, state.Reason = WorkloadMeasurementEvidenceMissing, "NoCompletedMeasurementMatches"
		}
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].UID < states[j].UID })
	return states
}

// SchedulerLowestMode selects the lowest measured unloaded baseline among the
// current and switchable modes, breaking ties by name; empty means no evidence.
func SchedulerLowestMode(baselines []NodeBaseline, node SchedulerNode) string {
	latest := map[string]*NodeBaseline{}
	for j := range baselines {
		b := &baselines[j]
		if b.Node != node.Node || len(b.State) != 0 || b.Scope != schedulerNodeWallScope || b.Meter == "" || !schedulerFinite(b.Watts) || b.Watts < 0 || b.ID == "" || b.Reference.Validate() != nil {
			continue
		}
		if b.PowerMode != node.PowerMode && !slices.Contains(node.SwitchableModes, b.PowerMode) {
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
	return schedulerPhysicalChangeReason(snapshot, n.Node) == "" && !schedulerReservedNode(snapshot, n.Node) && !n.Measurement && !slices.Contains(n.Reasons, NodeExcluded) && !slices.Contains(n.Reasons, NodePowerModeUnavailable) &&
		!slices.ContainsFunc(snapshot.Instances, func(i SchedulerInstance) bool { return i.Node == n.Node })
}

const schedulerNodeUnavailable = "NodeUnavailable"

// schedulerPhysicalChangeReason uses exact node and executor confirmation, not
// serving eligibility. A stopped executor still needs native drain confirmation;
// a terminated executor does not.
func schedulerPhysicalChangeReason(snapshot SchedulerSnapshot, node NodeReference) string {
	index := slices.IndexFunc(snapshot.Nodes, func(n SchedulerNode) bool { return n.Node == node })
	if index < 0 || !snapshot.Nodes[index].Ready {
		return schedulerNodeUnavailable
	}
	if slices.Contains(snapshot.Nodes[index].Reasons, NodeInstanceUnavailable) ||
		slices.ContainsFunc(snapshot.Instances, func(i SchedulerInstance) bool {
			return i.Node == node && !i.Terminated && !i.Ready
		}) {
		return NodeInstanceUnavailable
	}
	if schedulerReplayInitialScope(snapshot).nodes[node] {
		return "ReplayExcludedNode"
	}
	return ""
}

// schedulerCommonNodes preserves already-authorized Fixed power changes. New
// empty-Node savings are comparison candidates, never part of keep.
func schedulerCommonNodes(snapshot SchedulerSnapshot) []SchedulerNode {
	nodes := slices.Clone(snapshot.Nodes)
	for index, n := range nodes {
		for _, a := range snapshot.Actions {
			if a.Fixed && a.Node == n.Node && (a.Status == schedulerActionPlanned || a.Status == phaseRunning) && !schedulerReplayInitialScope(snapshot).nodes[n.Node] {
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

// schedulerSearchPopulation counts the observation window's arrivals and each
// Workload's load. Settings are valid (Validate): the control interval that
// spaces the change-detection boundaries is positive.
func schedulerSearchPopulation(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot) SchedulerSearch {
	search := SchedulerSearch{Bounds: settings, WindowStart: snapshot.ObservationEnd.Add(-settings.ObservationWindow), WindowEnd: snapshot.ObservationEnd, Population: "received events in [windowStart,windowEnd); each Workload's load from its windowStart; validated compute arrivals (including capacity/evidence rejections) replayed; outstanding retained independently"}
	if snapshot.ObservedSince.After(search.WindowStart) {
		search.WindowStart = snapshot.ObservedSince
	}
	inWindow := func(a SchedulerArrival) bool {
		return !a.At.Before(search.WindowStart) && a.At.Before(search.WindowEnd)
	}
	uidOf := func(a SchedulerArrival) string {
		if a.Definition != nil {
			return a.Definition.UID
		}
		return a.WorkloadUID
	}
	times := map[string][]time.Time{} // each Workload's compute arrivals
	for _, a := range snapshot.Arrivals {
		if ctx.Err() != nil {
			return search
		}
		if !inWindow(a) {
			continue
		}
		uid := uidOf(a)
		at := times[uid]
		search.Received++
		if a.Accepted {
			search.Accepted++
		}
		if a.computeInput() {
			search.ComputeInputs++
			at = append(at, a.At)
		}
		times[uid] = at
	}
	keys := make([]string, 0, len(times))
	for key := range times {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	index := make(map[string]int, len(keys))
	for _, key := range keys {
		load := SchedulerLoad{UID: key, WindowStart: search.WindowStart}
		if i := slices.IndexFunc(snapshot.Workloads, func(d *Definition) bool { return d != nil && d.UID == key }); i >= 0 {
			load.WindowStart = schedulerLoadStart(settings, snapshot.Workloads[i], times[key], search.WindowStart, search.WindowEnd, snapshot.LoadChangedAt[key])
		}
		load.Changed = load.WindowStart.After(schedulerLoadStretchStart(search.WindowStart, search.WindowEnd, snapshot.LoadChangedAt[key]))
		index[key] = len(search.Loads)
		search.Loads = append(search.Loads, load)
	}
	for _, a := range snapshot.Arrivals {
		if ctx.Err() != nil {
			return search
		}
		if !inWindow(a) {
			continue
		}
		load := &search.Loads[index[uidOf(a)]]
		if a.At.Before(load.WindowStart) {
			continue
		}
		load.Received++
		if a.Accepted {
			load.Accepted++
		}
		if a.computeInput() {
			load.ComputeInputs++
		}
	}
	for k := range search.Loads {
		load := &search.Loads[k]
		covered := search.WindowEnd.Sub(load.WindowStart)
		if covered <= 0 {
			continue
		}
		load.InputsPerSecond = float64(load.ComputeInputs) / covered.Seconds()
		load.PlannedPerSecond = load.InputsPerSecond
		if i := slices.IndexFunc(snapshot.Workloads, func(d *Definition) bool { return d != nil && d.UID == load.UID }); i >= 0 {
			if p := snapshot.Workloads[i].SLO.Percentile; p > 0 && p < 100 {
				load.PlannedPerSecond = schedulerPlannedRate(load.ComputeInputs, covered.Seconds(), 1-p/100)
			}
		}
	}
	return search
}

// schedulerLoadStart is where Workload d's load starts in the observation
// window [start, end): at the last change of its arrival rate. The stretch
// since the change an earlier decision detected (changed), while that change
// is inside the window, or else the whole window, is tested for the next one.
// The test decides whether the rate changed: it selects the control-interval
// boundary of the stretch after which d's compute arrivals (times) least fit a
// Poisson stream at the stretch's mean rate, when the exact two-sided binomial
// test of the count after it rejects that fit at d's evidence level
// (schedulerEvidenceLevel) shared among the stretch's boundaries. The
// arrivals decide when: the load starts at the instant within one interval of
// the selected boundary that best splits the stretch into two Poisson rates
// (the largest n1·ln(n1/t1) + n2·ln(n2/t2)). Candidates are the instants just
// after an arrival, which closes the first part: a part that starts right
// after an arrival holds whole gaps between arrivals, so its count over its
// length does not overstate its rate, while one that starts at an arrival
// does, without bound as it shortens. Each part is at least the stretch's
// mean gap between arrivals long: a shorter part holds less time than one
// arrival takes on average, so its rate is not judged from it. A stretch of
// one interval or less holds no boundary, so nothing is tested and the
// stretch stands.
func schedulerLoadStart(settings SchedulerSettings, d *Definition, times []time.Time, start, end, changed time.Time) time.Time {
	interval := settings.ControlInterval
	from := schedulerLoadStretchStart(start, end, changed)
	slices.SortFunc(times, time.Time.Compare)
	times = times[sort.Search(len(times), func(i int) bool { return !times[i].Before(from) }):]
	var splits []time.Time
	for split := end.Add(-interval); split.After(from); split = split.Add(-interval) {
		splits = append(splits, split)
	}
	level := schedulerEvidenceLevel(settings, d) / float64(len(splits))
	best, bestP := from, level
	for _, split := range splits {
		after := len(times) - sort.Search(len(times), func(i int) bool { return !times[i].Before(split) })
		if p := binomialTwoSided(len(times), after, end.Sub(split).Seconds()/end.Sub(from).Seconds()); p < bestP {
			best, bestP = split, p
		}
	}
	if best.Equal(from) {
		return best
	}
	lo, hi := best.Add(-interval), best.Add(interval)
	if lo.Before(from) {
		lo = from
	}
	if hi.After(end) {
		hi = end
	}
	poisson := func(n, t float64) float64 { // one part's log-likelihood term
		if n == 0 {
			return 0
		}
		return n * math.Log(n/t)
	}
	span, total := end.Sub(from).Seconds(), float64(len(times))
	gap := span / total // the test rejects only with arrivals
	at, top := best, math.Inf(-1)
	for k := 0; k < len(times); {
		next := k + 1 // arrivals at one instant stay in one part
		for next < len(times) && times[next].Equal(times[k]) {
			next++
		}
		instant := times[k].Add(1)
		t1 := instant.Sub(from).Seconds()
		if instant.After(lo) && instant.Before(hi) && t1 >= gap && span-t1 >= gap {
			if l := poisson(float64(next), t1) + poisson(total-float64(next), span-t1); l > top {
				at, top = instant, l
			}
		}
		k = next
	}
	return at
}

// schedulerLoadStretchStart is where the stretch tested for the next change of
// a Workload's arrival rate starts in the observation window [start, end): at
// the change an earlier decision detected (changed) while it is inside the
// window, else at the window start. A load starting after it is a detected
// change.
func schedulerLoadStretchStart(start, end, changed time.Time) time.Time {
	if changed.After(start) && changed.Before(end) {
		return changed
	}
	return start
}

// binomialTwoSided is the exact two-sided p-value of k successes in n trials
// of probability p: twice the smaller tail, at most 1.
func binomialTwoSided(n, k int, p float64) float64 {
	upper := 1.0 // P(X >= k)
	if k > 0 {
		upper = mathext.RegIncBeta(float64(k), float64(n-k+1), p)
	}
	return min(1, 2*min(binomialLowerTail(n, float64(k), p), upper))
}

// binomialLowerTail is P(X <= k) for X ~ Binomial(n, p), the regularized
// incomplete beta I_{1-p}(n - k, k + 1), exact at whole k. At a fractional k
// it is that beta function's continuous extension of the lower tail, between
// the tails of the whole counts around k; k is not rounded.
func binomialLowerTail(n int, k, p float64) float64 {
	if k >= float64(n) {
		return 1
	}
	return mathext.RegIncBeta(float64(n)-k, k+1, 1-p)
}

// schedulerEvidenceLevel is the significance a verdict on Workload d's
// evidence is held to: the share d's SLO percentile leaves, 1 -
// percentile/100, divided among the decisions that look at one observation
// window, ceil(ObservationWindow / ControlInterval), since each of them tests
// the same arrivals again. Settings are valid (Validate): both are positive.
func schedulerEvidenceLevel(settings SchedulerSettings, d *Definition) float64 {
	return (1 - d.SLO.Percentile/100) / math.Ceil(float64(settings.ObservationWindow)/float64(settings.ControlInterval))
}

// schedulerOnTimeShort reports whether onTime of n requests is significantly
// short of Workload d's SLO percentile p: the one-sided binomial lower tail,
// P(X <= onTime) for X ~ Binomial(n, p) (binomialLowerTail: the exact test
// at a whole onTime, its continuous extension at a fractional one), falls
// below d's evidence level. Discarding a configuration the scheduler decided
// on takes that much evidence; a shortfall the sample cannot tell from chance
// does not.
func schedulerOnTimeShort(settings SchedulerSettings, d *Definition, n int, onTime float64) bool {
	return binomialLowerTail(n, onTime, d.SLO.Percentile/100) < schedulerEvidenceLevel(settings, d)
}

// schedulerPlannedRate is the rate a Workload with n compute arrivals in a
// load window of seconds is planned at: the exact upper confidence limit of
// a Poisson rate at confidence 1 - alpha, χ²(1 - alpha; 2(n + 1)) / 2T. A
// configuration is chosen at the highest rate its evidence allows. No
// arrivals plan nothing.
func schedulerPlannedRate(n int, seconds, alpha float64) float64 {
	if n == 0 {
		return 0
	}
	return distuv.ChiSquared{K: float64(2 * (n + 1))}.Quantile(1-alpha) / (2 * seconds)
}

func schedulerMeasuredOptions(ctx context.Context, snapshot SchedulerSnapshot) []SchedulerOption {
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
	var nativeMaxBatch, nativeMaxQueue int32
	if p.Condition.Firstfloor != nil {
		nativeMaxBatch = p.Condition.Firstfloor.MaxBatchSize
		nativeMaxQueue = p.Condition.Firstfloor.MaxQueueSize
	}
	// Artifact and input contracts are executable semantics, not runtime-image
	// measurement validity. Preserve their option partition.
	return fmt.Sprint(i.Node, p.Condition.Artifact, p.Condition.RuntimeFamily, p.Condition.Backend, p.Condition.Preprocessor, p.Condition.ActiveProtocol.Dataset, p.Condition.PowerMode, p.Condition.Firstfloor != nil, nativeMaxBatch, nativeMaxQueue, p.Condition.RuntimeAccess, p.Condition.PlannedBatchSizes, i.Allocation, i.MaxBatch, i.MaxWait)
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
		parts = append(parts, fmt.Sprint(i.ID, i.Node.UID, schedulerState(i), uids, weights, i.Capacity, i.MaxBatch, i.MaxWait))
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
		// GPU UUIDs are scoped to the selected Node.
		if (a.GPUUUID == "") != (b.GPUUUID == "") {
			return false
		}
		b.GPUUUID = a.GPUUUID
	}
	return reflect.DeepEqual(a, b)
}
func schedulerProfileMatches(d *Definition, p *Profile) bool {
	return d != nil && profilePath(d, p) != nil
}

// PrepareServiceQuantiles builds, when profiles are loaded and off the decision
// path, the quantile tables of each definition's SLO percentile in every
// profile that can serve it. A percentile registered later builds on its first
// query.
func PrepareServiceQuantiles(definitions []*Definition, profiles []Profile) {
	for _, d := range definitions {
		for j := range profiles {
			if schedulerProfileMatches(d, &profiles[j]) {
				for _, b := range profiles[j].Batches {
					b.ServiceDistribution.Prepare(d.SLO.Percentile)
				}
			}
		}
	}
}

func schedulerPlan(settings SchedulerSettings, snap SchedulerSnapshot, c SchedulerCandidate) []Action {
	p := schedulerPlanState{settings: settings, snap: snap, target: &c, actions: []Action{}, nodeTail: map[string]string{}, prepares: []string{}, stops: map[string]string{}}
	p.changeNodes(c)
	p.prepareTargets(c)
	// Candidate ordinals and rolling witness text are not operation identity.
	route := ""
	current := SchedulerCandidate{Nodes: snap.Nodes, Instances: snap.Instances, Routing: snap.Routing}
	if schedulerConfigKey(current) != schedulerConfigKey(c) || !maps.EqualFunc(snap.Routing, c.Routing, maps.Equal[map[string]int32]) {
		route = p.add(schedulerActionRoute, NodeReference{}, "", "", schedulerConfigKey(c), p.prepares)
		if action := &p.actions[len(p.actions)-1]; action.Status == schedulerActionPlanned &&
			(len(p.reasons) != 0 || strings.HasPrefix(action.Reason, NodeInstanceUnavailable+":")) {
			action.Reason = strings.Join(p.reasons, ";")
		}
	}
	if len(p.reasons) == 0 {
		p.retireOld(c, route)
	}
	p.selectCosts()
	return p.actions
}

// selectCosts uses the nearest measured operation: exact cell, Node, device
// class, then configured default. Representation changes never veto a plan.
func (p *schedulerPlanState) selectCosts() {
	modes := map[NodeReference]string{}
	loaded := map[NodeReference][]string{}
	identities := schedulerMeasurementIdentities(p.snap, p.target)
	classes := measurementDeviceClasses(p.snap, p.target)
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
			a.CostProvenance = nil
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
			matches = func(cost OperationCost) bool {
				return measurementEqualState(state, cost.BeforeState, identities) && measurementEqualState(state, cost.TargetState, identities)
			}
			modes[a.Node] = a.To
		default:
			continue
		}
		var selected *OperationCost
		selectedRank := 3
		for j := range p.snap.Costs {
			cost := &p.snap.Costs[j]
			if cost.Kind != a.Kind {
				continue
			}
			if _, _, _, ok := capacityOperationCost(cost); !ok || cost.Reference.Validate() != nil {
				continue
			}
			class := cost.DeviceClass
			if class == "" {
				class = classes[cost.Node.UID]
			}
			rank := measurementRank(cost.Node.UID, class, a.Node.UID, classes[a.Node.UID],
				measurementOperationMatches(*cost, *a, identities) && matches(*cost))
			if rank < 3 && (selected == nil || measurementPreferred(rank, cost.MeasuredAt, cost.ID, selectedRank, selected.MeasuredAt, selected.ID)) {
				selected, selectedRank = cost, rank
			}
		}
		if selected != nil && (a.Status == schedulerActionPlanned || a.Status == "") {
			a.CostID = selected.ID
			a.CostProvenance = &OperationCostProvenance{Source: measurementSource(selectedRank), CostRef: selected.ID}
		}
		cost := schedulerCost(p.snap.Costs, a.CostID)
		_, _, _, measuredValid := capacityOperationCost(cost)
		if a.Status == schedulerActionPlanned || a.Status == "" || !measuredValid && (a.CostProvenance == nil || a.CostProvenance.Source != operationSourceDefault) {
			if mean, _, _, ok := capacityOperationCost(cost); ok {
				a.ExpectedSeconds = &mean
				a.ExpectedSource = &ActionExpectedSource{CostID: cost.ID, Reference: cost.Reference, Statistic: "mean", TrialCount: len(cost.Trials)}
				if a.CostProvenance == nil {
					a.CostProvenance = &OperationCostProvenance{Source: operationSourceNode, CostRef: cost.ID}
				}
			} else {
				value := p.settings.DefaultOperationCost[a.Kind]
				a.ExpectedSource = nil
				a.CostID = ""
				a.ExpectedSeconds = &value.Seconds
				a.CostProvenance = &OperationCostProvenance{Source: operationSourceDefault, Value: &value}
			}
		}
	}
}

// EstimateAction selects a duration using the same measured conditions as planning.
func EstimateAction(settings SchedulerSettings, snapshot SchedulerSnapshot, action Action) Action {
	p := schedulerPlanState{settings: settings, snap: snapshot, actions: []Action{action}}
	p.selectCosts()
	return p.actions[0]
}

type schedulerPlanState struct {
	settings SchedulerSettings
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
			a.CostProvenance = old.CostProvenance
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
		if sameNode && !slices.Contains(action.DependsOn, old.ID) {
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
		if wanted.Terminated && p.target.Routing[wanted.WorkloadUID][wanted.ID] <= 0 {
			continue // Retained occupancy is not a positive destination ACK.
		}
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
	if actual.Profile == nil || wanted.Profile == nil || !actual.Ready || actual.Stopped || actual.Terminated || wanted.Terminated ||
		actual.ID != wanted.ID || actual.Node != wanted.Node ||
		schedulerState(actual) != schedulerState(wanted) {
		return false
	}
	if !schedulerOwnerAllows(actual.OwnerWorkloadUID, wanted.WorkloadUID) {
		return false
	}
	var actualPolicy, wantedPolicy = actual.Profile.Condition.Firstfloor, wanted.Profile.Condition.Firstfloor
	if actualPolicy != nil && wantedPolicy != nil {
		a, w := *actualPolicy, *wantedPolicy
		a.DeadlinePolicy, w.DeadlinePolicy = "", ""
		return actual.Capacity == wanted.Capacity && actual.MaxBatch == wanted.MaxBatch && actual.MaxWait == wanted.MaxWait && reflect.DeepEqual(a, w)
	}
	return actual.Capacity == wanted.Capacity && actual.MaxBatch == wanted.MaxBatch && actual.MaxWait == wanted.MaxWait && reflect.DeepEqual(actualPolicy, wantedPolicy)
}

func schedulerInstanceRouted(routing map[string]map[string]int32, id string) bool {
	for _, weights := range routing {
		if weights[id] > 0 {
			return true
		}
	}
	return false
}

// A Pod-only identity carries no executor to stop or drain. Termination and
// exact Pod identity must both come from the physical snapshot.
func schedulerTerminatedPodOnly(i SchedulerInstance) bool {
	return i.PodUID != "" && i.ID == i.PodUID && i.Stopped && i.Terminated
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
			if reason := schedulerPhysicalChangeReason(p.snap, n.Node); reason != "" {
				p.reasons = appendUnique(p.reasons, fmt.Sprintf("%s:%s:%s", reason, n.Node.Name, n.Node.UID))
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
				n.PowerMode == SchedulerLowestMode(p.snap.Baselines, n)
			reboot := slices.ContainsFunc(oldNode.Reboots, func(fact PowerReboot) bool {
				return fact.From == from && fact.To == n.PowerMode
			})
			deps := []string{}
			drains := map[string]string{}
			for _, i := range p.snap.Instances {
				if i.Node == n.Node {
					if schedulerTerminatedPodOnly(i) {
						remove := p.add(schedulerActionRemove, n.Node, i.ID, schedulerState(i), "unloaded", nil)
						p.stops[i.ID] = remove
						deps = append(deps, remove)
						continue
					}
					st := p.add(schedulerActionStop, n.Node, i.ID, "", "", nil)
					dr := p.add(schedulerActionDrain, n.Node, i.ID, "", "", []string{st})
					p.stops[i.ID] = st
					drains[i.ID] = dr
					deps = append(deps, dr)
				}
			}
			// Observed reboot pairs remove first; unknown/in-place transitions
			// retain the loaded witness until mode/readiness proof.
			if reboot {
				p.removeNodeInstances(n.Node, deps)
			}
			if n.PowerMode != from {
				dep := p.add(schedulerActionPower, n.Node, "", from, n.PowerMode, deps)
				p.actions[len(p.actions)-1].Fixed = emptySaving
				deps = []string{dep}
			}
			if !reboot {
				p.removeNodeInstances(n.Node, deps)
			}
			// Authorized empty lowering runs independently of the target's route.
			needed := slices.ContainsFunc(c.Instances, func(i SchedulerInstance) bool { return i.Node == n.Node })
			if tail := p.nodeTail[n.Node.UID]; tail != "" && (needed || len(drains) != 0 || !p.actions[len(p.actions)-1].Fixed) {
				p.prepares = append(p.prepares, tail)
			}
		}
	}
}

func (p *schedulerPlanState) removeNodeInstances(node NodeReference, deps []string) {
	for _, i := range p.snap.Instances {
		if i.Node == node && !schedulerTerminatedPodOnly(i) {
			p.add(schedulerActionRemove, node, i.ID, schedulerState(i), "unloaded", deps)
		}
	}
}

func (p *schedulerPlanState) prepareTargets(c SchedulerCandidate) {
	for _, wanted := range c.Instances {
		if wanted.Terminated || schedulerReservedNode(p.snap, wanted.Node) {
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
		if reason := schedulerPhysicalChangeReason(p.snap, wanted.Node); reason != "" {
			p.reasons = appendUnique(p.reasons, fmt.Sprintf("%s:%s:%s", reason, wanted.Node.Name, wanted.Node.UID))
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
	return actual.Terminated == wanted.Terminated && (!actual.Terminated || actual.ID == wanted.ID) &&
		(actual.ID == wanted.ID || (actual.TargetID != "" && actual.TargetID == wanted.ID)) &&
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
			overlap := actual.Allocation.GPUUUID != "" && actual.Allocation.GPUUUID == wanted.Allocation.GPUUUID
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
		if schedulerPhysicalChangeReason(p.snap, wanted.Node) != "" {
			return false // Unknown physical ownership is not free coexistence capacity.
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
		return len(ValidateNodeBudget(node, allocations, nil)) == 0
	}
	return false
}

func (p *schedulerPlanState) retirePreparationBlocker(actual SchedulerInstance) {
	if schedulerPhysicalChangeReason(p.snap, actual.Node) != "" {
		return
	}
	for _, old := range p.snap.Actions {
		if old.Kind == schedulerActionRemove && old.Instance == actual.ID && old.Node == actual.Node && old.Status == phaseRunning {
			p.stops[actual.ID] = old.ID
			p.add(old.Kind, old.Node, old.Instance, old.From, old.To, old.DependsOn)
			return
		}
	}
	p.retireInstance(actual, nil)
}

func (p *schedulerPlanState) retireInstance(actual SchedulerInstance, deps []string) {
	if !schedulerTerminatedPodOnly(actual) {
		stop := p.add(schedulerActionStop, actual.Node, actual.ID, "", "", deps)
		drain := p.add(schedulerActionDrain, actual.Node, actual.ID, "", "", []string{stop})
		p.stops[actual.ID] = stop
		deps = []string{drain}
	}
	remove := p.add(schedulerActionRemove, actual.Node, actual.ID, schedulerState(actual), "unloaded", deps)
	if schedulerTerminatedPodOnly(actual) {
		p.stops[actual.ID] = remove
	}
}

func (p *schedulerPlanState) retireOld(c SchedulerCandidate, route string) {
	for _, i := range p.snap.Instances {
		if schedulerReservedNode(p.snap, i.Node) || schedulerPhysicalChangeReason(p.snap, i.Node) != "" {
			continue
		}
		exists := false
		for _, target := range c.Instances {
			if schedulerSamePhysicalTarget(i, target) {
				exists = true
			}
		}
		if !exists && p.stops[i.ID] == "" {
			var deps []string
			if schedulerInstanceRouted(p.snap.Routing, i.ID) {
				deps = []string{route}
			}
			p.retireInstance(i, deps)
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

// WorkloadUIDs is an exclusive whole-workload scope (reset/deletion/manual
// policy writes), not the ownership identity of a node-local autonomous plan.
func schedulerFixedReason(snapshot SchedulerSnapshot, uid string) string {
	for _, reservation := range snapshot.Reservations {
		if slices.Contains(reservation.WorkloadUIDs, uid) {
			return "ReservedScope:" + reservation.Owner
		}
	}
	return ""
}

func fixSchedulerWorkloads(decision *SchedulerDecision, snapshot SchedulerSnapshot) {
	for _, instance := range snapshot.Instances {
		if !instance.Ready && !instance.Stopped && !instance.Terminated {
			reason := "UnconfirmedInstance:" + instance.ID
			if len(instance.Reasons) > 0 {
				reason += ":" + strings.Join(instance.Reasons, ";")
			}
			decision.Reasons = appendUnique(decision.Reasons, reason)
		}
	}
	for index, state := range decision.Workloads {
		if state.State != WorkloadControlled {
			continue
		}
		reason := schedulerFixedReason(snapshot, state.UID)
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
