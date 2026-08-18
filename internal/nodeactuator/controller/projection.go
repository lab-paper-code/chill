// Package controller adapts durable Kubernetes transitions to the pure
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

// ProjectRequest verifies API identity and the local Node binding before the
// pure actuator core or a host adapter may observe the request.
func ProjectRequest(
	transition *edgev1alpha1.NodeActuation,
	assignedNode nodeactuator.ObjectIdentity,
) (nodeactuator.Request, error) {
	if transition == nil {
		return nodeactuator.Request{}, errors.New("NodeActuation is required")
	}
	if transition.Namespace == "" || transition.Name == "" || transition.UID == "" || transition.Generation < 1 {
		return nodeactuator.Request{}, errors.New("NodeActuation API identity is invalid")
	}
	request := nodeactuator.Request{
		Transition:                  nodeactuator.ObjectIdentity{Name: transition.Name, UID: string(transition.UID)},
		Generation:                  transition.Generation,
		Node:                        projectObjectIdentity(transition.Spec.Node),
		DeviceClass:                 transition.Spec.DeviceClass,
		PowerMode:                   transition.Spec.PowerMode,
		Target:                      projectTarget(transition.Spec.Actuator),
		PolicyName:                  transition.Spec.PolicyName,
		SettlingDurationSeconds:     transition.Spec.SettlingDurationSeconds,
		MaxActuationDurationSeconds: transition.Spec.MaxActuationDurationSeconds,
		Deadline:                    transition.Spec.Deadline.Time,
		StageRequestedAt:            transition.Spec.StageRequestedAt.Time,
	}
	if err := request.ValidateForNode(assignedNode); err != nil {
		return nodeactuator.Request{}, fmt.Errorf("validate projected NodeActuation: %w", err)
	}
	return request, nil
}

func ProjectSnapshot(
	transition *edgev1alpha1.NodeActuation,
	assignedNode nodeactuator.ObjectIdentity,
	lease LeaseRecord,
	now time.Time,
) (nodeactuator.Snapshot, error) {
	request, err := ProjectRequest(transition, assignedNode)
	if err != nil {
		return nodeactuator.Snapshot{}, err
	}
	if transition.Status.ObservedGeneration < 0 || transition.Status.ObservedGeneration > transition.Generation {
		return nodeactuator.Snapshot{}, errors.New("NodeActuation status observedGeneration is invalid")
	}
	if transition.Status.Lease != nil &&
		(lease.UID == "" || transition.Status.Lease.Name != lease.Name || transition.Status.Lease.UID != lease.UID) {
		return nodeactuator.Snapshot{}, errors.New("NodeActuation status Lease identity does not match observed Lease")
	}
	snapshot := nodeactuator.Snapshot{
		Request: request,
		Now:     now,
		Phase:   nodeactuator.Phase(transition.Status.Phase),
		Lock: nodeactuator.LockObservation{
			State: lease.State, RetryAfter: lease.RetryAfter,
		},
		Safety: nodeactuator.SafetyUnknown,
	}
	if transition.Status.Safety != nil {
		snapshot.SafetyObservedAt = &transition.Status.Safety.ObservedAt.Time
		switch transition.Status.Safety.Verdict {
		case edgev1alpha1.NodeActuationSafetyAllowed:
			snapshot.Safety = nodeactuator.SafetyAllowed
		case edgev1alpha1.NodeActuationSafetyBlocked:
			snapshot.Safety = nodeactuator.SafetyBlocked
		default:
			return nodeactuator.Snapshot{}, fmt.Errorf(
				"unsupported workload safety verdict %q", transition.Status.Safety.Verdict,
			)
		}
	}
	snapshot.PreviousObservation, err = projectObservation(transition.Status.PreviousObservation)
	if err != nil {
		return nodeactuator.Snapshot{}, fmt.Errorf("project previous observation: %w", err)
	}
	snapshot.TargetObservation, err = projectObservation(transition.Status.TargetObservation)
	if err != nil {
		return nodeactuator.Snapshot{}, fmt.Errorf("project target observation: %w", err)
	}
	snapshot.VerifiedAt = projectTime(transition.Status.VerifiedAt)
	snapshot.SettlingStartedAt = projectTime(transition.Status.SettlingStartedAt)
	if transition.Status.TerminalFailure != nil {
		snapshot.Failure = &nodeactuator.Failure{
			Stage:   nodeactuator.FailureStage(transition.Status.TerminalFailure.Stage),
			Reason:  transition.Status.TerminalFailure.Reason,
			Message: transition.Status.TerminalFailure.Message,
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

func projectTime(value *metav1.MicroTime) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.Time
	return &copy
}
