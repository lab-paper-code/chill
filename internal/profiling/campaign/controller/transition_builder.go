package controller

import (
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metadata"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	corev1 "k8s.io/api/core/v1"
)

// TransitionMaterial is the Node and power mode that establish a campaign
// state, and the selected policy's bound for the Node's agent to settle it.
type TransitionMaterial struct {
	Node                        edgev1alpha1.ProfilingRunObjectReference
	DeviceClass                 string
	PowerMode                   string
	MaxActuationDurationSeconds int32
}

func projectTransitionMaterial(prepared profiler.PreparedRun) (*TransitionMaterial, error) {
	if err := prepared.ValidateActuationBinding(); err != nil {
		return nil, err
	}
	if prepared.RunConfig.Actuation == nil {
		return nil, nil
	}
	return &TransitionMaterial{
		Node: edgev1alpha1.ProfilingRunObjectReference{
			Name: prepared.RunConfig.TargetNode.Name, UID: prepared.RunConfig.TargetNode.UID,
		},
		DeviceClass:                 prepared.RunConfig.State.DeviceClass,
		PowerMode:                   prepared.RunConfig.Actuation.PowerMode,
		MaxActuationDurationSeconds: int32(prepared.ActuationPolicy.Policy.MaxActuationDurationSeconds),
	}, nil
}

// projectNodeTransition projects the campaign's power mode on its granted Node
// into the progress rules. It is nil until the campaign requested the mode.
func projectNodeTransition(
	node *corev1.Node,
	material *TransitionMaterial,
	now time.Time,
) *batchenergyoptimization.Transition {
	if node.Labels[metadata.PowerMode] != material.PowerMode {
		return nil
	}
	maxDuration := time.Duration(material.MaxActuationDurationSeconds) * time.Second
	done, err := metadata.PowerModeOutcome(node, material.PowerMode, maxDuration, now)
	switch {
	case err != nil:
		return &batchenergyoptimization.Transition{Outcome: batchenergyoptimization.TransitionFailed}
	case !done:
		return &batchenergyoptimization.Transition{Outcome: batchenergyoptimization.TransitionInFlight}
	}
	return &batchenergyoptimization.Transition{
		Outcome: batchenergyoptimization.TransitionEstablished,
		EstablishedState: &batchenergyoptimization.State{
			TargetNode: node.Name, DeviceClass: material.DeviceClass, PowerMode: material.PowerMode,
		},
	}
}
