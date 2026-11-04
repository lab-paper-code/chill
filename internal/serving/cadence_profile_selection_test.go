package serving

import (
	"testing"
	"time"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/test/fixtures"
)

func TestProfileSelectionSeparatesCadenceTargetNotTraceProvenance(t *testing.T) {
	const newestProfileID = "newer"
	for _, differentTarget := range []bool{false, true} {
		name := "new trace and collection window supersede the same target"
		if differentTarget {
			name = "different offered-load context remains a distinct profile"
		}
		t.Run(name, func(t *testing.T) {
			older := Profile{ID: "older", MeasuredAt: time.Unix(1, 0), Condition: profilederivation.CurvePlanSpec{
				ActiveProtocol: profilederivation.CurveProtocolContract{Cadences: fixtures.Cadences(1)},
			}}
			newer := older
			newer.ID, newer.MeasuredAt = newestProfileID, older.MeasuredAt.Add(time.Second)
			newer.Condition = profilederivation.CloneCurvePlanSpec(older.Condition)
			cadence := newer.Condition.ActiveProtocol.Cadences[1]
			cadence.Provenance.Seed++
			cadence.Provenance.Sources[0].Object.Key = "new-actual-trace"
			cadence.Provenance.Sources[0].WindowEnd = cadence.Provenance.Sources[0].WindowEnd.Add(time.Minute)
			cadence.Provenance.Draws[0].ElementIndex = 1
			newer.Condition.ActiveProtocol.MetricObserverImage = "new-observer"
			newer.Condition.ActiveProtocol.MetricDurationSeconds++
			if differentTarget {
				cadence.Target.LoadContext = "different-recorded-offered-load"
				cadence.Target.Schedule[0].OfferedLoad = "different-recorded-rate"
				cadence.Provenance.Sources[0].LoadContext = cadence.Target.LoadContext
			}
			selected, err := SelectConditionProfiles([]Profile{older, newer}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if differentTarget {
				if len(selected) != 2 {
					t.Fatalf("distinct measured load populations disappeared: %+v", selected)
				}
			} else if len(selected) != 1 || selected[0].ID != newer.ID {
				t.Fatalf("trace/observer provenance split the same serving population: %+v", selected)
			}
		})
	}
}
