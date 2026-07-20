package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/lab-paper-code/chill/internal/profiler/worker"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

func TestExecutePublishesFencedWorkloadObservation(t *testing.T) {
	directory := t.TempDir()
	digest := writeWorkloadTestIntent(t, directory)
	opts := workloadTestOptions(directory, digest)
	pod := workloadTestPod()
	client := fake.NewSimpleClientset(pod.DeepCopy())
	initial := watch.NewRaceFreeFake()
	fence := watch.NewRaceFreeFake()
	watchCalls := make(chan string, 2)
	installPodObservationReactors(t, client, *pod, watchCalls, initial, fence)

	done := make(chan error, 1)
	go func() {
		done <- execute(context.Background(), opts, client.CoreV1(), time.Millisecond)
	}()
	waitForFile(t, opts.readyPath)
	if got := <-watchCalls; got != "10" {
		t.Fatalf("initial watch resourceVersion = %q, want 10", got)
	}
	if err := worker.PublishSignal(opts.endPath); err != nil {
		t.Fatal(err)
	}
	if got := <-watchCalls; got != "10" {
		t.Fatalf("end-fence watch resourceVersion = %q, want 10", got)
	}
	fence.Action(watch.Bookmark, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{ResourceVersion: "11"}})

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for WorkloadObserver executable")
	}
	reader, err := worker.NewWorkloadResultFile(worker.WorkloadResultFileConfig{
		Path: opts.resultPath, WaitTimeout: time.Second, PollInterval: time.Millisecond, MaxBytes: opts.maxResultBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Continuous || result.LastResourceVersion != "11" || result.Run.JobUID != "job-uid" {
		t.Fatalf("workload result = %#v", result)
	}
}

func TestExecuteRejectsPodOutsideFrozenNodeBeforeReady(t *testing.T) {
	directory := t.TempDir()
	digest := writeWorkloadTestIntent(t, directory)
	opts := workloadTestOptions(directory, digest)
	pod := workloadTestPod()
	pod.Spec.NodeName = "other-node"
	client := fake.NewSimpleClientset(pod)

	err := execute(context.Background(), opts, client.CoreV1(), time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("execute() error = %v", err)
	}
	if _, statErr := os.Stat(opts.readyPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid Pod published ready: %v", statErr)
	}
}

func TestOptionsRejectNonCanonicalSelectorKeys(t *testing.T) {
	opts := workloadTestOptions(t.TempDir(), "sha256:"+strings.Repeat("a", 64))
	opts.selectorLabelCSV = "z,a"
	if err := opts.validate(); err == nil || !strings.Contains(err.Error(), "sorted") {
		t.Fatalf("validate() error = %v", err)
	}
}

func installPodObservationReactors(
	t *testing.T,
	client *fake.Clientset,
	pod corev1.Pod,
	calls chan<- string,
	watchers ...watch.Interface,
) {
	t.Helper()
	client.PrependReactor("list", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{
			ListMeta: metav1.ListMeta{ResourceVersion: "10"},
			Items:    []corev1.Pod{pod},
		}, nil
	})
	var mu sync.Mutex
	index := 0
	client.PrependWatchReactor("pods", func(action clienttesting.Action) (bool, watch.Interface, error) {
		mu.Lock()
		defer mu.Unlock()
		watchAction := action.(clienttesting.WatchAction)
		calls <- watchAction.GetWatchRestrictions().ResourceVersion
		if index >= len(watchers) {
			return true, nil, errors.New("unexpected watch call")
		}
		watcher := watchers[index]
		index++
		return true, watcher, nil
	})
}

func writeWorkloadTestIntent(t *testing.T, directory string) string {
	t.Helper()
	payload, err := os.ReadFile("../../spikes/profilingrun/fixtures/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var intent profilingrun.Intent
	if err := json.Unmarshal(payload, &intent); err != nil {
		t.Fatal(err)
	}
	intent.SchemaVersion = profilingrun.SchemaVersion
	intent.State.ArtifactPath = "/artifact/model.onnx"
	canonical, err := intent.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "intent.json"), canonical, 0o600); err != nil {
		t.Fatal(err)
	}
	return digest
}

func workloadTestOptions(directory, digest string) options {
	return options{
		intentPath: filepath.Join(directory, "intent.json"), intentDigest: digest,
		readyPath: filepath.Join(directory, "workload-ready"), endPath: filepath.Join(directory, "measurement-end"),
		resultPath:   filepath.Join(directory, "workload-result.json"),
		policyDigest: "sha256:" + strings.Repeat("b", 64), selectorLabelCSV: "app,component",
		endWaitTimeout: time.Second, endFenceTimeout: 3 * time.Second,
		maxEvents: 100, maxResultBytes: 1 << 20,
		podNamespace: "chill-system", podName: "run", podUID: "pod-uid",
	}
}

func workloadTestPod() *corev1.Pod {
	controller := true
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "chill-system", Name: "run", UID: types.UID("pod-uid"), ResourceVersion: "10",
			OwnerReferences: []metav1.OwnerReference{{
				Kind: "Job", Name: "run", UID: types.UID("job-uid"), Controller: &controller,
			}},
		},
		Spec: corev1.PodSpec{
			NodeName: "lattepanda", Containers: []corev1.Container{{Name: "runtime", Image: "example/runtime:v1"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
