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

	"github.com/lab-paper-code/chill/internal/profiling/profiler/worker"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

func TestExecutePublishesFencedWorkloadObservation(t *testing.T) {
	directory := t.TempDir()
	writeWorkloadTestRunConfig(t, directory)
	opts := workloadTestOptions(directory)
	pod := workloadTestPod()
	client := fake.NewSimpleClientset(pod.DeepCopy())
	preflight := watch.NewRaceFreeFake()
	initial := watch.NewRaceFreeFake()
	watchCalls := make(chan string, 2)
	transient := *workloadTestPod()
	transient.Namespace = "default"
	transient.Name = "warmup-only"
	transient.UID = "warmup-only-uid"
	transient.OwnerReferences = nil
	installPodObservationReactors(t, client, *pod, transient, watchCalls, preflight, initial)

	done := make(chan error, 1)
	go func() {
		done <- execute(context.Background(), opts, client.CoreV1(), time.Millisecond)
	}()
	waitForFile(t, opts.readyPath)
	if got := <-watchCalls; got != "10" {
		t.Fatalf("preflight watch resourceVersion = %q, want 10", got)
	}
	if err := worker.PublishSignal(opts.windowStartPath); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, opts.windowReadyPath)
	if got := <-watchCalls; got != "20" {
		t.Fatalf("measurement watch resourceVersion = %q, want 20", got)
	}
	if err := worker.PublishSignal(opts.endPath); err != nil {
		t.Fatal(err)
	}
	fence := pod.DeepCopy()
	fence.ResourceVersion = "21"
	fence.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  endFenceContainerName,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
	}}
	initial.Modify(fence)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for WorkloadObserver executable")
	}
	payload, err := os.ReadFile(opts.resultPath)
	if err != nil {
		t.Fatal(err)
	}
	var result workloadobserver.Result
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	if !result.Continuous || result.InitialResourceVersion != "20" || result.LastResourceVersion != "21" ||
		result.Run.JobUID != "job-uid" {
		t.Fatalf("workload result = %#v", result)
	}
	for _, active := range result.InitialActivePods {
		if active.UID == "warmup-only-uid" {
			t.Fatalf("pre-warm-up Pod leaked into measurement window: %#v", active)
		}
	}
}

func TestExecuteRejectsPodOutsideFrozenNodeBeforeReady(t *testing.T) {
	directory := t.TempDir()
	writeWorkloadTestRunConfig(t, directory)
	opts := workloadTestOptions(directory)
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
	opts := workloadTestOptions(t.TempDir())
	opts.selectorLabelCSV = "z,a"
	if err := opts.validate(); err == nil || !strings.Contains(err.Error(), "sorted") {
		t.Fatalf("validate() error = %v", err)
	}
}

func installPodObservationReactors(
	t *testing.T,
	client *fake.Clientset,
	pod corev1.Pod,
	preflightOnly corev1.Pod,
	calls chan<- string,
	watchers ...watch.Interface,
) {
	t.Helper()
	listCalls := 0
	client.PrependReactor("list", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		listCalls++
		if listCalls == 1 {
			return true, &corev1.PodList{
				ListMeta: metav1.ListMeta{ResourceVersion: "10"},
				Items:    []corev1.Pod{pod, preflightOnly},
			}, nil
		}
		return true, &corev1.PodList{
			ListMeta: metav1.ListMeta{ResourceVersion: "20"},
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

func writeWorkloadTestRunConfig(t *testing.T, directory string) {
	t.Helper()
	payload, err := os.ReadFile("testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var runConfig profilingrun.RunConfig
	if err := json.Unmarshal(payload, &runConfig); err != nil {
		t.Fatal(err)
	}
	runConfig.SchemaVersion = profilingrun.RunConfigSchemaVersion
	runConfig.State.ArtifactPath = "/artifact/model.onnx"
	runConfig.Images.WorkloadObserver = "registry.example/workload-observer@sha256:" + strings.Repeat("d", 64)
	runConfig.Workload = profilingrun.CurrentWorkloadContract()
	canonical, err := runConfig.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "run-config.json"), canonical, 0o600); err != nil {
		t.Fatal(err)
	}
}

func workloadTestOptions(directory string) options {
	payload, _ := os.ReadFile(filepath.Join(directory, "run-config.json"))
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}
	return options{
		runConfig:        string(payload),
		readyPath:        filepath.Join(directory, "workload-ready"),
		windowStartPath:  filepath.Join(directory, "workload-window-start"),
		windowReadyPath:  filepath.Join(directory, "workload-window-ready"),
		endPath:          filepath.Join(directory, "measurement-end"),
		resultPath:       filepath.Join(directory, "workload-result.json"),
		selectorLabelCSV: "app,component",
		endWaitTimeout:   time.Second, endFenceTimeout: 3 * time.Second,
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
