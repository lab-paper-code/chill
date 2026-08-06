package worker

import (
	"github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

// canonicalRawMeasurement is the worker's sole mode-specific evidence
// projector. Runtime execution and immutable-store orchestration stay outside
// this schema boundary.
func canonicalRawMeasurement(
	intent profilingrun.Intent,
	intentDigest string,
	podUID string,
	runtime RuntimeMeasurement,
	metrics verifiedMetricResult,
	workload verifiedWorkloadResult,
) ([]byte, error) {
	metricRecord, err := metrics.record()
	if err != nil {
		return nil, err
	}
	workloadRecord, err := workload.record()
	if err != nil {
		return nil, err
	}
	if intent.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		idle, err := runtime.idleResult()
		if err != nil {
			return nil, err
		}
		raw := evidence.IdleRawMeasurement{
			SchemaVersion: evidence.IdleRawMeasurementSchemaVersion,
			IntentDigest:  intentDigest,
			PodUID:        podUID,
			Runtime:       idle,
			Metrics:       metricRecord,
			Workload:      &workloadRecord,
		}
		return raw.CanonicalJSON(intent)
	}
	active, err := runtime.activeResult()
	if err != nil {
		return nil, err
	}
	raw := evidence.RawMeasurement{
		SchemaVersion: evidence.RawMeasurementSchemaVersion,
		IntentDigest:  intentDigest,
		PodUID:        podUID,
		Runtime:       active,
		Metrics:       metricRecord,
		Workload:      &workloadRecord,
	}
	return raw.CanonicalJSON(intent)
}
