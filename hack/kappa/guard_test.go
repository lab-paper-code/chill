package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
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

func TestReplaySupplyUsesPolicyCapacity(t *testing.T) {
	d := &serving.Definition{UID: "w"}
	s := serving.SchedulerSettings{AutonomousWorkloads: []string{d.UID}, MinimumDemand: map[string]float64{d.UID: 30}}
	i := serving.SchedulerInstance{ID: "current", WorkloadUID: d.UID, Ready: true, MaxBatch: 4, Profile: &serving.Profile{ID: "profile"}}
	snap := serving.SchedulerSnapshot{Workloads: []*serving.Definition{d}, Instances: []serving.SchedulerInstance{i},
		Routing: map[string]map[string]int32{d.UID: {i.ID: 1000}}}
	in := serving.NewCapacityInput(s, snap.DispatchOccupancy, serving.SchedulerOption{Instance: i}, d)
	policy, b1 := 40.0, 26.0
	rows := map[string]serving.CapacityRow{in.Key(): {Status: serving.CapacityBuilt, Kappa: &policy}}
	in.MaxBatch = 1
	rows[in.Key()] = serving.CapacityRow{Status: serving.CapacityBuilt, Kappa: &b1}
	got := replayDemands(s, snap, rows, nil)
	if len(got) != 1 || got[0].KeepKappa != policy || got[0].TargetKappa != policy || got[0].KeepShort {
		t.Fatalf("batch-1 row changed policy supply: %+v", got)
	}
}

func TestReplayReaderPreservesStoredSweepTimestamps(t *testing.T) {
	const stored = `{
		"at":"2026-11-05T12:00:00Z",
		"decision":{
			"at":"2026-11-05T12:00:00Z",
			"status":"Keep",
			"capacity":{"search":{
				"startedAt":"2026-11-05T12:00:00.125Z",
				"finishedAt":"2026-11-05T12:00:01.375Z",
				"durationNanos":1250000000
			}}
		},
		"snapshot":{
			"at":"2026-11-05T12:00:00Z",
			"workloadIDs":["recorded-definition"]
		}
	}`
	// A stored snapshot references the catalog, so readTick must consume the
	// record through observation.DecodeSchedulerLine rather than an API dump.
	catalog := &observation.ObservationCatalog{}
	catalog.Add(observation.CatalogRow{
		Kind: "definition", Key: "recorded-definition",
		Body: json.RawMessage(`{"uid":"w","name":"recorded-workload"}`),
	})
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(stored)); err != nil {
		t.Fatal(err)
	}
	reader := replayReader{
		scanner:     bufio.NewScanner(bytes.NewReader(compact.Bytes())),
		catalog:     catalog,
		margin:      -1,
		definitions: map[string]*serving.Definition{},
	}
	tick, err := reader.readTick()
	if err != nil {
		t.Fatal(err)
	}
	if tick == nil || tick.r.Decision.Capacity == nil || tick.r.Decision.Capacity.Search == nil {
		t.Fatalf("stored sweep missing from replay input: %+v", tick)
	}
	search := tick.r.Decision.Capacity.Search
	started := time.Date(2026, time.November, 5, 12, 0, 0, 125000000, time.UTC)
	finished := time.Date(2026, time.November, 5, 12, 0, 1, 375000000, time.UTC)
	if !search.StartedAt.Equal(started) || !search.FinishedAt.Equal(finished) {
		t.Fatalf("replay changed stored sweep timestamps: %v to %v", search.StartedAt, search.FinishedAt)
	}
	if search.StartedAt.IsZero() || search.FinishedAt.IsZero() || !search.FinishedAt.After(search.StartedAt) {
		t.Fatalf("replay lost the nonzero sweep interval: %v to %v", search.StartedAt, search.FinishedAt)
	}
	if len(tick.snap.Workloads) != 1 || tick.snap.Workloads[0].UID != "w" {
		t.Fatalf("stored snapshot catalog was not restored: %+v", tick.snap.Workloads)
	}
}
