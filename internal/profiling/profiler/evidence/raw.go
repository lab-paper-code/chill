package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

const RawMeasurementSchemaVersion = "chill.dacs.io/profiling-raw-measurement.v1alpha5"

// RawMeasurement contains only facts available to the in-Job worker before it
// exits. Kubernetes terminal state is deliberately excluded.
type RawMeasurement struct {
	SchemaVersion string                     `json:"schemaVersion"`
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

func (m RawMeasurement) Validate(intent profilingrun.Intent) error {
	return m.validate(intent)
}

func (m RawMeasurement) validate(intent profilingrun.Intent) error {
	if m.SchemaVersion != RawMeasurementSchemaVersion {
		return fmt.Errorf("unsupported raw measurement schemaVersion %q", m.SchemaVersion)
	}
	if m.Runtime.SchemaVersion != profilingrun.RuntimeResultSchemaVersion {
		return errors.New("current raw measurement requires the current runtime result")
	}
	if strings.TrimSpace(m.PodUID) == "" || strings.TrimSpace(m.PodUID) != m.PodUID {
		return errors.New("raw measurement Pod UID is required")
	}
	if err := profilingrun.ValidateRuntime(intent, m.PodUID, m.Runtime); err != nil {
		return fmt.Errorf("validate runtime result: %w", err)
	}
	if err := validateMetricObservation(intent, m.Metrics, true); err != nil {
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
	if err := validateWorkloadObservation(intent, m.PodUID, runtimeStarted, runtimeEnded, *m.Workload); err != nil {
		return err
	}
	return nil
}

func (m RawMeasurement) CanonicalJSON(intent profilingrun.Intent) ([]byte, error) {
	if err := m.Validate(intent); err != nil {
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
	intent profilingrun.Intent,
	podUID string,
	runtimeStarted time.Time,
	runtimeEnded time.Time,
	result workloadobserver.Result,
) error {
	if err := result.Validate(); err != nil {
		return fmt.Errorf("validate workload observation: %w", err)
	}
	if result.Target.NodeName != intent.TargetNode.Name || result.Target.NodeUID != intent.TargetNode.UID ||
		result.Run.PodUID != podUID {
		return errors.New("workload observation target or Run Pod identity mismatch")
	}
	if result.CoverageStartedAt.After(runtimeStarted) || result.CoverageEndedAt.Before(runtimeEnded) {
		return errors.New("workload observation does not cover the runtime measurement window")
	}
	return nil
}

func validateMetricObservation(intent profilingrun.Intent, result metricobserver.Result, requireThermalIdentity bool) error {
	if err := result.Validate(); err != nil {
		return fmt.Errorf("validate metric observation: %w", err)
	}
	if requireThermalIdentity && result.Source.ThermalMetrics == nil {
		return errors.New("metric source thermal identity is required")
	}
	if result.Source.Kind != intent.Metrics.SourceKind ||
		result.Source.NodeName != intent.Metrics.NodeName ||
		result.Source.Endpoint != intent.Metrics.Endpoint ||
		result.Source.Metric != intent.Metrics.PowerMetric {
		return errors.New("metric source identity mismatch")
	}
	return nil
}
