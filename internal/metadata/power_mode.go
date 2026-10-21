package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	// PowerModeRequestedAt is when the writer requested PowerMode
	// (RFC 3339); its deadline counts from it.
	PowerModeRequestedAt = "chill.dacs.io/power-mode-requested-at"
	// PowerModeTransition is the PowerModeTransition JSON of the last apply.
	PowerModeTransition = "chill.dacs.io/power-mode-transition"
)

const (
	PowerModePending = "pending"
	PowerModeSuccess = "success"
	PowerModeFailed  = "failed"
)

// PowerModeTransitionRecord is the last apply on a Node, in the Node clock.
// RequestedAt is the PowerModeRequestedAt value it serves, so a new request
// of the same mode starts a new record. Generation is the generation at the
// fence, and the generation the target read-back produced once VerifiedAt is
// set.
type PowerModeTransitionRecord struct {
	PowerMode         string     `json:"powerMode"`
	RequestedAt       string     `json:"requestedAt,omitempty"`
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

// RequestPowerMode makes mode the Node's desired power mode and records now
// as its request time in the same patch. It patches with the resourceVersion
// the caller read, so a writer whose view of the Node, and with it the
// measurement grant, is stale gets a conflict. A mode the agent failed gets a
// new request time, which the agent applies afresh; any other mode already
// requested is left alone.
func RequestPowerMode(ctx context.Context, c client.Client, node *corev1.Node, mode string, now time.Time) error {
	_, requested := node.Annotations[PowerModeRequestedAt]
	if node.Labels[PowerMode] == mode && requested {
		transition, _, err := NodePowerModeTransition(node)
		if err != nil {
			return err
		}
		if !ServesRequest(node, transition, mode) || node.Labels[PowerModeState] != PowerModeFailed {
			return nil
		}
	}
	raw, err := json.Marshal(map[string]any{"metadata": map[string]any{
		"resourceVersion": node.ResourceVersion,
		"labels":          map[string]string{PowerMode: mode},
		"annotations":     map[string]string{PowerModeRequestedAt: now.UTC().Format(time.RFC3339Nano)},
	}})
	if err != nil {
		return err
	}
	if err := c.Patch(ctx, node, client.RawPatch(types.MergePatchType, raw)); err != nil {
		return fmt.Errorf("request power mode on Node %s: %w", node.Name, err)
	}
	return nil
}

// ServesRequest reports whether transition is the agent's record of the
// Node's current request for mode.
func ServesRequest(node *corev1.Node, transition PowerModeTransitionRecord, mode string) bool {
	return node.Labels[PowerMode] == mode && transition.PowerMode == mode &&
		transition.RequestedAt == node.Annotations[PowerModeRequestedAt]
}

// PowerModeDeadline is the end of the current request: its request time plus
// maxDuration. ok is false when the Node records no request time.
func PowerModeDeadline(node *corev1.Node, maxDuration time.Duration) (time.Time, bool) {
	requested, err := time.Parse(time.RFC3339Nano, node.Annotations[PowerModeRequestedAt])
	if err != nil {
		return time.Time{}, false
	}
	return requested.Add(maxDuration), true
}

// PowerModeOutcome reports the agent's progress toward mode on node. Done is
// true once the agent settled the current request and the host has not
// changed since. An error reports a failed apply, a host that left the mode,
// or no success within maxDuration of the request time.
func PowerModeOutcome(node *corev1.Node, mode string, maxDuration time.Duration, now time.Time) (bool, error) {
	transition, _, err := NodePowerModeTransition(node)
	if err != nil {
		return false, err
	}
	generation, _, err := NodePowerModeGeneration(node)
	if err != nil {
		return false, err
	}
	current := ServesRequest(node, transition, mode)
	state := node.Labels[PowerModeState]
	switch {
	case current && state == PowerModeSuccess && transition.Generation == generation && transition.ReadyAt != nil:
		return true, nil
	case current && state == PowerModeSuccess:
		return false, fmt.Errorf("Node %s left power mode %s at generation %d", node.Name, mode, generation)
	case current && state == PowerModeFailed:
		return false, fmt.Errorf("Node %s power mode %s failed: %s", node.Name, mode, transition.Failure)
	}
	deadline, ok := PowerModeDeadline(node, maxDuration)
	if !ok {
		return false, fmt.Errorf("Node %s has no power-mode request time", node.Name)
	}
	if !now.Before(deadline) {
		return false, fmt.Errorf("Node %s did not reach power mode %s within %s", node.Name, mode, maxDuration)
	}
	return false, nil
}

// NodeReadBack decodes the actuator the agent last read back into target.
func NodeReadBack(node *corev1.Node, target any) error {
	raw, ok := node.Annotations[PowerActuator]
	if !ok {
		return fmt.Errorf("Node %s has no power read-back", node.Name)
	}
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		return fmt.Errorf("decode Node %s %s: %w", node.Name, PowerActuator, err)
	}
	return nil
}

// HoldsPowerMode reports whether the agent settled mode for the current
// request and whether it still reads back actuator.
func HoldsPowerMode[T any](node *corev1.Node, mode string, actuator T, maxDuration time.Duration, now time.Time) (settled, readsBack bool) {
	if done, err := PowerModeOutcome(node, mode, maxDuration, now); err != nil || !done {
		return false, false
	}
	var readBack T
	return true, NodeReadBack(node, &readBack) == nil && reflect.DeepEqual(readBack, actuator)
}

// SetPowerModeTransition writes the agent's view of a request for tests and
// fixtures: desired mode, state, generation, request time, and record.
func SetPowerModeTransition(node *corev1.Node, state string, generation int64, record PowerModeTransitionRecord) {
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	raw, _ := json.Marshal(record) // strings, integers, and times always encode
	node.Labels[PowerMode], node.Labels[PowerModeState] = record.PowerMode, state
	node.Annotations[PowerModeRequestedAt] = record.RequestedAt
	node.Annotations[PowerModeGeneration] = strconv.FormatInt(generation, 10)
	node.Annotations[PowerModeTransition] = string(raw)
}
