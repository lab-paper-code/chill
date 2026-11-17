package serving

import (
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
