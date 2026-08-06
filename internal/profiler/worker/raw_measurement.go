package worker

import (
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

// canonicalRawMeasurement is the worker's sole mode-specific evidence
// projector. Runtime execution and immutable-store orchestration stay outside
// this schema boundary.
func canonicalRawMeasurement(
	intent FrozenIntent,
	podUID string,
	runtime RuntimeMeasurement,
	metrics metricobserver.VerifiedResult,
	workload workloadobserver.VerifiedResult,
) ([]byte, error) {
	intentRecord, err := intent.snapshot()
	if err != nil {
		return nil, err
	}
	if intentRecord.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		idle, err := runtime.idleResult()
		if err != nil {
			return nil, err
		}
		return evidence.ComposeIdleRawMeasurement(
			intent.verifiedIntent(), podUID, idle, metrics, workload,
		)
	}
	active, err := runtime.activeResult()
	if err != nil {
		return nil, err
	}
	return evidence.ComposeRawMeasurement(
		intent.verifiedIntent(), podUID, active, metrics, workload,
	)
}
