package serving

import (
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// poolsAddNode appends a Ready fixture Node with one unloaded baseline per mode.
func poolsAddNode(snap *SchedulerSnapshot, uid string, watts map[string]float64) NodeReference {
	n := snap.Nodes[0]
	n.Node = NodeReference{Name: "node-" + uid, UID: uid}
	snap.Nodes = append(snap.Nodes, n)
	for mode, w := range watts {
		b := snap.Baselines[0]
		b.ID, b.Node, b.PowerMode, b.Watts, b.Meter = "unloaded-"+uid+"-"+mode, n.Node, mode, w, "wall-"+uid
		b.Reference.Key = b.ID
		snap.Baselines = append(snap.Baselines, b)
	}
	return n.Node
}

// poolsInstance copies the fixture instance onto node at mode, serving uid.
func poolsInstance(base SchedulerInstance, id string, node NodeReference, mode, uid string) SchedulerInstance {
	p := *base.Profile
	p.ID, p.NodeName, p.Condition.NodeUID, p.Condition.PowerMode = p.ID+"@"+node.UID+"-"+mode, node.Name, node.UID, mode
	base.ID, base.Node, base.Profile, base.WorkloadUID, base.IdleMeter = id, node, &p, uid, "wall-"+node.UID
	return base
}

// poolsCost is a measured operation of 1 ms and 0.01 J.
func poolsCost(kind string, node NodeReference, from, to, mode string, loaded ...string) OperationCost {
	id := kind + "-" + node.UID + "-" + to
	return OperationCost{ID: id, Node: node, Kind: kind, From: from, To: to, PowerMode: mode, BeforeState: loaded, TargetState: loaded,
		Trials: []OperationTrial{{Duration: time.Millisecond, Joules: 0.01}}, EnergyScope: "whole-wall", Meter: "wall-" + node.UID,
		Scope: schedulerNodeWallScope, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: id, Size: 1}}
}

func poolsOption(i SchedulerInstance) SchedulerOption {
	return SchedulerOption{Instance: i, CompatibleUIDs: []string{i.WorkloadUID}}
}

func poolsOn(c SchedulerCandidate, node NodeReference) bool {
	return slices.ContainsFunc(c.Instances, func(i SchedulerInstance) bool { return i.Node == node })
}

func poolsAddWorkload(snap *SchedulerSnapshot, uid string) *Definition {
	d := *snap.Workloads[0]
	d.Name, d.UID = "workload-"+uid, uid
	snap.Workloads = append(snap.Workloads, &d)
	return &d
}

// poolsPlans lists the recorded node plans on node.
func poolsPlans(d SchedulerDecision, node NodeReference) []SchedulerNodePlan {
	if d.Capacity == nil {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(d.Capacity.Plans), func(p SchedulerNodePlan) bool { return p.Node != node })
}

func poolsEligibleChange(d SchedulerDecision, node NodeReference) bool {
	return slices.ContainsFunc(poolsPlans(d, node), func(p SchedulerNodePlan) bool { return p.Eligible && p.Key != "keep" })
}

// §9.1 소속 도출.
func TestBuildPoolsDerivesActivePoolsFromInstances(t *testing.T) {
	ref := func(uid string) NodeReference { return NodeReference{Name: "node-" + uid, UID: uid} }
	bound := func(uid string) []WorkloadReference { return []WorkloadReference{{Name: "name-" + uid, UID: uid}} }
	instances := []InstanceState{
		{ID: "owner-only", Node: ref("n1"), OwnerWorkloadUID: "w1"},
		{ID: "binding-only", Node: ref("n2"), Workloads: bound("w2")},
		{ID: "shared-node", Node: ref("n1"), Workloads: bound("w2")},
		{ID: "unowned-unbound", Node: ref("n3")},
		{ID: "measured", Node: ref("m")},
	}
	nodes := []SchedulerNode{
		{Node: ref("n1"), Ready: true}, {Node: ref("n2"), Ready: true}, {Node: ref("n3"), Ready: true},
		{Node: ref("n4"), Ready: true}, {Node: ref("n5"), Reasons: []string{NodePowerModeUnavailable}}, {Node: ref("m"), Ready: true, Measurement: true},
	}
	pools := BuildPools(instances, []NodeReference{ref("m")}, nodes, map[string]string{"w1": "name-w1", "w2": "name-w2"})
	active := map[string][]NodeReference{}
	for _, pool := range pools.Workloads {
		active[pool.Workload.UID] = pool.Nodes
	}
	if want := map[string][]NodeReference{"w1": {ref("n1")}, "w2": {ref("n1"), ref("n2")}}; !reflect.DeepEqual(active, want) {
		t.Fatalf("active POOLs %v, want %v", active, want)
	}
	if len(pools.Measurement) != 1 || pools.Measurement[0].Node != ref("m") || len(pools.Measurement[0].Instances) != 1 || pools.Measurement[0].Instances[0].ID != "measured" {
		t.Fatalf("measurement POOL %+v", pools.Measurement)
	}
	names := func(nodes []PoolNode) (out []string) {
		for _, n := range nodes {
			out = append(out, n.Node.UID)
		}
		return out
	}
	if got := names(pools.Available); !slices.Equal(got, []string{"n4"}) {
		t.Fatalf("available %v", got)
	}
	if got := names(pools.Other); !slices.Equal(got, []string{"n3", "n5"}) {
		t.Fatalf("other %v", got)
	}
}

// §9.1 Workload 판단 상태.
func TestSchedulerDecisionRecordsWorkloadStates(t *testing.T) {
	s, snap := schedulerFixture()
	poolsAddWorkload(&snap, "w2")
	poolsAddWorkload(&snap, "w3")
	s.AutonomousWorkloads = []string{"w1", "w2"}
	want := []SchedulerWorkloadState{
		{UID: "w1", State: WorkloadControlled},
		{UID: "w2", State: WorkloadMeasurementEvidenceMissing, Reason: "NoCompletedMeasurementMatches"},
		{UID: "w3", State: WorkloadDisabled, Reason: "AutonomousControlOff"},
	}
	if got := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, nil)).Workloads; !reflect.DeepEqual(got, want) {
		t.Fatalf("workload states %+v, want %+v", got, want)
	}
}

// §9.1 전체 POOL: Nodes lacking configuration stay with their reasons.
func TestSchedulerNodeReasonsKeepNodeWithoutPlacement(t *testing.T) {
	for _, reason := range []string{"", NodePlacementInventoryMissing, NodePowerModeUnavailable, NodeInstanceUnavailable} {
		t.Run("reason="+reason, func(t *testing.T) {
			s, snap := schedulerFixture()
			node := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6})
			if reason != "" {
				snap.Nodes[1].Reasons = []string{reason}
			}
			option := poolsInstance(snap.Instances[0], "opt", node, schedulerTestEcoMode, "w1")
			snap.Options = append(snap.Options, poolsOption(option))
			snap.Costs = []OperationCost{poolsCost(schedulerActionPrepare, node, "unloaded", schedulerState(option), schedulerTestEcoMode)}
			d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
			if !slices.Equal(d.Keep.Nodes[1].Reasons, snap.Nodes[1].Reasons) {
				t.Fatalf("Node reasons lost: %+v", d.Keep.Nodes[1])
			}
			lowered := slices.ContainsFunc(d.Keep.Actions, func(a Action) bool { return a.Node == node && a.Fixed })
			kept := reason == NodePowerModeUnavailable || reason == NodeInstanceUnavailable
			if kept && (lowered || d.Keep.Nodes[1].PowerMode != "fixed") {
				t.Fatalf("%s Node changed mode: %+v", reason, d.Keep)
			}
			if !kept && (!lowered || d.Keep.Nodes[1].PowerMode != schedulerTestEcoMode) {
				t.Fatalf("available Node was not lowered: %+v", d.Keep)
			}
			if placed := poolsEligibleChange(d, node); placed != (reason == "") {
				t.Fatalf("placed=%v with reason %q: %+v", placed, reason, poolsPlans(d, node))
			}
			// The other Nodes are still planned: one unreadable instance never makes the decision wait.
			if reason == NodeInstanceUnavailable && d.Keep.Status == schedulerStatusPending {
				t.Fatalf("one Node's unreadable instance made the decision Pending: %+v", d.Keep.Reasons)
			}
			if (reason == NodePlacementInventoryMissing || reason == NodeInstanceUnavailable) && !slices.ContainsFunc(poolsPlans(d, node), func(p SchedulerNodePlan) bool { return p.Reason == reason }) {
				t.Fatalf("excluded Node plan lacks its reason: %+v", poolsPlans(d, node))
			}
		})
	}
}

// §9.1 전체 POOL: ComparedNodes are the Nodes with an eligible change; only
// their baselines are needed.
func TestSchedulerComparedNodesLimitEnergyAndBaselineNeeds(t *testing.T) {
	s, snap := schedulerFixture()
	s.MaxReplicas = 1
	base := snap.Instances[0]
	poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6})
	poolsAddNode(&snap, "n3", nil)
	move := poolsAddNode(&snap, "n4", map[string]float64{"fixed": 10})
	eco := poolsAddNode(&snap, "n5", map[string]float64{"fixed": 10})
	moved := poolsInstance(base, "opt-move", move, "fixed", "w1")
	raised := poolsInstance(base, "opt-eco", eco, schedulerTestEcoMode, "w1")
	snap.Options = append(snap.Options, poolsOption(moved), poolsOption(raised))
	snap.Costs = []OperationCost{
		poolsCost(schedulerActionPrepare, move, "unloaded", schedulerState(moved), "fixed"),
		poolsCost(schedulerActionRemove, base.Node, schedulerState(base), "unloaded", "fixed", schedulerState(base)),
		poolsCost(schedulerActionPower, eco, "fixed", schedulerTestEcoMode, ""),
		poolsCost(schedulerActionPrepare, eco, "unloaded", schedulerState(raised), schedulerTestEcoMode),
	}
	d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
	if want := []NodeReference{base.Node, move}; !slices.Equal(d.Search.ComparedNodes, want) {
		t.Fatalf("ComparedNodes %v, want %v", d.Search.ComparedNodes, want)
	}
	if d.Keep.ExpectedJoules == nil || d.Target == nil {
		t.Fatalf("a Node without baseline outside ComparedNodes stopped the decision: %+v", d)
	}
	if !slices.ContainsFunc(poolsPlans(d, eco), func(p SchedulerNodePlan) bool {
		return strings.HasPrefix(p.Reason, "MissingUnloadedNodeBaseline:n5:"+schedulerTestEcoMode)
	}) {
		t.Fatalf("eco plan without its mode's baseline was not excluded: %+v", poolsPlans(d, eco))
	}
}

// §6, §9 row 6: a Node with a disabled Workload's instance is keep only, and
// that Workload is never judged.
func TestSchedulerDisabledWorkloadInstanceIsFixed(t *testing.T) {
	s, snap := schedulerFixture()
	poolsAddWorkload(&snap, "w2")
	disabled := schedulerTestCPU(snap.Instances[0])
	disabled.ID, disabled.WorkloadUID = "i2", "w2"
	snap.Instances = append(snap.Instances, disabled)
	snap.Routing = map[string]map[string]int32{"w1": {"i1": 1}, "w2": {"i2": 1}}
	node := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	option := poolsInstance(snap.Instances[0], "opt", node, "fixed", "w1")
	snap.Options = append(snap.Options, poolsOption(option))
	snap.Costs = []OperationCost{poolsCost(schedulerActionPrepare, node, "unloaded", schedulerState(option), "fixed")}
	capacityLoad(s, &snap, snap.Workloads[0], 150)
	d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, func(SchedulerOption) float64 { return 100 }))
	if d.Target == nil || !poolsOn(*d.Target, node) {
		t.Fatalf("fixture must add the n2 instance: %+v", d)
	}
	if !slices.ContainsFunc(poolsPlans(d, snap.Nodes[0].Node), func(p SchedulerNodePlan) bool { return p.Reason == schedulerPlanFixedWorkload }) {
		t.Fatalf("the disabled Workload's Node took a plan: %+v", poolsPlans(d, snap.Nodes[0].Node))
	}
	if !slices.ContainsFunc(d.Target.Instances, func(i SchedulerInstance) bool { return reflect.DeepEqual(i, disabled) }) ||
		!reflect.DeepEqual(d.Target.Routing["w2"], snap.Routing["w2"]) ||
		slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return a.Instance == disabled.ID }) {
		t.Fatalf("target changed the disabled Workload's instance: %+v", d.Target)
	}
	if len(d.Capacity.Workloads) != 1 || d.Capacity.Workloads[0].UID != "w1" {
		t.Fatalf("a disabled Workload entered the judgment: %+v", d.Capacity.Workloads)
	}
}

// §3: a controlled Workload needs an instance even at zero load.
func TestSchedulerMinimumInstanceSelectsFirstConfiguration(t *testing.T) {
	s, snap := schedulerFixture()
	option := snap.Options[0].Instance
	snap.Instances = nil
	snap.Costs = []OperationCost{poolsCost(schedulerActionPrepare, option.Node, "unloaded", schedulerState(option), "fixed")}
	d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
	if d.Keep.Status != SchedulerStatusOverload || d.Target == nil || d.Target.Keep || d.Status != schedulerStatusFeasible || !poolsOn(*d.Target, option.Node) {
		t.Fatalf("first configuration was not the target: %+v", d)
	}
}

// §4: the instances on one Node reference one ModelSpec.
func TestSchedulerSameNodeRequiresSameModel(t *testing.T) {
	for _, same := range []bool{true, false} {
		s, snap := schedulerFixture()
		s.AutonomousWorkloads = []string{"w1", "w2"}
		other := poolsAddWorkload(&snap, "w2")
		if !same {
			other.Model.UID = "m2"
		}
		node := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
		current := poolsInstance(snap.Instances[0], "i2", node, "fixed", "w2")
		snap.Instances = append(snap.Instances, current)
		snap.Routing = map[string]map[string]int32{"w1": {"i1": 1}, "w2": {"i2": 1}}
		added := poolsInstance(schedulerTestCPU(snap.Instances[0]), "opt", node, "fixed", "w1")
		snap.Options = append(snap.Options, poolsOption(current), poolsOption(added))
		d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
		both := slices.ContainsFunc(poolsPlans(d, node), func(p SchedulerNodePlan) bool {
			return strings.Contains(p.Key, "/w1") && strings.Contains(p.Key, "/w2")
		})
		if both != same {
			t.Fatalf("same model=%v planned both Workloads on one Node=%v", same, both)
		}
	}
}

// §4: moving a Node between active POOLs is remove + prepare in one plan.
func TestSchedulerDirectMoveBetweenActivePools(t *testing.T) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads = []string{"w1", "w2"}
	poolsAddWorkload(&snap, "w2").Model.UID = "m2"
	shared := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	spare := poolsAddNode(&snap, "n3", map[string]float64{"fixed": 10})
	current := poolsInstance(snap.Instances[0], "i2", shared, "fixed", "w2")
	snap.Instances = append(snap.Instances, current)
	snap.Routing = map[string]map[string]int32{"w1": {"i1": 1}, "w2": {"i2": 1}}
	// A different CPU, so w2's runner cannot be reused as w1's slot.
	w1 := poolsInstance(schedulerTestCPU(snap.Instances[0]), "opt-w1", shared, "fixed", "w1")
	w2 := poolsInstance(snap.Instances[0], "opt-w2", spare, "fixed", "w2")
	snap.Options = append(snap.Options, poolsOption(current), poolsOption(w1), poolsOption(w2))
	snap.Costs = []OperationCost{
		poolsCost(schedulerActionRemove, shared, schedulerState(current), "unloaded", "fixed"),
		poolsCost(schedulerActionPrepare, shared, "unloaded", schedulerState(w1), "fixed"),
	}
	d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
	i := slices.IndexFunc(poolsPlans(d, shared), func(p SchedulerNodePlan) bool { return p.Key == "profile-other-cpu@n2-fixed/b1/w1" })
	if i < 0 {
		t.Fatalf("no plan moved the Node from w2's to w1's active POOL: %+v", poolsPlans(d, shared))
	}
	if plan := poolsPlans(d, shared)[i]; !plan.Eligible || plan.Pre == 0 || plan.Post == 0 || plan.KappaRetained["w2"] == 0 {
		t.Fatalf("move must prepare before and remove after the route, w2 serving until then: %+v", plan)
	}
}

// §4: available Nodes start at their lowest mode; re-entry uses the empty-Node power cost.
func TestSchedulerAvailableNodeLoweringAndReentry(t *testing.T) {
	s, snap := schedulerFixture()
	idle := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6})
	// The re-entered Node runs "boost"; plans first lower it to eco, then raise it to the profile's mode.
	reentry := poolsAddNode(&snap, "n3", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6, "boost": 12})
	snap.Nodes[2].PowerMode = "boost"
	option := poolsInstance(snap.Instances[0], "opt", reentry, "fixed", "w1")
	snap.Options = append(snap.Options, poolsOption(option))
	// Re-entry prices the raise measured on the empty Node; a loaded-runner raise has another condition.
	power := poolsCost(schedulerActionPower, reentry, schedulerTestEcoMode, "fixed", "")
	power.Trials = []OperationTrial{{Duration: 2 * time.Second, Joules: 20}}
	loaded := poolsCost(schedulerActionPower, reentry, schedulerTestEcoMode, "fixed", "", schedulerState(option))
	loaded.ID += "-loaded"
	loaded.Trials = []OperationTrial{{Duration: 9 * time.Second, Joules: 90}}
	prepare := poolsCost(schedulerActionPrepare, reentry, "unloaded", schedulerState(option), "fixed")
	snap.Costs = []OperationCost{loaded, power, prepare}
	rows := capacityRowsForTest(s, snap, nil)
	d := capacityDecideForTest(t, s, snap, rows)
	if schedulerNodeMode(d.Keep.Nodes, idle) != schedulerTestEcoMode || !slices.ContainsFunc(d.Keep.Actions, func(a Action) bool { return a.Node == idle && a.Fixed }) {
		t.Fatalf("keep does not start the available Node at its lowest mode: %+v", d.Keep)
	}
	if slices.Contains(d.Search.ComparedNodes, idle) || !slices.Contains(d.Search.ComparedNodes, reentry) {
		t.Fatalf("ComparedNodes %v", d.Search.ComparedNodes)
	}
	plans := poolsPlans(d, reentry)
	i := slices.IndexFunc(plans, func(p SchedulerNodePlan) bool { return p.Eligible && p.Key != "keep" })
	if i < 0 || (plans[i].Pre-2001*time.Millisecond).Abs() > time.Microsecond || math.Abs(plans[i].Joules-20.01) > 1e-9 {
		t.Fatalf("re-entry did not use the empty-Node raise and the target-mode prepare: %+v", plans)
	}
	snap.Costs = []OperationCost{loaded, prepare}
	if !slices.ContainsFunc(poolsPlans(capacityDecideForTest(t, s, snap, rows), reentry), func(p SchedulerNodePlan) bool { return p.Reason == schedulerPlanMissingOperationCost }) {
		t.Fatal("re-entry without the empty-Node raise cost was not excluded")
	}
}

// §4: while the Fixed lowering runs the Node reports no mode; keep needs no power cost
// and the lowering alone is no transition that holds planning.
func TestSchedulerKeepWithRunningFixedLoweringNeedsNoPowerCost(t *testing.T) {
	s, snap := schedulerFixture()
	idle := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6})
	snap.Costs = nil
	rows := capacityRowsForTest(s, snap, nil)
	keep := capacityDecideForTest(t, s, snap, rows).Keep
	lowering := keep.Actions[slices.IndexFunc(keep.Actions, func(a Action) bool { return a.Node == idle && a.Fixed })]
	lowering.Status = phaseRunning
	snap.Actions = []Action{lowering}
	snap.InProgressTarget = &keep
	snap.Nodes[1].PowerMode = ""
	snap.Nodes[1].Reasons = []string{NodePowerModeUnavailable}
	d := capacityDecideForTest(t, s, snap, rows)
	if slices.ContainsFunc(d.Keep.Actions, func(a Action) bool { return a.Node == idle && !a.Fixed }) || slices.Contains(d.Reasons, schedulerTransitionInProgress) || d.Capacity == nil {
		t.Fatalf("running Fixed lowering held planning or became a compared power change: %+v", d)
	}
}

// §3.4 측정 POOL (serving side): a measurement Node takes no plan.
func TestSchedulerMeasurementNodeIsFixed(t *testing.T) {
	s, snap := schedulerFixture()
	empty := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6})
	busy := poolsAddNode(&snap, "n3", map[string]float64{"fixed": 10})
	other := poolsAddNode(&snap, "n4", map[string]float64{"fixed": 10})
	snap.Nodes[1].Measurement, snap.Nodes[2].Measurement = true, true
	measured := poolsInstance(snap.Instances[0], "measured", busy, "fixed", "")
	snap.Instances = append(snap.Instances, measured)
	elsewhere := poolsInstance(snap.Instances[0], "opt-other", other, "fixed", "w1")
	snap.Options = append(snap.Options,
		poolsOption(poolsInstance(snap.Instances[0], "opt-measurement", empty, "fixed", "w1")),
		poolsOption(poolsInstance(snap.Instances[0], "opt-busy", busy, "fixed", "w1")),
		poolsOption(elsewhere))
	snap.Costs = []OperationCost{poolsCost(schedulerActionPrepare, other, "unloaded", schedulerState(elsewhere), "fixed")}
	capacityLoad(s, &snap, snap.Workloads[0], 150)
	d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, func(SchedulerOption) float64 { return 100 }))
	if d.Target == nil || !poolsOn(*d.Target, other) {
		t.Fatalf("fixture must add the n4 instance: %+v", d)
	}
	for _, node := range []NodeReference{empty, busy} {
		if poolsEligibleChange(d, node) || !slices.ContainsFunc(poolsPlans(d, node), func(p SchedulerNodePlan) bool { return p.Reason == "MeasurementNode" }) {
			t.Fatalf("measurement Node %s took a plan: %+v", node.UID, poolsPlans(d, node))
		}
	}
	if poolsOn(*d.Target, empty) || slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return a.Node == empty || a.Node == busy }) ||
		!slices.ContainsFunc(d.Target.Instances, func(i SchedulerInstance) bool { return reflect.DeepEqual(i, measured) }) {
		t.Fatalf("target changed the measurement POOL: %+v", d.Target)
	}
}

// SCH-35: autonomy is rejected while the Workload is routed to a fixed-batch baseline.
func TestSchedulerFixedBatchAutonomyRejectsRoutedBaseline(t *testing.T) {
	_, snap := schedulerFixture()
	uid := snap.Instances[0].WorkloadUID
	if err := SchedulerFixedBatchAutonomy(snap, []string{uid}); err != nil {
		t.Fatal(err)
	}
	snap.Instances[0].FixedBatchSize = 1
	if err := SchedulerFixedBatchAutonomy(snap, []string{uid}); err == nil {
		t.Fatal("autonomy accepted for a Workload routed to a fixed-batch instance")
	}
	snap.Routing = map[string]map[string]int32{uid: {snap.Instances[0].ID: 0}}
	if err := SchedulerFixedBatchAutonomy(snap, []string{uid}); err != nil {
		t.Fatal(err)
	}
}
