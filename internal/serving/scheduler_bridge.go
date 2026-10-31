package serving

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"math"
	"slices"
	"time"
)

const schedulerBridgeRoute = "handoff-bridge"

// SchedulerBridgeStage is a prediction conditional on its prepare/ACK
// dependencies, never a claim that a planned executor is already live supply.
type SchedulerBridgeStage struct {
	Action         string        `json:"action"`
	UID            string        `json:"uid"`
	At             time.Duration `json:"atNanos"`
	Demand         float64       `json:"demand"`
	Backlog        float64       `json:"backlog"`
	PredictedKappa float64       `json:"predictedKappa"`
	WaitSeconds    float64       `json:"waitSeconds"`
	Reserve        float64       `json:"reserveFraction"`
}

type schedulerBridgeSchedule struct {
	actions   []Action
	instances map[string]SchedulerInstance
	stages    []SchedulerBridgeStage
	delays    []float64
	route     float64
}

// This is a planning-only topological frontier, not another runtime lifecycle.
// Only original physical blockers retire; bridges belong to the frozen goal.
type schedulerBridgeCompiler struct {
	p           *capacityPlanner
	choice      []int
	goal        SchedulerCandidate
	out         *schedulerBridgeSchedule
	before      map[string]bool
	ends        map[string]float64
	blocked     map[string]string // stop action -> Workload UID awaiting ACK
	retiring    map[string]bool   // physical executors stopped before final route
	unavailable map[string]bool
	prepares    map[string]string // future executor -> prepare action
	routing     map[string]map[string]int32
	previous    string
}

// ponytail: only final-goal prepares and canonical greedy handoffs; temporary
// variants or backtracking require evidence that this bounded search misses recovery.
func (p *capacityPlanner) bridgeSchedule(choice []int) (*schedulerBridgeSchedule, string) {
	needed := false
	for n, j := range choice {
		needed = needed || len(p.plans[n][j].record.RequiresBridge) > 0
	}
	if !needed {
		return nil, ""
	}
	if !p.settings.Controls.Routing {
		return nil, schedulerRoutedStopsFirst
	}
	// Preserve the global slow-window veto before allocating a DAG for a
	// steady-state capacity deficit that no handoff could repair.
	for _, w := range p.ws {
		demand, slow := p.reductionDemand(w, choice)
		if !slow {
			continue
		}
		kappa := 0.0
		for n, j := range choice {
			kappa += p.plans[n][j].record.Kappa[w.d.UID]
		}
		if kappa < demand.rate {
			return nil, schedulerWindowCapacityInsufficient
		}
	}
	b := schedulerBridgeCompiler{p: p, choice: choice,
		out:    &schedulerBridgeSchedule{instances: map[string]SchedulerInstance{}, delays: make([]float64, len(choice))},
		goal:   SchedulerCandidate{Nodes: slices.Clone(p.keep.Nodes), Routing: p.keep.Routing},
		before: map[string]bool{}, ends: map[string]float64{}, blocked: map[string]string{}, retiring: map[string]bool{},
		unavailable: map[string]bool{}, prepares: map[string]string{}, routing: map[string]map[string]int32{}}
	final, reason := b.initialize()
	if reason != "" {
		return nil, reason
	}
	if reason = b.physicalBudget(); reason != "" {
		return nil, reason
	}
	return b.compile(final)
}

func (b *schedulerBridgeCompiler) initialize() (string, string) {
	p, choice := b.p, b.choice
	for uid, weights := range p.keep.Routing {
		b.routing[uid] = maps.Clone(weights)
	}
	for _, i := range p.snapshot.Instances {
		b.out.instances[i.ID] = i
	}
	for n, j := range choice {
		plan := p.plans[n][j]
		projected, known := b.goal.Nodes[n].AtMode(plan.mode)
		if !known {
			return "", NodePlacementInventoryMissing
		}
		b.goal.Nodes[n] = projected
		b.goal.Instances = append(b.goal.Instances, plan.instances...)
		for _, i := range plan.instances {
			if _, current := b.out.instances[i.ID]; !current {
				b.out.instances[i.ID] = i
			}
		}
	}
	b.out.actions = schedulerPlan(p.snapshot, b.goal)
	byID, final := map[string]int{}, ""
	for k, a := range b.out.actions {
		byID[a.ID] = k
		if a.Kind == schedulerActionRoute {
			final = a.ID
		}
		if a.Kind == schedulerActionPrepare {
			b.prepares[a.Instance] = a.ID
		}
	}
	var visit func(string)
	visit = func(id string) {
		for _, dep := range b.out.actions[byID[id]].DependsOn {
			if !b.before[dep] {
				b.before[dep] = true
				visit(dep)
			}
		}
	}
	if final == "" {
		return "", schedulerRoutedStopsFirst
	}
	visit(final)
	for _, a := range b.out.actions {
		if !b.before[a.ID] || a.Kind != schedulerActionStop {
			continue
		}
		b.retiring[a.Instance] = true
		source := b.out.instances[a.Instance]
		if source.Stopped || !schedulerInstanceRouted(p.keep.Routing, a.Instance) {
			continue
		}
		uid := source.WorkloadUID
		if _, controlled := p.index[uid]; !controlled {
			return "", schedulerRoutedStopsFirst
		}
		for other, weights := range p.keep.Routing {
			if other != uid && weights[a.Instance] > 0 {
				return "", schedulerRoutedStopsFirst
			}
		}
		b.blocked[a.ID] = uid
	}
	return final, ""
}

func (b *schedulerBridgeCompiler) physicalBudget() string {
	p := b.p
	// A preparation still occupies a physical slot until exact removal. Bound
	// the peak conservatively, without borrowing unobserved future retirement.
	counts := map[string]int{}
	for _, i := range p.snapshot.Instances {
		uid := i.WorkloadUID
		if uid == "" {
			uid = i.OwnerWorkloadUID
		}
		counts[uid]++
	}
	for id := range b.prepares {
		counts[b.out.instances[id].WorkloadUID]++
	}
	uncreated := 0
	for _, node := range p.snapshot.Nodes {
		uncreated += schedulerUncreatedReservations(p.snapshot, node.Node)
	}
	if len(p.snapshot.Instances)+len(b.prepares)+uncreated > p.settings.MaxInstances {
		return schedulerPlanInfeasibleResources
	}
	for uid, count := range counts {
		if _, controlled := p.index[uid]; controlled && count > p.settings.MaxReplicas {
			return schedulerPlanInfeasibleResources
		}
	}
	return ""
}

func (b *schedulerBridgeCompiler) compile(final string) (*schedulerBridgeSchedule, string) {
	p, choice := b.p, b.choice
	for len(b.blocked) > 0 {
		b.resolve()
		advanced := false
		for _, w := range p.ws {
			pending := false
			for _, uid := range b.blocked {
				pending = pending || uid == w.d.UID
			}
			if pending && b.handoff(w) {
				advanced = true
				break
			}
		}
		if !advanced {
			return nil, schedulerRoutedStopsFirst
		}
	}
	b.resolve()
	for _, a := range b.out.actions {
		if b.before[a.ID] {
			if _, resolved := b.ends[a.ID]; !resolved {
				return nil, schedulerRoutedStopsFirst
			}
		}
	}
	for _, dep := range b.out.actions[slices.IndexFunc(b.out.actions, func(a Action) bool { return a.ID == final })].DependsOn {
		b.out.route = max(b.out.route, b.ends[dep])
	}
	for n := range choice {
		start := math.Inf(1)
		for _, a := range b.out.actions {
			if !b.before[a.ID] || a.Node != p.snapshot.Nodes[n].Node || a.Status == schedulerActionComplete {
				continue
			}
			at := 0.0
			for _, dep := range a.DependsOn {
				at = max(at, b.ends[dep])
			}
			start = min(start, at)
		}
		if !math.IsInf(start, 1) {
			b.out.delays[n] = start
		}
	}
	return b.out, ""
}

// resolve schedules only dependency-proven actions; a routed resource blocker
// cannot enter this frontier until handoff attaches an actual ACK dependency.
func (b *schedulerBridgeCompiler) resolve() {
	for {
		progress := false
		for _, a := range b.out.actions {
			_, blocked := b.blocked[a.ID]
			if !b.before[a.ID] || blocked {
				continue
			}
			if _, done := b.ends[a.ID]; done {
				continue
			}
			at, ready := 0.0, true
			for _, dep := range a.DependsOn {
				end, known := b.ends[dep]
				ready = ready && known
				at = max(at, end)
			}
			if !ready {
				continue
			}
			if a.Status != schedulerActionComplete && (a.Kind == schedulerActionPrepare || a.Kind == schedulerActionRemove || a.Kind == schedulerActionPower) {
				_, longest, _, valid := capacityOperationCost(schedulerCost(b.p.snapshot.Costs, a.CostID))
				if !valid {
					continue
				}
				at += longest
			}
			b.ends[a.ID] = at
			if a.Kind == schedulerActionStop || a.Kind == schedulerActionRemove {
				b.unavailable[a.Instance] = true
			}
			progress = true
		}
		if !progress {
			return
		}
	}
}

func (b *schedulerBridgeCompiler) handoff(w capacityWorkload) bool {
	uid := w.d.UID
	available, items := b.peers(uid)
	kappa := 0.0
	for _, item := range items {
		kappa += item.kappa
	}
	if kappa <= 0 {
		return false
	}
	at, deps := 0.0, []string{}
	if b.previous != "" {
		at, deps = b.ends[b.previous], append(deps, b.previous)
	}
	for _, item := range items {
		if prepare := b.prepares[item.id]; prepare != "" {
			deps, at = appendUnique(deps, prepare), max(at, b.ends[prepare])
		}
	}
	demand, _ := b.p.reductionDemand(w, b.choice)
	before, served := b.p.bridgeFlow(uid, demand.rate, b.routing[uid], b.out.instances)
	backlog := float64(w.queued) + max(0, demand.rate-served)*at
	if demand.slack <= 0 || kappa < demand.rate || backlog/kappa > demand.slack ||
		b.p.settings.TransitionReserveFraction > 0 && (before < demand.rate*(1+b.p.settings.TransitionReserveFraction) || kappa < demand.rate*(1+b.p.settings.TransitionReserveFraction)) {
		return false
	}
	weights, _, ok := capacityWeights(demand.rate, items)
	if !ok {
		return false
	}
	stage := SchedulerCandidate{Nodes: b.goal.Nodes, Routing: map[string]map[string]int32{}}
	for other, route := range b.routing {
		stage.Routing[other] = maps.Clone(route)
	}
	stage.Routing[uid] = map[string]int32{}
	for k, item := range items {
		if weights[k] > 0 {
			stage.Routing[uid][item.id] = weights[k]
		}
	}
	for _, i := range available {
		stage.Instances = append(stage.Instances, i)
	}
	slices.SortFunc(stage.Instances, func(a, c SchedulerInstance) int { return cmp.Compare(a.ID, c.ID) })
	sum := sha256.Sum256([]byte(schedulerConfigKey(stage)))
	stage.ID = "bridge-" + hex.EncodeToString(sum[:6])
	slices.Sort(deps)
	action := Action{ID: stage.ID, Kind: schedulerActionRoute, From: schedulerBridgeRoute, To: schedulerConfigKey(stage), DependsOn: deps, Status: schedulerActionPlanned, RouteTarget: &stage}
	for k := range b.out.actions {
		if b.blocked[b.out.actions[k].ID] == uid {
			b.out.actions[k].DependsOn = appendUnique(slices.Clone(b.out.actions[k].DependsOn), action.ID)
			delete(b.blocked, b.out.actions[k].ID)
		}
	}
	final := slices.IndexFunc(b.out.actions, func(a Action) bool { return a.Kind == schedulerActionRoute && a.RouteTarget == nil })
	b.out.actions = slices.Insert(b.out.actions, final, action)
	b.before[action.ID], b.ends[action.ID] = true, at
	b.routing, b.previous = stage.Routing, action.ID
	b.out.stages = append(b.out.stages, SchedulerBridgeStage{Action: action.ID, UID: uid, At: time.Duration(at * 1e9), Demand: demand.rate, Backlog: backlog, PredictedKappa: kappa, WaitSeconds: backlog / kappa, Reserve: b.p.settings.TransitionReserveFraction})
	return true
}

func (b *schedulerBridgeCompiler) peers(uid string) (map[string]SchedulerInstance, []capacityItem) {
	available, peers := map[string]SchedulerInstance{}, map[NodeReference]SchedulerInstance{}
	for _, i := range b.p.snapshot.Instances {
		if i.Ready && !i.Stopped && !i.Terminated && !b.unavailable[i.ID] {
			available[i.ID] = i
		}
	}
	// Goal instances are preferred to retirees on the same Node; κ from two
	// co-located executors is never added, even during the bridge.
	for _, i := range b.goal.Instances {
		if prepare := b.prepares[i.ID]; prepare != "" {
			if _, ready := b.ends[prepare]; ready {
				available[i.ID] = i
			}
		}
	}
	compatible := func(i SchedulerInstance) bool {
		if i.WorkloadUID != uid || b.retiring[i.ID] || schedulerReservedNode(b.p.snapshot, i.Node) {
			return false
		}
		for other, weights := range b.routing {
			if other == uid {
				continue
			}
			for id, weight := range weights {
				if weight > 0 && b.out.instances[id].Node == i.Node {
					return false
				}
			}
		}
		return !slices.ContainsFunc(b.p.snapshot.Outstanding, func(r SchedulerRequest) bool {
			return r.Phase == phasePending && r.Definition != nil && r.Definition.UID == uid && !schedulerProfileMatches(r.Definition, i.Profile)
		})
	}
	for _, i := range b.goal.Instances {
		if actual, ready := available[i.ID]; ready && compatible(actual) {
			peers[i.Node] = actual
		}
	}
	for _, i := range b.p.snapshot.Instances {
		if _, ready := available[i.ID]; ready && peers[i.Node].ID == "" && compatible(i) {
			peers[i.Node] = i
		}
	}
	items := []capacityItem{}
	for _, i := range peers {
		k, row := b.p.currentKappa(i)
		if k > 0 {
			items = append(items, capacityItem{id: i.ID, kappa: k, row: row, e: capacityInputEnergy(i)})
		}
	}
	slices.SortFunc(items, func(a, c capacityItem) int { return cmp.Compare(a.id, c.id) })
	return available, items
}

func (p *capacityPlanner) bridgeFlow(uid string, rate float64, routing map[string]int32, instances map[string]SchedulerInstance) (float64, float64) {
	units, kappa, served := 0.0, 0.0, 0.0
	for _, weight := range routing {
		units += float64(max(0, weight))
	}
	if units == 0 {
		return 0, 0
	}
	for id, weight := range routing {
		if weight <= 0 {
			continue
		}
		i := instances[id]
		if i.WorkloadUID != uid {
			continue
		}
		k, _ := p.currentKappa(i)
		kappa += k
		served += min(k, rate*float64(weight)/units)
	}
	return kappa, served
}

func (p *capacityPlanner) bridgeRequestJoules(b *schedulerBridgeSchedule, w capacityWorkload, finalMean, route float64) float64 {
	power := func(weights map[string]int32) float64 {
		units, served, watts := 0.0, 0.0, 0.0
		for _, weight := range weights {
			units += float64(max(0, weight))
		}
		for id, weight := range weights {
			if weight <= 0 || units == 0 {
				continue
			}
			i := b.instances[id]
			k, _ := p.currentKappa(i)
			flow := min(k, w.rate*float64(weight)/units)
			served, watts = served+flow, watts+flow*capacityInputEnergy(i)
		}
		return watts + max(0, w.rate-served)*finalMean
	}
	at, joules, weights := 0.0, 0.0, p.keep.Routing[w.d.UID]
	for _, stage := range b.stages {
		if stage.UID != w.d.UID {
			continue
		}
		until := stage.At.Seconds()
		joules += power(weights) * (until - at)
		at = until
		for _, action := range b.actions {
			if action.ID == stage.Action {
				weights = action.RouteTarget.Routing[w.d.UID]
				break
			}
		}
	}
	return joules + power(weights)*(route-at) + w.rate*finalMean*(p.period-route)
}

func (b *schedulerBridgeSchedule) nodeJoules(plan *capacityPlan, n int, r, t float64) float64 {
	joules := plan.energy(r, t)
	if b != nil && !plan.keep {
		joules += (plan.before - plan.mid) * b.delays[n]
	}
	return joules
}

func (b *schedulerBridgeSchedule) actionsFor(snapshot SchedulerSnapshot, target SchedulerCandidate) []Action {
	if b == nil {
		return schedulerPlan(snapshot, target)
	}
	actions := slices.Clone(b.actions)
	for k := range actions {
		if actions[k].Kind == schedulerActionRoute && actions[k].RouteTarget == nil {
			actions[k].To = schedulerConfigKey(target)
		}
	}
	return actions
}
