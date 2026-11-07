package serving

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strings"
)

// bounded reports whether the plan set respects maxReplicas, maxInstances
// and, with replicas OFF, each Workload's current instance count.
func (p *capacityPlanner) bounded(choice []int) bool {
	counts, total := make([]int, len(p.ws)), 0
	for n, j := range choice {
		plan := p.plans[n][j]
		total += plan.others
		for w, pending := range plan.pendingReplicas {
			counts[w] += pending
		}
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

// SchedulerReadyExpansion admits only additive, independently placed serving
// capacity. Callers verify physical identity and native policy ACK first.
// Already-Ready destinations need no preparation chain, so r=0: B=q and
// W=B/κnew, using the same demand, slack and integer weights as the planner.
func SchedulerReadyExpansion(settings SchedulerSettings, snapshot SchedulerSnapshot, rows []CapacityRow, uid string, current, added []SchedulerInstance) (map[string]int32, bool) {
	if len(current) == 0 || len(added) == 0 || !settings.Controls.Routing ||
		settings.ObservationWindow <= 0 || settings.ControlInterval <= 0 {
		return nil, false
	}
	if len(schedulerValidateObservationTimes(&snapshot)) != 0 {
		return nil, false
	}
	decision := SchedulerDecision{
		Search:    schedulerSearchPopulation(context.Background(), settings, snapshot),
		Workloads: []SchedulerWorkloadState{{UID: uid, State: WorkloadControlled}},
	}
	if !slices.ContainsFunc(snapshot.Workloads, func(d *Definition) bool { return d != nil && d.UID == uid }) {
		return nil, false
	}
	p := newCapacityPlanner(context.Background(), settings, snapshot, SchedulerCandidate{}, rows, decision)
	w := p.ws[0]
	nodes := map[NodeReference]bool{}
	items := make([]capacityItem, 0, len(current)+len(added))
	knew, kret := 0.0, 0.0
	for _, group := range [][]SchedulerInstance{current, added} {
		for _, i := range group {
			if i.Terminated || i.Profile == nil || i.WorkloadUID != uid || i.Node.UID == "" || nodes[i.Node] {
				return nil, false
			}
			nodes[i.Node] = true
			// Key the exact destination, not just another option with the same
			// profile: allocation, Node and measured occupancy all affect κ.
			k, row, reason := p.kappa(SchedulerOption{Instance: i}, 0)
			if reason != "" || k <= 0 || math.IsNaN(k) || math.IsInf(k, 0) {
				return nil, false
			}
			knew += k
			items = append(items, capacityItem{id: i.ID, kappa: k, row: row, e: capacityInputEnergy(i)})
		}
		if kret == 0 {
			kret = knew
		}
	}
	// Never share a currently serving Node, including other Workloads.
	for _, i := range added {
		if slices.ContainsFunc(snapshot.Instances, func(old SchedulerInstance) bool {
			return old.Node == i.Node && schedulerInstanceRouted(snapshot.Routing, old.ID)
		}) {
			return nil, false
		}
	}
	j := capacityJudge(w, knew, kret, true, 0)
	if !j.satisfied || knew < kret {
		return nil, false
	}
	slices.SortFunc(items, func(a, b capacityItem) int { return cmp.Or(cmp.Compare(a.e, b.e), cmp.Compare(a.id, b.id)) })
	weights, _, ok := capacityWeights(w.rate, items)
	if !ok {
		return nil, false
	}
	routing := make(map[string]int32, len(items))
	for n, item := range items {
		// Preserve every old source and actually use each admitted addition.
		if weights[n] <= 0 {
			return nil, false
		}
		routing[item.id] = weights[n]
	}
	return routing, true
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

// capacityWeights minimizes the largest integer weight/κ ratio, independently
// of request or backlog demand. It starts at floor(schedulerRouteUnits*κ/Σκ)
// and gives each remaining unit to the smallest next weight/κ (index breaks
// ties). Only a zero κ sum uses equal shares. y follows the integer weights;
// ok is false when any share of rate exceeds its κ.
func capacityWeights(rate float64, items []capacityItem) ([]int32, []float64, bool) {
	n := len(items)
	if n == 0 {
		return nil, nil, true
	}
	total := 0.0
	for _, item := range items {
		total += item.kappa
	}
	weights, y := make([]int32, n), make([]float64, n)
	sum := int32(0)
	for k, item := range items {
		share := 1 / float64(n)
		if total > 0 {
			share = item.kappa / total
		}
		weights[k] = int32(math.Floor(share * schedulerRouteUnits))
		sum += weights[k]
	}
	for ; sum < schedulerRouteUnits; sum++ {
		to, next := -1, 0.0
		for k, item := range items {
			kappa := item.kappa
			if total == 0 {
				kappa = 1
			}
			if kappa <= 0 {
				continue
			}
			if ratio := float64(weights[k]+1) / kappa; to < 0 || ratio < next {
				to, next = k, ratio
			}
		}
		weights[to]++
	}
	ok := true
	for k, weight := range weights {
		y[k] = rate * float64(weight) / schedulerRouteUnits
		ok = ok && y[k] <= items[k].kappa*(1+1e-12)
	}
	return weights, y, ok
}

// capacityResult is one plan set evaluated exactly: per-Workload judgments,
// weights and the analytic energy over [0, T].
type capacityResult struct {
	choice             []int
	route              float64
	keep               bool
	key                string
	joules             float64
	period, keepJoules float64
	worst              float64
	excesses           []float64 // per-Workload worst exceedances, descending
	feasible           bool
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
	bridge            *schedulerBridgeSchedule
}

// evaluate judges the plan set (one plan index per Node) at route r and sums
// its compared Node energy and request energy: [0, r] by the retained
// instances up to κ_ret at their current weights, the overflow
// max(0, λ − κ_ret)·r and [r, T] by the target integer-weight load y.
func (p *capacityPlanner) evaluate(choice []int, r float64) capacityResult {
	bridge, bridgeReason := p.bridgeSchedule(choice)
	if bridge != nil {
		r = bridge.route
	}
	if bridgeReason != "" {
		res := capacityResult{choice: choice, route: r, bridge: bridge, exclusion: bridgeReason, worst: math.Inf(1)}
		res.excesses = make([]float64, len(p.ws))
		for w := range res.excesses {
			res.excesses[w] = math.Inf(1)
		}
		return res
	}
	return p.evaluateAtRoute(choice, r, bridge)
}

func (p *capacityPlanner) evaluateAtRoute(choice []int, r float64, bridge *schedulerBridgeSchedule) capacityResult {
	res := p.evaluateAtWindow(choice, r, bridge, p.period)
	res.keepJoules = p.keepWatts * p.period
	if res.keep {
		res.keepJoules = res.joules
	}
	return res
}

func (p *capacityPlanner) evaluateAtWindow(choice []int, r float64, bridge *schedulerBridgeSchedule, t float64) capacityResult {
	res := capacityResult{choice: choice, route: r, keep: true, bridge: bridge, period: t}
	keys := make([]string, len(choice))
	items, retained := make([][]capacityItem, len(p.ws)), make([][]capacityItem, len(p.ws))
	for n, j := range choice {
		plan := p.plans[n][j]
		res.keep = res.keep && plan.keep
		keys[n] = p.snapshot.Nodes[n].Node.UID + "=" + plan.record.Key
		if p.compared[n] {
			res.joules += bridge.nodeJoules(plan, n, r, t)
		}
		for w := range p.ws {
			items[w] = append(items[w], plan.items[w]...)
			retained[w] = append(retained[w], plan.retained[w]...)
		}
	}
	res.feasible, res.final, res.worst = true, true, math.Inf(-1)
	res.excesses = make([]float64, len(p.ws))
	for w, ws := range p.ws {
		slices.SortFunc(items[w], func(a, b capacityItem) int { return cmp.Or(cmp.Compare(a.e, b.e), cmp.Compare(a.id, b.id)) })
		kret, weightSum := 0.0, 0.0
		rows := []string{}
		for _, item := range items[w] {
			rows = append(rows, item.row)
		}
		for _, item := range retained[w] {
			kret += item.kappa
			weightSum += float64(item.weight)
		}
		demand, slow, _, protectedRate := p.admissionDemand(ws, choice, kret)
		var weights []int32
		var y []float64
		var ok bool
		if !p.settings.Controls.Routing {
			weights, y, ok = capacityFixedWeights(protectedRate, items[w])
		} else {
			weights, y, ok = capacityWeights(protectedRate, items[w])
		}
		capacityEnergyLoad(y, ws.rate, protectedRate)
		units := 0.0
		for _, weight := range weights {
			units += float64(weight)
		}
		routes := p.weightRoutes(&res, items[w], weights)
		knew, marginOK := capacityPositiveSupply(items[w], weights, protectedRate, demand.rate)
		ok = ok && marginOK
		j := capacityJudge(demand, knew, kret, len(routes) > 0, r)
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
					excess = demand.rate*float64(weights[k])/units/item.kappa - 1
				}
				worst = max(worst, excess)
			}
		}
		if slow && !j.satisfied {
			res.exclusion = cmp.Or(res.exclusion, schedulerReductionCapacityInsufficient)
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
		if bridge != nil {
			res.joules += p.bridgeRequestJoules(bridge, ws, mean, r, t)
		} else {
			before := min(r, t)
			res.joules += initial*before + max(0, ws.rate-kret)*before*mean + (served*mean+max(0, ws.rate-served)*mean)*max(0, t-r)
		}
		res.judgments = append(res.judgments, j)
		res.weights = append(res.weights, routes)
		res.rows = append(res.rows, rows)
		res.sums = append(res.sums, [2]float64{knew, kret})
	}
	reason, reserves := p.transitionConstraints(choice)
	res.exclusion, res.reserves = cmp.Or(res.exclusion, reason), reserves
	slices.SortFunc(res.excesses, func(a, b float64) int { return cmp.Compare(b, a) })
	res.key = strings.Join(keys, "|")
	return res
}

// keepInsufficient uses the same observation and positive physical route supply.
// It grants no safety exception: final backlog, bridge and reserve checks remain.
func (p *capacityPlanner) keepInsufficient(w capacityWorkload) bool {
	kappa := 0.0
	for _, i := range p.snapshot.Instances {
		if i.WorkloadUID == w.d.UID && i.Ready && !i.Stopped && i.Routable[w.d.UID] && schedulerInstanceRouted(p.keep.Routing, i.ID) {
			k, _ := p.currentKappa(i)
			kappa += k
		}
	}
	j := capacityJudge(w, kappa, kappa, kappa > 0, 0)
	return !j.satisfied
}

// weightRoutes maps each item with a positive weight to that weight and
// marks the result when the weights leave a new or protected routed instance at 0.
func (p *capacityPlanner) weightRoutes(res *capacityResult, items []capacityItem, weights []int32) map[string]int32 {
	routes := map[string]int32{}
	for k, item := range items {
		if weights[k] > 0 {
			routes[item.id] = weights[k]
			continue
		}
		res.unroutedPrepare = res.unroutedPrepare || item.prepare
		res.unroutes = res.unroutes || p.partial && schedulerInstanceRouted(p.keep.Routing, item.id)
	}
	return routes
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
	if a.joules-a.keepJoules != b.joules-b.keepJoules {
		return a.joules-a.keepJoules < b.joules-b.keepJoules
	}
	if a.keep != b.keep {
		return a.keep
	}
	return a.key < b.key
}

// capacityPositiveSupply checks the actual integer route, not unrouted reserves.
func capacityPositiveSupply(items []capacityItem, weights []int32, protected, demand float64) (float64, bool) {
	kappa, units, allowed := 0.0, int32(0), true
	for _, weight := range weights {
		units += weight
	}
	for k, item := range items {
		if weights[k] > 0 {
			kappa += item.kappa
			if protected > demand && (units == 0 || float64(weights[k])*protected/float64(units) > item.kappa*(1+1e-12)) {
				allowed = false
			}
		}
	}
	return kappa, allowed
}
