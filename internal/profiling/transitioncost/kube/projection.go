package kube

import (
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
)

type servingSnapshot struct {
	readiness *transitioncost.ReadyObservation
	process   *transitioncost.ProcessIdentity
}

func projectServingPod(pod corev1.Pod, containerName string, receivedAt time.Time) servingSnapshot {
	ready, known := podReady(pod.Status.Conditions)
	if !known {
		return servingSnapshot{}
	}
	snapshot := servingSnapshot{readiness: &transitioncost.ReadyObservation{
		ObservedAt: receivedAt,
		Ready:      ready,
	}}
	if !ready {
		return snapshot
	}
	for index := range pod.Status.ContainerStatuses {
		status := pod.Status.ContainerStatuses[index]
		if status.Name != containerName {
			continue
		}
		if status.State.Running == nil || status.ContainerID == "" || status.State.Running.StartedAt.IsZero() {
			return snapshot
		}
		snapshot.process = &transitioncost.ProcessIdentity{
			ContainerName: status.Name,
			ContainerID:   status.ContainerID,
			StartedAt:     status.State.Running.StartedAt.Time,
			RestartCount:  status.RestartCount,
		}
		return snapshot
	}
	return snapshot
}

func podReady(conditions []corev1.PodCondition) (bool, bool) {
	for index := range conditions {
		if conditions[index].Type != corev1.PodReady {
			continue
		}
		switch conditions[index].Status {
		case corev1.ConditionTrue:
			return true, true
		case corev1.ConditionFalse:
			return false, true
		default:
			return false, false
		}
	}
	return false, false
}

func appendSnapshotReadiness(
	raw *transitioncost.RawObservation,
	snapshot servingSnapshot,
	last **bool,
	force bool,
) {
	observation := snapshot.readiness
	if observation == nil {
		return
	}
	// Ready=True closes the measurement only when the same API snapshot also
	// contains the selected running-process fact.
	if observation.Ready && snapshot.process == nil {
		return
	}
	if !force && *last != nil && **last == observation.Ready {
		return
	}
	value := observation.Ready
	*last = &value
	raw.Readiness = append(raw.Readiness, *observation)
}

func projectActuation(
	raw *transitioncost.RawObservation,
	status edgev1alpha1.NodeActuationStatus,
	deviceType string,
	model string,
	receivedAt time.Time,
) error {
	if raw.SourceReadBack.Fact.MappedAt.IsZero() && status.PreviousObservation != nil {
		readBack, err := projectReadBack(*status.PreviousObservation, deviceType, model, receivedAt)
		if err != nil {
			return fmt.Errorf("project previous NodeActuation observation: %w", err)
		}
		raw.SourceReadBack = readBack
	}
	if raw.TransitionStart == nil && status.ApplyStartedAt != nil {
		if status.ApplyStartedAt.IsZero() {
			return errors.New("NodeActuation apply-start timestamp is zero")
		}
		raw.TransitionStart = &transitioncost.MappedFact{
			OccurredAt: status.ApplyStartedAt.Time,
			MappedAt:   receivedAt,
		}
	}
	if raw.TargetReadBack == nil && status.TargetObservation != nil {
		readBack, err := projectReadBack(*status.TargetObservation, deviceType, model, receivedAt)
		if err != nil {
			return fmt.Errorf("project target NodeActuation observation: %w", err)
		}
		raw.TargetReadBack = &readBack
	}
	if status.TerminalFailure == nil {
		if status.Phase == edgev1alpha1.NodeActuationPhaseFailed {
			return errors.New("failed NodeActuation has no terminal failure")
		}
		return nil
	}
	if status.Phase != edgev1alpha1.NodeActuationPhaseFailed {
		return errors.New("NodeActuation terminal failure is not in Failed phase")
	}
	if status.TerminalFailure.Message == "" ||
		strings.TrimSpace(status.TerminalFailure.Message) != status.TerminalFailure.Message {
		return errors.New("NodeActuation terminal failure message is malformed")
	}
	failure := &transitioncost.Failure{ObservedAt: receivedAt, Message: status.TerminalFailure.Message}
	switch status.TerminalFailure.Stage {
	case edgev1alpha1.NodeActuationFailureStageApply:
		if raw.TargetReadBack != nil {
			return errors.New("apply failure contradicts a retained target read-back")
		}
		if raw.ActuationFailure == nil {
			raw.ActuationFailure = failure
		}
	case edgev1alpha1.NodeActuationFailureStageVerify:
		if raw.TargetReadBack != nil {
			return errors.New("verify failure contradicts a retained target read-back")
		}
		if raw.TargetReadBackFailure == nil {
			raw.TargetReadBackFailure = failure
		}
	case edgev1alpha1.NodeActuationFailureStageSettle:
		if raw.TargetReadBack == nil {
			return errors.New("settling failure has no retained target read-back")
		}
	default:
		return fmt.Errorf("unsupported NodeActuation terminal failure stage %q", status.TerminalFailure.Stage)
	}
	return nil
}

func projectReadBack(
	observation edgev1alpha1.NodeActuationObservation,
	deviceType string,
	model string,
	receivedAt time.Time,
) (transitioncost.StateReadBack, error) {
	if observation.ObservedAt.IsZero() || observation.PowerMode == "" ||
		strings.TrimSpace(observation.PowerMode) != observation.PowerMode {
		return transitioncost.StateReadBack{}, errors.New("read-back timestamp or power mode is malformed")
	}
	return transitioncost.StateReadBack{
		Fact: transitioncost.MappedFact{
			OccurredAt: observation.ObservedAt.Time,
			MappedAt:   receivedAt,
		},
		State: transitioncost.StateIdentity{
			DeviceType: deviceType,
			Model:      model,
			PowerMode:  observation.PowerMode,
		},
	}, nil
}
