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
