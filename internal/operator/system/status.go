package system

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

const (
	reasonComponentsAvailable = "ComponentsAvailable"
	reasonComponentsDisabled  = "ComponentsDisabled"
	reasonComponentsNotReady  = "ComponentsNotReady"
	reasonObservationFailed   = "ObservationFailed"

	reasonNodeDiscoveryReady       = "NodeDiscoveryReady"
	reasonNodeDiscoveryMissing     = "NodeDiscoveryMissing"
	reasonNodeDiscoveryPending     = "NodeDiscoveryPending"
	reasonNodeDiscoveryProgressing = "NodeDiscoveryProgressing"
	reasonNodeDiscoveryDegraded    = "NodeDiscoveryDegraded"

	reasonProfilerReady       = "ProfilerReady"
	reasonProfilerMissing     = "ProfilerMissing"
	reasonProfilerProgressing = "ProfilerProgressing"
	reasonProfilerDegraded    = "ProfilerDegraded"

	reasonNodeActuatorReady       = "NodeActuatorReady"
	reasonNodeActuatorMissing     = "NodeActuatorMissing"
	reasonNodeActuatorPending     = "NodeActuatorPending"
	reasonNodeActuatorProgressing = "NodeActuatorProgressing"
	reasonNodeActuatorDegraded    = "NodeActuatorDegraded"

	reasonEvidenceStoreReady       = "EvidenceStoreReady"
	reasonEvidenceStoreMissing     = "EvidenceStoreMissing"
	reasonEvidenceStoreProgressing = "EvidenceStoreProgressing"
	reasonEvidenceStoreDegraded    = "EvidenceStoreDegraded"

	daemonSetReplicaFailureConditionType = appsv1.DaemonSetConditionType("ReplicaFailure")
	deploymentProgressDeadlineReason     = "ProgressDeadlineExceeded"
)

// Observation contains the Kubernetes state needed to report ChillSystem readiness.
type Observation struct {
	ObservedGeneration int64

	Namespace string

	NodeDiscoveryEnabled       bool
	NodeDiscoveryDaemonSetName string
	NodeDiscoveryDaemonSet     *appsv1.DaemonSet
	NodeDiscoveryError         error

	ProfilerEnabled        bool
	ProfilerNamespace      string
	ProfilerDeploymentName string
	ProfilerDeployment     *appsv1.Deployment
	ProfilerError          error

	NodeActuatorEnabled       bool
	NodeActuatorNamespace     string
	NodeActuatorDaemonSetName string
	NodeActuatorDaemonSet     *appsv1.DaemonSet
	NodeActuatorError         error

	EvidenceStoreEnabled          bool
	EvidenceStoreNamespace        string
	EvidenceStoreDeploymentName   string
	EvidenceStoreDeployment       *appsv1.Deployment
	EvidenceStoreBootstrapJobName string
	EvidenceStoreBootstrapJob     *batchv1.Job
	EvidenceStoreError            error
}

func buildStatus(observed Observation, previousConditions []metav1.Condition, now metav1.Time) edgev1alpha1.ChillSystemStatus {
	status := edgev1alpha1.ChillSystemStatus{ObservedGeneration: observed.ObservedGeneration}
	components := make([]metav1.Condition, 0, 4)
	if observed.NodeDiscoveryEnabled {
		condition := nodeDiscoveryAvailableCondition(observed)
		condition.Type = edgev1alpha1.ChillSystemConditionNodeDiscoveryAvailable
		components = append(components, condition)
	}
	if observed.ProfilerEnabled {
		condition := profilerAvailableCondition(observed)
		condition.Type = edgev1alpha1.ChillSystemConditionProfilerAvailable
		components = append(components, condition)
	}
	if observed.NodeActuatorEnabled {
		condition := nodeActuatorAvailableCondition(observed)
		condition.Type = edgev1alpha1.ChillSystemConditionNodeActuatorAvailable
		components = append(components, condition)
	}
	if observed.EvidenceStoreEnabled {
		condition := evidenceStoreAvailableCondition(observed)
		condition.Type = edgev1alpha1.ChillSystemConditionEvidenceStoreAvailable
		components = append(components, condition)
	}

	for _, condition := range append(components, readyCondition(components)) {
		condition.ObservedGeneration = observed.ObservedGeneration
		condition.LastTransitionTime = now
		condition.Reason = truncateStatusText(condition.Reason, edgev1alpha1.ChillSystemReasonMaxLength)
		condition.Message = truncateStatusText(condition.Message, edgev1alpha1.ChillSystemMessageMaxLength)
		if previous := meta.FindStatusCondition(previousConditions, condition.Type); previous != nil {
			status.Conditions = append(status.Conditions, *previous)
		}
		meta.SetStatusCondition(&status.Conditions, condition)
	}
	return status
}

func readyCondition(components []metav1.Condition) metav1.Condition {
	if len(components) == 0 {
		return metav1.Condition{
			Type:    edgev1alpha1.ChillSystemConditionReady,
			Status:  metav1.ConditionTrue,
			Reason:  reasonComponentsDisabled,
			Message: "all managed components are disabled",
		}
	}
	for _, condition := range components {
		if condition.Status == metav1.ConditionFalse {
			return metav1.Condition{
				Type:    edgev1alpha1.ChillSystemConditionReady,
				Status:  metav1.ConditionFalse,
				Reason:  reasonComponentsNotReady,
				Message: fmt.Sprintf("%s is not available", condition.Type),
			}
		}
	}
	for _, condition := range components {
		if condition.Status == metav1.ConditionUnknown {
			return metav1.Condition{
				Type:    edgev1alpha1.ChillSystemConditionReady,
				Status:  metav1.ConditionUnknown,
				Reason:  reasonObservationFailed,
				Message: fmt.Sprintf("%s availability could not be observed", condition.Type),
			}
		}
	}
	return metav1.Condition{
		Type:    edgev1alpha1.ChillSystemConditionReady,
		Status:  metav1.ConditionTrue,
		Reason:  reasonComponentsAvailable,
		Message: "all enabled components are available",
	}
}

func nodeDiscoveryAvailableCondition(observed Observation) metav1.Condition {
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
			Status:  metav1.ConditionFalse,
			Reason:  reasonNodeDiscoveryProgressing,
			Message: fmt.Sprintf("node-discovery DaemonSet %q is progressing", daemonSet.Name),
		}
	}
	return metav1.Condition{
		Status:  metav1.ConditionTrue,
		Reason:  reasonNodeDiscoveryReady,
		Message: fmt.Sprintf("node-discovery DaemonSet %q is available", daemonSet.Name),
	}
}

func profilerAvailableCondition(observed Observation) metav1.Condition {
	if observed.ProfilerError != nil {
		return metav1.Condition{
			Status:  metav1.ConditionUnknown,
			Reason:  reasonObservationFailed,
			Message: statusMessage(observed.ProfilerError.Error(), "failed to observe Profiler Deployment"),
		}
	}
	if observed.ProfilerDeployment == nil {
		return metav1.Condition{
			Status:  metav1.ConditionFalse,
			Reason:  reasonProfilerMissing,
			Message: fmt.Sprintf("Profiler Deployment %q is missing", observed.ProfilerDeploymentName),
		}
	}

	deployment := observed.ProfilerDeployment
	if condition := deploymentFailureCondition(deployment); condition != nil {
		return metav1.Condition{
			Status:  metav1.ConditionFalse,
			Reason:  reasonProfilerDegraded,
			Message: statusMessage(condition.Message, fmt.Sprintf("Profiler Deployment %q is degraded", deployment.Name)),
		}
	}
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	if deployment.Status.ObservedGeneration < deployment.Generation ||
		deployment.Status.UpdatedReplicas < desired ||
		deployment.Status.AvailableReplicas < desired {
		return metav1.Condition{
			Status:  metav1.ConditionFalse,
			Reason:  reasonProfilerProgressing,
			Message: fmt.Sprintf("Profiler Deployment %q is progressing", deployment.Name),
		}
	}
	return metav1.Condition{
		Status:  metav1.ConditionTrue,
		Reason:  reasonProfilerReady,
		Message: fmt.Sprintf("Profiler Deployment %q is available", deployment.Name),
	}
}

func nodeActuatorAvailableCondition(observed Observation) metav1.Condition {
	if observed.NodeActuatorError != nil {
		return metav1.Condition{
			Status: metav1.ConditionUnknown, Reason: reasonObservationFailed,
			Message: statusMessage(observed.NodeActuatorError.Error(), "failed to observe NodeActuator DaemonSet"),
		}
	}
	if observed.NodeActuatorDaemonSet == nil {
		return metav1.Condition{
			Status: metav1.ConditionFalse, Reason: reasonNodeActuatorMissing,
			Message: fmt.Sprintf("NodeActuator DaemonSet %q is missing", observed.NodeActuatorDaemonSetName),
		}
	}
	daemonSet := observed.NodeActuatorDaemonSet
	if condition := daemonSetFailureCondition(daemonSet); condition != nil {
		return metav1.Condition{
			Status: metav1.ConditionFalse, Reason: reasonNodeActuatorDegraded,
			Message: statusMessage(condition.Message, fmt.Sprintf("NodeActuator DaemonSet %q is degraded", daemonSet.Name)),
		}
	}
	if daemonSet.Status.DesiredNumberScheduled == 0 {
		return metav1.Condition{
			Status: metav1.ConditionFalse, Reason: reasonNodeActuatorPending,
			Message: fmt.Sprintf("NodeActuator DaemonSet %q has no eligible Nodes", daemonSet.Name),
		}
	}
	if daemonSet.Status.NumberReady < daemonSet.Status.DesiredNumberScheduled {
		return metav1.Condition{
			Status: metav1.ConditionFalse, Reason: reasonNodeActuatorProgressing,
			Message: fmt.Sprintf("NodeActuator DaemonSet %q is progressing", daemonSet.Name),
		}
	}
	return metav1.Condition{
		Status: metav1.ConditionTrue, Reason: reasonNodeActuatorReady,
		Message: fmt.Sprintf("NodeActuator DaemonSet %q is available", daemonSet.Name),
	}
}

func evidenceStoreAvailableCondition(observed Observation) metav1.Condition {
	if observed.EvidenceStoreError != nil {
		return metav1.Condition{
			Status:  metav1.ConditionUnknown,
			Reason:  reasonObservationFailed,
			Message: statusMessage(observed.EvidenceStoreError.Error(), "failed to observe Evidence Store"),
		}
	}
	if observed.EvidenceStoreDeployment == nil {
		return metav1.Condition{
			Status:  metav1.ConditionFalse,
			Reason:  reasonEvidenceStoreMissing,
			Message: fmt.Sprintf("Evidence Store Deployment %q is missing", observed.EvidenceStoreDeploymentName),
		}
	}

	deployment := observed.EvidenceStoreDeployment
	if condition := deploymentFailureCondition(deployment); condition != nil {
		return metav1.Condition{
			Status:  metav1.ConditionFalse,
			Reason:  reasonEvidenceStoreDegraded,
			Message: statusMessage(condition.Message, fmt.Sprintf("Evidence Store Deployment %q is degraded", deployment.Name)),
		}
	}
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	if deployment.Status.ObservedGeneration < deployment.Generation ||
		deployment.Status.UpdatedReplicas < desired ||
		deployment.Status.AvailableReplicas < desired {
		return metav1.Condition{
			Status:  metav1.ConditionFalse,
			Reason:  reasonEvidenceStoreProgressing,
			Message: fmt.Sprintf("Evidence Store Deployment %q is progressing", deployment.Name),
		}
	}

	job := observed.EvidenceStoreBootstrapJob
	if job == nil {
		return metav1.Condition{
			Status:  metav1.ConditionFalse,
			Reason:  reasonEvidenceStoreProgressing,
			Message: fmt.Sprintf("Evidence Store bootstrap Job %q is pending", observed.EvidenceStoreBootstrapJobName),
		}
	}
	if condition := jobCondition(job, batchv1.JobFailed); condition != nil {
		return metav1.Condition{
			Status:  metav1.ConditionFalse,
			Reason:  reasonEvidenceStoreDegraded,
			Message: statusMessage(condition.Message, fmt.Sprintf("Evidence Store bootstrap Job %q failed", job.Name)),
		}
	}
	if jobCondition(job, batchv1.JobComplete) == nil {
		return metav1.Condition{
			Status:  metav1.ConditionFalse,
			Reason:  reasonEvidenceStoreProgressing,
			Message: fmt.Sprintf("Evidence Store bootstrap Job %q is progressing", job.Name),
		}
	}
	return metav1.Condition{
		Status:  metav1.ConditionTrue,
		Reason:  reasonEvidenceStoreReady,
		Message: fmt.Sprintf("Evidence Store Deployment %q is available and bootstrap succeeded", deployment.Name),
	}
}

func jobCondition(job *batchv1.Job, conditionType batchv1.JobConditionType) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		condition := &job.Status.Conditions[i]
		if condition.Type == conditionType && condition.Status == corev1.ConditionTrue {
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

func deploymentFailureCondition(deployment *appsv1.Deployment) *appsv1.DeploymentCondition {
	for i := range deployment.Status.Conditions {
		condition := &deployment.Status.Conditions[i]
		replicaFailure := condition.Type == appsv1.DeploymentReplicaFailure && condition.Status == corev1.ConditionTrue
		progressDeadlineExceeded := condition.Type == appsv1.DeploymentProgressing &&
			condition.Status == corev1.ConditionFalse &&
			condition.Reason == deploymentProgressDeadlineReason
		if replicaFailure || progressDeadlineExceeded {
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
