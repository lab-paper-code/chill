package serving

import (
	"context"
	"reflect"
	"testing"
)

func TestFeedbackRoutingUsesProfileSupportWithoutCapacityGate(t *testing.T) {
	_, snapshot := capacityFixture()
	a, b := *snapshot.Instances[0].Profile, *snapshot.Instances[0].Profile
	a.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{50e6})}
	b.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{100e6})}
	inputs := []FeedbackRouteInput{{InstanceID: "a", Profile: &a, MaxBatch: 1, ExistingWeight: 7}, {InstanceID: "b", Profile: &b, MaxBatch: 1, ExistingWeight: 3}}
	got, err := BuildFeedbackRouting(inputs, true)
	if err != nil || !reflect.DeepEqual(got, []int32{667, 333}) {
		t.Fatalf("2:1 support: %v %v", got, err)
	}
	got, err = BuildFeedbackRouting(inputs, false)
	if err != nil || !reflect.DeepEqual(got, []int32{7, 3}) {
		t.Fatalf("fixed routing: %v %v", got, err)
	}
}

func TestFeedbackRateWitnessTestsOnlyExplicitRates(t *testing.T) {
	settings, _ := capacityFixture()
	settings.CapacityWindows, settings.CapacityPassingWindows = 1, 1
	var queried []float64
	builder := capacityBuilder{settings: settings, percentile: 100, replay: func(_ context.Context, rate float64, n, seed int) (capacityWindowResult, error) {
		queried = append(queried, rate)
		return capacityWindowResult{met: rate == 4}, nil
	}}
	empty, err := judgeFeedbackRates(context.Background(), builder, "current", []float64{0})
	failed, failErr := judgeFeedbackRates(context.Background(), builder, "current", []float64{2, 2})
	if err != nil || failErr != nil || empty.KappaHat != 0 || failed.KappaHat != 0 || len(empty.Tests) != 0 || len(failed.Tests) != 1 || failed.Tests[0].Met {
		t.Fatalf("zero witnesses lost evidence: empty=%+v failed=%+v errors=%v/%v", empty, failed, err, failErr)
	}
	got, err := judgeFeedbackRates(context.Background(), builder, "current", []float64{4, 9, 4, 0})
	if err != nil || got.KappaHat != 4 || len(got.Tests) != 2 || !reflect.DeepEqual(queried, []float64{2, 4, 9}) {
		t.Fatalf("unexpected grid/interpolation: %+v %v queries=%v", got, err, queried)
	}
}
