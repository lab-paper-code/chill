// Package controller adapts durable Kubernetes handoffs to the pure
// NodeActuator contract. It performs no host operation.
package controller

import (
	"errors"
	"fmt"
	"time"

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
		PolicyName:                  handoff.Spec.PolicyName,
		SettlingDurationSeconds:     handoff.Spec.SettlingDurationSeconds,
		MaxActuationDurationSeconds: handoff.Spec.MaxActuationDurationSeconds,
		Deadline:                    handoff.Spec.Deadline.Time,
		StageRequestedAt:            handoff.Spec.StageRequestedAt.Time,
	}
	if err := request.ValidateForNode(assignedNode); err != nil {
		return nodeactuator.Request{}, fmt.Errorf("validate projected NodeActuation: %w", err)
	}
	return request, nil
}

func ProjectSnapshot(
	handoff *edgev1alpha1.NodeActuation,
	assignedNode nodeactuator.ObjectIdentity,
	lease LeaseBinding,
	now time.Time,
) (nodeactuator.Snapshot, error) {
	request, err := ProjectRequest(handoff, assignedNode)
	if err != nil {
		return nodeactuator.Snapshot{}, err
	}
	if handoff.Status.ObservedGeneration < 0 || handoff.Status.ObservedGeneration > handoff.Generation {
		return nodeactuator.Snapshot{}, errors.New("NodeActuation status observedGeneration is invalid")
	}
	if handoff.Status.Lease != nil &&
		(lease.UID == "" || handoff.Status.Lease.Name != lease.Name || handoff.Status.Lease.UID != lease.UID) {
		return nodeactuator.Snapshot{}, errors.New("NodeActuation status Lease identity does not match observed Lease")
	}
	snapshot := nodeactuator.Snapshot{
		Request: request,
		Now:     now,
		Phase:   nodeactuator.Phase(handoff.Status.Phase),
		Lease: nodeactuator.LeaseObservation{
			State: lease.State, RetryAfter: lease.RetryAfter,
		},
		Safety: nodeactuator.SafetyUnknown,
	}
	if handoff.Status.Safety != nil {
		snapshot.SafetyObservedAt = &handoff.Status.Safety.ObservedAt.Time
		switch handoff.Status.Safety.Verdict {
		case edgev1alpha1.NodeActuationSafetyAllowed:
			snapshot.Safety = nodeactuator.SafetyAllowed
		case edgev1alpha1.NodeActuationSafetyBlocked:
			snapshot.Safety = nodeactuator.SafetyBlocked
		default:
			return nodeactuator.Snapshot{}, fmt.Errorf(
				"unsupported workload safety verdict %q", handoff.Status.Safety.Verdict,
			)
		}
	}
	snapshot.PreviousObservation, err = projectObservation(handoff.Status.PreviousObservation)
	if err != nil {
		return nodeactuator.Snapshot{}, fmt.Errorf("project previous observation: %w", err)
	}
	snapshot.TargetObservation, err = projectObservation(handoff.Status.TargetObservation)
	if err != nil {
		return nodeactuator.Snapshot{}, fmt.Errorf("project target observation: %w", err)
	}
	snapshot.VerifiedAt = projectTime(handoff.Status.VerifiedAt)
	snapshot.SettlingStartedAt = projectTime(handoff.Status.SettlingStartedAt)
	if handoff.Status.TerminalFailure != nil {
		snapshot.Failure = &nodeactuator.Failure{
			Stage:   nodeactuator.FailureStage(handoff.Status.TerminalFailure.Stage),
			Reason:  handoff.Status.TerminalFailure.Reason,
			Message: handoff.Status.TerminalFailure.Message,
		}
	}
	if err := snapshot.Validate(); err != nil {
		return nodeactuator.Snapshot{}, fmt.Errorf("validate projected NodeActuation snapshot: %w", err)
	}
	return snapshot, nil
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

func projectObservation(observation *edgev1alpha1.NodeActuationObservation) (*nodeactuator.Observation, error) {
	if observation == nil {
		return nil, nil
	}
	projected := &nodeactuator.Observation{
		PowerMode:  observation.PowerMode,
		Target:     projectTarget(observation.Actuator),
		ObservedAt: observation.ObservedAt.Time,
	}
	if err := projected.Validate(); err != nil {
		return nil, err
	}
	return projected, nil
}

func projectTime(value *metav1.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.Time
	return &copy
}
