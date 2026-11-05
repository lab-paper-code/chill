package state

import (
	"context"
	"encoding/json"
	"errors"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ServingProfileAnnotation = "edge.dacs.io/serving-profile"
)

const (
	ServingWeightAnnotation = "edge.dacs.io/serving-weight"
)

const (
	// ServingRoutingAnnotation holds the last applied weight per Workload UID.
	ServingRoutingAnnotation = "edge.dacs.io/serving-routing"
)

const (
	ServingPolicyAnnotation = "edge.dacs.io/serving-policy"
)

const (
	ServingSlotAnnotation = "edge.dacs.io/serving-slot"
)

const (
	SourceFinalizer = "edge.dacs.io/serving-source-termination"
)

const (
	FirstfloorContainerName = "firstfloor"
)

func PhysicalID(podUID types.UID, containerID string) string {
	return string(podUID) + "/" + containerID
}

// NativeModel reads the startup Config mounted by the physical executor.
func NativeModel(ctx context.Context, reader client.Reader, pod *corev1.Pod) (string, error) {
	cm := &corev1.ConfigMap{}
	if err := reader.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, cm); err != nil {
		return "", err
	}
	var config native.Config
	if err := json.Unmarshal([]byte(cm.Data["config.json"]), &config); err != nil {
		return "", err
	}
	if config.Model == "" {
		return "", errors.New("native startup Config model required")
	}
	return config.Model, nil
}

func FirstfloorStatus(pod *corev1.Pod) *corev1.ContainerStatus {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == FirstfloorContainerName {
			return &pod.Status.ContainerStatuses[i]
		}
	}
	return nil
}

func SameNativeContainer(before, after *corev1.Pod) bool {
	a, b := FirstfloorStatus(before), FirstfloorStatus(after)
	return before.UID == after.UID && a != nil && b != nil && a.ContainerID != "" && a.ContainerID == b.ContainerID && b.State.Running != nil
}

func SameRunningContainer(before, after *corev1.Pod) bool {
	b := FirstfloorStatus(after)
	return SameNativeContainer(before, after) && b.Ready && after.DeletionTimestamp == nil && PodReady(after)
}

func PodReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func WorkloadOwner(owners []metav1.OwnerReference) string {
	if len(owners) == 1 && owners[0].APIVersion == edgev1alpha1.GroupVersion.String() && owners[0].Kind == "Workload" {
		return string(owners[0].UID)
	}
	return ""
}

// PodAwaitingExecutor permits requesting deletion, never declaring termination.
// The retained finalizer still requires podTerminated before retirement.
func PodAwaitingExecutor(pod *corev1.Pod) bool {
	status := FirstfloorStatus(pod)
	return (pod.Status.Phase == "" || pod.Status.Phase == corev1.PodPending) &&
		(status == nil || status.ContainerID == "" && status.State.Running == nil &&
			status.State.Terminated == nil && status.LastTerminationState.Terminated == nil)
}

func PodTerminated(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		return false
	}
	// A failed init may leave main containers waiting without ever creating an
	// executor. A nonempty running/unknown container identity is never stopped.
	for _, statuses := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses} {
		for _, status := range statuses {
			if status.State.Running != nil || status.State.Terminated == nil && (status.ContainerID != "" || status.State.Waiting == nil) {
				return false
			}
		}
	}
	return true
}
