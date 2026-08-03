package evidence

import (
	"fmt"

	"github.com/lab-paper-code/chill/internal/profilingrun"
)

// rawRecord is the finalization/loading boundary shared by active and idle
// evidence. The concrete raw schemas remain distinct and externally visible;
// only schema-neutral orchestration depends on this interface.
type rawRecord interface {
	canonicalRawJSON(profilingrun.Intent) ([]byte, error)
	canonicalExecutionJSON(ExecutionEvidence) ([]byte, error)
	rawIntentDigest() string
}

func decodeRawRecord(payload []byte, intent profilingrun.Intent) (rawRecord, error) {
	if intent.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		var raw IdleRawMeasurement
		if err := decodeExact(payload, &raw); err != nil {
			return nil, fmt.Errorf("decode idle raw measurement: %w", err)
		}
		return raw, nil
	}
	var raw RawMeasurement
	if err := decodeExact(payload, &raw); err != nil {
		return nil, fmt.Errorf("decode raw measurement: %w", err)
	}
	return raw, nil
}

func (m RawMeasurement) canonicalRawJSON(intent profilingrun.Intent) ([]byte, error) {
	return m.CanonicalJSON(intent)
}

func (m RawMeasurement) canonicalExecutionJSON(execution ExecutionEvidence) ([]byte, error) {
	return execution.CanonicalJSON(m)
}

func (m RawMeasurement) rawIntentDigest() string { return m.IntentDigest }

func (m IdleRawMeasurement) canonicalRawJSON(intent profilingrun.Intent) ([]byte, error) {
	return m.CanonicalJSON(intent)
}

func (m IdleRawMeasurement) canonicalExecutionJSON(execution ExecutionEvidence) ([]byte, error) {
	return execution.CanonicalIdleJSON(m)
}

func (m IdleRawMeasurement) rawIntentDigest() string { return m.IntentDigest }
