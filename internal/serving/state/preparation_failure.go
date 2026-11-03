package state

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"strings"

	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const podEvictedReason = "Evicted"

// PreparationPodError retains the exact status read that ended the prepare;
// a canceled final capture must not erase that observation.
type PreparationPodError struct {
	Pod    *core.Pod
	Reason string
}

func (e *PreparationPodError) Error() string { return e.Reason }

// ClassifyPreparationFailure is the one startup classification policy. A
// process exiting after launch (including cgroup OOM) or an authoritative
// artifact/permission refusal is deterministic. Delivery, unavailable nodes
// and externally reclaimed Pods are environmental. Unrecognized evidence is
// retryable, not guessed to be a physical incompatibility.
//
//nolint:gocyclo // Keep the explicitly required single, ordered startup classification policy together.
func ClassifyPreparationFailure(pod *core.Pod, node *core.Node, events []core.Event, err error, preparing bool) (serving.PreparationFailureObservation, bool) {
	const nodeUnavailable = "node-unavailable"
	if errors.Is(err, context.Canceled) {
		return serving.PreparationFailureObservation{}, false
	}
	observation := serving.PreparationFailureObservation{Classification: serving.PreparationUnknownTransient}
	if err != nil {
		observation.Reason, observation.ReasonKey = err.Error(), "unknown:"+err.Error()
	}
	permissionRefused := errors.Is(err, fs.ErrPermission) || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err)
	var unavailable bool
	if node != nil {
		for _, condition := range node.Status.Conditions {
			if condition.Type == core.NodeReady {
				observation.NodeReadyTransition = condition.LastTransitionTime.Time
				unavailable = condition.Status != core.ConditionTrue
				if unavailable {
					observation.Reason = fmt.Sprintf("%s; NodeReady=%s: %s", observation.Reason, condition.Status, condition.Reason)
				}
			}
		}
	}
	if pod != nil {
		reclaimed := pod.Status.Reason == podEvictedReason || strings.HasPrefix(pod.Status.Reason, "Preempt")
		reclamationReason := pod.Status.Reason
		for _, condition := range pod.Status.Conditions {
			if condition.Type == core.DisruptionTarget && condition.Status == core.ConditionTrue {
				reclaimed = true
				if reclamationReason == "" {
					reclamationReason = condition.Reason
				}
			}
		}
		for _, event := range events {
			if event.Reason == podEvictedReason || strings.HasPrefix(event.Reason, "Preempt") {
				reclaimed = true
				if reclamationReason == "" {
					reclamationReason = event.Reason
				}
			}
		}
		if reclaimed {
			observation.Classification, observation.ReasonKey = serving.PreparationTransient, "external-reclamation:"+reclamationReason
			observation.Reason = fmt.Sprintf("Pod %s: %s %s", pod.UID, reclamationReason, pod.Status.Message)
			return observation, true
		}
		for _, statuses := range [][]core.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses} {
			for _, status := range statuses {
				terminal := status.State.Terminated
				if terminal == nil && status.State.Running == nil {
					terminal = status.LastTerminationState.Terminated
				}
				if terminal != nil && (terminal.ExitCode != 0 || preparing && status.Name == FirstfloorContainerName) {
					observation.Reason = fmt.Sprintf("%s: %s (exit %d) %s", status.Name, terminal.Reason, terminal.ExitCode, strings.TrimSpace(terminal.Message))
					observation.ReasonKey = fmt.Sprintf("process-exit:%s:%s:%d", status.Name, terminal.Reason, terminal.ExitCode)
					if pod.DeletionTimestamp != nil && (terminal.FinishedAt.IsZero() || !terminal.FinishedAt.Time.Before(pod.DeletionTimestamp.Time)) {
						observation.Classification, observation.ReasonKey = serving.PreparationTransient, "external-reclamation:deleting"
					} else if unavailable && terminal.Reason != "OOMKilled" {
						observation.Classification, observation.ReasonKey = serving.PreparationTransient, nodeUnavailable
					} else if !terminal.StartedAt.IsZero() || terminal.Reason == "OOMKilled" {
						observation.Classification = serving.PreparationDeterministic
					} else {
						// A container identity alone is not proof exec started.
						// Before exec, only an authoritative filesystem refusal
						// makes this physical startup input deterministic.
						refusal := strings.ToLower(terminal.Message)
						if permissionRefused || strings.Contains(refusal, "permission denied") || strings.Contains(refusal, "no such file or directory") {
							observation.Classification, observation.ReasonKey = serving.PreparationDeterministic, "execution-refused:"+terminal.Reason
						} else {
							observation.Classification, observation.ReasonKey = serving.PreparationTransient, "before-process:"+terminal.Reason
						}
					}
					return observation, true
				}
				if waiting := status.State.Waiting; waiting != nil && waiting.Reason != "" && waiting.Reason != "ContainerCreating" && waiting.Reason != "PodInitializing" {
					observation.Classification, observation.ReasonKey = serving.PreparationTransient, "delivery:"+waiting.Reason
					var message strings.Builder
					message.WriteString(waiting.Message)
					for _, event := range events {
						message.WriteByte('\n')
						message.WriteString(event.Reason)
						message.WriteString(": ")
						message.WriteString(event.Message)
					}
					observation.Reason = fmt.Sprintf("%s: %s %s", status.Name, waiting.Reason, strings.TrimSpace(message.String()))
					// These are registry/gRPC protocol refusals, not an image,
					// workload, board, or free-form failure-message allowlist.
					protocol := strings.ToLower(message.String())
					if waiting.Reason == "InvalidImageName" || strings.Contains(protocol, "manifest unknown") || strings.Contains(protocol, "manifest_unknown") ||
						strings.Contains(protocol, "name_unknown") || strings.Contains(protocol, "404 not found") || strings.Contains(protocol, "403 forbidden") ||
						strings.Contains(protocol, "code = notfound") || strings.Contains(protocol, "code = permissiondenied") ||
						strings.Contains(protocol, "code = unauthenticated") || strings.Contains(protocol, "unauthorized") || strings.Contains(protocol, "denied") {
						observation.Classification, observation.ReasonKey = serving.PreparationDeterministic, "artifact-or-permission:"+waiting.Reason
					}
					return observation, true
				}
			}
		}
		if pod.Status.Phase == core.PodFailed {
			observation.ReasonKey = "pod-failed:" + pod.Status.Reason
			observation.Reason = fmt.Sprintf("Pod failed: %s %s", pod.Status.Reason, pod.Status.Message)
			if unavailable {
				observation.Classification, observation.ReasonKey = serving.PreparationTransient, nodeUnavailable
			}
			return observation, true
		}
	}
	if unavailable {
		observation.Classification, observation.ReasonKey = serving.PreparationTransient, nodeUnavailable
		return observation, true
	}
	if permissionRefused {
		observation.Classification, observation.ReasonKey = serving.PreparationDeterministic, "permission-refused"
		return observation, true
	}
	var network net.Error
	if errors.As(err, &network) || errors.Is(err, context.DeadlineExceeded) || apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) || apierrors.IsServiceUnavailable(err) || apierrors.IsTooManyRequests(err) {
		observation.Classification, observation.ReasonKey = serving.PreparationTransient, "environment-transport"
		return observation, true
	}
	if err == nil {
		return serving.PreparationFailureObservation{}, false
	}
	return observation, true
}
