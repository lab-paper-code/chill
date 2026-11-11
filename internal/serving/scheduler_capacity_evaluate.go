package serving

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strings"
	"sync"
)

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

// SchedulerReadyExpansion admits independently placed, already-Ready peers.
// Physical identity/policy ACK remain caller obligations. SLO assessment uses
// the same finite-demand replay and tuple ordering as exact planner finalists.
func SchedulerReadyExpansion(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot, uid string, current, added []SchedulerInstance) (map[string]int32, bool) {
	if ctx.Err() != nil || len(current) == 0 || len(added) == 0 || !settings.Controls.Routing || settings.ObservationWindow <= 0 || settings.ControlInterval <= 0 || len(schedulerValidateObservationTimes(&snapshot)) != 0 {
		return nil, false
	}
	var definition *Definition
	for _, d := range snapshot.Workloads {
		if d != nil && d.UID == uid {
			definition = d
			break
		}
	}
	if definition == nil {
		return nil, false
	}
	inputs, ok := schedulerReadyExpansionInputs(settings, snapshot, uid, current, added)
	if !ok {
		return nil, false
	}
	weights, err := BuildFeedbackRouting(inputs, true)
	if err != nil {
		return nil, false
	}
	routing := map[string]int32{}
	for k, input := range inputs {
		if weights[k] <= 0 {
			return nil, false
		}
		routing[input.InstanceID] = weights[k]
	}
	keep := SchedulerCandidate{Keep: true, Nodes: snapshot.Nodes, Instances: snapshot.Instances, Routing: snapshot.Routing}
	target := keep
	target.Keep = false
	target.Instances = slices.Clone(snapshot.Instances)
	for _, i := range added {
		if at := slices.IndexFunc(target.Instances, func(old SchedulerInstance) bool { return old.ID == i.ID }); at >= 0 {
			target.Instances[at] = i
		} else {
			target.Instances = append(target.Instances, i)
		}
	}
	target.Routing = map[string]map[string]int32{}
	for other, route := range snapshot.Routing {
		target.Routing[other] = route
	}
	target.Routing[uid] = routing
	target.Actions = []Action{{ID: "ready-expansion-route", Kind: schedulerActionRoute}}
	if len(target.Instances) > len(snapshot.Instances) {
		snapshot.Instances = slices.Concat(snapshot.Instances, target.Instances[len(snapshot.Instances):])
	}
	a, err := EvaluateFeedbackCandidate(ctx, settings, snapshot, keep, 0)
	if err != nil || len(a.InvalidReasons) > 0 {
		return nil, false
	}
	b, err := EvaluateFeedbackCandidate(ctx, settings, snapshot, target, 0)
	if err != nil || len(b.InvalidReasons) > 0 {
		return nil, false
	}
	p := newCapacityPlanner(ctx, settings, snapshot, keep, SchedulerDecision{Search: schedulerSearchPopulation(ctx, settings, snapshot), Workloads: []SchedulerWorkloadState{{UID: uid, State: WorkloadControlled}}})
	old, next := capacityResult{feedback: a.Workloads}, capacityResult{feedback: b.Workloads}
	p.rankFeedback(&old)
	p.rankFeedback(&next)
	if old.feasible && !next.feasible || !old.feasible && slices.Compare(next.excesses, old.excesses) > 0 {
		return nil, false
	}
	return routing, ctx.Err() == nil
}

func schedulerReadyExpansionInputs(settings SchedulerSettings, snapshot SchedulerSnapshot, uid string, current, added []SchedulerInstance) ([]FeedbackRouteInput, bool) {
	nodes := map[NodeReference]bool{}
	instances := append(slices.Clone(current), added...)
	inputs := make([]FeedbackRouteInput, len(instances))
	for k, i := range instances {
		if i.Terminated || !i.Ready || i.Stopped || i.Profile == nil || i.WorkloadUID != uid || i.Node.UID == "" || nodes[i.Node] {
			return nil, false
		}
		node := slices.IndexFunc(snapshot.Nodes, func(node SchedulerNode) bool { return node.Node == i.Node })
		if node < 0 || !snapshot.Nodes[node].Ready || !schedulerOwnerAllows(i.OwnerWorkloadUID, uid) || !i.Routable[uid] ||
			len(schedulerValidateInstance(settings, snapshot.Nodes[node], i, nil)) > 0 {
			return nil, false
		}
		allocations := append([]SchedulerAllocation{snapshot.Nodes[node].Reserved}, snapshot.Nodes[node].NonServing...)
		for _, actual := range snapshot.Instances {
			if actual.Node == i.Node && actual.ID != i.ID {
				allocations = append(allocations, actual.Allocation)
			}
		}
		allocations = append(allocations, i.Allocation)
		if len(ValidateNodeBudget(snapshot.Nodes[node], allocations, nil)) > 0 {
			return nil, false
		}
		nodes[i.Node] = true
		inputs[k] = FeedbackRouteInput{InstanceID: i.ID, Profile: i.Profile, MaxBatch: i.MaxBatch, ExistingWeight: i.Weight}
	}
	for _, i := range added {
		if slices.ContainsFunc(snapshot.Instances, func(old SchedulerInstance) bool {
			return old.Node == i.Node && schedulerInstanceRouted(snapshot.Routing, old.ID)
		}) {
			return nil, false
		}
	}
	slices.SortFunc(inputs, func(a, b FeedbackRouteInput) int {
		energy := func(id string) float64 {
			for _, i := range instances {
				if i.ID == id {
					return capacityInputEnergy(i)
				}
			}
			return 0
		}
		return cmp.Or(cmp.Compare(energy(a.InstanceID), energy(b.InstanceID)), cmp.Compare(a.InstanceID, b.InstanceID))
	})
	return inputs, true
}

// capacityWeights allocates actual integer routes from positive profile weights.
// Offered shares may exceed those weights: replay, not this allocator, judges SLO.
func capacityWeights(rate float64, items []capacityItem) ([]int32, []float64, bool) {
	weights, load := make([]int32, len(items)), make([]float64, len(items))
	total := 0.0
	for _, item := range items {
		total += float64(item.routingWeight)
	}
	if len(items) == 0 {
		return weights, load, true
	}
	if total <= 0 {
		return weights, load, false
	}
	sum := int32(0)
	for k, item := range items {
		weights[k] = int32(math.Floor(schedulerRouteUnits * float64(item.routingWeight) / total))
		sum += weights[k]
	}
	for ; sum < schedulerRouteUnits; sum++ {
		to, next := -1, 0.0
		for k, item := range items {
			if item.routingWeight <= 0 {
				continue
			}
			if ratio := float64(weights[k]+1) / float64(item.routingWeight); to < 0 || ratio < next {
				to, next = k, ratio
			}
		}
		weights[to]++
	}
	for k, weight := range weights {
		load[k] = rate * float64(weight) / schedulerRouteUnits
	}
	return weights, load, true
}
func capacityFixedWeights(rate float64, items []capacityItem) ([]int32, []float64, bool) {
	weights, load := make([]int32, len(items)), make([]float64, len(items))
	total := 0.0
	for k, item := range items {
		weights[k] = item.weight
		total += float64(item.weight)
	}
	for k := range items {
		if total > 0 {
			load[k] = rate * float64(weights[k]) / total
		}
	}
	return weights, load, len(items) == 0 || total > 0
}

type capacityResult struct {
	choice                            []int
	route                             float64
	keep                              bool
	key                               string
	joules, period, keepJoules, worst float64
	excesses                          []float64 // ordered flattened exact Workload tuples, never heuristic E2E
	feasible, final, weightsExceeded  bool
	unroutedPrepare, unroutes         bool
	weights                           []map[string]int32
	sums                              [][2]float64 // optimistic profile routing rates, not tested capacity
	feedback                          []FeedbackWorkloadScore
	slo                               []SchedulerEstimate
	conditional                       *ConditionalServiceEvidence
	assumptions, sources              []string
	unpaid                            []string
	withinUncertainty                 bool
	gainUncertainty                   float64
	exclusion                         string
	reserves                          []SchedulerTransitionReserve
	bridge                            *schedulerBridgeSchedule
}

type capacityFeedbackMemo struct {
	once   sync.Once
	result FeedbackCandidateResult
	err    error
}
type capacityArrivalMemo struct {
	once   sync.Once
	cohort *feedbackArrivalCohort
	err    error
}

type capacityReplayKey struct {
	config, plans string
	route         float64
}

func (p *capacityPlanner) evaluate(choice []int, r float64) capacityResult {
	bridge, reason := p.bridgeSchedule(choice)
	if bridge != nil {
		r = bridge.route
	}
	if reason != "" {
		return capacityResult{choice: choice, route: r, bridge: bridge, exclusion: reason, worst: math.Inf(1)}
	}
	res := p.evaluateAtRoute(choice, r, bridge)
	if slices.IndexFunc(choice, func(j int) bool { return j != 0 }) < 0 {
		actual := p.evaluateCurrentKeep(p.periodSeconds)
		if !res.better(&actual) {
			return actual
		}
	}
	return res
}
func (p *capacityPlanner) evaluateAtRoute(choice []int, r float64, bridge *schedulerBridgeSchedule) capacityResult {
	res := p.evaluateAtWindow(choice, r, bridge, p.periodSeconds)
	res.keepJoules = p.keepWatts * p.periodSeconds
	if res.keep {
		res.keepJoules = res.joules
	}
	return res
}
func (p *capacityPlanner) evaluateAtWindow(choice []int, r float64, bridge *schedulerBridgeSchedule, t float64) capacityResult {
	return p.assessResult(p.evaluateEnergyAtWindow(choice, r, bridge, t))
}
func (p *capacityPlanner) evaluateCurrentKeep(t float64) capacityResult {
	res := p.assessResult(p.evaluateEnergyForRoutes(make([]int, len(p.plans)), 0, nil, t, p.keep.Routing))
	res.keepJoules = res.joules
	return res
}
func (p *capacityPlanner) assessResult(res capacityResult) capacityResult {
	if res.exclusion != "" {
		return res
	}
	candidate := p.candidate(res)
	key := capacityReplayKey{schedulerConfigKey(candidate), res.key, res.route}
	entry, found := p.feedbackCache.Load(key)
	if !found {
		entry, _ = p.feedbackCache.LoadOrStore(key, &capacityFeedbackMemo{})
	}
	memo := entry.(*capacityFeedbackMemo)
	memo.once.Do(func() {
		p.feedbackCohort.once.Do(func() {
			p.feedbackCohort.cohort, p.feedbackCohort.err = newFeedbackArrivalCohort(p.ctx, p.settings, p.snapshot)
		})
		if p.feedbackCohort.err != nil {
			memo.err = p.feedbackCohort.err
			return
		}
		memo.result, memo.err = evaluateFeedbackCandidate(p.ctx, p.settings, p.snapshot, candidate, 0, p.feedbackCohort.cohort)
	})
	if memo.err != nil {
		p.feedbackCache.Delete(key)
		res.exclusion = memo.err.Error()
		return res
	}
	result := memo.result
	res.feedback, res.slo, res.conditional = result.Workloads, result.SLO, result.ConditionalService
	res.assumptions, res.sources = result.Assumptions, result.Sources
	if len(result.InvalidReasons) > 0 {
		res.exclusion = strings.Join(result.InvalidReasons, ",")
	}
	p.rankFeedback(&res)
	return res
}

// evaluateEnergyAtWindow preserves the original node/request energy equations.
// Replay energy is deliberately not consumed by native planner accounting.
func (p *capacityPlanner) evaluateEnergyAtWindow(choice []int, r float64, bridge *schedulerBridgeSchedule, t float64) capacityResult {
	return p.evaluateEnergyForRoutes(choice, r, bridge, t, nil)
}
func (p *capacityPlanner) evaluateEnergyForRoutes(choice []int, r float64, bridge *schedulerBridgeSchedule, t float64, frozen map[string]map[string]int32) capacityResult {
	res := capacityResult{choice: slices.Clone(choice), route: r, keep: true, bridge: bridge, period: t}
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
	for w, ws := range p.ws {
		slices.SortFunc(items[w], func(a, b capacityItem) int { return cmp.Or(cmp.Compare(a.e, b.e), cmp.Compare(a.id, b.id)) })
		retainedRate, weightSum := 0.0, 0.0
		for _, item := range retained[w] {
			retainedRate += float64(item.routingWeight)
			weightSum += float64(item.weight)
		}
		var weights []int32
		var load []float64
		var ok bool
		if frozen != nil {
			for k := range items[w] {
				items[w][k].weight = frozen[ws.d.UID][items[w][k].id]
			}
			weights, load, ok = capacityFixedWeights(ws.rate, items[w])
		} else if p.settings.Controls.Routing {
			weights, load, ok = capacityWeights(ws.rate, items[w])
		} else {
			weights, load, ok = capacityFixedWeights(ws.rate, items[w])
		}
		if !ok {
			res.exclusion = "MissingProfileRouting"
		}
		routes := p.weightRoutes(&res, items[w], weights)
		mu, served, mean := 0.0, 0.0, 0.0
		for k, item := range items[w] {
			if weights[k] > 0 {
				mu += float64(item.routingWeight)
			}
			served += load[k]
			mean += load[k] * item.e
		}
		if served > 0 {
			mean /= served
		}
		if len(routes) == 0 && (ws.rate > 0 || ws.queued > 0) {
			res.exclusion = "NoRoute:" + ws.d.UID
		}
		initial := 0.0
		if weightSum > 0 {
			for _, item := range retained[w] {
				initial += min(ws.rate, retainedRate) * float64(item.weight) / weightSum * item.e
			}
		}
		if bridge != nil {
			res.joules += p.bridgeRequestJoules(bridge, ws, mean, r, t)
		} else {
			before := min(r, t)
			res.joules += initial*before + max(0, ws.rate-retainedRate)*before*mean + (served*mean+max(0, ws.rate-served)*mean)*max(0, t-r)
		}
		res.weights = append(res.weights, routes)
		res.sums = append(res.sums, [2]float64{mu, retainedRate})
		if res.keep {
			currentCount := 0
			for id, weight := range p.keep.Routing[ws.d.UID] {
				if weight > 0 {
					currentCount++
					if routes[id] != weight {
						res.keep = false
					}
				}
			}
			if len(routes) != currentCount {
				res.keep = false
			}
		}
	}
	reason, reserves := p.transitionConstraints(choice)
	res.exclusion, res.reserves = cmp.Or(res.exclusion, reason), reserves
	res.key = strings.Join(keys, "|")
	return res
}
func (p *capacityPlanner) candidate(res capacityResult) SchedulerCandidate {
	c := SchedulerCandidate{Keep: res.keep, Nodes: slices.Clone(p.keep.Nodes), Routing: map[string]map[string]int32{}}
	for uid, routes := range p.keep.Routing {
		c.Routing[uid] = routes
	}
	for w, ws := range p.ws {
		c.Routing[ws.d.UID] = res.weights[w]
	}
	for n, j := range res.choice {
		plan := p.plans[n][j]
		c.Nodes[n].PowerMode = plan.mode
		c.Instances = append(c.Instances, plan.instances...)
	}
	c.Actions = res.bridge.actionsFor(p.settings, p.snapshot, c)
	return c
}
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

func (p *capacityPlanner) rankFeedback(res *capacityResult) {
	type tuple struct {
		uid                         string
		failure, success, potential float64
	}
	values := make([]tuple, 0, len(p.ws))
	res.feasible = res.exclusion == ""
	for _, ws := range p.ws {
		at := slices.IndexFunc(res.feedback, func(score FeedbackWorkloadScore) bool { return score.UID == ws.d.UID })
		if at < 0 {
			res.exclusion = "MissingFeedback:" + ws.d.UID
			res.feasible = false
			continue
		}
		score := res.feedback[at]
		value := tuple{uid: score.UID, success: score.SuccessExcess}
		if score.Population > 0 {
			value.failure = 1 - float64(score.OnTime)/float64(score.Population)
		}
		if score.FailureFraction > 0 {
			value.potential = math.Inf(1)
			if score.PotentialExcess != nil {
				value.potential = *score.PotentialExcess
			}
		}
		values = append(values, value)
		res.feasible = res.feasible && score.Satisfied
		res.worst = max(res.worst, value.success)
	}
	priority := func(uid string) int {
		if p.settings.SLOPriorityStrategy == SLOPriorityWorkload {
			if at := slices.Index(p.settings.WorkloadPriority, uid); at >= 0 {
				return at
			}
		}
		return len(p.settings.WorkloadPriority)
	}
	slices.SortStableFunc(values, func(a, b tuple) int {
		if order := cmp.Compare(priority(a.uid), priority(b.uid)); order != 0 {
			return order
		}
		return cmp.Or(cmp.Compare(b.failure, a.failure), cmp.Compare(b.success, a.success), cmp.Compare(b.potential, a.potential))
	})
	res.excesses = make([]float64, 0, 3*len(values))
	for _, value := range values {
		res.excesses = append(res.excesses, value.failure, value.success, value.potential)
	}
	res.final = res.feasible
}
func (a capacityResult) better(b *capacityResult) bool {
	if b == nil {
		return true
	}
	if (a.exclusion == "") != (b.exclusion == "") {
		return a.exclusion == ""
	}
	if a.feasible != b.feasible {
		return a.feasible
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
