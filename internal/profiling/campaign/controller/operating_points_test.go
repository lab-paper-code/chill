package controller

import (
	"reflect"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
)

func TestBuildRunFreezesNamedOperatingPoints(t *testing.T) {
	parent := testParent()
	parent.Spec.ActiveProtocol.OperatingPoints = api.MeasurementOperatingPointsSpec{
		Utilizations: []float64{.2, .8}, SparseMeanGapMilliseconds: 1000, PhaseDurationSeconds: 30,
		RequestLifetimeSeconds: 10, DeliveryMarginMilliseconds: 100, FramedDecodedRequestUpperBytes: 1024,
	}
	child, err := BuildRun(parent, campaign.Measurement{WorkloadMode: campaign.WorkloadModeActive, Batch: 2}, 1, "ort-cpu")
	if err != nil {
		t.Fatal(err)
	}
	if child.Spec.Measurement.OperatingPoints == nil || !reflect.DeepEqual(*child.Spec.Measurement.OperatingPoints, parent.Spec.ActiveProtocol.OperatingPoints) {
		t.Fatalf("child lost operating points: %+v", child.Spec.Measurement.OperatingPoints)
	}
	parent.Spec.ActiveProtocol.OperatingPoints.Utilizations[0] = .9
	if child.Spec.Measurement.OperatingPoints.Utilizations[0] != .2 {
		t.Fatal("parent mutation changed frozen child rates")
	}
}
