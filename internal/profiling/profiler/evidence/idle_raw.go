package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

const IdleRawMeasurementSchemaVersion = "chill.dacs.io/model-loaded-idle-raw-measurement.v1alpha6"

// IdleRawMeasurement retains only worker-visible facts. Its runtime result is
// distinct from active inference evidence, while metric/workload provenance is
// shared.
type IdleRawMeasurement struct {
	SchemaVersion string                         `json:"schemaVersion"`
	RunConfig     profilingrun.RunConfig         `json:"runConfig"`
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

func (m IdleRawMeasurement) Validate() error {
	return m.validate()
}

func (m IdleRawMeasurement) validate() error {
	runConfig := m.RunConfig
	if err := runConfig.Validate(); err != nil {
		return fmt.Errorf("validate RunConfig: %w", err)
	}
	if m.SchemaVersion != IdleRawMeasurementSchemaVersion {
		return fmt.Errorf("unsupported idle raw measurement schemaVersion %q", m.SchemaVersion)
	}
	if m.Runtime.SchemaVersion != profilingrun.IdleRuntimeResultSchemaVersion {
		return errors.New("current idle raw measurement requires the current idle runtime result")
	}
	if strings.TrimSpace(m.PodUID) == "" || strings.TrimSpace(m.PodUID) != m.PodUID {
		return errors.New("idle raw measurement Pod UID is required")
	}
	if err := profilingrun.ValidateIdleRuntime(runConfig, m.PodUID, m.Runtime); err != nil {
		return fmt.Errorf("validate idle runtime result: %w", err)
	}
	if err := validateMetricObservation(runConfig, m.Metrics); err != nil {
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
	if err := validateWorkloadObservation(runConfig, m.PodUID, windowStart, windowEnd, *m.Workload); err != nil {
		return err
	}
	return nil
}

func (m IdleRawMeasurement) CanonicalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
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
