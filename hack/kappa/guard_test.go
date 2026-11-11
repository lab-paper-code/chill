package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestReplayKeysIncludeFinalRouteAndWait(t *testing.T) {
	a := &serving.SchedulerCandidate{Instances: []serving.SchedulerInstance{{ID: "i", MaxBatch: 1}}, Routing: map[string]map[string]int32{"w": {"i": 1}}}
	before := replayKey(a)
	a.Routing["w"]["i"] = 0
	if replayKey(a) == before {
		t.Fatal("final-route change invisible")
	}
	a.Routing["w"]["i"] = 1
	a.Instances[0].MaxWait = time.Millisecond
	if replayKey(a) == before {
		t.Fatal("wait change invisible")
	}
}

func TestReplayPreservesRecordedControlPolicy(t *testing.T) {
	base := serving.SchedulerSettings{CapacityWindows: 20, PlanSearchWorkers: 2}
	recorded := serving.SchedulerSettings{ScaleDownHeadroomFraction: .25, MinimumDemand: map[string]float64{"w": 20}, ExcludedNodes: []string{"excluded"}, CompletionMarginMS: 29}
	got := replaySettings(base, recorded, -1)
	if got.ScaleDownHeadroomFraction != .25 || got.MinimumDemand["w"] != 20 || len(got.ExcludedNodes) != 1 || got.CompletionMarginMS != 29 || got.CapacityWindows != 20 {
		t.Fatalf("policy lost: %+v", got)
	}
}

func TestReplayRestoresRealArrivalWindowAndDefinition(t *testing.T) {
	end := time.Unix(100, 0)
	d := &serving.Definition{UID: "w"}
	snap := serving.SchedulerSnapshot{ObservationEnd: end, Workloads: []*serving.Definition{d}}
	arrivals := []replayArrival{
		{SchedulerArrival: serving.SchedulerArrival{ID: "before", At: end.Add(-2 * time.Second)}, DefinitionID: d.Identity()},
		{SchedulerArrival: serving.SchedulerArrival{ID: "inside", At: end.Add(-time.Second), Compute: true}, DefinitionID: d.Identity()},
		{SchedulerArrival: serving.SchedulerArrival{ID: "end", At: end}, DefinitionID: d.Identity()},
	}
	err := restoreReplayArrivals(&snap, arrivals, serving.SchedulerSettings{ObservationWindow: time.Second}, true, map[string]*serving.Definition{})
	if err != nil || len(snap.Arrivals) != 1 || snap.Arrivals[0].ID != "inside" || snap.Arrivals[0].Definition != d {
		t.Fatalf("arrival reconstruction: %+v, %v", snap.Arrivals, err)
	}
}

func TestReplayHydrationPreservesCapturedProfile(t *testing.T) {
	captured := &serving.Profile{ID: "captured"}
	catalog := &serving.Profile{ID: "catalog"}
	instances := []serving.SchedulerInstance{{ProfileID: "p", Profile: captured}, {ProfileID: "p"}}
	if err := hydrateReplayInstances(instances, map[string]*serving.Profile{"p": catalog}); err != nil {
		t.Fatal(err)
	}
	if instances[0].Profile != captured || instances[1].Profile != catalog {
		t.Fatalf("captured profile replaced or omitted profile not restored: %+v", instances)
	}
}

func TestReplayDemandUsesDirectOutcomesWithoutRows(t *testing.T) {
	decision := &serving.SchedulerDecision{
		Keep: serving.SchedulerCandidate{Feedback: []serving.FeedbackWorkloadScore{
			{UID: "other", Population: 2},
			{UID: "w", Population: 10, OnTime: 6, Rejected: 4, FailureFraction: .4},
		}},
		Target: &serving.SchedulerCandidate{Feedback: []serving.FeedbackWorkloadScore{
			{UID: "w", Population: 10, OnTime: 10, Satisfied: true},
		}},
		Capacity: &serving.SchedulerCapacity{Workloads: []serving.SchedulerCapacityWorkload{{UID: "w", Rate: 50}}},
	}
	got := replayDemands(decision)
	if len(got) != 1 || got[0].Rate != 50 || got[0].Keep == nil || got[0].Target == nil ||
		got[0].Keep.FailureFraction != .4 || !got[0].Target.Satisfied {
		t.Fatalf("direct workload outcomes lost: %+v", got)
	}
	decision.Keep.Feedback = nil
	decision.Target.Feedback = nil
	got = replayDemands(decision)
	if len(got) != 1 || got[0].Keep != nil || got[0].Target != nil {
		t.Fatalf("missing direct evidence became a fabricated score: %+v", got)
	}
}

func TestReplayGuardWithoutTargetDoesNotInventAdmission(t *testing.T) {
	at := time.Unix(100, 0)
	tick := &replayTick{r: record{At: at, Decision: &serving.SchedulerDecision{Status: "Keep"}}}
	got, err := evaluateReplayTick([]*replayTick{tick}, 0)
	if err != nil || len(got.Checks) != 0 || got.TargetKey != "-" || !got.At.Equal(at) {
		t.Fatalf("targetless guard fabricated an admission check: %+v, %v", got, err)
	}
}

func TestReplayGuardPreservesExclusiveOwnerReservation(t *testing.T) {
	at := time.Unix(100, 0)
	settings := serving.SchedulerSettings{
		ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second,
		MaxReplicas: 1, MaxInstances: 1, AutonomousWorkloads: []string{"w"},
		CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1, CapacityMinimumRequests: 1,
		CapacityGridSteps: 1, CapacityBisectionWidth: .1, CapacityTailSamples: 1,
		PlanSearchWorkers: 1, PlanSearchStrategy: "v2", PlanSearchEvaluations: 1, PlanExactFinalists: 1,
		DefaultOperationCost: map[string]serving.OperationCostValue{"prepare": {Seconds: 1}, "remove": {Seconds: 1}, "power": {Seconds: 1}},
		DefaultIdlePower:     &serving.IdlePowerDefault{NodeWatts: 1, PerInstanceWatts: 1},
	}
	if err := settings.Validate(); err != nil {
		t.Fatal(err)
	}
	node := serving.SchedulerNode{Node: serving.NodeReference{Name: "n", UID: "n-uid"}, PowerMode: "current", Ready: true, ObservedAt: at}
	workload := &serving.Definition{UID: "w", SLO: serving.SLO{Percentile: 99, TargetLatency: time.Second}, Admission: serving.Admission{Policy: serving.AdmissionPolicyReject, MaxQueuedRequests: 1024}}
	target := &serving.SchedulerCandidate{ID: "reserved-target", Nodes: []serving.SchedulerNode{node}}
	target.Nodes[0].PowerMode = "reserved"
	snapshot := serving.SchedulerSnapshot{
		At: at, ObservationEnd: at, Nodes: []serving.SchedulerNode{node}, Workloads: []*serving.Definition{workload},
		Options:      []serving.SchedulerOption{{CompatibleUIDs: []string{workload.UID}}},
		Arrivals:     []serving.SchedulerArrival{{ID: "accepted", At: at.Add(-time.Millisecond), Accepted: true, Compute: true, Definition: workload, WorkloadUID: workload.UID}},
		Reservations: []serving.SchedulerReservation{{Owner: "exclusive-owner", Nodes: []serving.NodeReference{node.Node}, WorkloadUIDs: []string{workload.UID}, Target: target}},
	}
	capture := func() []byte {
		t.Helper()
		raw, err := json.Marshal(struct {
			Snapshot serving.SchedulerSnapshot
			Target   *serving.SchedulerCandidate
		}{snapshot, target})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := capture()
	tick := &replayTick{r: record{At: at, Decision: &serving.SchedulerDecision{Target: target}}, snap: snapshot, settings: settings}
	got, err := evaluateReplayTick([]*replayTick{tick}, 0)
	if err != nil || len(got.Checks) != 1 || !got.Checks[0].Rejected || got.Checks[0].Reason != "TargetWorkloadOutsideControl" {
		t.Fatalf("exclusive owner reservation escaped new admission: %+v, %v", got, err)
	}
	if !bytes.Equal(before, capture()) {
		t.Fatal("new-admission diagnostic changed original snapshot, accepted arrivals or reserved target")
	}
}
