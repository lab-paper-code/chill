package evidence

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

// ComposeRawMeasurement correlates already-verified boundary values and
// canonicalizes one active raw record. It does not repeat their internal
// validation.
func ComposeRawMeasurement(
	config profilingrun.VerifiedRunConfig,
	podUID string,
	runtime profilingrun.VerifiedRuntimeResult,
	metrics metricobserver.VerifiedResult,
	workload workloadobserver.VerifiedResult,
) ([]byte, error) {
	runConfig, runtimeRecord, metricRecord, workloadRecord, err := activeCompositionRecords(
		config, runtime, metrics, workload,
	)
	if err != nil {
		return nil, err
	}
	if runConfig.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		return nil, errors.New("active raw composition received an idle RunConfig")
	}
	if runtimeRecord.SchemaVersion != profilingrun.RuntimeResultSchemaVersion {
		return nil, errors.New("current active raw measurement requires the current runtime result")
	}
	started, ended, err := runtimeRecord.MeasurementWindow()
	if err != nil {
		return nil, fmt.Errorf("read verified runtime measurement window: %w", err)
	}
	if err := validateVerifiedComposition(
		runConfig, podUID, metricRecord, workloadRecord, started, ended,
	); err != nil {
		return nil, err
	}
	raw := RawMeasurement{
		SchemaVersion: RawMeasurementSchemaVersion,
		RunConfig:     runConfig,
		PodUID:        podUID,
		Runtime:       runtimeRecord,
		Metrics:       metricRecord,
		Workload:      &workloadRecord,
	}
	return raw.canonicalJSON()
}

// ComposeIdleRawMeasurement is the model-loaded-idle counterpart to
// ComposeRawMeasurement.
func ComposeIdleRawMeasurement(
	config profilingrun.VerifiedRunConfig,
	podUID string,
	runtime profilingrun.VerifiedIdleRuntimeResult,
	metrics metricobserver.VerifiedResult,
	workload workloadobserver.VerifiedResult,
) ([]byte, error) {
	runConfig, err := config.Record()
	if err != nil {
		return nil, err
	}
	runtimeRecord, err := runtime.Record()
	if err != nil {
		return nil, err
	}
	metricRecord, err := metrics.Record()
	if err != nil {
		return nil, err
	}
	workloadRecord, err := workload.Record()
	if err != nil {
		return nil, err
	}
	if runConfig.Measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode {
		return nil, errors.New("idle raw composition received an active RunConfig")
	}
	if runtimeRecord.SchemaVersion != profilingrun.IdleRuntimeResultSchemaVersion {
		return nil, errors.New("current idle raw measurement requires the current idle runtime result")
	}
	started, ended, err := runtimeRecord.MeasurementWindow()
	if err != nil {
		return nil, fmt.Errorf("read verified idle measurement window: %w", err)
	}
	if err := validateVerifiedComposition(
		runConfig, podUID, metricRecord, workloadRecord, started, ended,
	); err != nil {
		return nil, err
	}
	raw := IdleRawMeasurement{
		SchemaVersion: IdleRawMeasurementSchemaVersion,
		RunConfig:     runConfig,
		PodUID:        podUID,
		Runtime:       runtimeRecord,
		Metrics:       metricRecord,
		Workload:      &workloadRecord,
	}
	return raw.canonicalJSON()
}

func activeCompositionRecords(
	config profilingrun.VerifiedRunConfig,
	runtime profilingrun.VerifiedRuntimeResult,
	metrics metricobserver.VerifiedResult,
	workload workloadobserver.VerifiedResult,
) (
	profilingrun.RunConfig,
	profilingrun.RuntimeResult,
	metricobserver.Result,
	workloadobserver.Result,
	error,
) {
	runConfig, err := config.Record()
	if err != nil {
		return profilingrun.RunConfig{}, profilingrun.RuntimeResult{}, metricobserver.Result{}, workloadobserver.Result{}, err
	}
	runtimeRecord, err := runtime.Record()
	if err != nil {
		return profilingrun.RunConfig{}, profilingrun.RuntimeResult{}, metricobserver.Result{}, workloadobserver.Result{}, err
	}
	metricRecord, err := metrics.Record()
	if err != nil {
		return profilingrun.RunConfig{}, profilingrun.RuntimeResult{}, metricobserver.Result{}, workloadobserver.Result{}, err
	}
	workloadRecord, err := workload.Record()
	if err != nil {
		return profilingrun.RunConfig{}, profilingrun.RuntimeResult{}, metricobserver.Result{}, workloadobserver.Result{}, err
	}
	return runConfig, runtimeRecord, metricRecord, workloadRecord, nil
}

func validateVerifiedComposition(
	runConfig profilingrun.RunConfig,
	podUID string,
	metrics metricobserver.Result,
	workload workloadobserver.Result,
	runtimeStarted time.Time,
	runtimeEnded time.Time,
) error {
	if strings.TrimSpace(podUID) == "" || strings.TrimSpace(podUID) != podUID {
		return errors.New("raw measurement Pod UID is required")
	}
	if metrics.Source.ThermalMetrics == nil {
		return errors.New("metric source thermal identity is required")
	}
	if !metricSourceMatches(metrics.Source, runConfig.Metrics) {
		return errors.New("metric source identity mismatch")
	}
	if !metrics.StartedAt.Before(runtimeEnded) || !runtimeStarted.Before(metrics.EndedAt) {
		return errors.New("runtime and metric observation windows do not overlap")
	}
	if workload.Target.NodeName != runConfig.TargetNode.Name ||
		workload.Target.NodeUID != runConfig.TargetNode.UID || workload.Run.PodUID != podUID {
		return errors.New("workload observation target or Run Pod identity mismatch")
	}
	if workload.CoverageStartedAt.After(runtimeStarted) || workload.CoverageEndedAt.Before(runtimeEnded) {
		return errors.New("workload observation does not cover the runtime measurement window")
	}
	return nil
}
