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

func TestReplayDemandsUsesSafeBatchCapacity(t *testing.T) {
	for _, scenario := range []string{"bounded", "missing-b1", "pending-b1", "rejected-b1", "faster-b1"} {
		t.Run(scenario, func(t *testing.T) {
			d := &serving.Definition{UID: "w"}
			s := serving.SchedulerSettings{AutonomousWorkloads: []string{d.UID}, MinimumDemand: map[string]float64{d.UID: 30}}
			current := serving.SchedulerInstance{ID: "current", WorkloadUID: d.UID, Ready: true, MaxBatch: 4, Profile: &serving.Profile{ID: "profile"}}
			added := current
			added.ID, added.Node = "added", serving.NodeReference{UID: "next"}
			snap := serving.SchedulerSnapshot{
				Workloads: []*serving.Definition{d}, Instances: []serving.SchedulerInstance{current},
				Routing: map[string]map[string]int32{d.UID: {current.ID: 1000}},
			}
			target := &serving.SchedulerCandidate{
				Instances: []serving.SchedulerInstance{current, added},
				Routing:   map[string]map[string]int32{d.UID: {current.ID: 500, added.ID: 500}},
			}
			rows := map[string]serving.CapacityRow{}
			for _, i := range target.Instances {
				in := serving.NewCapacityInput(s, snap.DispatchOccupancy, serving.SchedulerOption{Instance: i}, d)
				policy, b1 := 40.0, 26.0
				rows[in.Key()] = serving.CapacityRow{Key: in.Key(), Input: in, Status: serving.CapacityBuilt, Kappa: &policy}
				in.MaxBatch = 1
				row := serving.CapacityRow{Key: in.Key(), Input: in, Status: serving.CapacityBuilt, Kappa: &b1}
				switch scenario {
				case "missing-b1":
					continue
				case "pending-b1":
					row.Status = serving.CapacityPending
				case "rejected-b1":
					row.Status, row.Kappa = serving.CapacityRejected, nil
				case "faster-b1":
					b1 = 50
				}
				rows[row.Key] = row
			}
			want := 40.0
			if scenario == "bounded" {
				want = 26
			}
			got := replayDemands(s, snap, rows, target)
			if len(got) != 1 || got[0].Rate != 30 || got[0].KeepKappa != want || got[0].TargetKappa != 2*want ||
				got[0].KeepShort != (scenario == "bounded") || !got[0].SupplyIncrease {
				t.Fatalf("keep/target supply must use the server envelope and fallback: %+v, want keep κ=%g", got, want)
			}
		})
	}
}
