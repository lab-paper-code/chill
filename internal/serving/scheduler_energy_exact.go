package serving

import (
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

// bridgeRequestTimingCoefficients rewrites the piecewise request law as power
// differences at stage boundaries, plus the final-route boundary. Equal powers
// cancel regardless of routing-map, instance, or action identity.
func (p *capacityPlanner) bridgeRequestTimingCoefficients(res capacityResult) (map[string]float64, float64) {
	changes, route := map[string]float64{}, 0.0
	for w, ws := range p.ws {
		var items []capacityItem
		for n, j := range res.choice {
			items = append(items, p.plans[n][j].items[w]...)
		}
		for k := range items {
			items[k].weight = res.weights[w][items[k].id]
		}
		_, loads, _ := capacityFixedWeights(ws.rate, items)
		served, mean := 0.0, 0.0
		for k, load := range loads {
			served += load
			mean += load * items[k].e
		}
		if served > 0 {
			mean /= served
		}
		power := func(weights map[string]int32) float64 {
			units, served, watts := 0.0, 0.0, 0.0
			var ids []string
			for id, weight := range weights {
				units += float64(max(0, weight))
				ids = append(ids, id)
			}
			slices.Sort(ids)
			for _, id := range ids {
				weight := weights[id]
				if weight <= 0 || units == 0 {
					continue
				}
				instance := res.bridge.instances[id]
				flow := min(p.currentRoutingRate(instance), ws.rate*float64(weight)/units)
				served += flow
				watts += flow * capacityInputEnergy(instance)
			}
			return watts + max(0, ws.rate-served)*mean
		}
		before := power(p.keep.Routing[ws.d.UID])
		for _, stage := range res.bridge.stages {
			if stage.UID != ws.d.UID {
				continue
			}
			for _, action := range res.bridge.actions {
				if action.ID == stage.Action {
					after := power(action.RouteTarget.Routing[ws.d.UID])
					changes[stage.Action] += before - after
					before = after
					break
				}
			}
		}
		route += before - ws.rate*mean
	}
	return changes, route
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
	references []string
}

// requestPowerTerms projects the very same retained and final load equations as
// evaluateEnergyForRoutes. Profile references identify the batch-1 EPI source.
func (p *capacityPlanner) requestPowerTerms(res capacityResult) (map[string]schedulerNodeRequestPower, map[string]schedulerNodeRequestPower) {
	initial, final := map[string]schedulerNodeRequestPower{}, map[string]schedulerNodeRequestPower{}
	add := func(out map[string]schedulerNodeRequestPower, instance SchedulerInstance, watts float64) {
		if watts == 0 {
			return
		}
		term := out[instance.Node.UID]
		term.watts += watts
		if instance.Profile != nil {
			term.references = appendUnique(term.references, instance.Profile.ID)
		}
		out[instance.Node.UID] = term
	}
	for w, ws := range p.ws {
		var items, retained []capacityItem
		instances := map[string]SchedulerInstance{}
		for n, j := range res.choice {
			plan := p.plans[n][j]
			items = append(items, plan.items[w]...)
			retained = append(retained, plan.retained[w]...)
			for _, instance := range plan.instances {
				instances[instance.ID] = instance
			}
			for _, instance := range p.plans[n][0].instances {
				instances[instance.ID] = instance
			}
		}
		for k := range items {
			items[k].weight = res.weights[w][items[k].id]
		}
		_, load, _ := capacityFixedWeights(ws.rate, items)
		served, retainedRate, weightSum := 0.0, 0.0, 0.0
		for _, flow := range load {
			served += flow
		}
		for _, item := range retained {
			retainedRate += float64(item.routingWeight)
			weightSum += float64(item.weight)
		}
		for k, item := range items {
			if served > 0 {
				add(final, instances[item.id], ws.rate*load[k]/served*item.e)
				add(initial, instances[item.id], max(0, ws.rate-retainedRate)*load[k]/served*item.e)
			}
		}
		for _, item := range retained {
			if weightSum > 0 {
				add(initial, instances[item.id], min(ws.rate, retainedRate)*float64(item.weight)/weightSum*item.e)
			}
		}
	}
	return initial, final
}

// bridgePhaseRequestPower integrates the bridge's routed request law over a
// node's constant loaded-state phase. Only request power is averaged; the idle
// cell remains the phase's single node-wall reference.
func (p *capacityPlanner) bridgePhaseRequestPower(res capacityResult, node int, start, end float64) schedulerNodeRequestPower {
	out := schedulerNodeRequestPower{}
	uid := p.snapshot.Nodes[node].Node.UID
	add := func(watts float64, profile string, seconds float64) {
		if watts == 0 || seconds == 0 {
			return
		}
		out.watts += watts * seconds
		out.references = appendUnique(out.references, profile)
	}
	for w, ws := range p.ws {
		var finalItems []capacityItem
		instances := map[string]SchedulerInstance{}
		for n, j := range res.choice {
			plan := p.plans[n][j]
			finalItems = append(finalItems, plan.items[w]...)
			for _, instance := range plan.instances {
				instances[instance.ID] = instance
			}
		}
		for k := range finalItems {
			finalItems[k].weight = res.weights[w][finalItems[k].id]
		}
		_, loads, _ := capacityFixedWeights(ws.rate, finalItems)
		finalServed := 0.0
		for _, load := range loads {
			finalServed += load
		}
		power := func(weights map[string]int32, seconds float64) {
			units, served := 0.0, 0.0
			for _, weight := range weights {
				units += float64(max(0, weight))
			}
			for id, weight := range weights {
				if weight <= 0 || units == 0 {
					continue
				}
				instance := res.bridge.instances[id]
				flow := min(p.currentRoutingRate(instance), ws.rate*float64(weight)/units)
				served += flow
				if instance.Node.UID == uid && instance.Profile != nil {
					add(flow*capacityInputEnergy(instance), instance.Profile.ID, seconds)
				}
			}
			for k, item := range finalItems {
				instance := instances[item.id]
				if finalServed > 0 && instance.Node.UID == uid && instance.Profile != nil {
					add(max(0, ws.rate-served)*loads[k]/finalServed*item.e, instance.Profile.ID, seconds)
				}
			}
		}
		at, weights := 0.0, p.keep.Routing[ws.d.UID]
		for _, stage := range res.bridge.stages {
			if stage.UID != ws.d.UID {
				continue
			}
			until := stage.At.Seconds()
			if end > start {
				power(weights, max(0, min(end, until)-max(start, at)))
			}
			if until > end {
				at = end
				break
			}
			at = until
			for _, action := range res.bridge.actions {
				if action.ID == stage.Action {
					weights = action.RouteTarget.Routing[ws.d.UID]
					break
				}
			}
		}
		if end > start {
			power(weights, max(0, end-max(start, at)))
		} else {
			power(weights, 1)
		}
	}
	if end > start {
		out.watts /= end - start
	}
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
