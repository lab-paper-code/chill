package main

import (
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
