package serving

type schedulerReplayAction struct {
	action   Action
	started  bool
	done     bool
	blocked  bool
	start    float64
	finish   float64
	trial    OperationTrial
	wallRate float64
}

func (s *schedulerReplayState) completeActions() bool {
	for _, a := range s.actions {
		if a.started && !a.done && a.finish <= s.now {
			a.done = true
			switch a.action.Kind {
			case schedulerActionPrepare:
				i := s.byID[a.action.Instance]
				if i != nil {
					i.loaded = true
					i.ready = true
					i.hasNativeFinish = false // preparation creates a new physical actor
				}
			case schedulerActionRemove:
				if i := s.byID[a.action.Instance]; i != nil {
					i.loaded = false
					i.ready = false
				}
			case schedulerActionPower:
				for n := range s.nodes {
					if s.nodes[n].Node == a.action.Node {
						s.nodes[n].PowerMode = a.action.To
					}
				}
			}
		}
	}
	return true
}

func (s *schedulerReplayState) dependenciesReady(a *schedulerReplayAction) bool {
	if a.blocked || a.action.Status == schedulerActionFailed {
		return false
	}
	if a.action.Status != phaseRunning {
		for _, id := range a.action.DependsOn {
			dep := s.actionByID[id]
			if dep == nil || !dep.done {
				return false
			}
		}
	}
	return true
}

func (s *schedulerReplayState) applyRoute(target SchedulerCandidate) bool {
	s.previousRoute = s.snap.Routing
	if s.routingApplied {
		s.previousRoute = s.applied.Routing
	}
	s.applied = target
	invalidate := func(uid string) {
		if len(s.routeCredits[uid]) == len(s.instances) {
			return
		}
		for _, i := range s.instances {
			if s.snap.RoutingCredits[uid][i.config.ID] != 0 && s.retainedRouteCredit(uid, i.config, 1) == 0 {
				if s.creditInvalid == nil {
					s.creditInvalid = map[string]map[string]bool{}
				}
				if s.creditInvalid[uid] == nil {
					s.creditInvalid[uid] = map[string]bool{}
				}
				s.creditInvalid[uid][i.config.ID] = true
			}
		}
	}
	for uid := range target.Routing {
		invalidate(uid)
	}
	for uid := range s.previousRoute {
		if _, present := target.Routing[uid]; !present {
			invalidate(uid)
		}
	}
	for uid, credits := range s.routeCredits {
		for j, i := range s.instances {
			credits[j] = s.retainedRouteCredit(uid, i.config, credits[j])
		}
	}
	for _, i := range s.instances {
		for _, v := range target.Instances {
			if v.WorkloadUID == "" {
				continue // unbound physical occupancy is not an ingress destination
			}
			weight := v.Weight
			if route, explicit := target.Routing[v.WorkloadUID]; explicit {
				weight = route[v.ID]
			}
			if weight <= 0 {
				continue
			}
			if s.byID[v.ID] == i {
				if !i.loaded || !i.ready {
					s.out.reasons = appendUnique(s.out.reasons, "NoReadyCapacityOrTransitionPrerequisite")
					return false
				}
				if !schedulerOwnerAllows(i.config.OwnerWorkloadUID, v.WorkloadUID) {
					s.out.reasons = appendUnique(s.out.reasons, "IncompatiblePreparedOwner:"+i.config.ID)
					return false
				}
				if i.config.Profile == nil || v.Profile == nil || i.config.Profile.ID != v.Profile.ID {
					s.out.reasons = append(s.out.reasons, "ImmutableNativeProfileMismatch:"+v.ID)
					return false
				}
				i.config = v
				i.policy.MaxBatchSize = v.MaxBatch
				i.policy.MaxBatchWaitMS = float64(v.MaxWait) / 1e6
				i.stopped = false
			}
		}
	}
	s.routingApplied = true
	return true
}

func (s *schedulerReplayState) startActions() bool {
	for _, a := range s.actions {
		if a.done || a.started {
			continue
		}
		if !s.dependenciesReady(a) {
			continue
		}
		switch a.action.Kind {
		case schedulerActionRoute:
			if a.action.Status == phaseRunning {
				s.out.reasons = appendUnique(s.out.reasons, "InProgressRoutingAcknowledgementUnobserved:"+a.action.ID)
				return false
			}
			target := s.goal
			if a.action.RouteTarget != nil {
				target = *a.action.RouteTarget
			}
			if !s.applyRoute(target) {
				return false
			}
			a.done = true
		case schedulerActionStop:
			if i := s.byID[a.action.Instance]; i != nil {
				if s.retainedIngress(i) {
					continue
				}
				i.stopped = true
			}
			a.done = true
		case schedulerActionDrain:
			if i := s.byID[a.action.Instance]; i == nil || i.outstanding == 0 {
				a.done = true
			}
		default:
			if !s.startOperation(a) {
				return false
			}
		}
	}
	return true
}

func (s *schedulerReplayState) startOperation(a *schedulerReplayAction) bool {
	if a.action.Kind != schedulerActionPrepare && a.action.Kind != schedulerActionRemove && a.action.Kind != schedulerActionPower {
		s.out.reasons = append(s.out.reasons, "UnsupportedAction:"+a.action.Kind)
		return false
	}
	if a.action.Kind == schedulerActionPrepare && s.byID[a.action.Instance] == nil {
		s.out.reasons = append(s.out.reasons, "MissingActionInstanceState:"+a.action.ID)
		return false
	}
	if a.action.Kind == schedulerActionPrepare {
		target := s.byID[a.action.Instance]
		if target.config.Node != a.action.Node || schedulerState(target.config) != a.action.To {
			s.out.reasons = appendUnique(s.out.reasons, "PreparationTargetStateMismatch:"+a.action.ID)
			return false
		}
		active := []SchedulerInstance{}
		for _, i := range s.instances {
			if i.loaded || i == s.byID[a.action.Instance] {
				active = append(active, i.config)
			}
		}
		if reasons := schedulerValidateResources(s.settings, s.nodes, active); len(reasons) > 0 {
			s.out.reasons = append(s.out.reasons, "TemporaryReadinessResourceInfeasible")
			s.out.reasons = append(s.out.reasons, reasons...)
			return false
		}
	}
	cost := s.operationCost(a)
	if cost == nil {
		return false
	}
	if !s.sampleOperation(a, cost) {
		return false
	}
	if !s.adjustOperationBackground(a, cost) {
		cost = s.defaultOperation(a, "incompatible operation background estimate")
		if !s.sampleOperation(a, cost) {
			return false
		}
	}
	duration := float64(a.trial.Duration) - float64(a.action.Elapsed)
	a.started = true
	a.start = s.now
	a.finish = s.now + duration
	s.out.sources = appendUnique(s.out.sources, cost.ID)
	s.out.assumptions = appendUnique(s.out.assumptions, "operation trial wall joules replace baseline; paired trial mean wall power for continuation; every planned action completes within the horizon")
	return true
}

func (s *schedulerReplayState) operationCost(a *schedulerReplayAction) *OperationCost {
	if cost := actionDefaultCost(a.action); cost != nil {
		s.out.assumptions = appendUnique(s.out.assumptions, "configured default whole-wall operation cost; not a measurement")
		return cost
	}
	cost := schedulerCost(s.snap.Costs, a.action.CostID)
	_, _, _, valid := capacityOperationCost(cost)
	if valid {
		identities := schedulerMeasurementIdentities(s.snap, nil)
		nearest := a.action.CostProvenance != nil && (a.action.CostProvenance.Source == operationSourceNode || a.action.CostProvenance.Source == operationSourceDeviceClass)
		valid = cost.Reference.Validate() == nil && cost.Kind == a.action.Kind &&
			(nearest || cost.Node.UID == a.action.Node.UID && measurementOperationMatches(*cost, a.action, identities))
		if valid && !nearest && a.action.Kind == schedulerActionPower {
			state := []string{}
			for _, i := range s.instances {
				if i.loaded && i.config.Node.UID == a.action.Node.UID {
					state = append(state, schedulerState(i.config))
				}
			}
			valid = measurementEqualState(state, cost.BeforeState, identities) && measurementEqualState(state, cost.TargetState, identities)
		} else if valid && !nearest {
			valid = cost.PowerMode != "" && cost.PowerMode == schedulerNodeMode(s.nodes, a.action.Node)
		}
	}
	if valid {
		return cost
	}
	return s.defaultOperation(a, "missing or unmatched operation measurement")
}

func (s *schedulerReplayState) sampleOperation(a *schedulerReplayAction, cost *OperationCost) bool {
	if cost == nil {
		return false
	}
	trialOptions := []OperationTrial{}
	for _, t := range cost.Trials {
		if t.Duration <= 0 || !schedulerFinite(t.Joules) || !schedulerValidHalfWidth(t.EnergyUncertaintyJoules) || (cost.EnergyScope == "whole-wall" && t.Joules < 0) {
			continue
		}
		if a.action.Status == phaseRunning && t.Duration <= a.action.Elapsed {
			continue
		}
		trialOptions = append(trialOptions, t)
	}
	if len(trialOptions) == 0 {
		if a.action.CostProvenance != nil && a.action.CostProvenance.Source == operationSourceDefault {
			s.out.reasons = appendUnique(s.out.reasons, "InvalidDefaultOperationCost:"+a.action.Kind)
			return false
		}
		return s.sampleOperation(a, s.defaultOperation(a, "operation exceeded measured duration"))
	}
	a.trial = trialOptions[s.rng.IntN(len(trialOptions))]
	duration := float64(a.trial.Duration)
	a.wallRate = a.trial.Joules / duration
	if cost.EnergyScope == "target-baseline-delta" {
		if cost.SubtractedWatts == nil || !schedulerFinite(*cost.SubtractedWatts) {
			return s.sampleOperation(a, s.defaultOperation(a, "unknown measured energy subtraction"))
		}
		a.wallRate += *cost.SubtractedWatts / 1e9
	} else if cost.EnergyScope != "whole-wall" {
		return s.sampleOperation(a, s.defaultOperation(a, "unknown measured energy scope"))
	}
	return true
}

func (s *schedulerReplayState) adjustOperationBackground(a *schedulerReplayAction, cost *OperationCost) bool {
	if a.action.CostProvenance != nil && a.action.CostProvenance.Source != operationSourceExact {
		return true
	}
	state := []string{}
	active := []SchedulerInstance{}
	var node SchedulerNode
	for _, n := range s.nodes {
		if n.Node == a.action.Node {
			node = n
		}
	}
	for _, i := range s.instances {
		if i.loaded && i.config.Node == a.action.Node && ((a.action.Kind != schedulerActionPrepare && a.action.Kind != schedulerActionRemove) || i != s.byID[a.action.Instance]) {
			state = append(state, schedulerState(i.config))
			active = append(active, i.config)
		}
	}
	measured := append([]string(nil), cost.BeforeState...)
	removedFocal := ""
	if a.action.Kind == schedulerActionPrepare || a.action.Kind == schedulerActionRemove {
		focal := a.action.To
		if a.action.Kind == schedulerActionRemove {
			focal = a.action.From
		}
		identities := schedulerMeasurementIdentities(s.snap, nil)
		for j, measuredState := range measured {
			if MeasurementStateIdentity(measuredState, identities) == MeasurementStateIdentity(focal, identities) {
				removedFocal = measuredState
				measured = append(measured[:j], measured[j+1:]...)
				break
			}
		}
	}
	if !measurementEqualState(state, measured, schedulerMeasurementIdentities(s.snap, nil)) {
		if !s.adjustOperationBaseline(a, cost, node, active, removedFocal) {
			return false
		}
	}
	if a.wallRate < 0 {
		return false
	}
	return true
}

func (s *schedulerReplayState) adjustOperationBaseline(a *schedulerReplayAction, cost *OperationCost, node SchedulerNode, active []SchedulerInstance, removedFocal string) bool {
	if cost.BeforeBaselineWatts == nil || !schedulerFinite(*cost.BeforeBaselineWatts) || *cost.BeforeBaselineWatts < 0 {
		return false
	}
	before := *cost.BeforeBaselineWatts
	if removedFocal != "" {
		focal, ok := s.byID[a.action.Instance]
		if !ok || MeasurementStateIdentity(schedulerState(focal.config), schedulerMeasurementIdentities(s.snap, nil)) != MeasurementStateIdentity(removedFocal, schedulerMeasurementIdentities(s.snap, nil)) {
			return false
		}
		loaded, meter, ss, as := schedulerBaseline(s.settings, s.snap.Baselines, node, []SchedulerInstance{focal.config}, s.snap.Options)
		unloaded, um, us, ua := schedulerBaseline(s.settings, s.snap.Baselines, node, nil, s.snap.Options)
		if meter != cost.Meter || um != meter {
			return false
		}
		before -= loaded - unloaded
		s.out.sources = appendUnique(s.out.sources, append(ss.References, us.References...)...)
		s.out.assumptions = appendUnique(s.out.assumptions, append(as, ua...)...)
	}
	watts, meter, sources, assumptions := schedulerBaseline(s.settings, s.snap.Baselines, node, active, s.snap.Options)
	if before < 0 || meter != cost.Meter {
		return false
	}
	a.wallRate += (watts - before) / 1e9
	s.out.sources = appendUnique(s.out.sources, sources.References...)
	s.out.assumptions = appendUnique(s.out.assumptions, assumptions...)
	s.out.assumptions = appendUnique(s.out.assumptions, "operation whole-wall adjusted by coexist background idle difference only; focal prepare/removal energy retained in measured trial, background interaction unmeasured")
	return true
}

func (s *schedulerReplayState) unlockedActions() bool {
	unlocked := false
	for _, a := range s.actions {
		if a.started || a.done {
			continue
		}
		ok := s.dependenciesReady(a)
		if a.action.Kind == schedulerActionStop && s.retainedIngress(s.byID[a.action.Instance]) {
			ok = false
		}
		if ok && (a.action.Kind != schedulerActionDrain || s.byID[a.action.Instance] == nil || s.byID[a.action.Instance].outstanding == 0) {
			unlocked = true
		}
	}
	return unlocked
}

func schedulerCost(costs []OperationCost, id string) *OperationCost {
	for j := range costs {
		if costs[j].ID == id {
			return &costs[j]
		}
	}
	return nil
}
