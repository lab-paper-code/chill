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
	receivedAt time.Time
	process    *transitioncost.ProcessIdentity
}

func projectServingPod(pod corev1.Pod, containerName string, receivedAt time.Time) servingSnapshot {
	snapshot := servingSnapshot{receivedAt: receivedAt}
	if !podReady(pod.Status.Conditions) {
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

func podReady(conditions []corev1.PodCondition) bool {
	for _, condition := range conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
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
	if raw.TransitionStart == nil {
		if status.ApplyStartedAt == nil {
			markAbsent(&raw.MarkerBrackets.ApplyStartedAt, receivedAt)
		} else {
			if status.ApplyStartedAt.IsZero() {
				return errors.New("NodeActuation apply-start timestamp is zero")
			}
			if err := markPresent(&raw.MarkerBrackets.ApplyStartedAt, receivedAt); err != nil {
				return fmt.Errorf("bracket NodeActuation apply-start: %w", err)
			}
			raw.TransitionStart = &transitioncost.OccurredFact{
				OccurredAt: status.ApplyStartedAt.Time,
			}
		}
	}
	if raw.TargetReadBack == nil {
		if status.TargetObservation == nil {
			markAbsent(&raw.MarkerBrackets.TargetObservation, receivedAt)
		} else {
			readBack, err := projectReadBack(*status.TargetObservation, deviceType, model, receivedAt)
			if err != nil {
				return fmt.Errorf("project target NodeActuation observation: %w", err)
			}
			if err := markPresent(&raw.MarkerBrackets.TargetObservation, receivedAt); err != nil {
				return fmt.Errorf("bracket NodeActuation target observation: %w", err)
			}
			raw.TargetReadBack = &transitioncost.TargetStateReadBack{
				Fact: transitioncost.OccurredFact{
					OccurredAt: readBack.Fact.OccurredAt,
				},
				State: readBack.State,
			}
		}
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

func markAbsent(bracket *transitioncost.ReceiptTimeBracket, receivedAt time.Time) {
	if bracket.FirstPresentReceivedAt.IsZero() {
		bracket.LastAbsentReceivedAt = receivedAt
	}
}

func markPresent(bracket *transitioncost.ReceiptTimeBracket, receivedAt time.Time) error {
	if !bracket.FirstPresentReceivedAt.IsZero() {
		return nil
	}
	if bracket.LastAbsentReceivedAt.IsZero() {
		return errors.New("last-absent receipt time is missing")
	}
	if !receivedAt.After(bracket.LastAbsentReceivedAt) {
		return errors.New("first-present receipt time does not follow last-absent receipt time")
	}
	bracket.FirstPresentReceivedAt = receivedAt
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
