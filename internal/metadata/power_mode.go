package metadata

import (
	"context"
	"encoding/json"
	"fmt"
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

// RequestPowerMode makes mode the Node's desired power mode. It patches with
// the resourceVersion the caller read, so a writer whose view of the Node,
// and with it the measurement grant, is stale gets a conflict. A mode the
// agent failed is removed and written again, which the agent applies afresh;
// any other mode already requested is left alone.
func RequestPowerMode(ctx context.Context, c client.Client, node *corev1.Node, mode string) error {
	if node.Labels[PowerMode] == mode {
		transition, _, err := NodePowerModeTransition(node)
		if err != nil {
			return err
		}
		if node.Labels[PowerModeState] != PowerModeFailed || transition.PowerMode != mode {
			return nil
		}
		if err := patchPowerMode(ctx, c, node, nil); err != nil {
			return err
		}
	}
	return patchPowerMode(ctx, c, node, &mode)
}

func patchPowerMode(ctx context.Context, c client.Client, node *corev1.Node, mode *string) error {
	raw, err := json.Marshal(map[string]any{"metadata": map[string]any{
		"resourceVersion": node.ResourceVersion,
		"labels":          map[string]*string{PowerMode: mode},
	}})
	if err != nil {
		return err
	}
	if err := c.Patch(ctx, node, client.RawPatch(types.MergePatchType, raw)); err != nil {
		return fmt.Errorf("request power mode on Node %s: %w", node.Name, err)
	}
	return nil
}

// PowerModeOutcome reports the agent's progress toward mode on node. Done is
// true once the agent settled mode and the host has not changed since. An
// error reports a failed apply, or no success within maxDuration of since,
// the time the writer requested mode; a zero since counts from the agent's
// fence.
func PowerModeOutcome(node *corev1.Node, mode string, since time.Time, maxDuration time.Duration, now time.Time) (bool, error) {
	transition, _, err := NodePowerModeTransition(node)
	if err != nil {
		return false, err
	}
	generation, _, err := NodePowerModeGeneration(node)
	if err != nil {
		return false, err
	}
	current := node.Labels[PowerMode] == mode && transition.PowerMode == mode
	state := node.Labels[PowerModeState]
	switch {
	case current && state == PowerModeSuccess && transition.Generation == generation && transition.ReadyAt != nil:
		return true, nil
	case current && state == PowerModeSuccess:
		return false, fmt.Errorf("Node %s left power mode %s at generation %d", node.Name, mode, generation)
	case current && state == PowerModeFailed:
		return false, fmt.Errorf("Node %s power mode %s failed: %s", node.Name, mode, transition.Failure)
	}
	if since.IsZero() && current && transition.ApplyStartedAt != nil {
		since = *transition.ApplyStartedAt
	}
	if !since.IsZero() && !now.Before(since.Add(maxDuration)) {
		return false, fmt.Errorf("Node %s did not reach power mode %s within %s", node.Name, mode, maxDuration)
	}
	return false, nil
}
