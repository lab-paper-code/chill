package worker

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lab-paper-code/chill/internal/profilingrun"
)

// RuntimeMeasurement is a sealed process-boundary result. Only a runtime
// adapter that has validated one exact active or idle result can populate it.
type RuntimeMeasurement struct {
	payload []byte
	idle    bool
	loaded  bool
}

func sealActiveRuntimeMeasurement(result profilingrun.RuntimeResult) (RuntimeMeasurement, error) {
	payload, err := json.Marshal(result)
	if err != nil {
		return RuntimeMeasurement{}, fmt.Errorf("encode verified active runtime result: %w", err)
	}
	return RuntimeMeasurement{payload: payload, loaded: true}, nil
}

func sealIdleRuntimeMeasurement(result profilingrun.IdleRuntimeResult) (RuntimeMeasurement, error) {
	payload, err := json.Marshal(result)
	if err != nil {
		return RuntimeMeasurement{}, fmt.Errorf("encode verified idle runtime result: %w", err)
	}
	return RuntimeMeasurement{payload: payload, idle: true, loaded: true}, nil
}

func (m RuntimeMeasurement) activeResult() (profilingrun.RuntimeResult, error) {
	if !m.loaded || m.idle {
		return profilingrun.RuntimeResult{}, errors.New("verified active runtime result is missing")
	}
	var result profilingrun.RuntimeResult
	if err := json.Unmarshal(m.payload, &result); err != nil {
		return profilingrun.RuntimeResult{}, fmt.Errorf("decode sealed active runtime result: %w", err)
	}
	return result, nil
}

func (m RuntimeMeasurement) idleResult() (profilingrun.IdleRuntimeResult, error) {
	if !m.loaded || !m.idle {
		return profilingrun.IdleRuntimeResult{}, errors.New("verified idle runtime result is missing")
	}
	var result profilingrun.IdleRuntimeResult
	if err := json.Unmarshal(m.payload, &result); err != nil {
		return profilingrun.IdleRuntimeResult{}, fmt.Errorf("decode sealed idle runtime result: %w", err)
	}
	return result, nil
}
