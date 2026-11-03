package serving

import (
	"fmt"
	"reflect"
	"sort"
)

func schedulerValidHalfWidth(value float64) bool {
	return schedulerFinite(value) && value >= 0
}

func schedulerBaseline(baselines []NodeBaseline, node SchedulerNode, instances []SchedulerInstance, options []SchedulerOption) (float64, string, []string, []string, string) {
	state := []string{}
	for _, i := range instances {
		state = append(state, schedulerState(i))
	}
	sort.Strings(state)
	var matched, unloaded *NodeBaseline
	for j := range baselines {
		b := &baselines[j]
		if b.Node != node.Node || b.PowerMode != node.PowerMode || b.Scope != schedulerNodeWallScope || b.Meter == "" || !schedulerFinite(b.Watts) || b.Watts < 0 || !schedulerValidHalfWidth(b.WattsUncertainty) || b.ID == "" || b.Reference.Validate() != nil {
			continue
		}
		bs := append([]string(nil), b.State...)
		sort.Strings(bs)
		if fmt.Sprint(bs) == fmt.Sprint(state) && (matched == nil || b.MeasuredAt.After(matched.MeasuredAt)) {
			matched = b
		}
		if len(bs) == 0 && (unloaded == nil || b.MeasuredAt.After(unloaded.MeasuredAt)) {
			unloaded = b
		}
	}
	if matched != nil {
		return matched.Watts, matched.Meter, []string{matched.ID}, nil, ""
	}
	if unloaded == nil {
		return 0, "", nil, nil, "MissingUnloadedNodeBaseline:" + node.Node.UID + ":" + node.PowerMode
	}
	watts := unloaded.Watts
	sources := []string{unloaded.ID}
	for _, i := range instances {
		effective, ok := schedulerIdleAtMode(i, node.PowerMode, options)
		if !ok {
			return 0, "", nil, nil, "MissingRetainedRunnerTargetPowerProfile:" + i.ID
		}
		p := effective.Profile
		if p == nil || !schedulerFinite(p.IdleBaselineWatts) || p.IdleBaselineWatts < 0 || len(p.Evidence) == 0 {
			return 0, "", nil, nil, "MissingExactIdleSubtraction:" + i.ID
		}
		if effective.IdleMeter != unloaded.Meter || effective.IdleScope != schedulerNodeWallScope {
			return 0, "", nil, nil, "IdleMeterScopeMismatch:" + i.ID
		}
		watts += p.IdleBaselineWatts - unloaded.Watts
		sources = appendUnique(sources, p.ID)
	}
	if !schedulerFinite(watts) || watts < 0 {
		return 0, "", nil, nil, "DerivedNodeBaselineOutsidePhysicalRange:" + node.Node.UID
	}
	return watts, unloaded.Meter, sources, []string{"derived unloaded node baseline + (profile estimator's exact loaded idle - unloaded) once per physical instance; coexistence interference unmeasured; retained immutable bundles at a changed host mode use only matching measured mode/resource/model idle evidence"}, ""
}

// schedulerEqualState compares loaded-state multisets under schedulerCostState.
func schedulerEqualState(a, b []string) bool {
	norm := func(states []string) []string {
		out := make([]string, len(states))
		for k, s := range states {
			out[k] = schedulerCostState(s)
		}
		sort.Strings(out)
		return out
	}
	return reflect.DeepEqual(norm(a), norm(b))
}

func schedulerIdleAtMode(i SchedulerInstance, mode string, options []SchedulerOption) (SchedulerInstance, bool) {
	if i.Profile == nil {
		return i, false
	}
	if i.Profile.Condition.PowerMode == mode {
		return i, true
	}
	p := i.Profile.Condition
	for _, o := range options {
		j := o.Instance
		if j.Profile == nil || j.Node != i.Node || !reflect.DeepEqual(i.Allocation, j.Allocation) {
			continue
		}
		q := j.Profile.Condition
		if q.PowerMode == mode && p.RuntimeFamily == q.RuntimeFamily && p.Backend == q.Backend && reflect.DeepEqual(p.Artifact, q.Artifact) && reflect.DeepEqual(p.Preprocessor, q.Preprocessor) && reflect.DeepEqual(p.ActiveProtocol.Dataset, q.ActiveProtocol.Dataset) && reflect.DeepEqual(p.Firstfloor, q.Firstfloor) && reflect.DeepEqual(p.RuntimeAccess, q.RuntimeAccess) && reflect.DeepEqual(p.PlannedBatchSizes, q.PlannedBatchSizes) {
			return j, true
		}
	}
	return i, false
}
