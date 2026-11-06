package evidence

import (
	"encoding/json"
	"fmt"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

// rawRecord is the finalization/loading boundary shared by active and idle
// evidence. The concrete raw schemas remain distinct and externally visible;
// only schema-neutral orchestration depends on this interface.
type rawRecord interface {
	validate() error
	runConfig() profilingrun.RunConfig
	validateExecution(ExecutionEvidence) error
}

func decodeRawRecord(payload []byte) (rawRecord, error) {
	var header struct {
		RunConfig profilingrun.RunConfig `json:"runConfig"`
	}
	if err := json.Unmarshal(payload, &header); err != nil {
		return nil, fmt.Errorf("decode raw measurement RunConfig: %w", err)
	}
	if header.RunConfig.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
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

func (m RawMeasurement) runConfig() profilingrun.RunConfig {
	return m.RunConfig
}

func (m IdleRawMeasurement) runConfig() profilingrun.RunConfig {
	return m.RunConfig
}

func (m RawMeasurement) validateExecution(execution ExecutionEvidence) error {
	execution.RunConfig = m.RunConfig
	return execution.validateLinkedRaw(m.PodUID, m.Workload)
}

func (m IdleRawMeasurement) validateExecution(execution ExecutionEvidence) error {
	execution.RunConfig = m.RunConfig
	return execution.validateLinkedRaw(m.PodUID, m.Workload)
}
