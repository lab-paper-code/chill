package metadata

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Node power-mode metadata. One writer sets the desired mode; the Node's
// actuation agent applies it and owns every other key.
const (
	// PowerMode is the desired DeviceClass power-mode name.
	PowerMode = "chill.dacs.io/power-mode"
	// PowerModeState is the agent's progress toward PowerMode.
	PowerModeState = "chill.dacs.io/power-mode.state"
	// PowerModeGeneration counts the host power-mode changes the agent
	// observed, from 0. A measurement window compares it at start and end.
	PowerModeGeneration = "chill.dacs.io/power-mode-generation"
	// PowerModeBoot is the Node bootID the agent last observed.
	PowerModeBoot = "chill.dacs.io/power-mode-boot"
	// PowerModeTransition is the PowerModeTransition JSON of the last apply.
	PowerModeTransition = "chill.dacs.io/power-mode-transition"
)

const (
	PowerModePending = "pending"
	PowerModeSuccess = "success"
	PowerModeFailed  = "failed"
)

// PowerModeTransitionRecord is the last apply on a Node, in the Node clock.
// Generation is the generation at the fence, and the generation the target
// read-back produced once VerifiedAt is set.
type PowerModeTransitionRecord struct {
	PowerMode         string     `json:"powerMode"`
	Generation        int64      `json:"generation"`
	ApplyStartedAt    *time.Time `json:"applyStartedAt,omitempty"`
	VerifiedAt        *time.Time `json:"verifiedAt,omitempty"`
	SettlingStartedAt *time.Time `json:"settlingStartedAt,omitempty"`
	ReadyAt           *time.Time `json:"readyAt,omitempty"`
	Failure           string     `json:"failure,omitempty"`
}

// NodePowerModeGeneration reads the generation; ok is false when the Node has
// no actuation agent.
func NodePowerModeGeneration(node *corev1.Node) (int64, bool, error) {
	raw, ok := node.Annotations[PowerModeGeneration]
	if !ok {
		return 0, false, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, false, fmt.Errorf("Node %s %s %q is not a non-negative integer", node.Name, PowerModeGeneration, raw)
	}
	return value, true, nil
}

// NodePowerModeTransition reads the last apply; ok is false before the first.
func NodePowerModeTransition(node *corev1.Node) (PowerModeTransitionRecord, bool, error) {
	raw, ok := node.Annotations[PowerModeTransition]
	if !ok {
		return PowerModeTransitionRecord{}, false, nil
	}
	var record PowerModeTransitionRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return PowerModeTransitionRecord{}, false, fmt.Errorf("decode Node %s %s: %w", node.Name, PowerModeTransition, err)
	}
	return record, true, nil
}
