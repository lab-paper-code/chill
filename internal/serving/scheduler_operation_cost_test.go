package serving

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestOperationCostDefaultAndAbsentSettings(t *testing.T) {
	s, snap := schedulerFixture()
	n := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	wanted := poolsInstance(snap.Instances[0], "new", n, "fixed", "w1")
	snap.Options = append(snap.Options, poolsOption(wanted))
	capacityLoad(s, &snap, snap.Workloads[0], 150)
	s.DefaultOperationCost = map[string]OperationCostValue{"prepare": {Seconds: .001, Joules: .01}}
	rows := capacityRowsForTest(s, snap, nil)
	d := capacityDecideForTest(t, s, snap, rows)
	if d.Target == nil || !poolsOn(*d.Target, n) {
		t.Fatalf("default did not admit expansion: %+v", d)
	}
	a := requireTransitionAction(t, d.Target.Actions, schedulerActionPrepare)
	if a.CostProvenance == nil || a.CostProvenance.Source != operationSourceDefault || a.CostProvenance.Value.Seconds != .001 || a.CostID != "" {
		t.Fatalf("default provenance: %+v", a)
	}
	s.DefaultOperationCost = nil
	absent := capacityDecideForTest(t, s, snap, rows)
	if !slices.ContainsFunc(absent.Capacity.Plans, func(p SchedulerNodePlan) bool {
		return p.Node == n && p.Eligible && len(p.ActionCosts) > 0 && p.ActionCosts[0].Provenance.Source == operationSourceDefault
	}) {
		t.Fatal("absent key rejected an unmeasured operation")
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SchedulerSettings
	if err = json.Unmarshal(raw, &decoded); err != nil || decoded.Validate() != nil {
		t.Fatalf("absent settings unsafe: %v", err)
	}
	if got := operationCostDefault(decoded, "prepare"); got != schedulerDefaults.DefaultOperationCost["prepare"] {
		t.Fatalf("absent key did not use policy block: %+v", got)
	}
	target := SchedulerCandidate{Nodes: snap.Nodes, Instances: append(slices.Clone(snap.Instances), wanted)}
	s.DefaultOperationCost = map[string]OperationCostValue{"prepare": {Seconds: .001, Joules: .01}}
	target.Actions = schedulerPlan(s, snap, target)
	if result := schedulerReplayForTest(s, snap, target); len(result.reasons) != 0 {
		t.Fatalf("default replay blocked: %v", result.reasons)
	}
}

func TestOperationCostHistoricalProfileAndNearestMeasurement(t *testing.T) {
	s, snap := schedulerFixture()
	i := snap.Instances[0]
	old := "old-profile:[1 2]::1000:1"
	cost := poolsCost(schedulerActionPrepare, i.Node, "unloaded", old, "fixed")
	cost.StateIdentities = map[string]string{"old-profile": MeasurementProfileIdentity(i.Profile)}
	cost.DeviceClass = i.Profile.Condition.DeviceClass
	cost.ID = "older"
	cost.MeasuredAt = snap.At
	newer := cost
	newer.ID = "newer"
	newer.MeasuredAt = snap.At.Add(time.Second)
	tie := newer
	tie.ID = operationCostLatestRef
	snap.Costs = []OperationCost{newer, cost, tie}
	action := Action{Kind: schedulerActionPrepare, Node: i.Node, From: "unloaded", To: schedulerState(i), Status: schedulerActionPlanned}
	for range 2 {
		got := EstimateAction(s, snap, action)
		if got.CostID != operationCostLatestRef || got.CostProvenance.Source != operationSourceExact || got.CostProvenance.CostRef != operationCostLatestRef {
			t.Fatalf("latest stable cell not selected: %+v", got)
		}
		slices.Reverse(snap.Costs)
	}
	cases := []struct {
		name, source string
		cost         *OperationCost
	}{
		{operationSourceNode, operationSourceNode, &cost}, {"device class", operationSourceDeviceClass, &cost}, {operationSourceDefault, operationSourceDefault, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap.Costs = nil
			if tc.cost != nil {
				c := *tc.cost
				c.StateIdentities = nil
				if tc.source == operationSourceDeviceClass {
					c.Node.UID = "other-node"
				}
				snap.Costs = []OperationCost{c}
			}
			got := EstimateAction(s, snap, action)
			if got.CostProvenance == nil || got.CostProvenance.Source != tc.source || got.ExpectedSeconds == nil {
				t.Fatalf("nearest ladder: %+v", got)
			}
		})
	}
}

func TestOperationCostPowerAndIdleHistoricalLoadedState(t *testing.T) {
	s, snap := schedulerFixture()
	i := snap.Instances[0]
	old := "old-profile:[0]::1000:1"
	ids := map[string]string{"old-profile": MeasurementProfileIdentity(i.Profile)}
	cost := poolsCost(schedulerActionPower, i.Node, "fixed", "eco", "")
	cost.BeforeState = []string{old}
	cost.TargetState = []string{old}
	cost.StateIdentities = ids
	snap.Costs = []OperationCost{cost}
	a := EstimateAction(s, snap, Action{Kind: schedulerActionPower, Node: i.Node, From: "fixed", To: "eco", Status: schedulerActionPlanned})
	if a.CostID != cost.ID || a.CostProvenance.Source != operationSourceExact {
		t.Fatalf("loaded power did not match: %+v", a)
	}
	snap.Costs[0].TargetState = nil
	if got := EstimateAction(s, snap, a); got.CostProvenance.Source != operationSourceNode {
		t.Fatalf("power estimate did not fall back to Node measurement: %+v", got)
	}
	baseline := snap.Baselines[0]
	baseline.ID = "loaded-old"
	baseline.State = []string{old}
	baseline.StateIdentities = ids
	baseline.Watts = 17
	watts, _, refs, _ := schedulerBaseline(s, []NodeBaseline{baseline}, snap.Nodes[0], []SchedulerInstance{i}, snap.Options)
	if watts != 17 || !slices.Equal(refs, []string{"loaded-old"}) {
		t.Fatalf("historical idle stranded: %g %v", watts, refs)
	}
	watts, _, refs, _ = schedulerBaseline(s, nil, snap.Nodes[0], []SchedulerInstance{i}, snap.Options)
	if got := idleEstimateProvenance(watts, refs); got.Source != operationSourceDefault || watts != idlePowerDefault(s).NodeWatts+idlePowerDefault(s).PerInstanceWatts {
		t.Fatalf("idle default missing: %+v", got)
	}
}

func TestOperationCostIdleAtModeUsesLatestStableCell(t *testing.T) {
	_, snap := schedulerFixture()
	old := snap.Instances[0]
	target := old
	profile := *old.Profile
	profile.ID = operationCostTestProfile
	profile.Condition.PowerMode = "eco"
	profile.Condition.RuntimeImage = "rotated-runtime-image"
	profile.Condition.Artifact.Image = "new-artifact-version"
	profile.MeasuredAt = old.Profile.MeasuredAt.Add(time.Second)
	target.Profile = &profile
	target.Allocation.MemoryBytes += 500
	got, ok := schedulerIdleAtMode(old, "eco", []SchedulerOption{{Instance: target}})
	if !ok || got.Profile.ID != operationCostTestProfile {
		t.Fatalf("idle evidence stranded by version/memory: %+v %v", got, ok)
	}
	profile.Condition.LogicalModel = "different-model"
	if _, ok := schedulerIdleAtMode(old, "eco", []SchedulerOption{{Instance: target}}); ok {
		t.Fatal("idle evidence matched a different model")
	}
}

func TestOperationCostRetainedKappaUsesCurrentCell(t *testing.T) {
	s, snap := schedulerFixture()
	old := snap.Instances[0]
	newer := old
	p := *old.Profile
	p.ID = operationCostTestProfile
	p.MeasuredAt = p.MeasuredAt.Add(time.Second)
	newer.Profile = &p
	snap.Options = []SchedulerOption{poolsOption(newer)}
	planner := capacityPlannerForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
	if k, row := planner.currentKappa(old); k != 100 || row == "" {
		t.Fatalf("old executor lost latest cell capacity: %g %s", k, row)
	}
}

func TestOperationCostHorizonIsScoredOverCommonWindow(t *testing.T) {
	s, snap := schedulerFixture()
	n := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	wanted := poolsInstance(snap.Instances[0], "new", n, "fixed", "w1")
	snap.Options = append(snap.Options, poolsOption(wanted))
	s.DefaultOperationCost = map[string]OperationCostValue{"prepare": {Seconds: 2, Joules: 30}}
	p := capacityPlannerForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
	choice := capacityChoiceForTest(t, p, map[string]string{n.UID: planKey([]SchedulerInstance{wanted})})
	res := p.evaluate(choice, p.routeOf(choice))
	if res.exclusion == schedulerTransitionHorizon || res.period != 2+s.ComparisonPeriod.Seconds() {
		t.Fatalf("horizon veto/window: %+v", res)
	}
	keep := p.evaluateAtWindow(make([]int, len(choice)), 0, nil, res.period)
	if res.keepJoules != keep.joules || res.joules <= 30 {
		t.Fatalf("transition or same-window keep energy omitted: plan %g keep %g/%g", res.joules, res.keepJoules, keep.joules)
	}
}

func TestOperationCostSettingsValidation(t *testing.T) {
	for _, v := range []OperationCostValue{{Seconds: 0, Joules: 1}, {Seconds: -1, Joules: 1}, {Seconds: 1, Joules: -1}, {Seconds: 1e100, Joules: 1}, {Seconds: 1e-20, Joules: 1}} {
		s, _ := schedulerFixture()
		s.DefaultOperationCost = map[string]OperationCostValue{"prepare": v}
		if s.Validate() == nil {
			t.Fatalf("accepted invalid default %+v", v)
		}
	}
	s, _ := schedulerFixture()
	s.DefaultOperationCost = map[string]OperationCostValue{"unknown": {Seconds: 1, Joules: 1}}
	if s.Validate() == nil {
		t.Fatal("unknown operation accepted")
	}
	resolved := schedulerOperationSettings(schedulerDefaults)
	resolved.DefaultOperationCost["prepare"] = OperationCostValue{Seconds: 1}
	if schedulerDefaults.DefaultOperationCost["prepare"].Seconds == 1 {
		t.Fatal("settings mutated embedded policy")
	}
}

// Optional offline smoke consumes captured API data and caches actual computed
// capacity rows. It never contacts a cluster or replaces measured service data.
func TestOperationCostLiveSnapshotReplay(t *testing.T) {
	filename := os.Getenv("CHILL_OPERATION_COST_REPLAY")
	if filename == "" {
		t.Skip("set CHILL_OPERATION_COST_REPLAY to captured scheduler API JSON")
	}
	raw, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Settings SchedulerSettings `json:"settings"`
		State    struct {
			Snapshot SchedulerSnapshot `json:"snapshot"`
		} `json:"state"`
	}
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	s, snap := schedulerOperationSettings(capture.Settings), capture.State.Snapshot
	if filename := os.Getenv("CHILL_OPERATION_COST_PROFILES"); filename != "" {
		raw, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		var catalog struct {
			Profiles []Profile `json:"profiles"`
		}
		if err = json.Unmarshal(raw, &catalog); err != nil {
			t.Fatal(err)
		}
		ids, classes := map[string]string{}, map[string]string{}
		for j := range catalog.Profiles {
			p := &catalog.Profiles[j]
			ids[p.ID] = MeasurementProfileIdentity(p)
			classes[p.Condition.NodeUID] = p.Condition.DeviceClass
		}
		for j := range snap.Costs {
			snap.Costs[j].StateIdentities = ids
			snap.Costs[j].DeviceClass = classes[snap.Costs[j].Node.UID]
		}
		for j := range snap.Baselines {
			snap.Baselines[j].StateIdentities = ids
			snap.Baselines[j].DeviceClass = classes[snap.Baselines[j].Node.UID]
		}
	}
	table, cachedCount := operationCostReplayTable(t)
	table.Ensure(context.Background(), s, snap)
	table.Wait()
	rows := table.Rows()
	t.Logf("actual capacity rows=%d cached=%d", len(rows), cachedCount)
	before := capacityPlannerForTest(t, s, snap, rows)
	beforeEligible := 0
	for _, plans := range before.plans {
		for _, p := range plans {
			if p.preMax+p.postMax > before.period {
				p.record.Eligible = false
			}
			if p.record.Eligible {
				beforeEligible++
			}
		}
	}
	old, err := before.search(time.Now().Add(s.ControlInterval / 2))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("before horizon removal: eligible=%d choice=%s final=%v feasible=%v", beforeEligible, old.key, old.final, old.feasible)
	d, err := EvaluateScheduler(context.Background(), s, snap, rows)
	if err != nil {
		t.Fatal(err)
	}
	eligible := 0
	reasons, sources := map[string]int{}, map[string]int{}
	for _, p := range d.Capacity.Plans {
		if p.Eligible {
			eligible++
		} else {
			reasons[p.Reason]++
		}
		for _, c := range p.ActionCosts {
			if c.Provenance != nil {
				sources[c.Provenance.Source]++
			}
		}
	}
	t.Logf("after horizon removal: eligible=%d plans=%d status=%s reasons=%v sources=%v search=%+v workloads=%+v", eligible, len(d.Capacity.Plans), d.Status, reasons, sources, d.Capacity.Search, d.Capacity.Workloads)
	if d.Target == nil {
		t.Fatalf("no target: reasons=%v", d.Reasons)
	}
	selected := map[string]int{}
	for _, a := range d.Target.Actions {
		if a.CostProvenance != nil {
			selected[a.CostProvenance.Source]++
		}
	}
	t.Logf("chosen=%s instances=%d selected-cost-sources=%v period=%s", d.Target.ID, len(d.Target.Instances), selected, d.Capacity.Period)
	operationCostReplayReport(t, map[string]any{
		"capture": filename,
		"before":  map[string]any{"eligible": beforeEligible, "choice": old.key, "finalSLO": old.final, "transitionSLO": old.feasible, "routeSeconds": old.route, "windowSeconds": old.period},
		"after":   map[string]any{"eligible": eligible, "decision": d, "allCostSources": sources, "selectedCostSources": selected, "remainingRejections": reasons},
	})
	for _, uid := range s.AutonomousWorkloads {
		if !slices.ContainsFunc(d.Target.Instances, func(i SchedulerInstance) bool { return i.WorkloadUID == uid && d.Target.Routing[uid][i.ID] > 0 }) {
			t.Fatalf("no positive instance for %s", uid)
		}
	}
}

const (
	operationCostLatestRef   = "cost-latest-tie"
	operationCostTestProfile = "cost-current-profile"
)

func operationCostReplayTable(t *testing.T) (*CapacityTable, int) {
	t.Helper()
	cached := map[string]CapacityRow{}
	var record func(CapacityRow) error
	if filename := os.Getenv("CHILL_OPERATION_COST_ROWS"); filename != "" {
		raw, err := os.ReadFile(filename)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		for {
			var row CapacityRow
			err := decoder.Decode(&row)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			cached[row.Key] = row
		}
		file, err := os.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		var mu sync.Mutex
		record = func(row CapacityRow) error { mu.Lock(); defer mu.Unlock(); return json.NewEncoder(file).Encode(row) }
	}
	table := NewCapacityTable(2, record, nil)
	// Offline fixtures already contain fully computed rows. Production's load
	// callback intentionally supplies only Previous rows while recomputing.
	table.rows = cached
	return table, len(cached)
}

func operationCostReplayReport(t *testing.T, report map[string]any) {
	t.Helper()
	if path := os.Getenv("CHILL_OPERATION_COST_RESULT"); path != "" {
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
