package systemcontroller

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
	ComponentController    = "controller"
	ComponentNodeDiscovery = "node-discovery"

	reasonReady              = "Ready"
	reasonReconciling        = "Reconciling"
	reasonDisabled           = "Disabled"
	reasonMissing            = "Missing"
	reasonUnavailable        = "Unavailable"
	reasonObservationFailed  = "ObservationFailed"
	reasonNodeDiscoveryReady = "NodeDiscoveryReady"

	deploymentTimedOutReason             = "ProgressDeadlineExceeded"
	daemonSetReplicaFailureConditionType = appsv1.DaemonSetConditionType("ReplicaFailure")
)

// Observation contains the raw Kubernetes state used to build ChillSystem status.
type Observation struct {
	ObservedGeneration int64

	Namespace string

	ControllerDeploymentName string
	ControllerDeployment     *appsv1.Deployment
	ControllerError          error

	NodeDiscoveryEnabled       bool
	NodeDiscoveryDaemonSetName string
	NodeDiscoveryDaemonSet     *appsv1.DaemonSet
	NodeDiscoveryError         error

	DeviceClassCount  *int32
	DeviceClassError  error
	ObservedNodeCount *int32
	NodeError         error
}

func buildStatus(observed Observation, previousConditions []metav1.Condition, now metav1.Time) edgev1alpha1.ChillSystemStatus {
	conditions := append([]metav1.Condition(nil), previousConditions...)
	controller := deploymentComponentStatus(
		ComponentController,
		observed.Namespace,
		observed.ControllerDeploymentName,
		observed.ControllerDeployment,
		observed.ControllerError,
	)
	nodeDiscovery := daemonSetComponentStatus(
		ComponentNodeDiscovery,
		observed.Namespace,
		observed.NodeDiscoveryDaemonSetName,
		observed.NodeDiscoveryEnabled,
		observed.NodeDiscoveryDaemonSet,
		observed.NodeDiscoveryError,
	)

	status := edgev1alpha1.ChillSystemStatus{
		ObservedGeneration: observed.ObservedGeneration,
		ControllerState:    controller.State,
		NodeDiscoveryState: nodeDiscovery.State,
		DeviceClassCount:   observed.DeviceClassCount,
		ObservedNodeCount:  observed.ObservedNodeCount,
		Components:         []edgev1alpha1.ChillComponentStatus{controller, nodeDiscovery},
		Conditions:         conditions,
	}

	phase, reason, message := summarize(observed, controller, nodeDiscovery)
	reason = truncateStatusText(reason, edgev1alpha1.ChillSystemReasonMaxLength)
	message = truncateStatusText(message, edgev1alpha1.ChillSystemMessageMaxLength)
	status.Phase = phase
	status.Message = message
	status.Ready = conditionStatus(phase == edgev1alpha1.ChillSystemPhaseReady)

	setCondition(&status, metav1.Condition{
		Type:               edgev1alpha1.ChillSystemConditionReady,
		Status:             status.Ready,
		ObservedGeneration: observed.ObservedGeneration,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	})
	setCondition(&status, metav1.Condition{
		Type:               edgev1alpha1.ChillSystemConditionProgressing,
		Status:             conditionStatus(phase == edgev1alpha1.ChillSystemPhaseProgressing),
		ObservedGeneration: observed.ObservedGeneration,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	})
	setCondition(&status, metav1.Condition{
		Type:               edgev1alpha1.ChillSystemConditionDegraded,
		Status:             conditionStatus(phase == edgev1alpha1.ChillSystemPhaseDegraded),
		ObservedGeneration: observed.ObservedGeneration,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	})

	return status
}

func deploymentComponentStatus(name, namespace, workloadName string, deployment *appsv1.Deployment, err error) edgev1alpha1.ChillComponentStatus {
	component := edgev1alpha1.ChillComponentStatus{
		Name:      name,
		Kind:      "Deployment",
		Namespace: namespace,
		State:     edgev1alpha1.ComponentStateUnknown,
	}
	if err != nil {
		component.State = edgev1alpha1.ComponentStateUnknown
		component.Reason = reasonObservationFailed
		component.Message = statusMessage(err.Error(), "failed to observe Deployment")
		return component
	}
	if deployment == nil {
		component.State = edgev1alpha1.ComponentStateDegraded
		component.Reason = reasonMissing
		component.Message = fmt.Sprintf("Deployment %q is missing", workloadName)
		return component
	}

	component.Desired = deploymentDesiredReplicas(deployment)
	component.Ready = deployment.Status.ReadyReplicas
	component.Available = deployment.Status.AvailableReplicas
	if component.Desired == 0 {
		component.State = edgev1alpha1.ComponentStateDisabled
		component.Reason = reasonDisabled
		component.Message = fmt.Sprintf("Deployment %q is scaled to zero", deployment.Name)
		return component
	}
	if condition := deploymentFailureCondition(deployment); condition != nil {
		component.State = edgev1alpha1.ComponentStateDegraded
		component.Reason = statusReason(condition.Reason, reasonUnavailable)
		component.Message = statusMessage(condition.Message, fmt.Sprintf("Deployment %q is degraded", deployment.Name))
		return component
	}
	if deployment.Status.AvailableReplicas >= component.Desired {
		component.State = edgev1alpha1.ComponentStateReady
		component.Reason = reasonReady
		component.Message = fmt.Sprintf("Deployment %q is available", deployment.Name)
		return component
	}
	component.State = edgev1alpha1.ComponentStateProgressing
	component.Reason = reasonUnavailable
	component.Message = fmt.Sprintf("Deployment %q has %d/%d available replicas", deployment.Name, deployment.Status.AvailableReplicas, component.Desired)
	return component
}

func daemonSetComponentStatus(name, namespace, workloadName string, enabled bool, daemonSet *appsv1.DaemonSet, err error) edgev1alpha1.ChillComponentStatus {
	component := edgev1alpha1.ChillComponentStatus{
		Name:      name,
		Kind:      "DaemonSet",
		Namespace: namespace,
		State:     edgev1alpha1.ComponentStateUnknown,
	}
	if !enabled {
		component.State = edgev1alpha1.ComponentStateDisabled
		component.Reason = reasonDisabled
		component.Message = "node-discovery is disabled"
		return component
	}
	if err != nil {
		component.State = edgev1alpha1.ComponentStateUnknown
		component.Reason = reasonObservationFailed
		component.Message = statusMessage(err.Error(), "failed to observe DaemonSet")
		return component
	}
	if daemonSet == nil {
		component.State = edgev1alpha1.ComponentStateDegraded
		component.Reason = reasonMissing
		component.Message = fmt.Sprintf("DaemonSet %q is missing", workloadName)
		return component
	}

	component.Desired = daemonSet.Status.DesiredNumberScheduled
	component.Ready = daemonSet.Status.NumberReady
	component.Available = daemonSet.Status.NumberAvailable
	if daemonSet.Status.DesiredNumberScheduled == 0 {
		component.State = edgev1alpha1.ComponentStateProgressing
		component.Reason = reasonReconciling
		component.Message = fmt.Sprintf("DaemonSet %q has no scheduled nodes yet", daemonSet.Name)
		return component
	}
	if condition := daemonSetFailureCondition(daemonSet); condition != nil {
		component.State = edgev1alpha1.ComponentStateDegraded
		component.Reason = statusReason(condition.Reason, reasonUnavailable)
		component.Message = statusMessage(condition.Message, fmt.Sprintf("DaemonSet %q is degraded", daemonSet.Name))
		return component
	}
	if daemonSet.Status.NumberReady >= daemonSet.Status.DesiredNumberScheduled {
		component.State = edgev1alpha1.ComponentStateReady
		component.Reason = reasonNodeDiscoveryReady
		component.Message = fmt.Sprintf("DaemonSet %q is ready on %d nodes", daemonSet.Name, daemonSet.Status.NumberReady)
		return component
	}
	component.State = edgev1alpha1.ComponentStateProgressing
	component.Reason = reasonUnavailable
	component.Message = fmt.Sprintf("DaemonSet %q is ready on %d/%d nodes", daemonSet.Name, daemonSet.Status.NumberReady, daemonSet.Status.DesiredNumberScheduled)
	return component
}

func deploymentFailureCondition(deployment *appsv1.Deployment) *appsv1.DeploymentCondition {
	for i := range deployment.Status.Conditions {
		condition := &deployment.Status.Conditions[i]
		if condition.Type == appsv1.DeploymentReplicaFailure && condition.Status == corev1.ConditionTrue {
			return condition
		}
		if condition.Type == appsv1.DeploymentProgressing &&
			condition.Status == corev1.ConditionFalse &&
			condition.Reason == deploymentTimedOutReason {
			return condition
		}
	}
	return nil
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

func deploymentDesiredReplicas(deployment *appsv1.Deployment) int32 {
	if deployment.Spec.Replicas == nil {
		return 1
	}
	return *deployment.Spec.Replicas
}

func summarize(observed Observation, controller, nodeDiscovery edgev1alpha1.ChillComponentStatus) (edgev1alpha1.ChillSystemPhase, string, string) {
	if errors := observationErrors(observed); len(errors) > 0 {
		return edgev1alpha1.ChillSystemPhaseDegraded, reasonObservationFailed, strings.Join(errors, "; ")
	}
	if controller.State == edgev1alpha1.ComponentStateDegraded || controller.State == edgev1alpha1.ComponentStateUnknown {
		return edgev1alpha1.ChillSystemPhaseDegraded, controller.Reason, controller.Message
	}
	if controller.State == edgev1alpha1.ComponentStateProgressing {
		return edgev1alpha1.ChillSystemPhaseProgressing, controller.Reason, controller.Message
	}
	if controller.State == edgev1alpha1.ComponentStateDisabled {
		return edgev1alpha1.ChillSystemPhaseDegraded, controller.Reason, controller.Message
	}

	if nodeDiscovery.State == edgev1alpha1.ComponentStateDegraded || nodeDiscovery.State == edgev1alpha1.ComponentStateUnknown {
		return edgev1alpha1.ChillSystemPhaseDegraded, nodeDiscovery.Reason, nodeDiscovery.Message
	}
	if nodeDiscovery.State == edgev1alpha1.ComponentStateProgressing {
		return edgev1alpha1.ChillSystemPhaseProgressing, nodeDiscovery.Reason, nodeDiscovery.Message
	}
	if nodeDiscovery.State == edgev1alpha1.ComponentStateDisabled {
		return edgev1alpha1.ChillSystemPhaseReady, reasonReady, "CHILL controller is running; node-discovery is disabled"
	}
	return edgev1alpha1.ChillSystemPhaseReady, reasonReady, "CHILL controller and node-discovery are ready"
}

func observationErrors(observed Observation) []string {
	var errors []string
	for _, err := range []error{observed.DeviceClassError, observed.NodeError} {
		if err != nil {
			errors = append(errors, err.Error())
		}
	}
	return errors
}

func conditionStatus(ok bool) metav1.ConditionStatus {
	if ok {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func setCondition(status *edgev1alpha1.ChillSystemStatus, condition metav1.Condition) {
	meta.SetStatusCondition(&status.Conditions, condition)
}

func statusReason(value, fallback string) string {
	return truncateStatusText(statusText(value, fallback), edgev1alpha1.ChillSystemReasonMaxLength)
}

func statusMessage(value, fallback string) string {
	return truncateStatusText(statusText(value, fallback), edgev1alpha1.ChillSystemMessageMaxLength)
}

func statusText(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
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

func int32Ptr(value int32) *int32 {
	return &value
}
