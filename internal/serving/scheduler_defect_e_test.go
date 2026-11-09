package serving

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

func defectERequireNoPhysicalActions(t *testing.T, actions []Action, node NodeReference) {
	t.Helper()
	for _, action := range actions {
		if action.Node == node && action.Kind != schedulerActionRoute {
			t.Fatalf("unavailable node received physical work: %+v", action)
		}
	}
}

// T1/T13: keeping an unavailable physical target must not suppress its healthy
// peer's route-independent idle cleanup. The same probe identity can recover.
func TestDefectEIdleKeepRetainsUnavailablePhysicalTarget(t *testing.T) {
	const nodeNotReady = "node-not-ready"
	for _, unavailable := range []string{nodeNotReady, "probe", "native", "identity", "stopped"} {
		t.Run(unavailable, func(t *testing.T) {
			s, snap, rows, healthy, badNode := capacityR5Fixture()
			s.Controls.Routing = false
			since := snap.At.Add(-s.ObservationWindow)
			for k := 1; k < len(snap.Instances); k++ {
				snap.Instances[k].UnroutedSince = &since
				// Cleanup must repay its measured removal over H.
				profile := *snap.Instances[k].Profile
				profile.IdleBaselineWatts += 10
				snap.Instances[k].Profile = &profile
				snap.Options[k].Instance.Profile = &profile
			}
			bad := &snap.Instances[2]
			bad.PodUID, bad.OwnerWorkloadUID = "opi-pod", "w1"
			bad.Allocation = SchedulerAllocation{MemoryBytes: 1000}
			switch unavailable {
			case nodeNotReady:
				snap.Nodes[2].Ready = false
				bad.Ready = false
				snap.Nodes[2].Reasons = []string{NodeInstanceUnavailable}
			case "probe":
				bad.Ready, bad.ObservedAt = false, time.Time{}
				snap.Nodes[2].Reasons = []string{NodeInstanceUnavailable}
			case "native":
				bad.Ready = false // Projection can lack the node-wide reason.
			case "identity":
				bad.Ready, bad.Reasons = false, []string{"PolicyACKIdentityMismatch"}
			case "stopped":
				bad.Ready, bad.Stopped = false, true // Stop is not native drain proof.
			}
			before := *bad
			d := capacityDecideForTest(t, s, snap)
			if d.Target == nil {
				t.Fatal("missing target")
			}
			index := slices.IndexFunc(d.Target.Instances, func(i SchedulerInstance) bool { return i.ID == before.ID })
			if index < 0 || !reflect.DeepEqual(d.Target.Instances[index], before) {
				t.Fatalf("unavailable physical identity/allocation changed: before=%+v target=%+v", before, d.Target.Instances)
			}
			defectERequireNoPhysicalActions(t, d.Target.Actions, badNode)
			for _, kind := range []string{schedulerActionStop, schedulerActionDrain, schedulerActionRemove} {
				index := slices.IndexFunc(d.Target.Actions, func(a Action) bool { return a.Node == healthy && a.Kind == kind })
				if index < 0 || kind == schedulerActionStop && len(d.Target.Actions[index].DependsOn) != 0 {
					t.Fatalf("healthy unrouted cleanup lost independence: %+v", d.Target.Actions)
				}
			}
			// Exact successful probe recovery uses the original idle window, not a
			// new E cooldown or an owner reset.
			snap.Nodes[2].Ready, snap.Nodes[2].Reasons = true, nil
			bad.Ready, bad.Stopped, bad.Reasons, bad.ObservedAt = true, false, nil, snap.At
			recovered := capacityDecideForTest(t, s, snap)
			if recovered.Target == nil || poolsOn(*recovered.Target, badNode) || !slices.ContainsFunc(recovered.Target.Actions, func(a Action) bool {
				return a.Node == badNode && a.Kind == schedulerActionRemove
			}) {
				t.Fatalf("probe recovery did not restore ordinary cleanup: %+v", recovered.Target)
			}
		})
	}
}

// T2: withdrawing stale positive traffic is independent of physical retirement.
func TestDefectEUnavailableTrafficWithdrawalRetainsOccupancy(t *testing.T) {
	for _, routing := range []bool{true, false} {
		t.Run(fmt.Sprint(routing), func(t *testing.T) {
			s, snap := schedulerFixture()
			s.Controls.Routing = routing
			bad := snap.Instances[0]
			bad.ID, bad.Node, bad.Ready = "unconfirmed", poolsAddNode(&snap, "unconfirmed", map[string]float64{"fixed": 1}), false
			bad.OwnerWorkloadUID, bad.PodUID = "w1", "unconfirmed-pod"
			bad.ObservedAt = time.Time{}
			since := snap.At.Add(-s.ObservationWindow)
			bad.UnroutedSince = &since
			snap.Instances = append(snap.Instances, bad)
			snap.Nodes[1].Reasons = []string{NodeInstanceUnavailable}
			snap.Routing = map[string]map[string]int32{"w1": {"i1": 1, bad.ID: 1}}
			capacityLoad(s, &snap, snap.Workloads[0], 10)
			d := capacityDecideForTest(t, s, snap)
			if d.Target == nil || d.Workloads[0].State != WorkloadControlled || d.Target.Routing["w1"][bad.ID] != 0 || d.Target.Routing["w1"]["i1"] <= 0 {
				t.Fatalf("withdrawal froze workload or lost healthy destination: %+v", d)
			}
			if !slices.ContainsFunc(d.Target.Instances, func(i SchedulerInstance) bool { return reflect.DeepEqual(i, bad) }) {
				t.Fatalf("withdrawal erased physical occupancy: %+v", d.Target.Instances)
			}
			defectERequireNoPhysicalActions(t, d.Target.Actions, bad.Node)
			if d.Capacity.Workloads[0].RoutingRate != capacityThroughput(snap.Instances[0].Profile,snap.Instances[0].MaxBatch) {
				t.Fatalf("unconfirmed occupancy supplied routing rate: %+v", d.Capacity.Workloads)
			}
		})
	}
}

// The incident-shaped candidate has five idle physical sources, not five held
// cleanup scopes: only the four confirmed nodes get stop/drain/remove work.
func TestDefectEFiveNodeCandidateExcludesUnconfirmedCleanup(t *testing.T) {
	s, snap, _, _, badNode := capacityR5Fixture()
	s.Controls.Routing = false
	s.MaxInstances, s.MaxReplicas = 5, 5
	for _, name := range []string{"Nano", "Latte"} {
		node := poolsAddNode(&snap, name, map[string]float64{"fixed": 10})
		i := poolsInstance(snap.Instances[0], name, node, "fixed", "")
		snap.Instances = append(snap.Instances, i)
		snap.Options = append(snap.Options, poolsOption(i))
	}
	snap.Arrivals = nil
	snap.Routing = map[string]map[string]int32{"w1": {}}
	since := snap.At.Add(-s.ObservationWindow)
	for index := range snap.Instances {
		i := &snap.Instances[index]
		i.WorkloadUID, i.OwnerWorkloadUID, i.UnroutedSince = "", "", &since
		if i.Node == badNode {
			i.Ready = false
		}
	}
	snap.Nodes[2].Reasons = []string{NodeInstanceUnavailable}
	candidate := SchedulerCandidate{Nodes: slices.Clone(snap.Nodes), Routing: snap.Routing}
	for _, instance := range snap.Instances {
		if instance.Node == badNode {
			candidate.Instances = append(candidate.Instances, instance)
		}
	}
	candidate.Actions = schedulerPlan(s, snap, candidate)
	d := SchedulerDecision{Target: &candidate}
	if len(d.Target.Instances) != 1 || d.Target.Instances[0].Node != badNode {
		t.Fatalf("five-node cleanup did not retain only unavailable occupancy: %+v", d.Target)
	}
	defectERequireNoPhysicalActions(t, d.Target.Actions, badNode)
	for _, node := range snap.Nodes {
		if node.Node == badNode {
			continue
		}
		for _, kind := range []string{schedulerActionStop, schedulerActionDrain, schedulerActionRemove} {
			if !slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return a.Node == node.Node && a.Kind == kind }) {
				t.Fatalf("healthy %s lacks %s: %+v", node.Node.Name, kind, d.Target.Actions)
			}
		}
	}
}

// Exercise action lowering directly as well: callers cannot bypass the shared
// physical gate by omitting a source from an otherwise valid target.
func TestDefectEPhysicalLoweringUsesExactConfirmation(t *testing.T) {
	const nodeNotReady = "node-not-ready"
	for _, state := range []string{nodeNotReady, "wrong-node-uid", "reason", "native", "stopped-unconfirmed", "stopped-confirmed", "terminated"} {
		t.Run(state, func(t *testing.T) {
			s, snap := schedulerFixture()
			old := &snap.Instances[0]
			node := old.Node
			allowed := state == "stopped-confirmed" || state == "terminated"
			switch state {
			case nodeNotReady:
				snap.Nodes[0].Ready = false
			case "wrong-node-uid":
				snap.Nodes[0].Node.UID = "replacement-node"
			case "reason":
				snap.Nodes[0].Reasons = []string{NodeInstanceUnavailable}
			case "native":
				old.Ready = false
			case "stopped-unconfirmed":
				old.Stopped, old.Ready = true, false
			case "stopped-confirmed":
				old.Stopped = true
			case "terminated":
				old.Terminated, old.Stopped, old.Ready = true, true, false
			}
			c := SchedulerCandidate{Nodes: slices.Clone(snap.Nodes), Routing: map[string]map[string]int32{"w1": {}}}
			actions := schedulerPlan(s, snap, c)
			if !allowed {
				defectERequireNoPhysicalActions(t, actions, node)
				// Neither replacement preparation nor node-wide power may use
				// unknown occupancy as free coexistence capacity.
				wanted := *old
				wanted.ID, wanted.Terminated, wanted.Stopped, wanted.Ready = "defect-e-replacement", false, false, true
				c.Instances = []SchedulerInstance{wanted}
				c.Nodes[0].PowerMode = "changed"
				defectERequireNoPhysicalActions(t, schedulerPlan(s, snap, c), node)
			} else if !slices.ContainsFunc(actions, func(a Action) bool { return a.Node == node && a.Kind == schedulerActionRemove }) {
				t.Fatalf("known stopped/terminated cleanup was reclassified as unconfirmed: %+v", actions)
			}
		})
	}
}
