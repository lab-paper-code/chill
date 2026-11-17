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
