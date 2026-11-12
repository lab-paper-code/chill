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
		timingLaw := ""
		for n, j := range res.choice {
			if !p.compared[n] {
				continue
			}
			plan := p.plans[n][j]
			seconds := map[string]float64{"before": 0, "mid": 0, "after": 0}
			if plan.keep {
				seconds["before"] = res.period
			} else {
				seconds["mid"] = max(0, min(res.route, res.period)-plan.preMean)
				seconds["after"] = max(0, res.period-res.route-plan.postMean)
				if res.bridge != nil {
					delay := res.bridge.delays[n]
					seconds["before"] = min(delay, res.period)
					seconds["mid"] -= min(delay, seconds["mid"])
				}
			}
			for _, phase := range []string{"before", "mid", "after"} {
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
				if timingLaw == "" {
					timingLaw = p.energyTimingLaw(res)
				}
				raw, _ := json.Marshal(cell)
				add("operation:"+string(raw), cell, side.sign)
				// Whole-wall J and elapsed seconds are separate unknowns. Equal action
				// counts cancel J; timing cancels only with the same power/exposure law.
				add("duration:"+string(raw)+timingLaw, cell, side.sign)
			}
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

// energyTimingLaw names the timing-dependent part of the accounting equation.
// Constant keep-node energy does not depend on any operation duration. Structural
// equality proves cancellation without differentiating at an estimated duration.
func (p *capacityPlanner) energyTimingLaw(res capacityResult) string {
	var terms []string
	for n, j := range res.choice {
		if !p.compared[n] {
			continue
		}
		plan := p.plans[n][j]
		if plan.keep {
			continue
		}
		idle := plan.record.IdleCosts
		terms = append(terms, fmt.Sprintf("%s:%g:%g:%g:%g:%g:%g:%v:%v", plan.record.Node.UID, plan.preMean, plan.preMax, plan.postMean, plan.postMax, plan.mid, plan.after, idle["mid"].References, idle["after"].References))
	}
	for w, weights := range res.weights {
		terms = append(terms, fmt.Sprintf("request:%s:%g:%v", p.ws[w].d.UID, p.ws[w].rate, weights))
	}
	if res.bridge != nil {
		raw, _ := json.Marshal(res.bridge.stages)
		terms = append(terms, string(raw))
	}
	slices.Sort(terms)
	return fmt.Sprintf("/%g/%g/%s", res.period, res.route, strings.Join(terms, "|"))
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

func (p *capacityPlanner) recordRequestPower(keep, best capacityResult) {
	_, before := p.requestPowerTerms(keep)
	mid, after := p.requestPowerTerms(best)
	for n, plans := range p.plans {
		uid := p.snapshot.Nodes[n].Node.UID
		for j, plan := range plans {
			if j != 0 && j != best.choice[n] {
				continue
			}
			for _, phase := range []string{"before", "mid", "after"} {
				power := before[uid]
				if j == best.choice[n] {
					switch phase {
					case "mid":
						power = mid[uid]
					case "after":
						power = after[uid]
					}
					if best.bridge != nil && phase != "after" {
						start, end := 0.0, min(best.route, best.period)
						if !plan.keep {
							delay := min(best.bridge.delays[n], best.period)
							if phase == "before" {
								end = delay
							} else {
								start = min(delay+plan.preMean, best.period)
							}
						}
						if phase == "mid" || !plan.keep {
							power = p.bridgePhaseRequestPower(best, n, start, max(start, end))
						}
					}
				}
				idle := plan.record.IdleCosts[phase]
				watts := power.watts
				idle.RequestWatts, idle.RequestReferences = &watts, power.references
				plan.record.IdleCosts[phase] = idle
			}
		}
	}
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
