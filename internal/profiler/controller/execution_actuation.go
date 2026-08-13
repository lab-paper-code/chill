package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiler"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func (f *KubernetesExecutionFinalizer) observeActuation(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	prepared profiler.PreparedRun,
) (*profilerevidence.ActuationFacts, error) {
	if prepared.Intent.Actuation == nil {
		if run.Status.Actuation != nil {
			return nil, errors.New("fixed Run has an unexpected NodeActuation reference")
		}
		return nil, nil
	}
	if run.Status.Actuation == nil || run.Status.Actuation.Name == "" || run.Status.Actuation.UID == "" {
		return nil, errors.New("actuated Run has no complete NodeActuation reference")
	}
	handoff := &edgev1alpha1.NodeActuation{}
	key := types.NamespacedName{Namespace: run.Namespace, Name: run.Status.Actuation.Name}
	if err := f.reader.Get(ctx, key, handoff); err != nil {
		return nil, fmt.Errorf("get recorded NodeActuation %s: %w", key, err)
	}
	if err := validateFinalizedActuation(run, prepared.Intent, handoff); err != nil {
		return nil, err
	}
	return &profilerevidence.ActuationFacts{
		Run: profilerevidence.NamespacedObjectFacts{
			Namespace: run.Namespace, Name: run.Name, UID: string(run.UID),
		},
		Handoff: profilerevidence.NamespacedObjectFacts{
			Namespace: handoff.Namespace, Name: handoff.Name, UID: string(handoff.UID),
		},
		Previous:           projectEvidenceActuationObservation(handoff.Status.PreviousObservation),
		Target:             projectEvidenceActuationObservation(handoff.Status.TargetObservation),
		Restored:           projectEvidenceActuationObservation(handoff.Status.RestoredObservation),
		ReadyAt:            handoff.Status.ReadyAt.Time,
		RestoreRequestedAt: handoff.Spec.StageRequestedAt.Time,
		RestoredAt:         handoff.Status.RestoredAt.Time,
	}, nil
}

func validateFinalizedActuation(
	run *edgev1alpha1.ProfilingRun,
	intent profilingrun.Intent,
	handoff *edgev1alpha1.NodeActuation,
) error {
	if run == nil || handoff == nil || intent.Actuation == nil {
		return errors.New("Run, actuated intent, and NodeActuation are required")
	}
	if handoff.Namespace != run.Namespace || handoff.Name != run.Status.Actuation.Name ||
		string(handoff.UID) != run.Status.Actuation.UID || !metav1.IsControlledBy(handoff, run) {
		return errors.New("NodeActuation does not match the recorded Run handoff")
	}
	if handoff.Spec.Run.Name != run.Name || handoff.Spec.Run.UID != string(run.UID) ||
		handoff.Spec.Node.Name != intent.TargetNode.Name || handoff.Spec.Node.UID != intent.TargetNode.UID ||
		handoff.Spec.DeviceClass != intent.State.DeviceClass ||
		handoff.Spec.PowerMode != intent.Actuation.PowerMode ||
		!reflect.DeepEqual(handoff.Spec.Actuator, projectActuatorTarget(intent.Actuation.Actuator)) {
		return errors.New("NodeActuation material identity does not match frozen intent")
	}
	if handoff.Spec.DesiredStage != edgev1alpha1.NodeActuationDesiredStageRestore ||
		handoff.Status.Phase != edgev1alpha1.NodeActuationPhaseRestored ||
		handoff.Status.ObservedGeneration != handoff.Generation ||
		handoff.Status.TerminalFailure != nil {
		return errors.New("NodeActuation has not completed successful restoration")
	}
	if handoff.Status.PreviousObservation == nil || handoff.Status.TargetObservation == nil ||
		handoff.Status.RestoredObservation == nil || handoff.Status.ReadyAt == nil ||
		handoff.Status.RestoredAt == nil || handoff.Spec.StageRequestedAt.IsZero() {
		return errors.New("NodeActuation restoration provenance is incomplete")
	}
	return nil
}

func projectEvidenceActuationObservation(
	observation *edgev1alpha1.NodeActuationObservation,
) profilerevidence.ActuationObservation {
	return profilerevidence.ActuationObservation{
		PowerMode:  observation.PowerMode,
		Actuator:   projectProfilingActuatorTarget(observation.Actuator),
		BootID:     observation.BootID,
		ObservedAt: observation.ObservedAt.Time,
	}
}

func projectProfilingActuatorTarget(target edgev1alpha1.PowerModeActuator) profilingrun.ActuatorTarget {
	projected := profilingrun.ActuatorTarget{Kind: string(target.Kind)}
	if target.JetsonNVPModel != nil {
		projected.JetsonNVPModel = &profilingrun.JetsonNVPModelTarget{ID: target.JetsonNVPModel.ID}
	}
	if target.NvidiaLockedGPUClock != nil {
		projected.NvidiaLockedGPUClock = &profilingrun.NvidiaLockedGPUClockTarget{
			MinMHz: target.NvidiaLockedGPUClock.MinMHz,
			MaxMHz: target.NvidiaLockedGPUClock.MaxMHz,
			Mode:   target.NvidiaLockedGPUClock.Mode,
		}
	}
	return projected
}
