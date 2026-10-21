package controller

import (
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	corev1 "k8s.io/api/core/v1"
)

// observeActuation records the Node's last power-mode apply, which confirmed
// the Run before its Job, and the generation now. Evidence validation rejects
// the facts unless they match the RunConfig and no power change intervened.
func observeActuation(
	run *edgev1alpha1.ProfilingRun,
	prepared profiler.PreparedRun,
	node *corev1.Node,
) (*profilerevidence.ActuationFacts, error) {
	if prepared.RunConfig.Actuation == nil {
		return nil, nil
	}
	transition, ok, err := metadata.NodePowerModeTransition(node)
	if err != nil {
		return nil, err
	}
	if !ok || transition.VerifiedAt == nil || transition.ReadyAt == nil {
		return nil, errors.New("measurement Node has no settled power-mode apply")
	}
	actuator, err := readBackActuator(node)
	if err != nil {
		return nil, err
	}
	facts := &profilerevidence.ActuationFacts{
		Run: profilerevidence.NamespacedObjectFacts{
			Namespace: run.Namespace, Name: run.Name, UID: string(run.UID),
		},
		Target: profilerevidence.ActuationObservation{
			PowerMode: transition.PowerMode, Actuator: actuator, ObservedAt: transition.VerifiedAt.UTC(),
		},
		ReadyAt: transition.ReadyAt.UTC(),
	}
	generation, ok, err := metadata.NodePowerModeGeneration(node)
	if err != nil {
		return nil, fmt.Errorf("read measurement Node power-mode generation: %w", err)
	}
	if ok {
		facts.Generation = &profilerevidence.PowerModeGeneration{Start: transition.Generation, End: generation}
	}
	return facts, nil
}
