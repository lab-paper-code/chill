package system

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

const (
	reasonNodeDiscoveryDisabled    = "NodeDiscoveryDisabled"
	reasonNodeDiscoveryReady       = "NodeDiscoveryReady"
	reasonNodeDiscoveryMissing     = "NodeDiscoveryMissing"
	reasonNodeDiscoveryPending     = "NodeDiscoveryPending"
	reasonNodeDiscoveryProgressing = "NodeDiscoveryProgressing"
	reasonNodeDiscoveryDegraded    = "NodeDiscoveryDegraded"
	reasonObservationFailed        = "ObservationFailed"

	daemonSetReplicaFailureConditionType = appsv1.DaemonSetConditionType("ReplicaFailure")
)

// Observation contains the Kubernetes state needed to report ChillSystem readiness.
type Observation struct {
	ObservedGeneration int64

	Namespace string

	NodeDiscoveryEnabled       bool
	NodeDiscoveryDaemonSetName string
	NodeDiscoveryDaemonSet     *appsv1.DaemonSet
	NodeDiscoveryError         error
}

func buildStatus(observed Observation, previousConditions []metav1.Condition, now metav1.Time) edgev1alpha1.ChillSystemStatus {
	status := edgev1alpha1.ChillSystemStatus{
		ObservedGeneration: observed.ObservedGeneration,
	}
	if previous := meta.FindStatusCondition(previousConditions, edgev1alpha1.ChillSystemConditionReady); previous != nil {
		status.Conditions = append(status.Conditions, *previous)
	}

	condition := nodeDiscoveryReadyCondition(observed)
	condition.Type = edgev1alpha1.ChillSystemConditionReady
	condition.ObservedGeneration = observed.ObservedGeneration
	condition.LastTransitionTime = now
	condition.Reason = truncateStatusText(condition.Reason, edgev1alpha1.ChillSystemReasonMaxLength)
	condition.Message = truncateStatusText(condition.Message, edgev1alpha1.ChillSystemMessageMaxLength)
	meta.SetStatusCondition(&status.Conditions, condition)

	return status
}

func nodeDiscoveryReadyCondition(observed Observation) metav1.Condition {
	if !observed.NodeDiscoveryEnabled {
		return metav1.Condition{
			Status:  metav1.ConditionTrue,
			Reason:  reasonNodeDiscoveryDisabled,
			Message: "node-discovery is disabled",
		}
	}
	if observed.NodeDiscoveryError != nil {
		return metav1.Condition{
			Status:  metav1.ConditionUnknown,
			Reason:  reasonObservationFailed,
			Message: statusMessage(observed.NodeDiscoveryError.Error(), "failed to observe node-discovery DaemonSet"),
		}
	}
	if observed.NodeDiscoveryDaemonSet == nil {
		return metav1.Condition{
			Status:  metav1.ConditionFalse,
			Reason:  reasonNodeDiscoveryMissing,
			Message: fmt.Sprintf("node-discovery DaemonSet %q is missing", observed.NodeDiscoveryDaemonSetName),
		}
	}

	daemonSet := observed.NodeDiscoveryDaemonSet
	if condition := daemonSetFailureCondition(daemonSet); condition != nil {
		return metav1.Condition{
			Status:  metav1.ConditionFalse,
			Reason:  reasonNodeDiscoveryDegraded,
			Message: statusMessage(condition.Message, fmt.Sprintf("node-discovery DaemonSet %q is degraded", daemonSet.Name)),
		}
	}
	if daemonSet.Status.DesiredNumberScheduled == 0 {
		return metav1.Condition{
			Status:  metav1.ConditionFalse,
			Reason:  reasonNodeDiscoveryPending,
			Message: fmt.Sprintf("node-discovery DaemonSet %q has no scheduled Nodes", daemonSet.Name),
		}
	}
	if daemonSet.Status.NumberReady < daemonSet.Status.DesiredNumberScheduled {
		return metav1.Condition{
			Status: metav1.ConditionFalse,
			Reason: reasonNodeDiscoveryProgressing,
			Message: fmt.Sprintf(
				"node-discovery DaemonSet %q is Ready on %d/%d Nodes",
				daemonSet.Name,
				daemonSet.Status.NumberReady,
				daemonSet.Status.DesiredNumberScheduled,
			),
		}
	}

	return metav1.Condition{
		Status:  metav1.ConditionTrue,
		Reason:  reasonNodeDiscoveryReady,
		Message: fmt.Sprintf("node-discovery DaemonSet %q is Ready", daemonSet.Name),
	}
}

func daemonSetFailureCondition(daemonSet *appsv1.DaemonSet) *appsv1.DaemonSetCondition {
	for i := range daemonSet.Status.Conditions {
		condition := &daemonSet.Status.Conditions[i]
		if condition.Type == daemonSetReplicaFailureConditionType && condition.Status == corev1.ConditionTrue {
			return condition
		}
	}
	return nil
}

func statusMessage(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return truncateStatusText(value, edgev1alpha1.ChillSystemMessageMaxLength)
}

func truncateStatusText(value string, maxLength int) string {
	runes := []rune(value)
	if len(runes) <= maxLength {
		return value
	}
	suffix := []rune("...")
	if maxLength <= len(suffix) {
		return string(runes[:maxLength])
	}
	return string(runes[:maxLength-len(suffix)]) + string(suffix)
}
