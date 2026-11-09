package serving

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func TestFeedbackConsumerMinimumDemandSupplementsOnlyFiniteDeficit(t *testing.T) {
	for _, observed := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(observed), func(t *testing.T) {
			settings, snapshot, candidate := feedbackFixture()
			snapshot.Arrivals = snapshot.Arrivals[:observed]
			original := append([]SchedulerArrival(nil), snapshot.Arrivals...)
			settings.MinimumDemand = map[string]float64{snapshot.Workloads[0].UID: 3000}
			result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
			score := result.Workloads[0]
			if score.Population != 3 || score.ObservedArrivals != observed || score.ReplayedArrivals != observed || score.MinimumDemandArrivals != 3-observed || !slices.Contains(result.Sources, "minimum-demand") || len(result.Assumptions) == 0 || !slices.Equal(original, snapshot.Arrivals) {
				t.Fatalf("minimum demand altered observed pattern or lost its model source: %+v", result)
			}
			candidate.Keep = false
			again := feedbackEvaluateForTest(t, settings, snapshot, candidate)
			if !reflect.DeepEqual(result, again) {
				t.Fatalf("keep and same target must receive identical demand cohort: keep=%+v target=%+v", result, again)
			}
		})
	}
}

func TestFeedbackConsumerObservedDemandAboveFloorIsUnchanged(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	baseline := feedbackEvaluateForTest(t, settings, snapshot, candidate)
	settings.MinimumDemand = map[string]float64{snapshot.Workloads[0].UID: 1000}
	withFloor := feedbackEvaluateForTest(t, settings, snapshot, candidate)
	if !reflect.DeepEqual(baseline, withFloor) || withFloor.Workloads[0].MinimumDemandArrivals != 0 || slices.Contains(withFloor.Sources, "minimum-demand") {
		t.Fatalf("floor below observed cohort changed replay: baseline=%+v floor=%+v", baseline, withFloor)
	}
}
