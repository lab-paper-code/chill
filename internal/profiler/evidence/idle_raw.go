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
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

const (
	idleRawMeasurementSchemaVersionV1Alpha1 = "chill.dacs.io/model-loaded-idle-raw-measurement.v1alpha1"
	IdleRawMeasurementSchemaVersion         = "chill.dacs.io/model-loaded-idle-raw-measurement.v1alpha2"
)

// IdleRawMeasurement retains only worker-visible facts. Its runtime result is
// distinct from active inference evidence, while metric/workload provenance is
// shared.
type IdleRawMeasurement struct {
	SchemaVersion string                         `json:"schemaVersion"`
	IntentDigest  string                         `json:"intentDigest"`
	PodUID        string                         `json:"podUID"`
	Runtime       profilingrun.IdleRuntimeResult `json:"runtime"`
	Metrics       metricobserver.Result          `json:"metrics"`
	Workload      *workloadobserver.Result       `json:"workload"`
}

func (m *IdleRawMeasurement) UnmarshalJSON(payload []byte) error {
	type wire IdleRawMeasurement
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("idle raw measurement contains multiple JSON values")
		}
		return err
	}
	*m = IdleRawMeasurement(decoded)
	return nil
}

func (m IdleRawMeasurement) Validate(intent profilingrun.Intent) error {
	digest, err := intent.Digest()
	if err != nil {
		return fmt.Errorf("validate idle Run intent: %w", err)
	}
	return m.validate(intent, digest)
}

func (m IdleRawMeasurement) validate(intent profilingrun.Intent, intentDigest string) error {
	if m.SchemaVersion != IdleRawMeasurementSchemaVersion &&
		m.SchemaVersion != idleRawMeasurementSchemaVersionV1Alpha1 {
		return fmt.Errorf("unsupported idle raw measurement schemaVersion %q", m.SchemaVersion)
	}
	if m.SchemaVersion == idleRawMeasurementSchemaVersionV1Alpha1 &&
		m.Runtime.SchemaVersion != profilingrun.IdleRuntimeResultSchemaVersionV1Alpha1 {
		return errors.New("v1alpha1 idle raw measurement requires the retained v1alpha1 runtime result")
	}
	if m.SchemaVersion == IdleRawMeasurementSchemaVersion &&
		m.Runtime.SchemaVersion != profilingrun.IdleRuntimeResultSchemaVersion {
		return errors.New("current idle raw measurement requires the current idle runtime result")
	}
	if m.IntentDigest != intentDigest {
		return errors.New("idle raw measurement intent digest mismatch")
	}
	if strings.TrimSpace(m.PodUID) == "" || strings.TrimSpace(m.PodUID) != m.PodUID {
		return errors.New("idle raw measurement Pod UID is required")
	}
	if err := profilingrun.ValidateIdleRuntime(intent, m.PodUID, m.Runtime); err != nil {
		return fmt.Errorf("validate idle runtime result: %w", err)
	}
	if err := validateMetricObservation(intent, m.Metrics, true); err != nil {
		return err
	}
	windowStart, windowEnd, err := m.Runtime.MeasurementWindow()
	if err != nil {
		return fmt.Errorf("read idle runtime measurement window: %w", err)
	}
	if !m.Metrics.StartedAt.Before(windowEnd) || !windowStart.Before(m.Metrics.EndedAt) {
		return errors.New("idle runtime and metric observation windows do not overlap")
	}
	if m.Workload == nil {
		return errors.New("idle raw measurement workload observation is required")
	}
	if err := validateWorkloadObservation(intent, m.PodUID, windowStart, windowEnd, *m.Workload); err != nil {
		return err
	}
	return nil
}

func (m IdleRawMeasurement) CanonicalJSON(intent profilingrun.Intent) ([]byte, error) {
	if err := m.Validate(intent); err != nil {
		return nil, err
	}
	return m.canonicalJSON()
}

func (m IdleRawMeasurement) canonicalJSON() ([]byte, error) {
	canonical := m
	canonical.Metrics.StartedAt = m.Metrics.StartedAt.UTC()
	canonical.Metrics.EndedAt = m.Metrics.EndedAt.UTC()
	canonical.Metrics.Samples = append([]metricobserver.Sample(nil), m.Metrics.Samples...)
	for index := range canonical.Metrics.Samples {
		canonical.Metrics.Samples[index].ObservedAt = canonical.Metrics.Samples[index].ObservedAt.UTC()
	}
	canonical.Metrics.Failures = append([]metricobserver.Failure(nil), m.Metrics.Failures...)
	for index := range canonical.Metrics.Failures {
		canonical.Metrics.Failures[index].ObservedAt = canonical.Metrics.Failures[index].ObservedAt.UTC()
	}
	workload := m.Workload.CanonicalCopy()
	canonical.Workload = &workload
	return json.Marshal(canonical)
}
