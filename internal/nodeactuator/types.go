// Package nodeactuator contains the Kubernetes-independent contract consumed
// by the sole CHILL host-state writer.
package nodeactuator

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ActuatorKindJetsonNVPModel       = "JetsonNVPModel"
	ActuatorKindNvidiaLockedGPUClock = "NvidiaLockedGPUClock"
)

type ObjectIdentity struct {
	Name string
	UID  string
}

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

// Request is the verified durable handoff presented to the Lease and
// host-adapter state machine. It is not evidence that mutation has occurred.
type Request struct {
	Handoff                     ObjectIdentity
	Generation                  int64
	Node                        ObjectIdentity
	DeviceClass                 string
	PowerMode                   string
	Target                      Target
	PolicyName                  string
	SettlingDurationSeconds     int32
	MaxActuationDurationSeconds int32
	Deadline                    time.Time
	StageRequestedAt            time.Time
}

func (r Request) ValidateForNode(assigned ObjectIdentity) error {
	for field, identity := range map[string]ObjectIdentity{
		"handoff": r.Handoff, "Node": r.Node, "assigned Node": assigned,
	} {
		if err := identity.Validate(); err != nil {
			return fmt.Errorf("validate %s identity: %w", field, err)
		}
	}
	if r.Node != assigned {
		return errors.New("handoff Node identity does not match this NodeActuator instance")
	}
	if r.Generation < 1 {
		return errors.New("handoff generation must be positive")
	}
	for field, value := range map[string]string{
		"deviceClass": r.DeviceClass, "powerMode": r.PowerMode,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("handoff %s is required without surrounding whitespace", field)
		}
	}
	if err := r.Target.Validate(); err != nil {
		return fmt.Errorf("validate handoff target: %w", err)
	}
	if strings.TrimSpace(r.PolicyName) == "" || strings.TrimSpace(r.PolicyName) != r.PolicyName {
		return errors.New("handoff policy identity is required without surrounding whitespace")
	}
	if r.SettlingDurationSeconds < 1 || r.SettlingDurationSeconds > 86_400 {
		return errors.New("handoff settling duration must be between 1 second and 24 hours")
	}
	if r.MaxActuationDurationSeconds <= r.SettlingDurationSeconds ||
		r.MaxActuationDurationSeconds > 86_400 {
		return errors.New("handoff maximum duration must exceed settling and be at most 24 hours")
	}
	if r.StageRequestedAt.IsZero() || r.Deadline.IsZero() || !r.StageRequestedAt.Before(r.Deadline) {
		return errors.New("handoff stage request and deadline are invalid")
	}
	return nil
}

func (i ObjectIdentity) Validate() error {
	if strings.TrimSpace(i.Name) == "" || strings.TrimSpace(i.Name) != i.Name ||
		strings.TrimSpace(i.UID) == "" || strings.TrimSpace(i.UID) != i.UID {
		return errors.New("name and UID are required without surrounding whitespace")
	}
	return nil
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
