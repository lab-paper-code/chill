// Package kube projects terminal Kubernetes objects into the Kubernetes-free
// profiling evidence domain. It does not read, reconcile, or retain objects.
package kube

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/lab-paper-code/chill/internal/profiler/evidence"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Observation is one already-read terminal execution view. ObservedAt is the
// time at which NodePods finished being listed, not a Kubernetes object time.
type Observation struct {
	Job        *batchv1.Job
	Pod        *corev1.Pod
	Node       *corev1.Node
	NodePods   []corev1.Pod
	ObservedAt time.Time
}

// ProjectTerminalFacts validates the structural Kubernetes relationships and
// copies only facts used by the evidence domain. Scientific admission and
// intent-specific validation remain outside this adapter.
func ProjectTerminalFacts(observation Observation) (evidence.TerminalFacts, error) {
	if observation.Job == nil || observation.Pod == nil || observation.Node == nil {
		return evidence.TerminalFacts{}, errors.New("Job, Pod, and Node observations are required")
	}
	job := observation.Job
	pod := observation.Pod
	node := observation.Node
	if err := validateTerminalObjects(job, pod, node, observation.ObservedAt); err != nil {
		return evidence.TerminalFacts{}, err
	}

	containers, err := projectContainers(pod)
	if err != nil {
		return evidence.TerminalFacts{}, err
	}
	workload, err := projectNodeWorkload(node.Name, observation.NodePods)
	if err != nil {
		return evidence.TerminalFacts{}, err
	}

	owner := metav1.GetControllerOf(pod)
	return evidence.TerminalFacts{
		Job: evidence.JobTerminalFacts{
			Namespace:      job.Namespace,
			Name:           job.Name,
			UID:            string(job.UID),
			Succeeded:      job.Status.Succeeded,
			Failed:         job.Status.Failed,
			CompletionTime: job.Status.CompletionTime.Time,
		},
		Pod: evidence.PodTerminalFacts{
			Namespace:          pod.Namespace,
			Name:               pod.Name,
			UID:                string(pod.UID),
			ControllerOwnerUID: string(owner.UID),
			NodeName:           pod.Spec.NodeName,
			Phase:              string(pod.Status.Phase),
		},
		Node: evidence.NodeTerminalFacts{
			Name: node.Name,
			UID:  string(node.UID),
		},
		Containers: containers,
		NodeWorkload: evidence.NodeWorkload{
			ObservedAt: observation.ObservedAt,
			Pods:       workload,
		},
	}, nil
}

func validateTerminalObjects(job *batchv1.Job, pod *corev1.Pod, node *corev1.Node, observedAt time.Time) error {
	if job.Namespace == "" || job.Name == "" || job.UID == "" || job.Status.CompletionTime == nil {
		return errors.New("Job identity or completion time is incomplete")
	}
	complete := false
	failed := false
	for _, condition := range job.Status.Conditions {
		complete = complete || condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue
		failed = failed || condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue
	}
	if !complete || failed || job.Status.Succeeded != 1 || job.Status.Failed != 0 {
		return errors.New("Job is not an unambiguous terminal success")
	}
	if observedAt.IsZero() || observedAt.Before(job.Status.CompletionTime.Time) {
		return errors.New("Node workload observation predates Job completion")
	}
	if pod.Namespace == "" || pod.Name == "" || pod.UID == "" || pod.Namespace != job.Namespace ||
		pod.Status.Phase != corev1.PodSucceeded {
		return errors.New("Pod identity or terminal phase is invalid")
	}
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.Kind != "Job" || owner.UID != job.UID {
		return errors.New("Pod is not controller-owned by the Job")
	}
	if node.Name == "" || node.UID == "" || pod.Spec.NodeName == "" || pod.Spec.NodeName != node.Name {
		return errors.New("Pod binding and observed Node do not match")
	}
	if len(pod.Spec.EphemeralContainers) != 0 || len(pod.Status.EphemeralContainerStatuses) != 0 {
		return errors.New("profiling Pod contains unexpected ephemeral containers")
	}
	return nil
}

func projectContainers(pod *corev1.Pod) ([]evidence.ContainerTerminal, error) {
	requested := make(map[string]string, len(pod.Spec.InitContainers)+len(pod.Spec.Containers))
	for _, container := range pod.Spec.InitContainers {
		if err := addRequestedImage(requested, container.Name, container.Image); err != nil {
			return nil, err
		}
	}
	for _, container := range pod.Spec.Containers {
		if err := addRequestedImage(requested, container.Name, container.Image); err != nil {
			return nil, err
		}
	}

	statuses := append([]corev1.ContainerStatus(nil), pod.Status.InitContainerStatuses...)
	statuses = append(statuses, pod.Status.ContainerStatuses...)
	if len(statuses) != len(requested) {
		return nil, fmt.Errorf("container spec/status count mismatch: %d specs, %d statuses", len(requested), len(statuses))
	}
	result := make([]evidence.ContainerTerminal, 0, len(statuses))
	seen := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		image, ok := requested[status.Name]
		if !ok {
			return nil, fmt.Errorf("container status %q has no matching spec", status.Name)
		}
		if _, duplicate := seen[status.Name]; duplicate {
			return nil, fmt.Errorf("duplicate container status %q", status.Name)
		}
		seen[status.Name] = struct{}{}
		if status.State.Terminated == nil {
			return nil, fmt.Errorf("container %q has no terminal state", status.Name)
		}
		terminated := status.State.Terminated
		result = append(result, evidence.ContainerTerminal{
			Name:           status.Name,
			RequestedImage: image,
			ImageID:        status.ImageID,
			RestartCount:   status.RestartCount,
			ExitCode:       terminated.ExitCode,
			Reason:         terminated.Reason,
			StartedAt:      terminated.StartedAt.Time,
			FinishedAt:     terminated.FinishedAt.Time,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func addRequestedImage(images map[string]string, name, image string) error {
	if name == "" || image == "" {
		return errors.New("container name or requested image is empty")
	}
	if _, duplicate := images[name]; duplicate {
		return fmt.Errorf("duplicate container spec %q", name)
	}
	images[name] = image
	return nil
}

func projectNodeWorkload(nodeName string, pods []corev1.Pod) ([]evidence.WorkloadPod, error) {
	result := make([]evidence.WorkloadPod, 0, len(pods))
	seen := make(map[string]struct{}, len(pods))
	for i := range pods {
		pod := &pods[i]
		if pod.Spec.NodeName != nodeName {
			return nil, fmt.Errorf("workload Pod %s/%s is not bound to Node %q", pod.Namespace, pod.Name, nodeName)
		}
		if pod.Namespace == "" || pod.Name == "" || pod.UID == "" || pod.Status.Phase == "" {
			return nil, fmt.Errorf("workload Pod at index %d has incomplete identity or phase", i)
		}
		if _, duplicate := seen[string(pod.UID)]; duplicate {
			return nil, fmt.Errorf("duplicate workload Pod UID %q", pod.UID)
		}
		seen[string(pod.UID)] = struct{}{}
		images := podImages(pod)
		result = append(result, evidence.WorkloadPod{
			Namespace: pod.Namespace,
			Name:      pod.Name,
			UID:       string(pod.UID),
			Phase:     string(pod.Status.Phase),
			Images:    images,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		return left.Namespace+"\x00"+left.Name+"\x00"+left.UID < right.Namespace+"\x00"+right.Name+"\x00"+right.UID
	})
	return result, nil
}

func podImages(pod *corev1.Pod) []string {
	images := make([]string, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers)+len(pod.Spec.EphemeralContainers))
	for _, container := range pod.Spec.InitContainers {
		images = append(images, container.Image)
	}
	for _, container := range pod.Spec.Containers {
		images = append(images, container.Image)
	}
	for _, container := range pod.Spec.EphemeralContainers {
		images = append(images, container.Image)
	}
	sort.Strings(images)
	return images
}
