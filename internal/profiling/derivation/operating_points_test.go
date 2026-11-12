package profilederivation

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func TestOperatingPointEnergyUsesOnlyUtilizationPopulation(t *testing.T) {
	// The saturation and sparse phases deliberately have much higher power.
	const raw = `{
	  "trialID":"physical-pod","batch":2,
	  "windowStart":"2026-11-13T00:00:00Z","windowEnd":"2026-11-13T00:00:30Z",
	  "completedCalls":3,"completedItems":6,"latencyMilliseconds":[1,2,3],
	  "samples":[{"observedAt":"2026-11-13T00:00:00Z","watts":100},{"observedAt":"2026-11-13T00:00:30Z","watts":100}],
	  "phases":[
	    {"id":"saturation","kind":"saturation","windowStart":"2026-11-13T00:00:00Z","windowEnd":"2026-11-13T00:00:10Z","completedItems":2,"samples":[{"observedAt":"2026-11-13T00:00:00Z","watts":100},{"observedAt":"2026-11-13T00:00:10Z","watts":100}]},
	    {"id":"utilization-0","kind":"utilization","windowStart":"2026-11-13T00:00:10Z","windowEnd":"2026-11-13T00:00:20Z","completedItems":2,"samples":[{"observedAt":"2026-11-13T00:00:10Z","watts":30},{"observedAt":"2026-11-13T00:00:20Z","watts":30}]},
	    {"id":"sparse","kind":"sparse","windowStart":"2026-11-13T00:00:20Z","windowEnd":"2026-11-13T00:00:30Z","completedItems":2,"samples":[{"observedAt":"2026-11-13T00:00:20Z","watts":200},{"observedAt":"2026-11-13T00:00:30Z","watts":200}]}
	  ]
	}`
	var trial Trial
	if err := json.Unmarshal([]byte(raw), &trial); err != nil {
		t.Fatal(err)
	}
	energy, err := IncrementalWorkTrial(trial, IdleBaseline{ModelLoadedIdleMeanWallWatts: 10})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(energy.IncrementalWorkJoulesPerItem-100) > 1e-9 {
		t.Fatalf("EPI = %g, want (30*10 - 10*10)/2 = 100; saturation and sparse must not enter energy population", energy.IncrementalWorkJoulesPerItem)
	}
	encoded, err := json.Marshal(energy)
	if err != nil {
		t.Fatal(err)
	}
	var measured struct {
		PhasePower []struct {
			ID            string  `json:"id"`
			MeanWallWatts float64 `json:"meanWallWatts"`
		} `json:"phasePower"`
	}
	if err := json.Unmarshal(encoded, &measured); err != nil {
		t.Fatal(err)
	}
	if len(measured.PhasePower) != 3 {
		t.Fatalf("phase power evidence = %s", encoded)
	}
	for index, want := range []float64{100, 30, 200} {
		if measured.PhasePower[index].MeanWallWatts != want {
			t.Fatalf("phase %d power = %g, want %g", index, measured.PhasePower[index].MeanWallWatts, want)
		}
	}
}

func TestSaturationDispatchUsesActualBackloggedBatchService(t *testing.T) {
	start := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	phase := OperatingPointPhase{ID: "saturation", Kind: "saturation", WindowStart: start, WindowEnd: start.Add(time.Second),
		CompletedItems: 4, Samples: []Sample{{start, 30}, {start.Add(time.Second), 30}}}
	phase.NativeSamples = []NativeServiceSample{
		{BatchID: 1, BatchSize: 2, RecordIDs: []string{"a", "b"}, ServiceSample: firstfloor.ServiceSample{TrialID: "pod", ServiceDurationNanos: 3e6},
			StartedAt: start, FinishedAt: start.Add(3 * time.Millisecond)},
		{BatchID: 2, BatchSize: 2, RecordIDs: []string{"c", "d"}, ServiceSample: firstfloor.ServiceSample{TrialID: "pod", ServiceDurationNanos: 9e6},
			StartedAt: start.Add(5 * time.Millisecond), FinishedAt: start.Add(14 * time.Millisecond), LatestEnqueuedAt: start.Add(2 * time.Millisecond)},
	}
	// Keep every phase's power while using only the utilization input count.
	ladder := phase
	ladder.ID, ladder.Kind, ladder.NativeSamples = phaseKindUtilization, phaseKindUtilization, nil
	trial := Trial{Batch: 2, CompletedCalls: 4, CompletedItems: 8, LatencyMilliseconds: []float64{1, 1, 1, 1},
		WindowStart: start, WindowEnd: start.Add(time.Second), Samples: phase.Samples, Phases: []OperatingPointPhase{phase, ladder}}
	energy, err := IncrementalWorkTrial(trial, IdleBaseline{ModelLoadedIdleMeanWallWatts: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(energy.DispatchOccupancyNanosPerInput) != 1 || energy.DispatchOccupancyNanosPerInput[0] != 1e6 {
		t.Fatalf("d=%v, want (5ms-3ms)/2 = 1ms per input", energy.DispatchOccupancyNanosPerInput)
	}
	if got := float64(phase.NativeSamples[0].ServiceDurationNanos) + 2*energy.DispatchOccupancyNanosPerInput[0]; got != 5e6 {
		t.Fatalf("cycle=S+B*d=%g, want 5ms", got)
	}
	trial.Phases[0].NativeSamples[1].LatestEnqueuedAt = start.Add(4 * time.Millisecond)
	energy, err = IncrementalWorkTrial(trial, IdleBaseline{ModelLoadedIdleMeanWallWatts: 10})
	if err != nil || len(energy.DispatchOccupancyNanosPerInput) != 0 {
		t.Fatalf("non-backlogged pair entered d: %+v %v", energy.DispatchOccupancyNanosPerInput, err)
	}
}
