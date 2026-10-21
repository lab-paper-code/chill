package controller

import (
	"context"
	"fmt"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
	settled, readsBack := metadata.HoldsPowerMode(
		node, config.Actuation.PowerMode, config.Actuation.Actuator, maxDuration, time.Now(),
	)
	if !settled {
		return "the target Node has not settled the Run's power mode", nil
	}
	if !readsBack {
		return "the target Node's read-back actuator does not confirm the Run's required state", nil
	}
	return "", nil
}
