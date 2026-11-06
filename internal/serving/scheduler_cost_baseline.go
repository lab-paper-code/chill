package serving

func (s *schedulerReplayState) defaultOperation(a *schedulerReplayAction, reason string) *OperationCost {
	value := s.settings.DefaultOperationCost[a.action.Kind]
	a.action.CostID = ""
	a.action.CostProvenance = &OperationCostProvenance{Source: operationSourceDefault, Value: &value}
	s.out.assumptions = appendUnique(s.out.assumptions, "configured default whole-wall operation cost: "+reason)
	return actionDefaultCost(a.action)
}

func schedulerValidHalfWidth(value float64) bool {
	return schedulerFinite(value) && value >= 0
}

func selectIdleMeasurements(baselines []NodeBaseline, node SchedulerNode, state []string, identities, classes map[string]string) (unloaded, selected *NodeBaseline, selectedRank int) {
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
			b.PowerMode == node.PowerMode && measurementEqualState(b.State, state, identities))
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
	for _, i := range instances {
		state = append(state, schedulerState(i))
	}
	classes := measurementDeviceClasses(SchedulerSnapshot{Instances: instances, Options: options}, nil)
	unloaded, selected, rank := selectIdleMeasurements(baselines, node, state, identities, classes)
	if selected != nil && rank == 0 {
		provenance := IdlePowerProvenance{Source: operationSourceExact, Watts: selected.Watts, References: []string{selected.ID}}
		return selected.Watts, selected.Meter, provenance, nil
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
	return IdlePowerProvenance{Source: operationSourceExact, Watts: watts, References: sources, Reconstructed: true}, true
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
