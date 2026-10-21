package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const requiredStateNotConfirmedReason = "RequiredStateNotConfirmed"

func (r *RunReconciler) reconcileRequiredState(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	resolved ResolvedRun,
) (bool, error) {
	unconfirmed, err := r.confirmRequiredState(ctx, resolved.Prepared)
	if err != nil {
		return true, err
	}
	if unconfirmed != "" {
		return true, r.publishExecutionOutcome(
			ctx, run, metav1.ConditionFalse, requiredStateNotConfirmedReason, unconfirmed,
		)
	}
	return false, nil
}

// confirmRequiredState returns why the target Node does not hold the Run's
// required state: the agent settled the Run's power mode and reads back the
// selected actuator. A reason is permanent; only a transient API failure
// returns an error, which requeues.
func (r *RunReconciler) confirmRequiredState(ctx context.Context, prepared profiler.PreparedRun) (string, error) {
	config := prepared.RunConfig
	node := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: config.TargetNode.Name}, node); err != nil {
		if apierrors.IsNotFound(err) {
			return "the target Node no longer exists", nil
		}
		return "", fmt.Errorf("get target Node %s: %w", config.TargetNode.Name, err)
	}
	if string(node.UID) != config.TargetNode.UID {
		return "the target Node was replaced by a different object", nil
	}
	maxDuration := time.Duration(prepared.ActuationPolicy.Policy.MaxActuationDurationSeconds) * time.Second
	done, err := metadata.PowerModeOutcome(node, config.Actuation.PowerMode, time.Time{}, maxDuration, time.Now())
	if err != nil || !done {
		return "the target Node has not settled the Run's power mode", nil
	}
	actuator, err := readBackActuator(node)
	if err != nil || !reflect.DeepEqual(actuator, config.Actuation.Actuator) {
		return "the target Node's read-back actuator does not confirm the Run's required state", nil
	}
	return "", nil
}

// readBackActuator is the actuator target the Node's agent last read back.
func readBackActuator(node *corev1.Node) (profilingrun.ActuatorTarget, error) {
	var actuator profilingrun.ActuatorTarget
	if err := json.Unmarshal([]byte(node.Annotations[metadata.PowerActuator]), &actuator); err != nil {
		return profilingrun.ActuatorTarget{}, fmt.Errorf("decode Node %s %s: %w", node.Name, metadata.PowerActuator, err)
	}
	return actuator, nil
}

// GrantedNode reads the Node a measurement root measures on, the Node whose
// measurement taint names root, so the root may write its power mode with
// metadata.RequestPowerMode on this read.
func GrantedNode(ctx context.Context, reader client.Reader, root types.UID, name, uid string) (*corev1.Node, error) {
	node := &corev1.Node{}
	if err := reader.Get(ctx, types.NamespacedName{Name: name}, node); err != nil {
		return nil, fmt.Errorf("get measurement Node %s: %w", name, err)
	}
	if string(node.UID) != uid {
		return nil, fmt.Errorf("measurement Node %s UID %s is not %s", name, node.UID, uid)
	}
	if holder, tainted := profilingrun.MeasurementHolder(node); !tainted || holder != string(root) {
		return nil, fmt.Errorf("measurement Node %s is not granted to %s", name, root)
	}
	return node, nil
}
