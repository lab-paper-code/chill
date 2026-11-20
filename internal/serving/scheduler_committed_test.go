package serving

import (
	"context"
	"slices"
	"testing"
	"time"
)

// committedFixture loads w1 with 40 req/s against 25 req/s instances. i1 runs
// on n1; n2's second instance is in flight under a reservation whose target
// routes both; n3 could prepare a third.
func committedFixture() (SchedulerSettings, SchedulerSnapshot, NodeReference, NodeReference) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod, s.MaxReplicas = time.Minute, 10*time.Second, time.Minute, 3
	snap.ObservedSince = snap.ObservationEnd.Add(-time.Hour)
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	n3 := poolsAddNode(&snap, "n3", map[string]float64{"fixed": 10})
	i1 := snap.Instances[0]
	slot2, slot3 := poolsInstance(i1, "slot2", n2, "fixed", "w1"), poolsInstance(i1, "slot3", n3, "fixed", "w1")
	snap.Options = append(snap.Options, poolsOption(slot2), poolsOption(slot3))
	snap.Costs = []OperationCost{
		poolsCost(schedulerActionPrepare, n2, "unloaded", schedulerState(slot2), "fixed"),
		poolsCost(schedulerActionPrepare, n3, "unloaded", schedulerState(slot3), "fixed"),
		poolsCost(schedulerActionRemove, n2, schedulerState(slot2), "unloaded", "fixed"),
		poolsCost(schedulerActionRemove, n3, schedulerState(slot3), "unloaded", "fixed"),
	}
	capacityServiceRatesForTest(&snap, map[string]float64{i1.Profile.ID: 25, slot2.Profile.ID: 25, slot3.Profile.ID: 25})
	capacityLoad(s, &snap, snap.Workloads[0], 40)
	snap.Routing = map[string]map[string]int32{"w1": {"i1": schedulerRouteUnits}}
	return s, snap, n2, n3
}

// inFlight adds n2's preparing pod and the reservation that owns its target.
func inFlight(snap *SchedulerSnapshot, n2 NodeReference) {
	var target SchedulerInstance
	for _, o := range snap.Options {
		if o.Instance.Node == n2 {
			target = o.Instance
		}
	}
	pod := target
	pod.ID, pod.TargetID, pod.Ready, pod.WorkloadUID, pod.Weight, pod.OwnerWorkloadUID = "pod2", target.ID, false, "", 0, "w1"
	snap.Instances = append(snap.Instances, pod)
	goal := SchedulerCandidate{ID: "owner", Instances: []SchedulerInstance{snap.Instances[0], target},
		Routing: map[string]map[string]int32{"w1": {"i1": 500, target.ID: 500}}}
	snap.Reservations = []SchedulerReservation{{Owner: "owner", Nodes: []NodeReference{n2}, Instances: []string{target.ID}, Target: &goal}}
}

func preparesOn(d SchedulerDecision, node NodeReference) bool {
	return d.Target != nil && slices.ContainsFunc(d.Target.Actions, func(a Action) bool { return a.Kind == schedulerActionPrepare && a.Node == node })
}

// 40 req/s need a second instance. While it is being prepared it is capacity
// already committed: no decision prepares a third for the same shortfall. The
// decision names the preparing instance its predictions count as Ready; its
// keep stays the running configuration.
func TestPreparingInstanceIsCommittedCapacity(t *testing.T) {
	s, snap, n2, n3 := committedFixture()
	if d := capacityDecideForTest(t, s, snap); !preparesOn(d, n2) && !preparesOn(d, n3) || d.Committed != nil {
		t.Fatalf("40 req/s on one 25 req/s instance prepared nothing: %s %v committed %v", d.Status, d.Reasons, d.Committed)
	}
	inFlight(&snap, n2)
	d := capacityDecideForTest(t, s, snap)
	if preparesOn(d, n3) || preparesOn(d, n2) {
		t.Fatalf("a second prepare for the shortfall the preparing instance covers: %s %v %+v", d.Status, d.Reasons, d.Target.Actions)
	}
	if !slices.Equal(d.Committed, []string{"pod2"}) || !slices.ContainsFunc(d.Keep.Instances, func(i SchedulerInstance) bool { return i.ID == "pod2" && !i.Ready }) {
		t.Fatalf("the record must name the committed instance beside the running keep: committed %v keep %+v", d.Committed, d.Keep.Instances)
	}
	// The admission of a launch sees the same committed capacity.
	if reason, err := SchedulerTargetAdmission(context.Background(), s, snap, *d.Target); err != nil || reason != "" {
		t.Fatalf("the decision's own target is not admitted: %q %v", reason, err)
	}
}

// A preparation that failed or left stops counting: the shortfall is covered
// again, and the third prepare is chosen.
func TestFailedOrRemovedPreparationStopsCounting(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(*SchedulerSnapshot)
	}{
		{"failed", func(snap *SchedulerSnapshot) { snap.Instances[len(snap.Instances)-1].Terminated = true }},
		{"removed", func(snap *SchedulerSnapshot) { snap.Instances = snap.Instances[:len(snap.Instances)-1] }},
		{"reservation released", func(snap *SchedulerSnapshot) { snap.Reservations = nil }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, snap, n2, n3 := committedFixture()
			inFlight(&snap, n2)
			c.change(&snap)
			if d := capacityDecideForTest(t, s, snap); !preparesOn(d, n3) {
				t.Fatalf("the shortfall is uncovered but nothing was prepared: %s %v %+v", d.Status, d.Reasons, d.Target)
			}
		})
	}
}

// The measured verdict judges what ran in the window: the preparing instance
// is committed capacity for the plan, never serving for the measurement.
func TestPreparingInstanceIsNotServingForTheMeasuredVerdict(t *testing.T) {
	s, snap, n2, _ := committedFixture()
	inFlight(&snap, n2)
	p := capacityPlannerForTest(t, s, snap)
	if got := p.keep.Routing["w1"]; len(got) != 2 || got["pod2"] != 500 {
		t.Fatalf("plan does not count the preparing instance: %v", got)
	}
	if serving := p.ws[0].serving; len(serving) != 1 || serving[0].ProfileID != snap.Instances[0].Profile.ID {
		t.Fatalf("measured serving includes the preparing instance: %+v", serving)
	}
	if res := p.evaluate(make([]int, len(p.plans)), 0); res.running[0] {
		t.Fatal("keep with the committed instance is not the serving that was measured")
	}
}

// A Workload is routed when the running snapshot routes it, with positive
// weight, to a Ready instance. A preparing instance counted as Ready in the
// committed view does not make it routed.
func TestRoutedNeedsAReadyInstanceWithWeightInTheRunningSnapshot(t *testing.T) {
	for _, c := range []struct {
		name    string
		arrange func(snap *SchedulerSnapshot, n2 NodeReference)
		routed  bool
	}{
		{"ready and weighted", func(*SchedulerSnapshot, NodeReference) {}, true},
		{"routed but not Ready", func(snap *SchedulerSnapshot, _ NodeReference) { snap.Instances[0].Ready = false }, false},
		{"Ready but zero weight", func(snap *SchedulerSnapshot, _ NodeReference) {
			snap.Routing = map[string]map[string]int32{"w1": {"i1": 0}}
		}, false},
		{"only a preparing instance, Ready in the committed view", func(snap *SchedulerSnapshot, n2 NodeReference) {
			inFlight(snap, n2)
			snap.Instances[0].Ready = false
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, snap, n2, _ := committedFixture()
			c.arrange(&snap, n2)
			if got := capacityPlannerForTest(t, s, snap).ws[0].routed; got != c.routed {
				t.Fatalf("routed = %v, want %v", got, c.routed)
			}
		})
	}
}
