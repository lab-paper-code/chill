package projection

import (
	"reflect"
	"testing"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestFrozenOperatingPointsRoundTripWithoutAliasing(t *testing.T) {
	config := profilingrun.RunConfig{Measurement: profilingrun.MeasurementContract{OperatingPoints: &profilingrun.OperatingPointsContract{
		Utilizations: []float64{.2, .8}, SparseMeanGapMilliseconds: 1000, PhaseDurationSeconds: 30, PostLoadDurationSeconds: 30,
		RequestLifetimeSeconds: 10, DeliveryMarginMilliseconds: 100, FramedDecodedRequestUpperBytes: 1024, QueueBytesCap: 64 << 20,
	}}}
	protocol := projectCurveProtocol(config)
	if !reflect.DeepEqual(config.Measurement.OperatingPoints, (*profilingrun.OperatingPointsContract)(protocol.OperatingPoints)) {
		t.Fatalf("frozen operating points changed: %+v", protocol.OperatingPoints)
	}
	config.Measurement.OperatingPoints.Utilizations[0] = .9
	if protocol.OperatingPoints.Utilizations[0] != .2 {
		t.Fatal("RunConfig mutated frozen arrival rates")
	}
}
