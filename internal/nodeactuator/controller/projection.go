// Package controller adapts durable Kubernetes handoffs to the pure
// NodeActuator contract. It performs no host operation.
package controller

import (
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ProjectRequest verifies API identity, ownership, and the local Node binding
// before the pure actuator core or a host adapter may observe the request.
func ProjectRequest(
	handoff *edgev1alpha1.NodeActuation,
	assignedNode nodeactuator.ObjectIdentity,
) (nodeactuator.Request, error) {
	if handoff == nil {
		return nodeactuator.Request{}, errors.New("NodeActuation is required")
	}
	controller := metav1.GetControllerOf(handoff)
	if handoff.Namespace == "" || handoff.Name == "" || handoff.UID == "" || handoff.Generation < 1 ||
		controller == nil || controller.APIVersion != edgev1alpha1.GroupVersion.String() ||
		controller.Kind != "ProfilingRun" || controller.Name != handoff.Spec.Run.Name ||
		string(controller.UID) != handoff.Spec.Run.UID {
		return nodeactuator.Request{}, errors.New("NodeActuation API identity or ProfilingRun ownership is invalid")
	}
	request := nodeactuator.Request{
		Handoff:                     nodeactuator.ObjectIdentity{Name: handoff.Name, UID: string(handoff.UID)},
		Generation:                  handoff.Generation,
		Run:                         projectObjectIdentity(handoff.Spec.Run),
		Node:                        projectObjectIdentity(handoff.Spec.Node),
		DeviceClass:                 handoff.Spec.DeviceClass,
		PowerMode:                   handoff.Spec.PowerMode,
		Target:                      projectTarget(handoff.Spec.Actuator),
		PolicyDigest:                handoff.Spec.PolicyDigest,
		SettlingDurationSeconds:     handoff.Spec.SettlingDurationSeconds,
		MaxActuationDurationSeconds: handoff.Spec.MaxActuationDurationSeconds,
		Deadline:                    handoff.Spec.Deadline.Time,
		DesiredStage:                string(handoff.Spec.DesiredStage),
		StageRequestedAt:            handoff.Spec.StageRequestedAt.Time,
	}
	if err := request.ValidateForNode(assignedNode); err != nil {
		return nodeactuator.Request{}, fmt.Errorf("validate projected NodeActuation: %w", err)
	}
	return request, nil
}

func projectObjectIdentity(reference edgev1alpha1.ProfilingRunObjectReference) nodeactuator.ObjectIdentity {
	return nodeactuator.ObjectIdentity{Name: reference.Name, UID: reference.UID}
}

func projectTarget(actuator edgev1alpha1.PowerModeActuator) nodeactuator.Target {
	target := nodeactuator.Target{Kind: string(actuator.Kind)}
	if actuator.JetsonNVPModel != nil {
		target.JetsonNVPModel = &nodeactuator.JetsonNVPModelTarget{ID: actuator.JetsonNVPModel.ID}
	}
	if actuator.NvidiaLockedGPUClock != nil {
		target.NvidiaLockedGPUClock = &nodeactuator.NvidiaLockedGPUClockTarget{
			MinMHz: actuator.NvidiaLockedGPUClock.MinMHz,
			MaxMHz: actuator.NvidiaLockedGPUClock.MaxMHz,
			Mode:   actuator.NvidiaLockedGPUClock.Mode,
		}
	}
	return target
}
