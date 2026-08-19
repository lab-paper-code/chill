package evidence

import (
	"fmt"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

// rawRecord is the finalization/loading boundary shared by active and idle
// evidence. The concrete raw schemas remain distinct and externally visible;
// only schema-neutral orchestration depends on this interface.
type rawRecord interface {
	validate(profilingrun.Intent) error
	validateExecution(ExecutionEvidence) error
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

func (m RawMeasurement) validateExecution(execution ExecutionEvidence) error {
	return execution.validateLinkedRaw(m.PodUID, m.Workload)
}

func (m IdleRawMeasurement) validateExecution(execution ExecutionEvidence) error {
	return execution.validateLinkedRaw(m.PodUID, m.Workload)
}
