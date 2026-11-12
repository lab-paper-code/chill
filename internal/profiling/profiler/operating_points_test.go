package profiler

import (
	"testing"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestFreezePhaseDurationCoversEveryPhaseAndPreservesMargin(t *testing.T) {
	request := PreparationRequest{
		Measurement: profilingrun.MeasurementContract{WorkloadMode: profilingrun.EmpiricalCadenceFixedBatchWorkloadMode, DurationSeconds: 30,
			OperatingPoints: &profilingrun.OperatingPointsContract{Utilizations: []float64{.2, .8}, SparseMeanGapMilliseconds: 1000,
				PhaseDurationSeconds: 30, RequestLifetimeSeconds: 10, DeliveryMarginMilliseconds: 100, FramedDecodedRequestUpperBytes: 1024}},
		Metrics: MetricObservationPolicy{DurationSeconds: 31},
	}
	for range 2 {
		if err := request.FreezePhaseDuration(); err != nil {
			t.Fatal(err)
		}
		if request.Measurement.DurationSeconds != 120 || request.Metrics.DurationSeconds != 121 {
			t.Fatalf("phase observation horizon=%+v %+v", request.Measurement, request.Metrics)
		}
	}
	idle := PreparationRequest{Measurement: profilingrun.MeasurementContract{WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode, DurationSeconds: 30}, Metrics: MetricObservationPolicy{DurationSeconds: 31}}
	if err := idle.FreezePhaseDuration(); err != nil || idle.Measurement.DurationSeconds != 30 || idle.Metrics.DurationSeconds != 31 {
		t.Fatalf("changed idle horizon: %+v, %v", idle, err)
	}
}
