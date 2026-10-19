package serving

import (
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

// poolsInstance copies the fixture instance onto node at mode, bound to uids.
func poolsInstance(base SchedulerInstance, id string, node NodeReference, mode string, uids ...string) SchedulerInstance {
	p := *base.Profile
	p.ID, p.NodeName, p.Condition.NodeUID, p.Condition.PowerMode = "profile-"+node.UID+"-"+mode, node.Name, node.UID, mode
	base.ID, base.Node, base.Profile, base.WorkloadUIDs, base.IdleMeter = id, node, &p, uids, "wall-"+node.UID
	return base
}

// poolsCost is a measured operation; its 10W background baseline lets replay
// adjust it to a different coexisting state (e.g. re-entry into an empty Node).
func poolsCost(kind string, node NodeReference, from, to, mode string, loaded ...string) OperationCost {
	id, before := kind+"-"+node.UID+"-"+to, 10.0
	return OperationCost{ID: id, Node: node, Kind: kind, From: from, To: to, PowerMode: mode, BeforeState: loaded, TargetState: loaded, BeforeBaselineWatts: &before,
		Trials: []OperationTrial{{Duration: time.Millisecond, Joules: 0.01}}, EnergyScope: "whole-wall", Meter: "wall-" + node.UID,
		Scope: schedulerNodeWallScope, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: id, Size: 1}}
}

func poolsOption(i SchedulerInstance) SchedulerOption {
	return SchedulerOption{Instance: i, CompatibleUIDs: i.WorkloadUIDs}
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
	if got := schedulerEvaluateForTest(t, s, snap).Workloads; !reflect.DeepEqual(got, want) {
		t.Fatalf("workload states %+v, want %+v", got, want)
	}
}

// §9.1 전체 POOL: Nodes lacking configuration stay with their reasons.
func TestSchedulerNodeReasonsKeepNodeWithoutPlacement(t *testing.T) {
	for _, reason := range []string{"", NodePlacementInventoryMissing, NodePowerModeUnavailable} {
		t.Run("reason="+reason, func(t *testing.T) {
			s, snap := schedulerFixture()
			node := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6})
			if reason != "" {
				snap.Nodes[1].Reasons = []string{reason}
			}
			snap.Options = append(snap.Options, poolsOption(poolsInstance(snap.Instances[0], "opt", node, schedulerTestEcoMode, "w1")))
			candidates, _ := schedulerEnumerateForTest(t, s, snap)
			placed := false
			for _, c := range candidates {
				if !slices.Equal(c.Nodes[1].Reasons, snap.Nodes[1].Reasons) {
					t.Fatalf("Node reasons lost: %+v", c.Nodes[1])
				}
				placed = placed || poolsOn(c, node)
				lowered := slices.ContainsFunc(c.Actions, func(a Action) bool { return a.Node == node && a.Fixed })
				if reason == NodePowerModeUnavailable && (lowered || c.Nodes[1].PowerMode != "fixed" || slices.ContainsFunc(c.Actions, func(a Action) bool { return a.Node == node })) {
					t.Fatalf("PowerModeUnavailable Node changed mode: %+v", c)
				}
				if reason != NodePowerModeUnavailable && c.Keep && (!lowered || c.Nodes[1].PowerMode != schedulerTestEcoMode) {
					t.Fatalf("available Node was not lowered: %+v", c)
				}
			}
			if placed != (reason == "") {
				t.Fatalf("placed=%v with reason %q", placed, reason)
			}
		})
	}
}

// §9.1 전체 POOL: ComparedNodes and per-candidate baseline needs.
func TestSchedulerComparedNodesLimitEnergyAndBaselineNeeds(t *testing.T) {
	s, snap := schedulerFixture()
	s.MaxCandidates, s.MaxReplicas = 200, 1
	base := snap.Instances[0]
	idle := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6})
	bare := poolsAddNode(&snap, "n3", nil)
	move := poolsAddNode(&snap, "n4", map[string]float64{"fixed": 10})
	eco := poolsAddNode(&snap, "n5", map[string]float64{"fixed": 10})
	moved := poolsInstance(base, "opt-move", move, "fixed", "w1")
	raised := poolsInstance(base, "opt-eco", eco, schedulerTestEcoMode, "w1")
	snap.Options = append(snap.Options, poolsOption(moved), poolsOption(raised))
	snap.Costs = []OperationCost{
		poolsCost(schedulerActionPrepare, move, "unloaded", schedulerState(moved), "fixed"),
		poolsCost(schedulerActionRemove, base.Node, schedulerState(base), "unloaded", "fixed", schedulerState(base)),
		poolsCost(schedulerActionPower, eco, "fixed", schedulerTestEcoMode, "", schedulerState(raised)),
		poolsCost(schedulerActionPrepare, eco, "unloaded", schedulerState(raised), schedulerTestEcoMode),
	}
	decision := schedulerEvaluateForTest(t, s, snap)
	if want := []NodeReference{base.Node, move, eco}; !slices.Equal(decision.Search.ComparedNodes, want) {
		t.Fatalf("ComparedNodes %v, want %v", decision.Search.ComparedNodes, want)
	}
	if decision.Keep.ExpectedJoules == nil {
		t.Fatalf("Node %s without baseline outside ComparedNodes stopped the decision: %v", bare.UID, decision.Keep.Reasons)
	}
	for _, c := range decision.Candidates {
		for _, node := range []NodeReference{idle, bare} {
			ids := func(c SchedulerCandidate) (out []string) {
				for _, i := range c.Instances {
					if i.Node == node {
						out = append(out, i.ID)
					}
				}
				return out
			}
			if schedulerNodeMode(c.Nodes, node) != schedulerNodeMode(decision.Keep.Nodes, node) || !slices.Equal(ids(c), ids(decision.Keep)) {
				t.Fatalf("candidate %s changed uncompared Node %s", c.ID, node.UID)
			}
		}
		missing := slices.ContainsFunc(c.Reasons, func(r string) bool { return strings.HasPrefix(r, "MissingUnloadedNodeBaseline") })
		if poolsOn(c, eco) {
			schedulerRequireReason(t, c.Reasons, "MissingUnloadedNodeBaseline:n5:"+schedulerTestEcoMode)
		} else if missing {
			t.Fatalf("candidate %s without the eco Node lacks a baseline: %v", c.ID, c.Reasons)
		}
	}
}

// §9.1 후보: a Disabled Workload's instance is the same in every candidate.
func TestSchedulerDisabledWorkloadInstanceIsFixed(t *testing.T) {
	s, snap := schedulerFixture()
	poolsAddWorkload(&snap, "w2")
	disabled := schedulerTestCPU(snap.Instances[0])
	disabled.ID, disabled.WorkloadUIDs = "i2", []string{"w2"}
	snap.Instances = append(snap.Instances, disabled)
	snap.Routing = map[string]map[string]int32{"w1": {"i1": 1}, "w2": {"i2": 1}}
	node := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	snap.Options = append(snap.Options, poolsOption(poolsInstance(snap.Instances[0], "opt", node, "fixed", "w1")))
	candidates, _ := schedulerEnumerateForTest(t, s, snap)
	if len(candidates) < 2 {
		t.Fatal("fixture enumerated no alternative")
	}
	for _, c := range candidates {
		if !slices.ContainsFunc(c.Instances, func(i SchedulerInstance) bool { return reflect.DeepEqual(i, disabled) }) ||
			!reflect.DeepEqual(c.Routing["w2"], snap.Routing["w2"]) ||
			slices.ContainsFunc(c.Actions, func(a Action) bool { return a.Instance == disabled.ID }) {
			t.Fatalf("candidate %s changed the Disabled Workload's instance: %+v", c.ID, c)
		}
	}
}

// §9.1 후보: MinimumInstance and first configuration.
func TestSchedulerMinimumInstanceSelectsFirstConfiguration(t *testing.T) {
	s, snap := schedulerFixture()
	option := snap.Options[0].Instance
	snap.Instances = nil
	snap.Costs = []OperationCost{poolsCost(schedulerActionPrepare, option.Node, "unloaded", schedulerState(option), "fixed")}
	decision := schedulerEvaluateForTest(t, s, snap)
	schedulerRequireReason(t, decision.Keep.Reasons, "MinimumInstance:w1")
	if decision.Target == nil || decision.Target.Keep || !poolsOn(*decision.Target, option.Node) {
		t.Fatalf("first configuration was not the target: %+v", decision)
	}
}

// §9.1 후보: all instances on one Node reference one ModelSpec.
func TestSchedulerSameNodeRequiresSameModel(t *testing.T) {
	for _, same := range []bool{true, false} {
		s, snap := schedulerFixture()
		other := poolsAddWorkload(&snap, "w2")
		if !same {
			other.Model.UID = "m2"
		}
		node := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
		snap.Instances = append(snap.Instances, poolsInstance(snap.Instances[0], "i2", node, "fixed", "w2"))
		snap.Options = append(snap.Options, poolsOption(poolsInstance(schedulerTestCPU(snap.Instances[0]), "opt", node, "fixed", "w1")))
		candidates, _ := schedulerEnumerateForTest(t, s, snap)
		placed := slices.ContainsFunc(candidates, func(c SchedulerCandidate) bool {
			return slices.ContainsFunc(c.Instances, func(i SchedulerInstance) bool { return i.Node == node && schedulerHas(i.WorkloadUIDs, "w1") })
		})
		if placed != same {
			t.Fatalf("same model=%v placed=%v", same, placed)
		}
	}
}

// §9.1 후보: moving a Node between active POOLs is remove + prepare in one DAG.
func TestSchedulerDirectMoveBetweenActivePools(t *testing.T) {
	s, snap := schedulerFixture()
	s.MaxCandidates = 200
	s.AutonomousWorkloads = []string{"w1", "w2"}
	poolsAddWorkload(&snap, "w2").Model.UID = "m2"
	shared := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	spare := poolsAddNode(&snap, "n3", map[string]float64{"fixed": 10})
	snap.Instances = append(snap.Instances, poolsInstance(snap.Instances[0], "i2", shared, "fixed", "w2"))
	snap.Options = append(snap.Options,
		// A different CPU, so w2's runner cannot be reused as w1's slot.
		poolsOption(poolsInstance(schedulerTestCPU(snap.Instances[0]), "opt-w1", shared, "fixed", "w1")),
		poolsOption(poolsInstance(snap.Instances[0], "opt-w2", spare, "fixed", "w2")))
	candidates, _ := schedulerEnumerateForTest(t, s, snap)
	found := false
	for _, c := range candidates {
		if slices.ContainsFunc(c.Actions, func(a Action) bool { return a.Kind == "membership" }) {
			t.Fatalf("membership action planned: %+v", c.Actions)
		}
		removed := slices.ContainsFunc(c.Actions, func(a Action) bool { return a.Kind == schedulerActionRemove && a.Node == shared && a.Instance == "i2" })
		prepared := slices.ContainsFunc(c.Actions, func(a Action) bool { return a.Kind == schedulerActionPrepare && a.Node == shared })
		found = found || removed && prepared
	}
	if !found {
		t.Fatal("no candidate moved the Node from w2's to w1's active POOL")
	}
}

// §9.1 후보: available Nodes start at their lowest mode; re-entry uses the loaded power cost.
func TestSchedulerAvailableNodeLoweringAndReentry(t *testing.T) {
	s, snap := schedulerFixture()
	s.MaxCandidates = 200
	idle := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6})
	// The re-entered Node runs "boost"; candidates first lower it to eco, then raise it to the profile's mode.
	reentry := poolsAddNode(&snap, "n3", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6, "boost": 12})
	snap.Nodes[2].PowerMode = "boost"
	option := poolsInstance(snap.Instances[0], "opt", reentry, "fixed", "w1")
	snap.Options = append(snap.Options, poolsOption(option))
	power := poolsCost(schedulerActionPower, reentry, schedulerTestEcoMode, "fixed", "", schedulerState(option))
	snap.Costs = []OperationCost{power, poolsCost(schedulerActionPrepare, reentry, "unloaded", schedulerState(option), "fixed")}
	// The re-entry candidate keeps i1, so its only operations are on the re-entered Node.
	reentered := func(d SchedulerDecision) SchedulerCandidate {
		for _, c := range d.Candidates {
			if poolsOn(c, reentry) && slices.ContainsFunc(c.Instances, func(i SchedulerInstance) bool { return i.ID == "i1" }) {
				return c
			}
		}
		t.Fatal("missing re-entry candidate")
		return SchedulerCandidate{}
	}
	decision := schedulerEvaluateForTest(t, s, snap)
	for _, c := range decision.Candidates {
		if schedulerNodeMode(c.Nodes, idle) != schedulerTestEcoMode || !slices.ContainsFunc(c.Actions, func(a Action) bool { return a.Node == idle && a.Fixed }) {
			t.Fatalf("candidate %s does not start the available Node at its lowest mode: %+v", c.ID, c)
		}
	}
	if slices.Contains(decision.Search.ComparedNodes, idle) || !slices.Contains(decision.Search.ComparedNodes, reentry) {
		t.Fatalf("ComparedNodes %v", decision.Search.ComparedNodes)
	}
	c := reentered(decision)
	var onNode []Action
	for _, a := range c.Actions {
		if a.Node == reentry {
			onNode = append(onNode, a)
		}
	}
	if len(onNode) < 3 || !onNode[0].Fixed || onNode[0].To != schedulerTestEcoMode || !slices.Contains(onNode[1].DependsOn, onNode[0].ID) {
		t.Fatalf("Fixed lowering does not precede the re-entry actions: %+v", onNode)
	}
	if !slices.Contains(c.Assumptions, "ReentryPowerCostFromLoadedRunner") || slices.ContainsFunc(c.Reasons, func(r string) bool { return strings.HasPrefix(r, "MissingOperationCost") }) {
		t.Fatalf("re-entry power did not use the loaded transition cost: %v %v", c.Assumptions, c.Reasons)
	}
	snap.Costs = snap.Costs[1:]
	schedulerRequireReason(t, reentered(schedulerEvaluateForTest(t, s, snap)).Reasons, "MissingOperationCost")
}

// §9.1 후보: SLO target set.
func TestSchedulerSLOTargetsIncludeOnlyAffectedDisabledWorkloads(t *testing.T) {
	n1, n2 := NodeReference{Name: "node1", UID: "n1"}, NodeReference{Name: "node2", UID: "n2"}
	snap := SchedulerSnapshot{
		Workloads: []*Definition{{UID: "w1"}, {UID: "w2"}, {UID: "w3"}},
		Instances: []SchedulerInstance{{ID: "a", Node: n1, WorkloadUIDs: []string{"w2"}}, {ID: "b", Node: n2, WorkloadUIDs: []string{"w3"}}},
	}
	c := SchedulerCandidate{Instances: snap.Instances, Actions: []Action{
		{Kind: schedulerActionPower, Node: n1, From: "fixed", To: "eco"},
		{Kind: schedulerActionPower, Node: n2, From: "fixed", To: "eco", Fixed: true},
	}}
	if got := schedulerSLOTargets(snap, c, []string{"w1"}); !reflect.DeepEqual(got, map[string]bool{"w1": true, "w2": true}) {
		t.Fatalf("SLO targets %v", got)
	}
}

// §9.1 측정 POOL (serving side).
func TestSchedulerMeasurementNodeIsFixed(t *testing.T) {
	s, snap := schedulerFixture()
	s.MaxCandidates = 200
	empty := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10, schedulerTestEcoMode: 6})
	busy := poolsAddNode(&snap, "n3", map[string]float64{"fixed": 10})
	other := poolsAddNode(&snap, "n4", map[string]float64{"fixed": 10})
	snap.Nodes[1].Measurement, snap.Nodes[2].Measurement = true, true
	measured := poolsInstance(snap.Instances[0], "measured", busy, "fixed")
	snap.Instances = append(snap.Instances, measured)
	snap.Options = append(snap.Options,
		poolsOption(poolsInstance(snap.Instances[0], "opt-measurement", empty, "fixed", "w1")),
		poolsOption(poolsInstance(snap.Instances[0], "opt-other", other, "fixed", "w1")))
	candidates, _ := schedulerEnumerateForTest(t, s, snap)
	if len(candidates) < 2 {
		t.Fatal("fixture enumerated no alternative")
	}
	for _, c := range candidates {
		if poolsOn(c, empty) || schedulerNodeMode(c.Nodes, empty) != "fixed" ||
			slices.ContainsFunc(c.Actions, func(a Action) bool { return a.Node == empty || a.Node == busy }) ||
			!slices.ContainsFunc(c.Instances, func(i SchedulerInstance) bool { return reflect.DeepEqual(i, measured) }) {
			t.Fatalf("candidate %s changed the measurement POOL: %+v", c.ID, c)
		}
	}
}
