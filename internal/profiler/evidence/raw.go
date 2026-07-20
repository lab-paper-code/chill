package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

const (
	legacyRawMeasurementSchemaVersion = "chill.dacs.io/profiling-raw-measurement.v1alpha1"
	RawMeasurementSchemaVersion       = "chill.dacs.io/profiling-raw-measurement.v1alpha2"
)

// RawMeasurement contains only facts available to the in-Job worker before it
// exits. Kubernetes terminal state is deliberately excluded.
type RawMeasurement struct {
	SchemaVersion string                     `json:"schemaVersion"`
	IntentDigest  string                     `json:"intentDigest"`
	PodUID        string                     `json:"podUID"`
	Runtime       profilingrun.RuntimeResult `json:"runtime"`
	Metrics       metricobserver.Result      `json:"metrics"`
}

// UnmarshalJSON keeps retained immutable v1alpha1 evidence readable while the
// current schema exposes only the MetricObserver result name.
func (m *RawMeasurement) UnmarshalJSON(payload []byte) error {
	var wire struct {
		SchemaVersion string                     `json:"schemaVersion"`
		IntentDigest  string                     `json:"intentDigest"`
		PodUID        string                     `json:"podUID"`
		Runtime       profilingrun.RuntimeResult `json:"runtime"`
		Metrics       *metricobserver.Result     `json:"metrics"`
		LegacyMetrics *metricobserver.Result     `json:"power"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("raw measurement contains multiple JSON values")
		}
		return err
	}
	if wire.Metrics != nil && wire.LegacyMetrics != nil {
		return errors.New("raw measurement contains multiple metric observations")
	}
	if wire.Metrics == nil && wire.LegacyMetrics == nil {
		return errors.New("raw measurement metric observation is required")
	}
	m.SchemaVersion = wire.SchemaVersion
	m.IntentDigest = wire.IntentDigest
	m.PodUID = wire.PodUID
	m.Runtime = wire.Runtime
	if wire.Metrics != nil {
		m.Metrics = *wire.Metrics
	} else {
		m.Metrics = *wire.LegacyMetrics
	}
	return nil
}

func (m RawMeasurement) Validate(intent profilingrun.Intent) error {
	if m.SchemaVersion != RawMeasurementSchemaVersion && m.SchemaVersion != legacyRawMeasurementSchemaVersion {
		return fmt.Errorf("unsupported raw measurement schemaVersion %q", m.SchemaVersion)
	}
	digest, err := intent.Digest()
	if err != nil {
		return fmt.Errorf("validate Run intent: %w", err)
	}
	if m.IntentDigest != digest {
		return errors.New("raw measurement intent digest mismatch")
	}
	if strings.TrimSpace(m.PodUID) == "" || strings.TrimSpace(m.PodUID) != m.PodUID {
		return errors.New("raw measurement Pod UID is required")
	}
	if err := profilingrun.ValidateRuntime(intent, m.PodUID, m.Runtime); err != nil {
		return fmt.Errorf("validate runtime result: %w", err)
	}
	if err := validateMetricObservation(intent, m.Metrics, m.SchemaVersion == RawMeasurementSchemaVersion); err != nil {
		return err
	}
	runtimeStarted, runtimeEnded, err := m.Runtime.MeasurementWindow()
	if err != nil {
		return fmt.Errorf("read runtime measurement window: %w", err)
	}
	if !m.Metrics.StartedAt.Before(runtimeEnded) || !runtimeStarted.Before(m.Metrics.EndedAt) {
		return errors.New("runtime and metric observation windows do not overlap")
	}
	return nil
}

func (m RawMeasurement) CanonicalJSON(intent profilingrun.Intent) ([]byte, error) {
	if err := m.Validate(intent); err != nil {
		return nil, err
	}
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
	if canonical.SchemaVersion == legacyRawMeasurementSchemaVersion {
		return json.Marshal(struct {
			SchemaVersion string                     `json:"schemaVersion"`
			IntentDigest  string                     `json:"intentDigest"`
			PodUID        string                     `json:"podUID"`
			Runtime       profilingrun.RuntimeResult `json:"runtime"`
			Metrics       metricobserver.Result      `json:"power"`
		}{canonical.SchemaVersion, canonical.IntentDigest, canonical.PodUID, canonical.Runtime, canonical.Metrics})
	}
	return json.Marshal(canonical)
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
