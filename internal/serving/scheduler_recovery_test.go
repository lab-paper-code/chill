package serving

import (
	"encoding/json"
	"slices"
	"testing"
)

// SCH-26/50, AC-45: an exact terminal observation cannot become stale live
// capacity. Logical admission-stop alone is not physical termination proof.
func TestCapacityTerminalLedgerAllowsIndependentRecoveryAC45(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "logical-stop", true: "confirmed-termination"}[terminal], func(t *testing.T) {
			s, snap := schedulerFixture()
			s.MinimumDemand = map[string]float64{"w1": 10}
			old := &snap.Instances[0]
			old.Ready, old.Stopped, old.ObservedAt = false, true, snap.At.Add(-2*s.MaxObservationAge)
			old.Routable = map[string]bool{"w1": false}
			// Use the stored snapshot boundary so the proof must survive JSON,
			// rather than being inferred from a reason string or elapsed time.
			if terminal {
				if err := json.Unmarshal([]byte(`{"terminated":true}`), old); err != nil {
					t.Fatal(err)
				}
			}
			snap.Nodes[0].Reasons = []string{NodeInstanceUnavailable}
			snap.Routing = map[string]map[string]int32{"w1": {old.ID: 1000}}
			node := poolsAddNode(&snap, "recovery", map[string]float64{"fixed": 10})
			snap.Nodes[len(snap.Nodes)-1].Reasons = nil
			replacement := poolsInstance(snap.Options[0].Instance, "replacement", node, "fixed", "w1")
			snap.Options = append(snap.Options, poolsOption(replacement))
			snap.Costs = []OperationCost{poolsCost(schedulerActionPrepare, node, "unloaded", schedulerState(replacement), "fixed")}
			rows := capacityRowsForTest(s, snap, nil)
			decision := capacityDecideForTest(t, s, snap, rows)
			if !terminal {
				if decision.Status != schedulerStatusPending || !slices.Contains(decision.Reasons, "UnknownOrStaleInstance:"+old.ID) {
					t.Fatalf("unconfirmed stopped executor bypassed freshness: %+v", decision)
				}
				return
			}
			if decision.Target == nil || decision.Target.Keep || decision.Status == schedulerStatusPending {
				t.Fatalf("terminal ledger blocked independent replacement: status=%s reasons=%v", decision.Status, decision.Reasons)
			}
			if decision.Target.Routing["w1"][old.ID] > 0 || !slices.ContainsFunc(decision.Target.Actions, func(a Action) bool {
				return a.Kind == schedulerActionPrepare && a.Node == node
			}) {
				t.Fatalf("recovery retained dead positive route or omitted prepare: %+v", decision.Target)
			}
			// The tombstone remains an allocation/ownership obligation; recovery
			// must not manufacture physical absence to pass the global guard.
			if !slices.ContainsFunc(decision.Target.Instances, func(i SchedulerInstance) bool {
				return i.ID == old.ID && slices.Equal(i.Allocation.CPUs, old.Allocation.CPUs)
			}) {
				t.Fatal("independent recovery erased retained physical allocation")
			}
		})
	}
}

func TestCapacityTerminalSlotIsRepreparedWithoutReuseAC45(t *testing.T) {
	s, snap := schedulerFixture()
	s.MinimumDemand = map[string]float64{"w1": 10}
	old := &snap.Instances[0]
	old.Ready, old.Stopped, old.Terminated = false, true, true
	old.ObservedAt = snap.At.Add(-2 * s.MaxObservationAge)
	until := snap.At.Add(s.ObservationWindow)
	old.HeldUntil = &until
	snap.ScaleDown = map[string]SchedulerDownState{"w1": {Allowed: false}}
	snap.Routing = map[string]map[string]int32{"w1": {old.ID: 1000}}
	snap.Costs = []OperationCost{
		poolsCost(schedulerActionRemove, old.Node, schedulerState(*old), "unloaded", "fixed"),
		poolsCost(schedulerActionPrepare, old.Node, "unloaded", schedulerState(*old), "fixed"),
	}
	rows := capacityRowsForTest(s, snap, nil)
	decision := capacityDecideForTest(t, s, snap, rows)
	if decision.Target == nil || decision.Target.Keep || !slices.ContainsFunc(decision.Target.Actions, func(a Action) bool {
		return a.Kind == schedulerActionPrepare && a.Node == old.Node && a.Instance != old.ID
	}) {
		t.Fatalf("terminal slot was reused or slow-gated instead of prepared: status=%s reasons=%v", decision.Status, decision.Reasons)
	}
	for _, instance := range decision.Target.Instances {
		if instance.ID == old.ID || instance.Terminated {
			t.Fatal("terminal physical identity became replacement capacity")
		}
	}
}
