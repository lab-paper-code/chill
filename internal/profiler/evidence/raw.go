package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lab-paper-code/chill/internal/powerobserver"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

const RawMeasurementSchemaVersion = "chill.dacs.io/profiling-raw-measurement.v1alpha1"

// RawMeasurement contains only facts available to the in-Job worker before it
// exits. Kubernetes terminal state is deliberately excluded.
type RawMeasurement struct {
	SchemaVersion string                     `json:"schemaVersion"`
	IntentDigest  string                     `json:"intentDigest"`
	PodUID        string                     `json:"podUID"`
	Runtime       profilingrun.RuntimeResult `json:"runtime"`
	Power         powerobserver.Result       `json:"power"`
}

func (m RawMeasurement) Validate(intent profilingrun.Intent) error {
	if m.SchemaVersion != RawMeasurementSchemaVersion {
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
	if err := validatePower(intent, m.Power); err != nil {
		return err
	}
	runtimeStarted, runtimeEnded, err := m.Runtime.MeasurementWindow()
	if err != nil {
		return fmt.Errorf("read runtime measurement window: %w", err)
	}
	if !m.Power.StartedAt.Before(runtimeEnded) || !runtimeStarted.Before(m.Power.EndedAt) {
		return errors.New("runtime and power observation windows do not overlap")
	}
	return nil
}

func (m RawMeasurement) CanonicalJSON(intent profilingrun.Intent) ([]byte, error) {
	if err := m.Validate(intent); err != nil {
		return nil, err
	}
	canonical := m
	canonical.Power.StartedAt = canonical.Power.StartedAt.UTC()
	canonical.Power.EndedAt = canonical.Power.EndedAt.UTC()
	canonical.Power.Samples = append([]powerobserver.Sample(nil), m.Power.Samples...)
	for i := range canonical.Power.Samples {
		canonical.Power.Samples[i].ObservedAt = canonical.Power.Samples[i].ObservedAt.UTC()
	}
	canonical.Power.Failures = append([]powerobserver.Failure(nil), m.Power.Failures...)
	for i := range canonical.Power.Failures {
		canonical.Power.Failures[i].ObservedAt = canonical.Power.Failures[i].ObservedAt.UTC()
	}
	return json.Marshal(canonical)
}

func validatePower(intent profilingrun.Intent, result powerobserver.Result) error {
	if err := result.Validate(); err != nil {
		return fmt.Errorf("validate power observation: %w", err)
	}
	if result.Source.Kind != intent.Power.SourceKind ||
		result.Source.NodeName != intent.Power.NodeName ||
		result.Source.Endpoint != intent.Power.Endpoint ||
		result.Source.Metric != intent.Power.Metric {
		return errors.New("power source identity mismatch")
	}
	return nil
}
