package kube

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/lab-paper-code/chill/internal/workloadobserver"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

func reachesEndFence(pod corev1.Pod, request Request) bool {
	if pod.Namespace != request.Run.Namespace || pod.Name != request.Run.PodName ||
		string(pod.UID) != request.Run.PodUID {
		return false
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == request.EndFenceContainerName && status.State.Terminated != nil &&
			status.State.Terminated.ExitCode == 0 {
			return true
		}
	}
	return false
}

func initialActivePods(items []corev1.Pod, request Request) (map[string]workloadobserver.Pod, error) {
	active := make(map[string]workloadobserver.Pod, len(items))
	for index := range items {
		pod, err := ProjectPod(items[index], request.Target.NodeName, request.SelectorLabelKeys)
		if err != nil {
			return nil, fmt.Errorf("project listed Pod: %w", err)
		}
		if !isActive(pod.Phase) {
			continue
		}
		if _, duplicate := active[pod.UID]; duplicate {
			return nil, fmt.Errorf("list Pods: duplicate Pod UID %q", pod.UID)
		}
		active[pod.UID] = pod
	}
	run, present := active[request.Run.PodUID]
	if !present || run.Namespace != request.Run.Namespace || run.Name != request.Run.PodName {
		return nil, errors.New("list Pods: active set does not contain the frozen Run Pod identity")
	}
	return active, nil
}

func ProjectPod(pod corev1.Pod, nodeName string, labelKeys []string) (workloadobserver.Pod, error) {
	if pod.Spec.NodeName != nodeName {
		return workloadobserver.Pod{}, fmt.Errorf(
			"Pod %s/%s belongs to Node %q, expected %q",
			pod.Namespace,
			pod.Name,
			pod.Spec.NodeName,
			nodeName,
		)
	}
	if strings.TrimSpace(pod.Namespace) == "" || strings.TrimSpace(pod.Name) == "" ||
		strings.TrimSpace(string(pod.UID)) == "" || strings.TrimSpace(string(pod.Status.Phase)) == "" {
		return workloadobserver.Pod{}, fmt.Errorf("Pod %s/%s has incomplete identity or phase", pod.Namespace, pod.Name)
	}

	images := make([]string, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers)+len(pod.Spec.EphemeralContainers))
	for index := range pod.Spec.InitContainers {
		images = append(images, pod.Spec.InitContainers[index].Image)
	}
	for index := range pod.Spec.Containers {
		images = append(images, pod.Spec.Containers[index].Image)
	}
	for index := range pod.Spec.EphemeralContainers {
		images = append(images, pod.Spec.EphemeralContainers[index].Image)
	}
	images = canonicalStrings(images)
	if len(images) == 0 || images[0] == "" {
		return workloadobserver.Pod{}, fmt.Errorf("Pod %s/%s has an empty container image", pod.Namespace, pod.Name)
	}

	projected := workloadobserver.Pod{
		Namespace: pod.Namespace,
		Name:      pod.Name,
		UID:       string(pod.UID),
		Phase:     string(pod.Status.Phase),
		Images:    images,
	}
	if owner := metav1.GetControllerOf(&pod); owner != nil {
		projected.Owner = &workloadobserver.OwnerIdentity{
			Kind: owner.Kind,
			Name: owner.Name,
			UID:  string(owner.UID),
		}
	}
	for _, key := range labelKeys {
		if value, present := pod.Labels[key]; present {
			if projected.Labels == nil {
				projected.Labels = make(map[string]string)
			}
			projected.Labels[key] = value
		}
	}
	return projected, nil
}

func transition(
	eventType watch.EventType,
	pod workloadobserver.Pod,
	active map[string]workloadobserver.Pod,
) (workloadobserver.EventKind, bool) {
	current, exists := active[pod.UID]
	switch eventType {
	case watch.Added, watch.Modified:
		if !isActive(pod.Phase) {
			if exists {
				return workloadobserver.EventExited, true
			}
			return "", false
		}
		if !exists {
			return workloadobserver.EventEntered, true
		}
		if reflect.DeepEqual(current, pod) {
			return "", false
		}
		return workloadobserver.EventUpdated, true
	case watch.Deleted:
		if exists {
			return workloadobserver.EventExited, true
		}
		return "", false
	default:
		return "", false
	}
}

func applyTransition(kind workloadobserver.EventKind, pod workloadobserver.Pod, active map[string]workloadobserver.Pod) {
	if kind == workloadobserver.EventExited {
		delete(active, pod.UID)
		return
	}
	active[pod.UID] = pod
}

func canonicalStrings(values []string) []string {
	result := append([]string{}, values...)
	slices.Sort(result)
	return slices.Compact(result)
}

func orderedPods(active map[string]workloadobserver.Pod) []workloadobserver.Pod {
	pods := make([]workloadobserver.Pod, 0, len(active))
	for _, pod := range active {
		pods = append(pods, pod)
	}
	sort.Slice(pods, func(i, j int) bool {
		left := pods[i].Namespace + "\x00" + pods[i].Name + "\x00" + pods[i].UID
		right := pods[j].Namespace + "\x00" + pods[j].Name + "\x00" + pods[j].UID
		return left < right
	})
	return pods
}

func isActive(phase string) bool {
	return phase == string(corev1.PodPending) || phase == string(corev1.PodRunning) || phase == string(corev1.PodUnknown)
}
