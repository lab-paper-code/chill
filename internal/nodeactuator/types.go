// Package nodeactuator contains the Kubernetes-independent contract consumed
// by the sole CHILL host-state writer.
package nodeactuator

import (
	"errors"
	"fmt"
	"time"
)

const (
	ActuatorKindJetsonNVPModel       = "JetsonNVPModel"
	ActuatorKindNvidiaLockedGPUClock = "NvidiaLockedGPUClock"
)

type Target struct {
	Kind                 string
	JetsonNVPModel       *JetsonNVPModelTarget
	NvidiaLockedGPUClock *NvidiaLockedGPUClockTarget
}

type JetsonNVPModelTarget struct {
	ID int32
}

type NvidiaLockedGPUClockTarget struct {
	MinMHz int32
	MaxMHz int32
	Mode   *int32
}

// Request is one host read or apply: the mode and its typed target, bounded
// by Deadline. It is not evidence that mutation has occurred.
type Request struct {
	PowerMode string
	Target    Target
	Deadline  time.Time
}

// Observation is one independent host read-back.
type Observation struct {
	PowerMode  string
	Target     Target
	ObservedAt time.Time
}

func (t Target) Validate() error {
	switch t.Kind {
	case ActuatorKindJetsonNVPModel:
		if t.JetsonNVPModel == nil || t.NvidiaLockedGPUClock != nil || t.JetsonNVPModel.ID < 0 {
			return errors.New("JetsonNVPModel requires only a non-negative ID")
		}
	case ActuatorKindNvidiaLockedGPUClock:
		clock := t.NvidiaLockedGPUClock
		if clock == nil || t.JetsonNVPModel != nil || clock.MinMHz < 1 || clock.MaxMHz < clock.MinMHz ||
			(clock.Mode != nil && *clock.Mode != 0 && *clock.Mode != 1) {
			return errors.New("NvidiaLockedGPUClock requires a valid range and optional mode 0 or 1")
		}
	default:
		return fmt.Errorf("unsupported actuator kind %q", t.Kind)
	}
	return nil
}
