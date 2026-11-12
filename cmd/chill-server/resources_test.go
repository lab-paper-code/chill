package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestLoadGeneratorResourcePodSelected(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "system",
		Labels:    map[string]string{loadGeneratorRoleLabel: loadGeneratorRole},
	}}
	if !loadGeneratorResourcePodSelected(pod, "system") {
		t.Fatal("load generator not selected")
	}
	if loadGeneratorResourcePodSelected(pod, "other") {
		t.Fatal("load generator outside server namespace selected")
	}
	delete(pod.Labels, loadGeneratorRoleLabel)
	if loadGeneratorResourcePodSelected(pod, "system") {
		t.Fatal("Pod without role selected")
	}
	pod.Labels[loadGeneratorRoleLabel] = "system"
	if loadGeneratorResourcePodSelected(pod, "system") {
		t.Fatal("different role selected")
	}
}

// SCH-AC-53: replacement accepts the last issued identity, never aliases UIDs.
func TestResourcesHandshakeReplacementAndRestart(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: directory, Store: "evidence", Retention: 2 * time.Hour,
		Writer: schedulerPowerArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "edge", UID: "node-uid"}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "system", UID: "pod-old"},
		Spec:       corev1.PodSpec{NodeName: "edge"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			ContainerID: "containerd://old",
			State:       corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}}},
	}
	kube := fake.NewSimpleClientset(node, pod)
	receiver := &resourceReceiver{
		kube: kube, log: log, namespace: "system", selectPod: func(*corev1.Pod) bool { return true },
		maxSamples: 10, maxContainers: 10, maxThermal: 10, maxErrors: 10,
	}
	batch := observation.ResourceBatch{
		Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Samples: []observation.ResourceSample{},
	}
	targets, err := receiver.receive(ctx, batch)
	if err != nil || len(targets) != 1 {
		t.Fatalf("handshake=%v %v", targets, err)
	}
	pod.UID = types.UID("pod-new")
	pod.Status.ContainerStatuses[0].ContainerID = "containerd://new"
	if _, err := kube.CoreV1().Pods("system").Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour).UTC()
	batch.Samples = []observation.ResourceSample{{
		SampledAt:  oldTime,
		Containers: []observation.ContainerResources{{ResourceTarget: targets[0], CgroupVersion: 2}},
		Thermal:    []observation.ThermalResource{},
	}}
	next, err := receiver.receive(ctx, batch)
	if err != nil || len(next) != 1 || next[0].PodUID != "pod-new" {
		t.Fatalf("replacement=%v %v", next, err)
	}
	if _, err := receiver.receive(ctx, batch); err == nil {
		t.Fatal("obsolete identity accepted after replacement response")
	}
	receiver.issued = nil
	if _, err := receiver.receive(ctx, batch); err == nil {
		t.Fatal("restart accepted unconfirmed targets")
	}
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(directory, "v1alpha7", "*", "resources.*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("node batch files=%v err=%v", files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var row observation.ResourceObservation
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	if row.ReceivedAt == nil || !row.ReceivedAt.After(oldTime) || row.Samples[0].SampledAt != oldTime {
		t.Fatalf("node/server clocks=%+v", row)
	}
}

func TestProcessResourcesDirectorySamplingAndIdentity(t *testing.T) {
	directory := t.TempDir()
	ctx := context.Background()
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: directory, Store: "evidence", Retention: 2 * time.Hour,
		Writer: schedulerPowerArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := os.WriteFile(filepath.Join(directory, "definitions.jsonl"), []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Minute)
	producer := newProcessResources(log, directory, "server-pod", at, time.Minute)
	if err := producer.record(at); err != nil {
		t.Fatal(err)
	}
	if err := producer.record(at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(directory, "v1alpha7", "*", "resources.*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("files=%v", files)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var first, second observation.ResourceObservation
	if err := decoder.Decode(&first); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&second); err != nil {
		t.Fatal(err)
	}
	if first.PodUID != "server-pod" || first.ProcessStartedAt == nil || *first.ProcessStartedAt != at ||
		first.GCCPUSeconds == nil || first.GCCycles == nil || first.ObservationDirectoryBytes == nil ||
		*first.ObservationDirectoryBytes < 5 || second.ObservationDirectoryBytes != nil ||
		second.DirectoryObservedAt != nil {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}
