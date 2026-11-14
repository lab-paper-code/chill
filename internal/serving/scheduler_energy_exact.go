package serving

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

func schedulerEnergyState(instances []SchedulerInstance) []string {
	state := make([]string, 0, len(instances))
	for _, instance := range instances {
		state = append(state, schedulerState(instance))
	}
	slices.Sort(state)
	return state
}

type schedulerMissingExposure struct {
	amount float64
	cell   SchedulerMissingEnergyCell
}

type schedulerMissingOperation struct {
	cell      SchedulerMissingEnergyCell
	pre, post int
}

type schedulerEnergyTime struct {
	expression string
	value      float64
	sources    map[string]bool
}

const schedulerEnergyTimeMin = "min"

// combineEnergyTimes folds measured constants and zero-duration dependency
// actions. Action hashes never become part of a duration expression.
func combineEnergyTimes(kind string, times []schedulerEnergyTime) schedulerEnergyTime {
	out := schedulerEnergyTime{expression: "0"}
	if len(times) == 0 {
		return out
	}
	constant, haveConstant := 0.0, false
	expressions := make([]string, 0, len(times))
	for _, term := range times {
		if len(term.sources) == 0 {
			if !haveConstant || kind == "max" && term.value > constant || kind == schedulerEnergyTimeMin && term.value < constant {
				constant = term.value
			}
			haveConstant = true
			continue
		}
		expressions = append(expressions, term.expression)
		if out.sources == nil {
			out.sources = map[string]bool{}
		}
		for key := range term.sources {
			out.sources[key] = true
		}
	}
	if len(expressions) == 0 || kind == schedulerEnergyTimeMin && haveConstant && constant == 0 {
		return schedulerEnergyTime{expression: fmt.Sprint(constant), value: constant}
	}
	if haveConstant && (constant != 0 || kind == schedulerEnergyTimeMin) {
		expressions = append(expressions, fmt.Sprint(constant))
	}
	slices.Sort(expressions)
	expressions = slices.Compact(expressions)
	out.expression = expressions[0]
	if len(expressions) > 1 {
		out.expression = kind + "(" + strings.Join(expressions, ",") + ")"
	}
	return out
}

// bridgeEnergyTimes retains the dependency DAG while erasing action identity.
// Only operation-cell duration symbols and measured elapsed constants survive.
func (p *capacityPlanner) bridgeEnergyTimes(res capacityResult, operations map[string]schedulerMissingOperation) (map[string]schedulerEnergyTime, []schedulerEnergyTime, schedulerEnergyTime) {
	actions := map[string]Action{}
	final := ""
	for _, action := range res.bridge.actions {
		actions[action.ID] = action
		if action.Kind == schedulerActionRoute && action.RouteTarget == nil {
			final = action.ID
		}
	}
	ends, starts := map[string]schedulerEnergyTime{}, map[string]schedulerEnergyTime{}
	var end func(string) schedulerEnergyTime
	end = func(id string) schedulerEnergyTime {
		if value, ok := ends[id]; ok {
			return value
		}
		action := actions[id]
		var dependencies []schedulerEnergyTime
		for _, dep := range action.DependsOn {
			dependencies = append(dependencies, end(dep))
		}
		value := combineEnergyTimes("max", dependencies)
		starts[id] = value
		if action.Status != schedulerActionComplete && action.Status != schedulerActionCancel &&
			(action.Kind == schedulerActionPrepare || action.Kind == schedulerActionRemove || action.Kind == schedulerActionPower) {
			if action.CostProvenance != nil && action.CostProvenance.Source == operationSourceExact {
				_, longest, _ := actionOperationCost(p.settings, p.snapshot.Costs, action)
				if len(value.sources) == 0 {
					value.value += longest
					value.expression = fmt.Sprint(value.value)
				} else if longest != 0 {
					value.expression = fmt.Sprintf("sum(%s,%g)", value.expression, longest)
				}
			} else {
				key := fmt.Sprintf("%v:%s:%s:%s", action.Node, action.Kind, action.From, action.To)
				for candidate, operation := range operations {
					cell := operation.cell
					if cell.Node == action.Node && cell.Kind == action.Kind && cell.From == action.From && cell.To == action.To {
						key = candidate
						break
					}
				}
				sources := map[string]bool{key: true}
				for source := range value.sources {
					sources[source] = true
				}
				duration := fmt.Sprintf("duration(%q)", key)
				if value.expression == "0" {
					value.expression = duration
				} else {
					value.expression = fmt.Sprintf("sum(%s,%s)", value.expression, duration)
				}
				value.sources = sources
			}
		}
		ends[id] = value
		return value
	}
	route := end(final)
	before := map[string]bool{}
	var visit func(string)
	visit = func(id string) {
		for _, dep := range actions[id].DependsOn {
			if !before[dep] {
				before[dep] = true
				visit(dep)
			}
		}
	}
	visit(final)
	delays := make([]schedulerEnergyTime, len(res.choice))
	for n := range res.choice {
		var candidates []schedulerEnergyTime
		for id := range before {
			action := actions[id]
			if action.Node == p.snapshot.Nodes[n].Node && action.Status != schedulerActionComplete {
				candidates = append(candidates, starts[id])
			}
		}
		delays[n] = combineEnergyTimes(schedulerEnergyTimeMin, candidates)
	}
	return ends, delays, route
}

// schedulerLoadSegment is one constant routing state of a bridge.
type schedulerLoadSegment struct {
	start, end float64
	rates      []schedulerRate
}

// schedulerBridgeEvent switches one Workload's routing at a bridge stage.
type schedulerBridgeEvent struct {
	at      float64
	w       int
	action  string
	weights map[string]int32
}

// bridgeEvents orders the bridge's route stages by time.
func (p *capacityPlanner) bridgeEvents(res capacityResult) []schedulerBridgeEvent {
	var events []schedulerBridgeEvent
	for _, stage := range res.bridge.stages {
		w, ok := p.index[stage.UID]
		if !ok {
			continue
		}
		for _, action := range res.bridge.actions {
			if action.ID == stage.Action {
				events = append(events, schedulerBridgeEvent{at: stage.At.Seconds(), w: w, action: stage.Action, weights: action.RouteTarget.Routing[stage.UID]})
				break
			}
		}
	}
	slices.SortStableFunc(events, func(a, b schedulerBridgeEvent) int { return cmp.Compare(a.at, b.at) })
	return events
}

// bridgeStateRates gives each routed bridge instance its flow, up to its
// routing rate; the rest of a Workload goes to its final routing.
func (p *capacityPlanner) bridgeStateRates(res capacityResult, state []map[string]int32, shares [][]schedulerRate) []schedulerRate {
	var rates []schedulerRate
	for w, ws := range p.ws {
		units, served := 0.0, 0.0
		ids := make([]string, 0, len(state[w]))
		for id, weight := range state[w] {
			units += float64(max(0, weight))
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			weight := state[w][id]
			if weight <= 0 || units == 0 {
				continue
			}
			instance := res.bridge.instances[id]
			flow := min(p.currentRoutingRate(instance), ws.rate*float64(weight)/units)
			served += flow
			rates = append(rates, schedulerRate{id: id, node: instance.Node, model: p.loadModels.of(instance), rate: flow})
		}
		rates = appendScaledRates(rates, shares[w], max(0, ws.rate-served))
	}
	return rates
}

// bridgeSegments lists the bridge's routing states over [0, min(route, window)).
func (p *capacityPlanner) bridgeSegments(res capacityResult, window float64) []schedulerLoadSegment {
	shares := p.finalShares(res)
	state := make([]map[string]int32, len(p.ws))
	for w, ws := range p.ws {
		state[w] = p.keep.Routing[ws.d.UID]
	}
	var segments []schedulerLoadSegment
	at, end := 0.0, min(res.route, window)
	for _, event := range p.bridgeEvents(res) {
		if until := min(event.at, end); until > at {
			segments = append(segments, schedulerLoadSegment{start: at, end: until, rates: p.bridgeStateRates(res, state, shares)})
			at = until
		}
		state[event.w] = event.weights
	}
	if end > at {
		segments = append(segments, schedulerLoadSegment{start: at, end: end, rates: p.bridgeStateRates(res, state, shares)})
	}
	return segments
}

// bridgeRequestTimingCoefficients rewrites the piecewise load law as power
// differences at stage boundaries, plus the final-route boundary. Equal powers
// cancel regardless of routing-map, instance, or action identity. Unknown node
// load watts are missing cells of their own and add nothing here.
func (p *capacityPlanner) bridgeRequestTimingCoefficients(res capacityResult) (map[string]float64, float64) {
	shares := p.finalShares(res)
	state := make([]map[string]int32, len(p.ws))
	for w, ws := range p.ws {
		state[w] = p.keep.Routing[ws.d.UID]
	}
	changes := map[string]float64{}
	before := knownLoadWatts(p.bridgeStateRates(res, state, shares))
	for _, event := range p.bridgeEvents(res) {
		state[event.w] = event.weights
		after := knownLoadWatts(p.bridgeStateRates(res, state, shares))
		changes[event.action] += before - after
		before = after
	}
	_, final := p.requestRates(res)
	return changes, before - knownLoadWatts(final)
}

func knownLoadWatts(rates []schedulerRate) float64 {
	watts := 0.0
	for _, load := range nodeLoadWatts(rates) {
		if load.known {
			watts += load.watts
		}
	}
	return watts
}

// nodePlans builds keep without operation cells. Any non-exact candidate
// operation therefore leaves an uncancelled whole-wall joules unknown before
// routing or native request replay. Idle/duration-only differences still replay.
func (p *capacityPlanner) hasUnsharedMissingOperation(choice []int) bool {
	for n := range choice {
		if p.compared[n] && len(p.plans[n][0].record.ActionCosts) != 0 {
			return false
		}
	}
	for n, j := range choice {
		if !p.compared[n] {
			continue
		}
		for _, action := range p.plans[n][j].record.ActionCosts {
			if action.Provenance == nil || action.Provenance.Source != operationSourceExact {
				return true
			}
		}
	}
	return false
}

// missingEnergyCells uses the same signed exposure rule as gainBound. A missing
// target cell is an independent unknown even when its estimate was borrowed from
// the same measured row as a different target cell.
func (p *capacityPlanner) missingEnergyCells(reference, candidate capacityResult) []SchedulerMissingEnergyCell {
	exposures := map[string]schedulerMissingExposure{}
	add := func(key string, cell SchedulerMissingEnergyCell, amount float64) {
		old := exposures[key]
		old.amount += amount
		old.cell = cell
		exposures[key] = old
	}
	for _, side := range []struct {
		result capacityResult
		sign   float64
	}{{reference, 1}, {candidate, -1}} {
		res := side.result
		operations := map[string]schedulerMissingOperation{}
		for n, j := range res.choice {
			if !p.compared[n] {
				continue
			}
			plan := p.plans[n][j]
			seconds := map[string]float64{schedulerEnergyPhaseBefore: 0, schedulerEnergyPhaseMid: 0, schedulerEnergyPhaseAfter: 0}
			if plan.keep {
				seconds[schedulerEnergyPhaseBefore] = res.period
			} else {
				seconds[schedulerEnergyPhaseMid] = max(0, min(res.route, res.period)-plan.preMean)
				seconds[schedulerEnergyPhaseAfter] = max(0, res.period-res.route-plan.postMean)
				if res.bridge != nil {
					delay := res.bridge.delays[n]
					seconds[schedulerEnergyPhaseBefore] = min(delay, res.period)
					seconds[schedulerEnergyPhaseMid] -= min(delay, seconds[schedulerEnergyPhaseMid])
				}
			}
			for _, phase := range []string{schedulerEnergyPhaseBefore, schedulerEnergyPhaseMid, schedulerEnergyPhaseAfter} {
				idle, ok := plan.record.IdleCosts[phase]
				if !ok || idle.Source == operationSourceExact {
					continue
				}
				cell := SchedulerMissingEnergyCell{Node: plan.record.Node, Phase: phase, PowerMode: idle.PowerMode, State: idle.State, Required: "node-wall idle watts for this node, power mode and loaded-state multiset"}
				identity := cell
				identity.Phase = ""
				raw, _ := json.Marshal(identity)
				add("idle:"+string(raw), cell, side.sign*seconds[phase])
			}
			for k, a := range plan.record.ActionCosts {
				if a.Provenance != nil && a.Provenance.Source == operationSourceExact {
					continue
				}
				cell := plan.energyCells[k]
				raw, _ := json.Marshal(cell)
				key := string(raw)
				add("operation:"+key, cell, side.sign)
				operation := operations[key]
				operation.cell = cell
				if plan.energyPre[k] {
					operation.pre++
				} else {
					operation.post++
				}
				operations[key] = operation
			}
		}
		// A node's load watts not identified at one load is one unknown,
		// cancelled only by the same load for the same seconds.
		for _, unknown := range res.loadUnknowns {
			cell := SchedulerMissingEnergyCell{Node: unknown.node, Phase: unknown.phase, Kind: schedulerLoadPowerCell, State: unknown.identities,
				Required: "identified awake step, hold, input slope and service time of these configurations at these input rates"}
			add("load:"+unknown.key, cell, side.sign*unknown.seconds)
		}
		// Whole-wall J and elapsed seconds are separate unknowns. Each source's
		// duration law already includes every occurrence and its chain location.
		for key, law := range p.energyTimingLaws(res, operations) {
			add("duration:"+key+law, operations[key].cell, side.sign)
		}
	}
	keys := make([]string, 0, len(exposures))
	for key, exposure := range exposures {
		if exposure.amount != 0 {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	var missing []SchedulerMissingEnergyCell
	seen := map[string]bool{}
	for _, key := range keys {
		cell := exposures[key].cell
		raw, _ := json.Marshal(cell)
		if !seen[string(raw)] {
			missing = append(missing, cell)
			seen[string(raw)] = true
		}
	}
	return missing
}

// energyTimingLaws projects the accounting equation onto each missing duration.
// For r >= preMean, idle energy is mid*(min(r,H)-min(preMean,H)) +
// after*H - after*min(H,r+postMean). The constant after*H drops out.
// Structural equality proves cancellation without perturbing estimated times.
func (p *capacityPlanner) energyTimingLaws(res capacityResult, operations map[string]schedulerMissingOperation) map[string]string {
	if len(operations) == 0 {
		return nil
	}
	preLaws, postLaws, hasPost, routeTime, hasPre := p.energyDurationLaws(res, operations)
	var bridgeTimes map[string]schedulerEnergyTime
	var bridgeDelays []schedulerEnergyTime
	var bridgeRoute schedulerEnergyTime
	if res.bridge != nil {
		bridgeTimes, bridgeDelays, bridgeRoute = p.bridgeEnergyTimes(res, operations)
		routeTime = bridgeRoute.expression
	}
	bridgeRequests, requestDelta := p.energyRequestTimingDelta(res, hasPre)
	route := fmt.Sprintf("min(%g,%s)", res.period, routeTime)
	laws := make(map[string]string, len(operations))
	for key, operation := range operations {
		terms := map[string]float64{}
		routeDepends := operation.pre > 0
		if res.bridge != nil {
			routeDepends = bridgeRoute.sources[key]
		}
		add := func(expression string, plan *capacityPlan, phase string, sign float64) {
			addEnergyIdleTerm(terms, expression, plan, phase, sign)
		}
		for n, j := range res.choice {
			plan := p.plans[n][j]
			if !p.compared[n] || plan.keep {
				continue
			}
			own := operation.cell.Node == plan.record.Node
			if routeDepends {
				add(route, plan, schedulerEnergyPhaseMid, 1)
			}
			if own && operation.pre > 0 {
				add(fmt.Sprintf("min(%g,pre(%s))", res.period, preLaws[n]), plan, schedulerEnergyPhaseMid, -1)
			}
			if routeDepends || own && operation.post > 0 {
				after := route
				if plan.postMean != 0 || hasPost[n] {
					after = fmt.Sprintf("min(%g,sum(%s,post(%s)))", res.period, routeTime, postLaws[n])
				}
				// With no post duration this shares the route expression, so
				// equal mid/after powers cancel before comparing laws.
				add(after, plan, schedulerEnergyPhaseAfter, -1)
			}
			if res.bridge != nil {
				delay := bridgeDelays[n]
				if delay.sources[key] {
					add(fmt.Sprintf("min(%g,%s)", res.period, delay.expression), plan, schedulerEnergyPhaseBefore, 1)
				}
				if delay.expression != "0" && (delay.sources[key] || routeDepends || own && operation.pre > 0) {
					add(fmt.Sprintf("bridge-mid(%s,%s,%s)", route, delay.expression, preLaws[n]), plan, schedulerEnergyPhaseMid, -1)
				}
			}
		}
		if routeDepends {
			terms[route] += requestDelta
		}
		for action, coefficient := range bridgeRequests {
			at := bridgeTimes[action]
			if coefficient != 0 && at.sources[key] {
				terms[fmt.Sprintf("min(%g,%s)", res.period, at.expression)] += coefficient
			}
		}
		var ordered []string
		for expression, coefficient := range terms {
			if coefficient != 0 {
				ordered = append(ordered, fmt.Sprintf("%g*%s", coefficient, expression))
			}
		}
		if len(ordered) > 0 {
			slices.Sort(ordered)
			laws[key] = "/" + strings.Join(ordered, "|")
		}
	}
	return laws
}

type schedulerNodeRequestPower struct {
	watts      float64
	unknown    bool
	references []string
}

// schedulerLoadPowerCell marks a missing cell of a node's load watts.
const schedulerLoadPowerCell = "load-power"

// finalShares is each Workload's final routing as instance shares of one
// input per second.
func (p *capacityPlanner) finalShares(res capacityResult) [][]schedulerRate {
	shares := make([][]schedulerRate, len(p.ws))
	for w := range p.ws {
		var items []capacityItem
		for n, j := range res.choice {
			items = append(items, p.plans[n][j].items[w]...)
		}
		for k := range items {
			items[k].weight = res.weights[w][items[k].id]
		}
		_, loads, _ := capacityFixedWeights(1, items)
		for k, item := range items {
			if loads[k] > 0 {
				shares[w] = append(shares[w], schedulerRate{id: item.id, node: item.node, model: item.model, rate: loads[k]})
			}
		}
	}
	return shares
}

func appendScaledRates(rates, shares []schedulerRate, rate float64) []schedulerRate {
	if rate <= 0 {
		return rates
	}
	for _, share := range shares {
		share.rate *= rate
		rates = append(rates, share)
	}
	return rates
}

// requestRates gives every instance its input rate before and after the
// route: retained current instances first serve up to their routing rate,
// the rest of a Workload goes to its final routing; after the route the whole
// Workload follows the final routing.
func (p *capacityPlanner) requestRates(res capacityResult) (initial, final []schedulerRate) {
	shares := p.finalShares(res)
	for w, ws := range p.ws {
		var retained []capacityItem
		for n, j := range res.choice {
			retained = append(retained, p.plans[n][j].retained[w]...)
		}
		retainedRate, weightSum := 0.0, 0.0
		for _, item := range retained {
			retainedRate += float64(item.routingWeight)
			weightSum += float64(item.weight)
		}
		for _, item := range retained {
			if weightSum > 0 {
				initial = append(initial, schedulerRate{id: item.id, node: item.node, model: item.model,
					rate: min(ws.rate, retainedRate) * float64(item.weight) / weightSum})
			}
		}
		initial = appendScaledRates(initial, shares[w], max(0, ws.rate-retainedRate))
		final = appendScaledRates(final, shares[w], ws.rate)
	}
	return initial, final
}

// addLoadJoules adds each node's load watts for seconds; a node whose watts
// are not identified is held as an unknown instead.
func (res *capacityResult) addLoadJoules(rates []schedulerRate, phase string, seconds float64) {
	if seconds <= 0 {
		return
	}
	for _, load := range nodeLoadWatts(rates) {
		if load.known {
			res.joules += load.watts * seconds
			continue
		}
		res.loadUnknowns = append(res.loadUnknowns, schedulerLoadUnknown{node: load.node, phase: phase, key: load.key, identities: load.identities, seconds: seconds})
	}
}

func nodeRequestPower(rates []schedulerRate) map[string]schedulerNodeRequestPower {
	out := map[string]schedulerNodeRequestPower{}
	for _, load := range nodeLoadWatts(rates) {
		out[load.node.UID] = schedulerNodeRequestPower{watts: load.watts, unknown: !load.known, references: load.references}
	}
	return out
}

// requestPowerTerms projects the same load watts evaluateEnergyForRoutes adds,
// before and after the route, per node UID.
func (p *capacityPlanner) requestPowerTerms(res capacityResult) (map[string]schedulerNodeRequestPower, map[string]schedulerNodeRequestPower) {
	initial, final := p.requestRates(res)
	return nodeRequestPower(initial), nodeRequestPower(final)
}

// bridgePhaseRequestPower averages one node's bridge load watts over a phase;
// the idle cell remains the phase's single node-wall reference. An empty phase
// reports the state at its start.
func (p *capacityPlanner) bridgePhaseRequestPower(res capacityResult, node int, start, end float64) schedulerNodeRequestPower {
	uid := p.snapshot.Nodes[node].Node.UID
	out := schedulerNodeRequestPower{}
	segments := p.bridgeSegments(res, res.period)
	if len(segments) == 0 {
		return out
	}
	if end <= start {
		index := slices.IndexFunc(segments, func(s schedulerLoadSegment) bool { return start < s.end })
		if index < 0 {
			index = len(segments) - 1
		}
		load, ok := nodeRequestPower(segments[index].rates)[uid]
		if !ok {
			return out
		}
		return load
	}
	for _, segment := range segments {
		seconds := max(0, min(end, segment.end)-max(start, segment.start))
		load, ok := nodeRequestPower(segment.rates)[uid]
		if seconds == 0 || !ok {
			continue
		}
		out.watts += load.watts * seconds
		out.unknown = out.unknown || load.unknown
		for _, reference := range load.references {
			out.references = appendUnique(out.references, reference)
		}
	}
	out.watts /= end - start
	return out
}

func (p *capacityPlanner) energyDurationLaws(res capacityResult, operations map[string]schedulerMissingOperation) ([]string, []string, []bool, string, bool) {
	preLaws, postLaws := make([]string, len(res.choice)), make([]string, len(res.choice))
	hasPost := make([]bool, len(res.choice))
	var routes []string
	fixedRoute := 0.0
	hasPre := false
	for n, j := range res.choice {
		plan := p.plans[n][j]
		var pre, post []string
		for key, operation := range operations {
			if operation.cell.Node != plan.record.Node {
				continue
			}
			if operation.pre > 0 {
				pre = append(pre, fmt.Sprintf("%s*%d", key, operation.pre))
				hasPre = true
			}
			if operation.post > 0 {
				post = append(post, fmt.Sprintf("%s*%d", key, operation.post))
			}
		}
		slices.Sort(pre)
		slices.Sort(post)
		hasPost[n] = len(post) > 0
		preLaws[n] = fmt.Sprintf("%g:%v", plan.preMean, pre)
		postLaws[n] = fmt.Sprintf("%g:%v", plan.postMean, post)
		if !plan.keep {
			if len(pre) > 0 {
				routes = append(routes, fmt.Sprintf("%g:%v", plan.preMax, pre))
			} else {
				fixedRoute = max(fixedRoute, plan.preMax)
			}
		}
	}
	if fixedRoute > 0 {
		routes = append(routes, fmt.Sprint(fixedRoute))
	}
	slices.Sort(routes)
	routes = slices.Compact(routes)
	routeTime := fmt.Sprintf("route(max(%v)):%g", routes, res.route)
	return preLaws, postLaws, hasPost, routeTime, hasPre
}

func (p *capacityPlanner) energyRequestTimingDelta(res capacityResult, hasPre bool) (map[string]float64, float64) {
	if res.bridge != nil {
		if hasPre {
			return p.bridgeRequestTimingCoefficients(res)
		}
	} else if hasPre {
		initial, final := p.requestPowerTerms(res)
		before, after := 0.0, 0.0
		for _, node := range p.snapshot.Nodes {
			before += initial[node.Node.UID].watts
			after += final[node.Node.UID].watts
		}
		return nil, before - after
	}
	return nil, 0
}

func addEnergyIdleTerm(terms map[string]float64, expression string, plan *capacityPlan, phase string, sign float64) {
	watts := plan.mid
	switch phase {
	case schedulerEnergyPhaseBefore:
		watts = plan.before
	case schedulerEnergyPhaseAfter:
		watts = plan.after
	}
	idle := plan.record.IdleCosts[phase]
	if idle.Source != operationSourceExact {
		// Missing watts remain a symbol for their target cell, including
		// when the provisional numeric value is zero.
		expression += fmt.Sprintf("*idle(%s:%s:%v)", plan.record.Node.UID, idle.PowerMode, idle.State)
		terms[expression] += sign
	} else {
		terms[expression] += sign * watts
	}
}
