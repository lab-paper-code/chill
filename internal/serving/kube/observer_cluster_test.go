package kube

import (
	"errors"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

// AC-54: baseline, physical restart/image evidence, deletion, and issue recovery.
func TestClusterPodAndIssueTransitions(t *testing.T) {
	var rows []serving.ClusterObservation
	fail := false
	o := &Observer{issues: map[string]ObserverIssue{}, config: ObserverConfig{
		SelectClusterPod: func(p *corev1.Pod) bool { return p.Labels["selected"] == "yes" },
		OnCluster: func(row serving.ClusterObservation) error {
			if fail {
				return errors.New("disk full")
			}
			rows = append(rows, row)
			return nil
		},
	}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "system", Name: "server", UID: "pod-1", Labels: map[string]string{"selected": "yes"}}, Spec: corev1.PodSpec{NodeName: "node", Containers: []corev1.Container{{Name: "server", Image: "mutable:tag"}}}}
	o.observeClusterList([]corev1.Pod{*pod})
	o.observeClusterList([]corev1.Pod{*pod})
	if len(rows) != 1 || rows[0].Event != "observed" || rows[0].Pod.Containers[0].RestartCount != nil || rows[0].Pod.Containers[0].ImageID != "" {
		t.Fatalf("baseline fabricated or duplicated: %+v", rows)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "server", ContainerID: "containerd://new", ImageID: "sha256:actual", RestartCount: 1, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}}}}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
	fail = true
	o.observeClusterPod(pod, string(watch.Modified))
	if len(rows) != 1 {
		t.Fatal("failed append published")
	}
	fail = false
	o.observeClusterList([]corev1.Pod{*pod})
	changed := rows[1]
	container := changed.Pod.Containers[0]
	if changed.Event != clusterEventChanged || *container.RestartCount != 1 || container.ImageID != "sha256:actual" || container.LastTerminated.Reason != "OOMKilled" || container.LastTerminated.ExitCode != 137 || *changed.Pod.Ready != "False" {
		t.Fatalf("lost physical evidence: %+v", changed)
	}
	o.observeClusterPod(pod, string(watch.Deleted))
	pod.UID = "pod-2"
	o.observeClusterPod(pod, string(watch.Added))
	if rows[2].Event != clusterEventDeleted || rows[3].Event != "created" || rows[3].Pod.UID != "pod-2" {
		t.Fatalf("identity transitions: %+v", rows)
	}
	pod.Labels = nil
	o.observeClusterPod(pod, string(watch.Modified))
	if len(rows) != 4 {
		t.Fatal("unselected Pod recorded")
	}
	const unreadyReason = "unready"
	o.issue("pod-2/containerd://new", errors.New(unreadyReason))
	o.issue("pod-2/containerd://new", errors.New(unreadyReason))
	o.issue("pod-2/containerd://new", errors.New("probe failed"))
	fail = true
	o.issue("pod-2/containerd://new", nil)
	fail = false
	o.issue("pod-2/containerd://new", nil)
	if len(rows) != 7 || rows[4].Event != "raised" || rows[5].Event != clusterEventChanged || rows[5].PreviousReason != unreadyReason || rows[6].Event != "resolved" || rows[6].PreviousReason != "probe failed" {
		t.Fatalf("issue transitions: %+v", rows)
	}
}

func TestClusterWatchIncludesSeparateControlNamespace(t *testing.T) {
	o := &Observer{config: ObserverConfig{Namespace: "serving"}}
	if o.clusterWatchNamespace() != "serving" {
		t.Fatal("native watch lost namespace scope")
	}
	o.config.OnCluster = func(serving.ClusterObservation) error { return nil }
	if o.clusterWatchNamespace() != metav1.NamespaceAll {
		t.Fatal("cluster watch omitted release control namespace")
	}
}
