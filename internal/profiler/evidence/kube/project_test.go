package kube

import (
	"reflect"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestProjectTerminalFacts(t *testing.T) {
	observation := validObservation()
	facts, err := ProjectTerminalFacts(observation)
	if err != nil {
		t.Fatal(err)
	}
	if facts.Job.UID != "job-uid" || facts.Pod.ControllerOwnerUID != facts.Job.UID {
		t.Fatal("Job-to-Pod ownership was not projected")
	}
	if facts.Node.Name != "edge-node" || facts.Pod.NodeName != facts.Node.Name {
		t.Fatal("actual Node binding was not projected")
	}
	if len(facts.Containers) != 3 || facts.Containers[0].Name != "artifact" ||
		facts.Containers[1].Name != "power-observer" || facts.Containers[2].Name != "runtime" {
		t.Fatalf("unexpected projected containers: %#v", facts.Containers)
	}
	if len(facts.NodeWorkload.Pods) != 2 || facts.NodeWorkload.Pods[0].Name != "profiling-pod" {
		t.Fatalf("unexpected projected Node workload: %#v", facts.NodeWorkload.Pods)
	}
}

func TestProjectTerminalFactsIgnoresMutableMetadata(t *testing.T) {
	baseline := validObservation()
	want, err := ProjectTerminalFacts(baseline)
	if err != nil {
		t.Fatal(err)
	}

	drifted := validObservation()
	drifted.Job.ResourceVersion = "99"
	drifted.Job.Labels = map[string]string{"changed": "true"}
	drifted.Pod.Annotations = map[string]string{"changed": "true"}
	drifted.Pod.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "other-controller"}}
	drifted.Node.ResourceVersion = "100"
	drifted.Node.Labels = map[string]string{"topology.kubernetes.io/zone": "changed"}
	drifted.NodePods[0].ResourceVersion = "101"
	got, err := ProjectTerminalFacts(drifted)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mutable Kubernetes metadata changed projected facts\nwant: %#v\n got: %#v", want, got)
	}
}

func TestProjectTerminalFactsRejectsInvalidKubernetesViews(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Observation)
		match  string
	}{
		{
			name: "incomplete Job",
			mutate: func(observation *Observation) {
				observation.Job.Status.Conditions = nil
			},
			match: "terminal success",
		},
		{
			name: "owner mismatch",
			mutate: func(observation *Observation) {
				observation.Pod.OwnerReferences[0].UID = "other-job"
			},
			match: "controller-owned",
		},
		{
			name: "nonterminal Pod",
			mutate: func(observation *Observation) {
				observation.Pod.Status.Phase = corev1.PodRunning
			},
			match: "terminal phase",
		},
		{
			name: "observed Node mismatch",
			mutate: func(observation *Observation) {
				observation.Node.Name = "other-node"
			},
			match: "do not match",
		},
		{
			name: "container status mismatch",
			mutate: func(observation *Observation) {
				observation.Pod.Status.ContainerStatuses = observation.Pod.Status.ContainerStatuses[:1]
			},
			match: "count mismatch",
		},
		{
			name: "foreign Node workload Pod",
			mutate: func(observation *Observation) {
				observation.NodePods[1].Spec.NodeName = "other-node"
			},
			match: "not bound",
		},
		{
			name: "unexpected ephemeral container",
			mutate: func(observation *Observation) {
				observation.Pod.Spec.EphemeralContainers = []corev1.EphemeralContainer{{
					EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", Image: "debug:v1"},
				}}
			},
			match: "ephemeral",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := validObservation()
			test.mutate(&observation)
			_, err := ProjectTerminalFacts(observation)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("expected error containing %q, got %v", test.match, err)
			}
		})
	}
}

func validObservation() Observation {
	completed := time.Date(2026, time.July, 16, 12, 1, 0, 0, time.UTC)
	truth := true
	jobUID := types.UID("job-uid")
	podUID := types.UID("pod-uid")
	done := func(name, image string) corev1.ContainerStatus {
		return corev1.ContainerStatus{
			Name:         name,
			Image:        image,
			ImageID:      image + "-id",
			RestartCount: 0,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode:   0,
				Reason:     "Completed",
				StartedAt:  metav1.NewTime(completed.Add(-time.Minute)),
				FinishedAt: metav1.NewTime(completed.Add(-time.Second)),
			}},
		}
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "profiling-job", UID: jobUID},
		Status: batchv1.JobStatus{
			Succeeded:      1,
			CompletionTime: timePointer(completed),
			Conditions: []batchv1.JobCondition{{
				Type: batchv1.JobComplete, Status: corev1.ConditionTrue,
			}},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "chill-system",
			Name:      "profiling-pod",
			UID:       podUID,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: jobUID, Controller: &truth,
			}},
		},
		Spec: corev1.PodSpec{
			NodeName:       "edge-node",
			InitContainers: []corev1.Container{{Name: "artifact", Image: "artifact@sha256:1"}},
			Containers: []corev1.Container{
				{Name: "runtime", Image: "runtime@sha256:2"},
				{Name: "power-observer", Image: "power@sha256:3"},
			},
		},
		Status: corev1.PodStatus{
			Phase:                 corev1.PodSucceeded,
			InitContainerStatuses: []corev1.ContainerStatus{done("artifact", "artifact@sha256:1")},
			ContainerStatuses: []corev1.ContainerStatus{
				done("runtime", "runtime@sha256:2"),
				done("power-observer", "power@sha256:3"),
			},
		},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "edge-node", UID: "node-uid"}}
	systemPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "edge-metric", UID: "system-pod-uid"},
		Spec: corev1.PodSpec{
			NodeName:       node.Name,
			InitContainers: []corev1.Container{{Name: "setup", Image: "setup:v1"}},
			Containers:     []corev1.Container{{Name: "exporter", Image: "exporter:v1"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	return Observation{
		Job:        job,
		Pod:        pod,
		Node:       node,
		NodePods:   []corev1.Pod{*pod.DeepCopy(), systemPod},
		ObservedAt: completed.Add(time.Second),
	}
}

func timePointer(value time.Time) *metav1.Time {
	result := metav1.NewTime(value)
	return &result
}
