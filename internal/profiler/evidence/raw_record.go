package evidence

import (
	"fmt"

	"github.com/lab-paper-code/chill/internal/profilingrun"
)

// rawRecord is the finalization/loading boundary shared by active and idle
// evidence. The concrete raw schemas remain distinct and externally visible;
// only schema-neutral orchestration depends on this interface.
type rawRecord interface {
	validate(profilingrun.Intent, string) error
	canonicalRawJSON() ([]byte, error)
	validateExecution(ExecutionEvidence, string) error
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

func (m RawMeasurement) canonicalRawJSON() ([]byte, error) {
	return m.canonicalJSON()
}

func (m RawMeasurement) validateExecution(execution ExecutionEvidence, intentDigest string) error {
	return execution.validateLinkedRaw(m.PodUID, m.Workload, intentDigest)
}

func (m IdleRawMeasurement) canonicalRawJSON() ([]byte, error) {
	return m.canonicalJSON()
}

func (m IdleRawMeasurement) validateExecution(execution ExecutionEvidence, intentDigest string) error {
	return execution.validateLinkedRaw(m.PodUID, m.Workload, intentDigest)
}
