package observation

import (
	"context"
	"testing"
	"time"
)

// A session reads its meter's recorded samples as stored: overlapping
// records' shared samples once, nothing interpolated at the window edges,
// another meter's samples never, and ambiguous raw points remain unchosen.
func TestPowerSamplesReturnsRecordedSamplesOfOneMeter(t *testing.T) {
	for _, scenario := range []operatingPowerScenario{{name: "recorded"}, {name: "conflict", conflictingSample: true}} {
		t.Run(scenario.name, func(t *testing.T) {
			started := observationTestHour.Add(time.Hour - 8*time.Second)
			observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, started)
			from := started.Add(time.Second)
			to := from.Add(10 * time.Second)
			meters := seedOperatingPower(t, observationLog, from, scenario)
			clock.set(to)
			got, err := observationLog.PowerSamples(context.Background(), meters[0], from.Add(time.Second), to)
			if scenario.conflictingSample {
				if err != nil || len(got.Samples) != 3 || !got.Samples[0].ObservedAt.Equal(got.Samples[1].ObservedAt) ||
					got.Samples[0].Watts == got.Samples[1].Watts {
					t.Fatalf("conflicting raw readings were lost or chosen: %v %+v", err, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// wall-a recorded 0 W at from, 40 W at from+4s (twice) and 100 W at
			// from+10s; the window starts after the first sample.
			if len(got.Samples) != 2 || !got.Samples[0].ObservedAt.Equal(from.Add(4*time.Second)) || got.Samples[0].Watts != 40 ||
				!got.Samples[1].ObservedAt.Equal(to) || got.Samples[1].Watts != 100 {
				t.Fatalf("samples not as recorded: %+v", got.Samples)
			}
			if got.GapUntil != nil || len(got.Coverage.Sources) == 0 {
				t.Fatalf("coverage of a fully read window: %+v %v", got.Coverage, got.GapUntil)
			}
			// wall-b recorded 20 W at from, from+6s (twice) and from+10s.
			other, err := observationLog.PowerSamples(context.Background(), meters[1], from, to)
			if err != nil || len(other.Samples) != 3 || other.Samples[0].Watts != 20 || other.Samples[1].Watts != 20 || other.Samples[2].Watts != 20 {
				t.Fatalf("wall-b read is not its own recorded samples: %v %+v", err, other.Samples)
			}
		})
	}
}
