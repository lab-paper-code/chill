package worker

import (
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

// canonicalRawMeasurement is the worker's sole mode-specific evidence
// projector. Runtime execution and immutable-store orchestration stay outside
// this schema boundary.
func canonicalRawMeasurement(
	config profilingrun.VerifiedRunConfig,
	podUID string,
	runtime RuntimeMeasurement,
	metrics metricobserver.VerifiedResult,
	workload workloadobserver.VerifiedResult,
) ([]byte, error) {
	runConfig, err := config.Record()
	if err != nil {
		return nil, err
	}
	if runConfig.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		idle, err := runtime.idleResult()
		if err != nil {
			return nil, err
		}
		return evidence.ComposeIdleRawMeasurement(
			config, podUID, idle, metrics, workload,
		)
	}
	active, err := runtime.activeResult()
	if err != nil {
		return nil, err
	}
	return evidence.ComposeRawMeasurement(
		config, podUID, active, metrics, workload,
	)
}
