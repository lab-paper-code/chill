package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func stateActuationName(parentName string) string {
	return parentName + "-actuation"
}

// ensureRequiredState establishes the parent state once, before its first Run.
// A stored operation is authoritative and is never compared with mutable facts.
func (r *Reconciler) ensureRequiredState(
	ctx context.Context,
	parent *edgev1alpha1.ModelLoadedIdleBaseline,
) (established, failed bool, err error) {
	key := client.ObjectKey{Namespace: parent.Namespace, Name: stateActuationName(parent.Name)}
	existing := &edgev1alpha1.NodeActuation{}
	if getErr := r.client.Get(ctx, key, existing); getErr == nil {
		return projectStateActuation(parent, existing)
	} else if !apierrors.IsNotFound(getErr) {
		return false, false, fmt.Errorf("get idle baseline NodeActuation %q: %w", key.Name, getErr)
	}

	prototype, err := buildRun(parent, 1)
	if err != nil {
		return false, false, err
	}
	requested, err := profilercontroller.ProjectRequest(prototype.Spec)
	if err != nil {
		return false, false, err
	}
	prepared, err := r.preparer.Prepare(ctx, requested.Preparation)
	if err != nil {
		return false, false, fmt.Errorf("prepare idle baseline state: %w", err)
	}
	desired, err := buildStateActuation(parent, prepared, r.now())
	if err != nil {
		return false, false, err
	}
	if desired == nil {
		return true, false, nil
	}
	if createErr := r.client.Create(ctx, desired); createErr == nil {
		return false, false, nil
	} else if !apierrors.IsAlreadyExists(createErr) {
		return false, false, fmt.Errorf("create idle baseline NodeActuation %q: %w", desired.Name, createErr)
	}
	if getErr := r.client.Get(ctx, key, existing); getErr != nil {
		return false, false, fmt.Errorf("get existing idle baseline NodeActuation %q: %w", key.Name, getErr)
	}
	return projectStateActuation(parent, existing)
}

func buildStateActuation(
	parent *edgev1alpha1.ModelLoadedIdleBaseline,
	prepared profiler.PreparedRun,
	now time.Time,
) (*edgev1alpha1.NodeActuation, error) {
	if err := prepared.ValidateActuationBinding(); err != nil {
		return nil, fmt.Errorf("validate idle baseline actuation binding: %w", err)
	}
	if prepared.Intent.Actuation == nil {
		return nil, nil
	}
	if parent == nil || parent.UID == "" {
		return nil, errors.New("a ModelLoadedIdleBaseline with UID is required")
	}
	if prepared.Intent.TargetNode.Name != parent.Spec.State.TargetNode ||
		prepared.Intent.TargetNode.UID == "" ||
		prepared.Intent.State.DeviceClass != parent.Spec.State.Candidate.DeviceClass ||
		prepared.Intent.State.PowerMode != parent.Spec.State.Candidate.PowerMode {
		return nil, errors.New("prepared actuation does not match the idle baseline state")
	}
	policy := prepared.ActuationPolicy.Policy
	target := prepared.Intent.Actuation.Actuator
	actuator := edgev1alpha1.PowerModeActuator{
		Kind: edgev1alpha1.PowerModeActuatorKind(target.Kind),
	}
	if target.JetsonNVPModel != nil {
		actuator.JetsonNVPModel = &edgev1alpha1.JetsonNVPModelTarget{ID: target.JetsonNVPModel.ID}
	}
	if target.NvidiaLockedGPUClock != nil {
		actuator.NvidiaLockedGPUClock = &edgev1alpha1.NvidiaLockedGPUClockTarget{
			MinMHz: target.NvidiaLockedGPUClock.MinMHz,
			MaxMHz: target.NvidiaLockedGPUClock.MaxMHz,
		}
		if target.NvidiaLockedGPUClock.Mode != nil {
			mode := *target.NvidiaLockedGPUClock.Mode
			actuator.NvidiaLockedGPUClock.Mode = &mode
		}
	}
	requestedAt := metav1.NewTime(now.UTC())
	return &edgev1alpha1.NodeActuation{
		ObjectMeta: metav1.ObjectMeta{
			Name: stateActuationName(parent.Name), Namespace: parent.Namespace,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				parent, edgev1alpha1.GroupVersion.WithKind(idleBaselineKind),
			)},
		},
		Spec: edgev1alpha1.NodeActuationSpec{
			Node: edgev1alpha1.ProfilingRunObjectReference{
				Name: prepared.Intent.TargetNode.Name, UID: prepared.Intent.TargetNode.UID,
			},
			DeviceClass:                 prepared.Intent.State.DeviceClass,
			PowerMode:                   prepared.Intent.Actuation.PowerMode,
			Actuator:                    actuator,
			PolicyName:                  prepared.Intent.Actuation.PolicyName,
			SettlingDurationSeconds:     int32(policy.SettlingDurationSeconds),
			MaxActuationDurationSeconds: int32(policy.MaxActuationDurationSeconds),
			StageRequestedAt:            requestedAt,
			Deadline: metav1.NewTime(now.UTC().Add(
				time.Duration(policy.MaxActuationDurationSeconds) * time.Second,
			)),
		},
	}, nil
}

func projectStateActuation(
	parent *edgev1alpha1.ModelLoadedIdleBaseline,
	actuation *edgev1alpha1.NodeActuation,
) (established, failed bool, err error) {
	owner := metav1.GetControllerOf(actuation)
	if owner == nil || owner.APIVersion != edgev1alpha1.GroupVersion.String() ||
		owner.Kind != idleBaselineKind || owner.UID != parent.UID {
		return false, false, errors.New("NodeActuation is not owned by this idle baseline")
	}
	if actuation.Namespace != parent.Namespace || actuation.Name != stateActuationName(parent.Name) ||
		actuation.Spec.Node.Name != parent.Spec.State.TargetNode || actuation.Spec.Node.UID == "" ||
		actuation.Spec.DeviceClass != parent.Spec.State.Candidate.DeviceClass ||
		actuation.Spec.PowerMode != parent.Spec.State.Candidate.PowerMode {
		return false, false, errors.New("NodeActuation does not match the idle baseline state")
	}
	switch actuation.Status.Phase {
	case "", edgev1alpha1.NodeActuationPhasePending,
		edgev1alpha1.NodeActuationPhaseAcquiringLease,
		edgev1alpha1.NodeActuationPhaseCheckingSafety,
		edgev1alpha1.NodeActuationPhaseObservingPrevious,
		edgev1alpha1.NodeActuationPhaseApplying,
		edgev1alpha1.NodeActuationPhaseVerifying,
		edgev1alpha1.NodeActuationPhaseSettling:
		return false, false, nil
	case edgev1alpha1.NodeActuationPhaseFailed:
		if actuation.Status.TerminalFailure == nil {
			return false, false, errors.New("failed NodeActuation omitted its terminal failure")
		}
		return false, true, nil
	case edgev1alpha1.NodeActuationPhaseReady:
		observation := actuation.Status.TargetObservation
		if actuation.Status.ObservedGeneration != actuation.Generation ||
			actuation.Status.TerminalFailure != nil || actuation.Status.ReadyAt == nil ||
			actuation.Status.ReadyAt.IsZero() || observation == nil ||
			observation.PowerMode != actuation.Spec.PowerMode ||
			!reflect.DeepEqual(observation.Actuator, actuation.Spec.Actuator) {
			return false, false, errors.New("ready NodeActuation does not prove its declared target")
		}
		return true, false, nil
	default:
		return false, false, fmt.Errorf("unsupported NodeActuation phase %q", actuation.Status.Phase)
	}
}
