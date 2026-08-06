package worker

import (
	"errors"

	"github.com/lab-paper-code/chill/internal/profilingrun"
)

// RuntimeMeasurement is a sealed process-boundary result. Only a runtime
// adapter that has validated one exact active or idle result can populate it.
type RuntimeMeasurement struct {
	active profilingrun.VerifiedRuntimeResult
	idle   profilingrun.VerifiedIdleRuntimeResult
	mode   runtimeMeasurementMode
}

type runtimeMeasurementMode uint8

const (
	runtimeMeasurementEmpty runtimeMeasurementMode = iota
	runtimeMeasurementActive
	runtimeMeasurementIdle
)

func activeRuntimeMeasurement(result profilingrun.VerifiedRuntimeResult) RuntimeMeasurement {
	return RuntimeMeasurement{active: result, mode: runtimeMeasurementActive}
}

func idleRuntimeMeasurement(result profilingrun.VerifiedIdleRuntimeResult) RuntimeMeasurement {
	return RuntimeMeasurement{idle: result, mode: runtimeMeasurementIdle}
}

func (m RuntimeMeasurement) activeResult() (profilingrun.VerifiedRuntimeResult, error) {
	if m.mode != runtimeMeasurementActive {
		return profilingrun.VerifiedRuntimeResult{}, errors.New("verified active runtime result is missing")
	}
	return m.active, nil
}

func (m RuntimeMeasurement) idleResult() (profilingrun.VerifiedIdleRuntimeResult, error) {
	if m.mode != runtimeMeasurementIdle {
		return profilingrun.VerifiedIdleRuntimeResult{}, errors.New("verified idle runtime result is missing")
	}
	return m.idle, nil
}
