package kube

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metadata"
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

// projectNode retains the facts the Node's actuation agent published about
// the transition to the target mode. It reports whether the agent failed it.
func projectNode(raw *transitioncost.RawObservation, node *corev1.Node, request Request, receivedAt time.Time) (bool, error) {
	generation, agent, err := metadata.NodePowerModeGeneration(node)
	if err != nil {
		return false, err
	}
	if agent {
		if raw.Generation == nil {
			raw.Generation = &transitioncost.PowerModeGeneration{Start: generation}
		}
		raw.Generation.End = generation
	}
	if raw.SourceReadBack.Fact.MappedAt.IsZero() {
		state, err := readBackState(node, request)
		if err != nil {
			return false, err
		}
		// The read-back annotation carries no host time: its receipt is its time.
		raw.SourceReadBack = transitioncost.StateReadBack{
			Fact:  transitioncost.MappedFact{OccurredAt: receivedAt, MappedAt: receivedAt},
			State: state,
		}
	}
	transition, _, err := metadata.NodePowerModeTransition(node)
	if err != nil {
		return false, err
	}
	if node.Labels[metadata.PowerMode] != request.TargetPowerMode || transition.PowerMode != request.TargetPowerMode {
		// The agent has not taken up the target mode yet.
		transition = metadata.PowerModeTransitionRecord{}
	}
	if raw.TransitionStart == nil {
		if transition.ApplyStartedAt == nil {
			markAbsent(&raw.MarkerBrackets.ApplyStartedAt, receivedAt)
		} else {
			if err := markPresent(&raw.MarkerBrackets.ApplyStartedAt, receivedAt); err != nil {
				return false, fmt.Errorf("bracket Node power-mode apply-start: %w", err)
			}
			raw.TransitionStart = &transitioncost.OccurredFact{OccurredAt: *transition.ApplyStartedAt}
		}
	}
	if raw.TargetReadBack == nil {
		if transition.VerifiedAt == nil {
			markAbsent(&raw.MarkerBrackets.TargetObservation, receivedAt)
		} else {
			state, err := readBackState(node, request)
			if err != nil {
				return false, err
			}
			if err := markPresent(&raw.MarkerBrackets.TargetObservation, receivedAt); err != nil {
				return false, fmt.Errorf("bracket Node power-mode target read-back: %w", err)
			}
			raw.TargetReadBack = &transitioncost.TargetStateReadBack{
				Fact: transitioncost.OccurredFact{OccurredAt: *transition.VerifiedAt}, State: state,
			}
		}
	}
	// A failure after the target read-back leaves readiness to decide.
	if node.Labels[metadata.PowerModeState] != metadata.PowerModeFailed || transition.Failure == "" ||
		raw.TargetReadBack != nil {
		return false, nil
	}
	if raw.ActuationFailure == nil {
		raw.ActuationFailure = &transitioncost.Failure{ObservedAt: receivedAt, Message: transition.Failure}
	}
	return true, nil
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

// readBackState names the mode of the actuator the agent last read back.
func readBackState(node *corev1.Node, request Request) (transitioncost.StateIdentity, error) {
	var actuator edgev1alpha1.PowerModeActuator
	if err := json.Unmarshal([]byte(node.Annotations[metadata.PowerActuator]), &actuator); err != nil {
		return transitioncost.StateIdentity{}, fmt.Errorf("read Node %s power read-back: %w", node.Name, err)
	}
	state := transitioncost.StateIdentity{DeviceType: request.DeviceType, Model: request.Model}
	switch {
	case reflect.DeepEqual(actuator, request.SourceActuator):
		state.PowerMode = request.SourcePowerMode
	case reflect.DeepEqual(actuator, request.TargetActuator):
		state.PowerMode = request.TargetPowerMode
	default:
		return transitioncost.StateIdentity{}, fmt.Errorf(
			"Node %s power read-back %s is neither the source nor the target mode",
			node.Name, node.Annotations[metadata.PowerActuator])
	}
	return state, nil
}
