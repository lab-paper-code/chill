package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const requiredStateNotConfirmedReason = "RequiredStateNotConfirmed"

func (r *RunReconciler) reconcileRequiredState(
	ctx context.Context,
	request ctrl.Request,
	run *edgev1alpha1.ProfilingRun,
	frozen FrozenRun,
) (bool, error) {
	transition, err := r.findConfirmingTransition(ctx, run, frozen.Prepared.Intent)
	if err != nil {
		return true, err
	}
	if transition == nil {
		return true, r.publishExecutionOutcome(
			ctx,
			run,
			metav1.ConditionFalse,
			requiredStateNotConfirmedReason,
			"the most recent successful Node transition does not confirm the Run's required state",
		)
	}
	if err := r.publishActuationReference(ctx, run, transition); err != nil {
		return true, fmt.Errorf("publish confirming NodeActuation reference: %w", err)
	}
	if run.Status.Job == nil {
		return true, r.ensureMeasurementJob(ctx, request, run, frozen)
	}
	return false, nil
}

func (r *RunReconciler) findConfirmingTransition(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	intent profilingrun.Intent,
) (*edgev1alpha1.NodeActuation, error) {
	if run.Status.Actuation != nil {
		transition := &edgev1alpha1.NodeActuation{}
		key := client.ObjectKey{Namespace: run.Namespace, Name: run.Status.Actuation.Name}
		if err := r.Get(ctx, key, transition); err != nil {
			return nil, fmt.Errorf("get recorded confirming NodeActuation %s: %w", key, err)
		}
		if string(transition.UID) != run.Status.Actuation.UID {
			return nil, errors.New("recorded confirming NodeActuation UID does not match the observed object")
		}
		if err := validateConfirmingTransition(intent, transition); err != nil {
			return nil, fmt.Errorf("validate recorded confirming NodeActuation: %w", err)
		}
		return transition, nil
	}

	transitions := &edgev1alpha1.NodeActuationList{}
	if err := r.List(ctx, transitions, client.InNamespace(run.Namespace)); err != nil {
		return nil, fmt.Errorf("list NodeActuations for state confirmation: %w", err)
	}
	var latest *edgev1alpha1.NodeActuation
	for index := range transitions.Items {
		candidate := &transitions.Items[index]
		if !successfullyCompletedOnNode(candidate, intent.TargetNode) {
			continue
		}
		if latest == nil || candidate.Status.ReadyAt.After(latest.Status.ReadyAt.Time) ||
			(candidate.Status.ReadyAt.Equal(latest.Status.ReadyAt) && candidate.Name > latest.Name) {
			latest = candidate
		}
	}
	if latest == nil || validateConfirmingTransition(intent, latest) != nil {
		return nil, nil
	}
	return latest.DeepCopy(), nil
}

func successfullyCompletedOnNode(
	transition *edgev1alpha1.NodeActuation,
	target profilingrun.NodeSnapshot,
) bool {
	return transition != nil && transition.Namespace != "" && transition.Name != "" && transition.UID != "" &&
		transition.Generation > 0 && transition.Spec.Node.Name == target.Name &&
		transition.Spec.Node.UID == target.UID && transition.Status.Phase == edgev1alpha1.NodeActuationPhaseReady &&
		transition.Status.ObservedGeneration == transition.Generation && transition.Status.TerminalFailure == nil &&
		transition.Status.TargetObservation != nil && !transition.Status.TargetObservation.ObservedAt.IsZero() &&
		transition.Status.ReadyAt != nil && !transition.Status.ReadyAt.IsZero() &&
		!transition.Status.ReadyAt.Before(&transition.Status.TargetObservation.ObservedAt)
}

func validateConfirmingTransition(
	intent profilingrun.Intent,
	transition *edgev1alpha1.NodeActuation,
) error {
	if intent.Actuation == nil || transition == nil {
		return errors.New("actuated intent and NodeActuation are required")
	}
	if !successfullyCompletedOnNode(transition, intent.TargetNode) {
		return errors.New("NodeActuation is not a successful transition on the target Node")
	}
	if transition.Spec.DeviceClass != intent.State.DeviceClass ||
		transition.Spec.PowerMode != intent.Actuation.PowerMode ||
		!reflect.DeepEqual(transition.Spec.Actuator, projectActuatorTarget(intent.Actuation.Actuator)) ||
		transition.Status.TargetObservation.PowerMode != intent.Actuation.PowerMode ||
		!reflect.DeepEqual(transition.Status.TargetObservation.Actuator, projectActuatorTarget(intent.Actuation.Actuator)) {
		return errors.New("NodeActuation does not confirm the frozen required state")
	}
	return nil
}

func projectActuatorTarget(target profilingrun.ActuatorTarget) edgev1alpha1.PowerModeActuator {
	projected := edgev1alpha1.PowerModeActuator{Kind: edgev1alpha1.PowerModeActuatorKind(target.Kind)}
	if target.JetsonNVPModel != nil {
		projected.JetsonNVPModel = &edgev1alpha1.JetsonNVPModelTarget{ID: target.JetsonNVPModel.ID}
	}
	if target.NvidiaLockedGPUClock != nil {
		projected.NvidiaLockedGPUClock = &edgev1alpha1.NvidiaLockedGPUClockTarget{
			MinMHz: target.NvidiaLockedGPUClock.MinMHz,
			MaxMHz: target.NvidiaLockedGPUClock.MaxMHz,
			Mode:   target.NvidiaLockedGPUClock.Mode,
		}
	}
	return projected
}
