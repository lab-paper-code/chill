package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

const RawMeasurementSchemaVersion = "chill.dacs.io/profiling-raw-measurement.v1alpha7"

// RawMeasurement contains only facts available to the in-Job worker before it
// exits. Kubernetes terminal state is deliberately excluded.
type RawMeasurement struct {
	SchemaVersion string                     `json:"schemaVersion"`
	RunConfig     profilingrun.RunConfig     `json:"runConfig"`
	PodUID        string                     `json:"podUID"`
	Runtime       profilingrun.RuntimeResult `json:"runtime"`
	Metrics       metricobserver.Result      `json:"metrics"`
	Workload      *workloadobserver.Result   `json:"workload,omitempty"`
}

// UnmarshalJSON strictly decodes the current metric field name.
func (m *RawMeasurement) UnmarshalJSON(payload []byte) error {
	type wire RawMeasurement
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("raw measurement contains multiple JSON values")
		}
		return err
	}
	*m = RawMeasurement(decoded)
	return nil
}

func (m RawMeasurement) Validate() error {
	return m.validate()
}

func (m RawMeasurement) validate() error {
	runConfig := m.RunConfig
	if err := runConfig.Validate(); err != nil {
		return fmt.Errorf("validate RunConfig: %w", err)
	}
	if m.SchemaVersion != RawMeasurementSchemaVersion {
		return fmt.Errorf("unsupported raw measurement schemaVersion %q", m.SchemaVersion)
	}
	if m.Runtime.SchemaVersion != profilingrun.RuntimeResultSchemaVersion {
		return errors.New("current raw measurement requires the current runtime result")
	}
	if strings.TrimSpace(m.PodUID) == "" || strings.TrimSpace(m.PodUID) != m.PodUID {
		return errors.New("raw measurement Pod UID is required")
	}
	if err := profilingrun.ValidateRuntime(runConfig, m.PodUID, m.Runtime); err != nil {
		return fmt.Errorf("validate runtime result: %w", err)
	}
	if err := validateMetricObservation(runConfig, m.Metrics); err != nil {
		return err
	}
	runtimeStarted, runtimeEnded, err := m.Runtime.MeasurementWindow()
	if err != nil {
		return fmt.Errorf("read runtime measurement window: %w", err)
	}
	if !m.Metrics.StartedAt.Before(runtimeEnded) || !runtimeStarted.Before(m.Metrics.EndedAt) {
		return errors.New("runtime and metric observation windows do not overlap")
	}
	if m.Workload == nil {
		return errors.New("raw measurement workload observation is required")
	}
	if err := validateWorkloadObservation(runConfig, m.PodUID, runtimeStarted, runtimeEnded, *m.Workload); err != nil {
		return err
	}
	return nil
}

func (m RawMeasurement) CanonicalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m.canonicalJSON()
}

func (m RawMeasurement) canonicalJSON() ([]byte, error) {
	canonical := m
	canonical.Metrics.StartedAt = canonical.Metrics.StartedAt.UTC()
	canonical.Metrics.EndedAt = canonical.Metrics.EndedAt.UTC()
	canonical.Metrics.Samples = append([]metricobserver.Sample(nil), m.Metrics.Samples...)
	for i := range canonical.Metrics.Samples {
		canonical.Metrics.Samples[i].ObservedAt = canonical.Metrics.Samples[i].ObservedAt.UTC()
	}
	canonical.Metrics.Failures = append([]metricobserver.Failure(nil), m.Metrics.Failures...)
	for i := range canonical.Metrics.Failures {
		canonical.Metrics.Failures[i].ObservedAt = canonical.Metrics.Failures[i].ObservedAt.UTC()
	}
	if m.Workload != nil {
		workload := m.Workload.CanonicalCopy()
		canonical.Workload = &workload
	}
	return json.Marshal(canonical)
}

func validateWorkloadObservation(
	config profilingrun.RunConfig,
	podUID string,
	runtimeStarted time.Time,
	runtimeEnded time.Time,
	result workloadobserver.Result,
) error {
	if err := result.Validate(); err != nil {
		return fmt.Errorf("validate workload observation: %w", err)
	}
	if result.Target.NodeName != config.TargetNode.Name || result.Target.NodeUID != config.TargetNode.UID ||
		result.Run.PodUID != podUID {
		return errors.New("workload observation target or Run Pod identity mismatch")
	}
	if result.CoverageStartedAt.After(runtimeStarted) || result.CoverageEndedAt.Before(runtimeEnded) {
		return errors.New("workload observation does not cover the runtime measurement window")
	}
	return nil
}

func validateMetricObservation(config profilingrun.RunConfig, result metricobserver.Result) error {
	if err := result.Validate(); err != nil {
		return fmt.Errorf("validate metric observation: %w", err)
	}
	if result.Source.ThermalMetrics == nil {
		return errors.New("metric source thermal identity is required")
	}
	if !metricSourceMatches(result.Source, config.Metrics) {
		return errors.New("metric source identity mismatch")
	}
	return nil
}

func metricSourceMatches(source metricobserver.SourceIdentity, contract profilingrun.MetricContract) bool {
	thermal := source.ThermalMetrics
	return source.Kind == contract.SourceKind &&
		source.NodeName == contract.NodeName &&
		source.Endpoint == contract.Endpoint &&
		source.Metric == contract.PowerMetric &&
		maps.Equal(source.MetricLabels, contract.MetricLabels) &&
		source.ThermalEndpoint == contract.ThermalEndpoint &&
		thermal != nil &&
		thermal.ObservationAvailable == contract.ThermalMetrics.ObservationAvailable &&
		thermal.Active == contract.ThermalMetrics.Active &&
		thermal.CPUCoreThrottleEvents == contract.ThermalMetrics.CPUCoreThrottleEvents &&
		thermal.CPUPackageThrottleEvents == contract.ThermalMetrics.CPUPackageThrottleEvents &&
		thermal.Latched == contract.ThermalMetrics.Latched &&
		thermal.MaximumTemperatureCelsius == contract.ThermalMetrics.MaximumTemperatureCelsius
}
