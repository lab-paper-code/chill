package worker

import (
	"errors"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

// canonicalRawMeasurement is the worker's sole mode-specific evidence
// projector. Runtime execution and immutable-store orchestration stay outside
// this schema boundary.
func canonicalRawMeasurement(
	intent profilingrun.Intent,
	intentDigest string,
	podUID string,
	runtime RuntimeMeasurement,
	metrics metricobserver.Result,
	workload workloadobserver.Result,
) ([]byte, error) {
	if intent.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		if runtime.Idle == nil {
			return nil, errors.New("verified idle runtime result is missing")
		}
		raw := evidence.IdleRawMeasurement{
			SchemaVersion: evidence.IdleRawMeasurementSchemaVersion,
			IntentDigest:  intentDigest,
			PodUID:        podUID,
			Runtime:       *runtime.Idle,
			Metrics:       metrics,
			Workload:      &workload,
		}
		return raw.CanonicalJSON(intent)
	}
	if runtime.Active == nil {
		return nil, errors.New("verified active runtime result is missing")
	}
	raw := evidence.RawMeasurement{
		SchemaVersion: evidence.RawMeasurementSchemaVersion,
		IntentDigest:  intentDigest,
		PodUID:        podUID,
		Runtime:       *runtime.Active,
		Metrics:       metrics,
		Workload:      &workload,
	}
	return raw.CanonicalJSON(intent)
}
