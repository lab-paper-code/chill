package serving

import (
	"math"
	"slices"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// energy charges committed operations in full and waiting/serving only in [0, t].
func (p *capacityPlan) energy(r, t float64) float64 {
	if p.keep {
		return p.before * t
	}
	return p.preJ + p.mid*max(0, min(r, t)-p.preMean) + p.postJ + p.after*max(0, t-r-p.postMean)
}

// A trial's shift/gap error is deterministic: repeated uses add, shared uses
// cancel with their sign before distinct evidence bounds are summed.
type schedulerBoundSource struct {
	reference evidencestore.ObjectIdentity
	trial     int
}

type schedulerEnergyBounds map[schedulerBoundSource]float64

func (bounds schedulerEnergyBounds) halfWidth() float64 {
	total := 0.0
	for _, value := range bounds {
		total += math.Abs(value)
	}
	return total
}

type schedulerBaselineBound struct {
	reference evidencestore.ObjectIdentity
	watts     float64 // signed coefficient of the exact baseline reconstruction
}

func schedulerBaselineUncertainty(baselines []NodeBaseline, provenance IdlePowerProvenance, instances int) schedulerBaselineBound {
	if len(provenance.References) == 0 {
		return schedulerBaselineBound{}
	}
	for _, b := range baselines {
		if b.ID == provenance.References[0] {
			coefficient := 1.0
			if provenance.Reconstructed {
				// unloaded + sum(loaded idle - unloaded), not n independent errors
				coefficient -= float64(instances)
			}
			return schedulerBaselineBound{reference: b.Reference, watts: coefficient * b.WattsUncertainty}
		}
	}
	return schedulerBaselineBound{}
}

func (plan *capacityPlan) addBounds(bounds schedulerEnergyBounds, r, t, sign float64) {
	add := func(b schedulerBaselineBound, seconds float64) {
		if b.watts != 0 {
			bounds[schedulerBoundSource{reference: b.reference, trial: -1}] += sign * seconds * b.watts
		}
	}
	if plan.keep {
		add(plan.beforeBound, t)
		return
	}
	for source, value := range plan.operationBounds {
		bounds[source] += sign * value
	}
	add(plan.midBound, max(0, min(r, t)-plan.preMean))
	add(plan.afterBound, max(0, t-r-plan.postMean))
}

// chain plans the Node change with schedulerPlan and sums the measured costs
// of the operations before the route (those the route depends on) and after
// it. It returns the instances stopped before the route and those removed
// before it.
func (p *capacityPlanner) chain(n int, plan *capacityPlan) (map[string]bool, map[string]bool) {
	node := p.snapshot.Nodes[n].Node
	c := SchedulerCandidate{Nodes: slices.Clone(p.keep.Nodes), Routing: p.keep.Routing}
	c.Nodes[n].PowerMode = plan.mode
	for _, i := range p.snapshot.Instances {
		if i.Node != node {
			c.Instances = append(c.Instances, i)
		}
	}
	c.Instances = append(c.Instances, plan.instances...)
	actions := schedulerPlan(p.settings, p.snapshot, c)
	byID := map[string]Action{}
	route := ""
	for _, a := range actions {
		byID[a.ID] = a
		if a.Kind == schedulerActionRoute {
			route = a.ID
		}
	}
	before := map[string]bool{}
	var visit func(id string)
	visit = func(id string) {
		for _, dep := range byID[id].DependsOn {
			if !before[dep] {
				before[dep] = true
				visit(dep)
			}
		}
	}
	visit(route)
	stopped, removed := map[string]bool{}, map[string]bool{}
	for _, a := range actions {
		if a.Node != node || a.Status == schedulerActionComplete || a.Status == schedulerActionCancel {
			continue
		}
		switch a.Kind {
		case schedulerActionStop:
			stopped[a.Instance] = stopped[a.Instance] || before[a.ID]
			continue
		case schedulerActionRemove:
			removed[a.Instance] = before[a.ID]
		case schedulerActionPrepare, schedulerActionPower:
		default:
			continue
		}
		cost := schedulerCost(p.snapshot.Costs, a.CostID)
		mean, longest, joules := actionOperationCost(p.settings, p.snapshot.Costs, a)
		plan.record.ActionCosts = append(plan.record.ActionCosts, SchedulerActionCost{Action: a.ID, Kind: a.Kind, Provenance: a.CostProvenance})
		var trials []OperationTrial
		if cost != nil {
			trials = cost.Trials
		}
		for trial, measured := range trials {
			if measured.EnergyUncertaintyJoules != 0 {
				if plan.operationBounds == nil {
					plan.operationBounds = schedulerEnergyBounds{}
				}
				source := schedulerBoundSource{reference: cost.Reference, trial: trial}
				plan.operationBounds[source] += measured.EnergyUncertaintyJoules / float64(len(cost.Trials))
			}
		}
		if before[a.ID] {
			plan.preMean, plan.preMax, plan.preJ = plan.preMean+mean, plan.preMax+longest, plan.preJ+joules
		} else {
			plan.postMean, plan.postMax, plan.postJ = plan.postMean+mean, plan.postMax+longest, plan.postJ+joules
		}
	}
	plan.record.Pre, plan.record.Post = time.Duration(plan.preMax*1e9), time.Duration(plan.postMax*1e9)
	plan.record.Joules = plan.preJ + plan.postJ
	plan.record.EnergyUncertaintyJoules = plan.operationBounds.halfWidth()
	return stopped, removed
}

// capacityOperationCost is a cost's mean and longest trial seconds and mean
// wall joules; a target-baseline-delta trial adds back its subtracted watts.
func capacityOperationCost(cost *OperationCost) (float64, float64, float64, bool) {
	if cost == nil || cost.Meter == "" || cost.Scope != schedulerNodeWallScope || len(cost.Trials) == 0 {
		return 0, 0, 0, false
	}
	if cost.EnergyScope == schedulerTargetBaselineDelta && (cost.SubtractedWatts == nil || !schedulerFinite(*cost.SubtractedWatts)) || cost.EnergyScope != schedulerWholeWall && cost.EnergyScope != schedulerTargetBaselineDelta {
		return 0, 0, 0, false
	}
	var mean, longest, joules float64
	for _, t := range cost.Trials {
		d := t.Duration.Seconds()
		if d <= 0 || !schedulerFinite(t.Joules) || !schedulerValidHalfWidth(t.EnergyUncertaintyJoules) {
			return 0, 0, 0, false
		}
		j := t.Joules
		if cost.EnergyScope == schedulerTargetBaselineDelta {
			j += *cost.SubtractedWatts * d
		}
		mean += d / float64(len(cost.Trials))
		joules += j / float64(len(cost.Trials))
		longest = max(longest, d)
	}
	return mean, longest, joules, true
}

// ActionDeadlineExceeded marks a running action past its deadline; its goal
// is abandoned and the next decision re-plans.
const ActionDeadlineExceeded = "ActionDeadlineExceeded"

// ActionDeadline is how long a running action may take: the longest measured
// trial of its cost (none for route, stop and drain) plus one observation
// window.
func ActionDeadline(window time.Duration, costs []OperationCost, a Action) time.Duration {
	longest := time.Duration(0)
	if cost := actionDefaultCost(a); cost != nil {
		longest = cost.Trials[0].Duration
	}
	if cost := schedulerCost(costs, a.CostID); cost != nil {
		for _, t := range cost.Trials {
			longest = max(longest, t.Duration)
		}
	}
	return longest + window
}

// payback reverts optional replacements and new empty-Node savings when they
// do not repay their transition at the least load over the same period T.
// Empty-Node savings must pay independently, including in overload plan sets.
func (p *capacityPlanner) payback(best capacityResult) capacityResult {
	for n, j := range best.choice {
		plan := p.plans[n][j]
		emptySaving := !plan.keep && plan.mode != p.plans[n][0].mode &&
			len(p.plans[n][0].instances) == 0 && len(plan.instances) == 0
		if !emptySaving && (!best.feasible || !plan.replaces) {
			continue
		}
		alt := slices.Clone(best.choice)
		alt[n] = 0
		r := p.routeOf(alt)
		kept := p.evaluate(alt, r)
		if best.feasible && !kept.feasible || !best.feasible && !kept.feasible && slices.Compare(best.excesses,kept.excesses)<0 {
			continue
		}
		paid := p.leastLoadJoules(alt, r) > p.leastLoadJoules(best.choice, best.route)
		bound, withinUncertainty := 0.0, false
		if emptySaving {
			bound = p.gainBound(kept, best)
			withinUncertainty = (kept.joules-kept.keepJoules)-(best.joules-best.keepJoules) <= bound
			paid = paid && !withinUncertainty
		}
		if !emptySaving && !kept.feasible || !p.admits(&kept) || paid {
			continue
		}
		kept.unpaid = append(slices.Clone(best.unpaid), p.snapshot.Nodes[n].Node.UID)
		kept.withinUncertainty = best.withinUncertainty || withinUncertainty
		kept.gainUncertainty = best.gainUncertainty
		if withinUncertainty {
			kept.gainUncertainty = max(kept.gainUncertainty, bound)
		}
		best = kept
	}
	return best
}

// minimumGain requires nominal gain over T to exceed the deterministic
// operation and selected-baseline sensitivity bounds. Signed exposures to
// identical immutable evidence cancel before distinct residuals are summed.
// Profile statistical uncertainty, including BEO idle fallback, is outside
// this hurdle; it is not a joint confidence interval for predicted gain.
func (p *capacityPlanner) minimumGain(best capacityResult) capacityResult {
	if !best.feasible || best.keep {
		return best
	}
	keep := p.evaluateCurrentKeep(best.period)
	keep.keepJoules = keep.joules
	if !keep.feasible {
		return best
	}
	best.gainUncertainty = p.gainBound(keep, best)
	if keep.joules-best.joules > best.gainUncertainty {
		return best
	}
	keep.withinUncertainty = true
	keep.gainUncertainty = best.gainUncertainty
	return keep
}

func (p *capacityPlanner) gainBound(keep, best capacityResult) float64 {
	bounds := schedulerEnergyBounds{}
	for n, j := range best.choice {
		if p.compared[n] {
			p.plans[n][keep.choice[n]].addBounds(bounds, keep.route, best.period, 1)
			p.plans[n][j].addBounds(bounds, best.route, best.period, -1)
			if best.bridge != nil && !p.plans[n][j].keep {
				plan, delay := p.plans[n][j], best.bridge.delays[n]
				for _, weighted := range []struct {
					bound   schedulerBaselineBound
					sign    float64
					seconds float64
				}{{plan.beforeBound, -1, min(delay, best.period)}, {plan.midBound, 1, min(delay, max(0, min(best.route, best.period)-plan.preMean))}} {
					if weighted.bound.watts != 0 {
						bounds[schedulerBoundSource{reference: weighted.bound.reference, trial: -1}] += weighted.sign * weighted.seconds * weighted.bound.watts
					}
				}
			}
		}
	}
	return bounds.halfWidth()
}

// routeOf is the route time of a plan set: its longest chain before the route.
func (p *capacityPlanner) routeOf(choice []int) float64 {
	r := 0.0
	for n, j := range choice {
		if plan := p.plans[n][j]; !plan.keep {
			r = max(r, plan.preMax)
		}
	}
	return r
}

// leastLoadJoules is the plan set's energy over T with every Workload at its
// least rate of the observation window.
func (p *capacityPlanner) leastLoadJoules(choice []int, r float64) float64 {
	rates := make([]float64, len(p.ws))
	for w := range p.ws {
		rates[w], p.ws[w].rate = p.ws[w].rate, p.ws[w].minRate
	}
	oldKeepWatts := p.keepWatts
	p.keepWatts = p.evaluateEnergyForRoutes(make([]int, len(p.plans)), 0, nil, 1, p.keep.Routing).joules
	bridge,_:=p.bridgeSchedule(choice)
	result := p.evaluateEnergyAtWindow(choice, r, bridge, p.period)
	joules := result.joules-p.keepWatts*p.period
	p.keepWatts = oldKeepWatts
	for w := range p.ws {
		p.ws[w].rate = rates[w]
	}
	return joules
}
