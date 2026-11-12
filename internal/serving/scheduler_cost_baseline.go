package serving

func (s *schedulerReplayState) defaultOperation(a *schedulerReplayAction, reason string) *OperationCost {
	value := s.settings.DefaultOperationCost[a.action.Kind]
	if !schedulerFinite(value.Seconds) || value.Seconds <= 0 || !schedulerFinite(value.Joules) || value.Joules < 0 {
		s.out.reasons = appendUnique(s.out.reasons, "InvalidDefaultOperationCost:"+a.action.Kind)
		return nil
	}
	a.action.CostID = ""
	a.action.CostProvenance = &OperationCostProvenance{Source: operationSourceDefault, Value: &value}
	s.out.assumptions = appendUnique(s.out.assumptions, "configured default whole-wall operation cost: "+reason)
	return actionDefaultCost(a.action)
}

func schedulerValidHalfWidth(value float64) bool {
	return schedulerFinite(value) && value >= 0
}

func selectIdleMeasurements(baselines []NodeBaseline, node SchedulerNode, state []string, meter string, identities, classes map[string]string) (unloaded, selected *NodeBaseline, selectedRank int) {
	selectedRank = 3
	for j := range baselines {
		b := &baselines[j]
		if b.Scope != schedulerNodeWallScope || b.Meter == "" || !schedulerFinite(b.Watts) || b.Watts < 0 || !schedulerValidHalfWidth(b.WattsUncertainty) || b.ID == "" || b.Reference.Validate() != nil {
			continue
		}
		class := b.DeviceClass
		if class == "" {
			class = classes[b.Node.UID]
		}
		rank := measurementRank(b.Node.UID, class, node.Node.UID, classes[node.Node.UID],
			b.PowerMode == node.PowerMode && (meter == "" || b.Meter == meter) && measurementEqualState(b.State, state, identities))
		if rank < 3 && (selected == nil || measurementPreferred(rank, b.MeasuredAt, b.ID, selectedRank, selected.MeasuredAt, selected.ID)) {
			selected, selectedRank = b, rank
		}
		if b.Node.UID == node.Node.UID && b.PowerMode == node.PowerMode && len(b.State) == 0 &&
			(unloaded == nil || measurementPreferred(0, b.MeasuredAt, b.ID, 0, unloaded.MeasuredAt, unloaded.ID)) {
			unloaded = b
		}
	}
	return unloaded, selected, selectedRank
}

func schedulerBaseline(settings SchedulerSettings, baselines []NodeBaseline, node SchedulerNode, instances []SchedulerInstance, options []SchedulerOption) (float64, string, IdlePowerProvenance, []string) {
	identities := schedulerMeasurementIdentities(SchedulerSnapshot{Baselines: baselines, Instances: instances, Options: options}, nil)
	state := make([]string, 0, len(instances))
	meter := ""
	if len(instances) > 0 {
		meter = instances[0].IdleMeter
	}
	for _, i := range instances {
		state = append(state, schedulerState(i))
	}
	classes := measurementDeviceClasses(SchedulerSnapshot{Instances: instances, Options: options}, nil)
	unloaded, selected, rank := selectIdleMeasurements(baselines, node, state, meter, identities, classes)
	if selected != nil && rank == 0 {
		provenance := IdlePowerProvenance{Source: operationSourceExact, Watts: selected.Watts, References: []string{selected.ID}}
		return selected.Watts, selected.Meter, provenance, nil
	}
	if len(instances) == 1 {
		i := instances[0]
		if p := i.Profile; p != nil && p.Condition.NodeUID == node.Node.UID && p.Condition.PowerMode == node.PowerMode &&
			i.IdleMeter != "" && i.IdleScope == schedulerNodeWallScope && schedulerFinite(p.IdleBaselineWatts) && p.IdleBaselineWatts >= 0 {
			provenance := IdlePowerProvenance{Source: operationSourceExact, Watts: p.IdleBaselineWatts, References: []string{p.ID}}
			return provenance.Watts, i.IdleMeter, provenance, nil
		}
	}
	if unloaded != nil {
		if provenance, ok := reconstructIdlePower(unloaded, instances, node.PowerMode, options); ok {
			return provenance.Watts, unloaded.Meter, provenance,
				[]string{"derived unloaded baseline plus measured per-instance idle increments; coexistence interaction unmeasured"}
		}
	}
	if selected != nil {
		scope := selected.Meter
		if rank == 2 {
			scope = "estimate:" + node.Node.UID + ":" + selected.Meter
		}
		provenance := IdlePowerProvenance{Source: measurementSource(rank), Watts: selected.Watts, References: []string{selected.ID}}
		return selected.Watts, scope, provenance,
			[]string{"nearest idle measurement transferred to target cell; not an exact target measurement"}
	}
	fallback := settings.DefaultIdlePower
	watts := fallback.NodeWatts + float64(len(instances))*fallback.PerInstanceWatts
	provenance := IdlePowerProvenance{Source: operationSourceDefault, Watts: watts}
	return watts, "default:" + node.Node.UID, provenance,
		[]string{"configured idle-power estimate replaces absent or incompatible baseline measurement; not measured"}
}

func reconstructIdlePower(unloaded *NodeBaseline, instances []SchedulerInstance, mode string, options []SchedulerOption) (IdlePowerProvenance, bool) {
	watts := unloaded.Watts
	sources := []string{unloaded.ID}
	for _, i := range instances {
		effective, ok := schedulerIdleAtMode(i, mode, options)
		p := effective.Profile
		if !ok || p == nil || !schedulerFinite(p.IdleBaselineWatts) || p.IdleBaselineWatts < 0 ||
			effective.IdleMeter != unloaded.Meter || effective.IdleScope != schedulerNodeWallScope {
			return IdlePowerProvenance{}, false
		}
		watts += p.IdleBaselineWatts - unloaded.Watts
		sources = appendUnique(sources, p.ID)
	}
	if !schedulerFinite(watts) || watts < 0 {
		return IdlePowerProvenance{}, false
	}
	return IdlePowerProvenance{Source: "reconstructed", Watts: watts, References: sources, Reconstructed: true}, true
}

func schedulerIdleAtMode(i SchedulerInstance, mode string, options []SchedulerOption) (SchedulerInstance, bool) {
	if i.Profile == nil {
		return i, false
	}
	if i.Profile.Condition.PowerMode == mode {
		return i, true
	}
	cell := MeasurementProfileIdentity(i.Profile)
	var selected *SchedulerInstance
	for index := range options {
		j := &options[index].Instance
		if j.Profile == nil || j.Node.UID != i.Node.UID || j.Profile.Condition.PowerMode != mode || MeasurementProfileIdentity(j.Profile) != cell {
			continue
		}
		if selected == nil || j.Profile.MeasuredAt.After(selected.Profile.MeasuredAt) ||
			j.Profile.MeasuredAt.Equal(selected.Profile.MeasuredAt) && j.Profile.ID < selected.Profile.ID {
			selected = j
		}
	}
	if selected != nil {
		return *selected, true
	}
	return i, false
}
