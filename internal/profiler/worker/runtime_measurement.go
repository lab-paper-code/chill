package worker

import (
	"errors"

	"github.com/lab-paper-code/chill/internal/profilingrun"
)

// RuntimeMeasurement is an in-process sum type. Exactly one result must match
// the frozen measurement mode; it is not a serialized transport contract.
type RuntimeMeasurement struct {
	Active *profilingrun.RuntimeResult
	Idle   *profilingrun.IdleRuntimeResult
}

// Validate owns mode/result compatibility at the runtime boundary. Raw
// evidence projection may therefore assume it received one verified variant.
func (m RuntimeMeasurement) Validate(intent profilingrun.Intent, podUID string) error {
	if intent.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		if m.Idle == nil || m.Active != nil {
			return errors.New("idle runtime result is missing or ambiguous")
		}
		return profilingrun.ValidateIdleRuntime(intent, podUID, *m.Idle)
	}
	if m.Active == nil || m.Idle != nil {
		return errors.New("active runtime result is missing or ambiguous")
	}
	return profilingrun.ValidateRuntime(intent, podUID, *m.Active)
}
