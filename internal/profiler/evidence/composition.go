package evidence

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

// ComposeRawMeasurement correlates already-verified boundary values and
// canonicalizes one active raw record. It does not repeat their internal
// validation.
func ComposeRawMeasurement(
	intent profilingrun.VerifiedIntent,
	podUID string,
	runtime profilingrun.VerifiedRuntimeResult,
	metrics metricobserver.VerifiedResult,
	workload workloadobserver.VerifiedResult,
) ([]byte, error) {
	intentRecord, runtimeRecord, metricRecord, workloadRecord, err := activeCompositionRecords(
		intent, runtime, metrics, workload,
	)
	if err != nil {
		return nil, err
	}
	if intentRecord.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		return nil, errors.New("active raw composition received an idle Run intent")
	}
	if runtimeRecord.SchemaVersion != profilingrun.RuntimeResultSchemaVersion {
		return nil, errors.New("current active raw measurement requires the current runtime result")
	}
	started, ended, err := runtimeRecord.MeasurementWindow()
	if err != nil {
		return nil, fmt.Errorf("read verified runtime measurement window: %w", err)
	}
	if err := validateVerifiedComposition(
		intentRecord, podUID, metricRecord, workloadRecord, started, ended,
	); err != nil {
		return nil, err
	}
	raw := RawMeasurement{
		SchemaVersion: RawMeasurementSchemaVersion,
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
	intent profilingrun.VerifiedIntent,
	podUID string,
	runtime profilingrun.VerifiedIdleRuntimeResult,
	metrics metricobserver.VerifiedResult,
	workload workloadobserver.VerifiedResult,
) ([]byte, error) {
	intentRecord, err := intent.Record()
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
	if intentRecord.Measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode {
		return nil, errors.New("idle raw composition received an active Run intent")
	}
	if runtimeRecord.SchemaVersion != profilingrun.IdleRuntimeResultSchemaVersion {
		return nil, errors.New("current idle raw measurement requires the current idle runtime result")
	}
	started, ended, err := runtimeRecord.MeasurementWindow()
	if err != nil {
		return nil, fmt.Errorf("read verified idle measurement window: %w", err)
	}
	if err := validateVerifiedComposition(
		intentRecord, podUID, metricRecord, workloadRecord, started, ended,
	); err != nil {
		return nil, err
	}
	raw := IdleRawMeasurement{
		SchemaVersion: IdleRawMeasurementSchemaVersion,
		PodUID:        podUID,
		Runtime:       runtimeRecord,
		Metrics:       metricRecord,
		Workload:      &workloadRecord,
	}
	return raw.canonicalJSON()
}

func activeCompositionRecords(
	intent profilingrun.VerifiedIntent,
	runtime profilingrun.VerifiedRuntimeResult,
	metrics metricobserver.VerifiedResult,
	workload workloadobserver.VerifiedResult,
) (
	profilingrun.Intent,
	profilingrun.RuntimeResult,
	metricobserver.Result,
	workloadobserver.Result,
	error,
) {
	intentRecord, err := intent.Record()
	if err != nil {
		return profilingrun.Intent{}, profilingrun.RuntimeResult{}, metricobserver.Result{}, workloadobserver.Result{}, err
	}
	runtimeRecord, err := runtime.Record()
	if err != nil {
		return profilingrun.Intent{}, profilingrun.RuntimeResult{}, metricobserver.Result{}, workloadobserver.Result{}, err
	}
	metricRecord, err := metrics.Record()
	if err != nil {
		return profilingrun.Intent{}, profilingrun.RuntimeResult{}, metricobserver.Result{}, workloadobserver.Result{}, err
	}
	workloadRecord, err := workload.Record()
	if err != nil {
		return profilingrun.Intent{}, profilingrun.RuntimeResult{}, metricobserver.Result{}, workloadobserver.Result{}, err
	}
	return intentRecord, runtimeRecord, metricRecord, workloadRecord, nil
}

func validateVerifiedComposition(
	intentRecord profilingrun.Intent,
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
	if metrics.Source.Kind != intentRecord.Metrics.SourceKind ||
		metrics.Source.NodeName != intentRecord.Metrics.NodeName ||
		metrics.Source.Endpoint != intentRecord.Metrics.Endpoint ||
		metrics.Source.Metric != intentRecord.Metrics.PowerMetric {
		return errors.New("metric source identity mismatch")
	}
	if !metrics.StartedAt.Before(runtimeEnded) || !runtimeStarted.Before(metrics.EndedAt) {
		return errors.New("runtime and metric observation windows do not overlap")
	}
	if workload.Target.NodeName != intentRecord.TargetNode.Name ||
		workload.Target.NodeUID != intentRecord.TargetNode.UID || workload.Run.PodUID != podUID {
		return errors.New("workload observation target or Run Pod identity mismatch")
	}
	if workload.CoverageStartedAt.After(runtimeStarted) || workload.CoverageEndedAt.Before(runtimeEnded) {
		return errors.New("workload observation does not cover the runtime measurement window")
	}
	return nil
}
