package serving

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
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
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod, s.MaxObservationAge = time.Minute, 10*time.Second, time.Minute, time.Hour
	snap.ObservedSince = snap.ObservationEnd.Add(-time.Hour)
	s.MinimumDemand = map[string]float64{"w1": 10}
	old := &snap.Instances[0]
	old.Ready, old.Stopped, old.Terminated = false, true, true
	old.ObservedAt = snap.At.Add(-2 * s.MaxObservationAge)
	until := snap.At.Add(s.ObservationWindow)
	old.HeldUntil = &until
	snap.ScaleDown = map[string]SchedulerDownState{"w1": {Allowed: false}}
	snap.Routing = map[string]map[string]int32{"w1": {old.ID: 1000}}
	// Reproduce E5-7's terminal route1000 while recent demand is growing.
	capacityLoad(s, &snap, snap.Workloads[0], 10)
	recent := snap.ObservationEnd.Add(-s.ControlInterval)
	snap.Arrivals = slices.DeleteFunc(snap.Arrivals, func(a SchedulerArrival) bool { return a.At.Before(recent) })
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

// E5-7: a dead route1000 is not live supply, even while demand grows.
func TestGrowingProtectsOnlyNonterminalExactRoutes(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		instances []SchedulerInstance
		routes    map[string]int32
		want      string
	}{
		{"terminated", []SchedulerInstance{{ID: "old", Terminated: true}}, nil, ""},
		{"live", []SchedulerInstance{{ID: "old", Ready: true}}, nil, schedulerDemandIncreasing},
		{"not-ready", []SchedulerInstance{{ID: "old"}}, nil, schedulerDemandIncreasing},
		{"logical-stop", []SchedulerInstance{{ID: "old", Stopped: true}}, nil, schedulerDemandIncreasing},
		{"missing", nil, nil, schedulerDemandIncreasing},
		{"target-alias", []SchedulerInstance{{ID: "other", TargetID: "old", Terminated: true}}, nil, schedulerDemandIncreasing},
		{"dead-live-drop", []SchedulerInstance{{ID: "old", Terminated: true}, {ID: "live", Ready: true}}, nil, schedulerDemandIncreasing},
		{"dead-live-retain", []SchedulerInstance{{ID: "old", Terminated: true}, {ID: "live", Ready: true}}, map[string]int32{"live": 1000}, ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			p := capacityPlanner{snapshot: SchedulerSnapshot{Instances: scenario.instances},
				keep: SchedulerCandidate{Routing: map[string]map[string]int32{"w": {"old": 1000}}}}
			if len(scenario.instances) == 2 {
				p.keep.Routing["w"]["live"] = 1000
			}
			w := capacityWorkload{d: &Definition{UID: "w"}, growing: true}
			if got := p.timingReason(w, false, false, true, scenario.routes); got != scenario.want {
				t.Fatalf("timingReason = %q, want %q", got, scenario.want)
			}
			if got := p.timingReason(w, true, false, true, scenario.routes); got != schedulerDemandIncreasing {
				t.Fatalf("slow growth protection changed: %q", got)
			}
			if got := p.timingReason(w, true, true, true, scenario.routes); got != "" {
				t.Fatalf("proved recovery exception changed: %q", got)
			}
		})
	}
}
