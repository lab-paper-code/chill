package profilederivation

import (
	"math"
	"testing"
	"time"
)

func TestIdleMeasurementDoesNotSelectTrialsByEnvironmentalObservations(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	observation := IdleObservation{
		StateIdentity: "state", TrialID: "idle", ModelResidentAtEnd: true,
		WindowStart: start, WindowEnd: start.Add(10 * time.Second),
		Samples:  []Sample{{start, 4}, {start.Add(10 * time.Second), 6}},
		Failures: 3, SourceTimestampAbsent: true,
		ExternalWorkloadChanges: []string{"pod-replaced"},
	}
	trial, err := MeasureIdle(observation)
	if err != nil || trial.ModelLoadedIdleMeanWallWatts != 5 {
		t.Fatalf("idle measurement = %+v, error = %v", trial, err)
	}
	if len(observation.Samples) != 2 || observation.Failures != 3 {
		t.Fatal("calculation changed the observations")
	}
	observation.Samples[1].Watts = math.NaN()
	if _, err := MeasureIdle(observation); err == nil {
		t.Fatal("non-finite power produced a numeric result")
	}
}

func TestIncrementalEnergyKeepsNegativeMeasuredEstimate(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	trial := Trial{
		StateIdentity: "state", EnvironmentIdentity: "environment", TrialID: "active",
		Batch: 1, CompletedItems: 1, CompletedCalls: 1,
		LatencyMilliseconds: []float64{1},
		WindowStart:         start, WindowEnd: start.Add(time.Second),
		Samples: []Sample{{start, 4}, {start.Add(time.Second), 4}},
	}
	baseline := IdleBaseline{
		StateIdentity: "state", EnvironmentIdentity: "environment",
		MeasurementProtocolIdentity: "idle", ModelLoadedIdleMeanWallWatts: 5,
	}
	result, err := IncrementalWorkTrial(trial, baseline)
	if err != nil || result.IncrementalWorkJoulesPerItem != -1 {
		t.Fatalf("incremental measurement = %+v, error = %v", result, err)
	}
}

func TestNativeTimingSurvivesEnergyProjectionWithoutReplacingClientLatency(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	trial := Trial{StateIdentity: "state", EnvironmentIdentity: "environment", TrialID: "active",
		Batch: 2, CompletedCalls: 1, CompletedItems: 2, WindowStart: start, WindowEnd: start.Add(time.Second),
		LatencyMilliseconds: []float64{10}, Samples: []Sample{{start, 6}, {start.Add(time.Second), 6}},
		NativeSamples: []NativeServiceSample{{BatchID: 3, BatchSize: 2, RunnerServiceMilliseconds: 5, BackendCallMilliseconds: 2, NativeRequestMilliseconds: 8}},
	}
	baseline := IdleBaseline{StateIdentity: "state", EnvironmentIdentity: "environment", MeasurementProtocolIdentity: "idle", ModelLoadedIdleMeanWallWatts: 4}
	result, err := IncrementalWorkTrial(trial, baseline)
	if err != nil || result.IncrementalWorkJoulesPerItem != 1 || result.LatencyP99Milliseconds != 10 ||
		len(result.NativeSamples) != 1 || result.NativeSamples[0].RunnerServiceMilliseconds != 5 || result.ClientLatencyMilliseconds[0] != 10 {
		t.Fatalf("distinct timings or exact idle energy subtraction changed: %+v %v", result, err)
	}
	trial.NativeSamples[0].BackendCallMilliseconds = 6
	if _, err := IncrementalWorkTrial(trial, baseline); err == nil {
		t.Fatal("inverted overlapping backend/service spans accepted")
	}
	if result.NativeSamples[0].BackendCallMilliseconds != 2 {
		t.Fatal("projected native evidence aliases mutable trial")
	}
}

func TestCurveCopiesRetainedNativeEvidence(t *testing.T) {
	point := Point{Trials: []EnergyTrial{{ClientLatencyMilliseconds: []float64{10},
		NativeSamples: []NativeServiceSample{{BatchID: 1, RunnerServiceMilliseconds: 5}}}}}
	copy := clonePoint(point)
	copy.Trials[0].ClientLatencyMilliseconds[0] = 100
	copy.Trials[0].NativeSamples[0].RunnerServiceMilliseconds = 50
	if point.Trials[0].ClientLatencyMilliseconds[0] != 10 || point.Trials[0].NativeSamples[0].RunnerServiceMilliseconds != 5 {
		t.Fatal("curve projection permits mutation of retained native evidence")
	}
}
