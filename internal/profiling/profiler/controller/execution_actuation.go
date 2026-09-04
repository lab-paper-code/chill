package controller

import (
	"context"
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"k8s.io/apimachinery/pkg/types"
)

func (f *KubernetesExecutionFinalizer) observeActuation(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	prepared profiler.PreparedRun,
) (*profilerevidence.ActuationFacts, error) {
	if prepared.RunConfig.Actuation == nil {
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
	if err := validateFinalizedActuation(run, prepared.RunConfig, handoff); err != nil {
		return nil, err
	}
	return &profilerevidence.ActuationFacts{
		Run: profilerevidence.NamespacedObjectFacts{
			Namespace: run.Namespace, Name: run.Name, UID: string(run.UID),
		},
		Handoff: profilerevidence.NamespacedObjectFacts{
			Namespace: handoff.Namespace, Name: handoff.Name, UID: string(handoff.UID),
		},
		Target:  projectEvidenceActuationObservation(handoff.Status.TargetObservation),
		ReadyAt: handoff.Status.ReadyAt.Time,
	}, nil
}

func validateFinalizedActuation(
	run *edgev1alpha1.ProfilingRun,
	config profilingrun.RunConfig,
	handoff *edgev1alpha1.NodeActuation,
) error {
	if run == nil || handoff == nil || config.Actuation == nil {
		return errors.New("Run, actuated config, and NodeActuation are required")
	}
	if handoff.Namespace != run.Namespace || handoff.Name != run.Status.Actuation.Name ||
		string(handoff.UID) != run.Status.Actuation.UID {
		return errors.New("NodeActuation does not match the recorded confirming transition")
	}
	if err := validateConfirmingTransition(config, handoff); err != nil {
		return err
	}
	return nil
}

func projectEvidenceActuationObservation(
	observation *edgev1alpha1.NodeActuationObservation,
) profilerevidence.ActuationObservation {
	return profilerevidence.ActuationObservation{
		PowerMode:  observation.PowerMode,
		Actuator:   projectProfilingActuatorTarget(observation.Actuator),
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
