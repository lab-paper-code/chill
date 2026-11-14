package kube

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
)

func TestSessionLoadJobBoundsAndResources(t *testing.T) {
	beo := sessionRoot(sessionSettingsFixture())
	beo.Spec.Session.LevelDurationLimitSeconds = 10.25
	beo.Spec.Session.RequestLifetimeSeconds = 7
	config := SessionConfig{Namespace: "chill-system", RunnerTimeout: 1500 * time.Millisecond}
	for _, scenario := range []struct {
		batch    int
		deadline int64
	}{{1, 60}, {2, 40}, {4, 40}} {
		job := sessionLoadJob(beo, testNode, sessionLoad{batch: scenario.batch, config: config, preparation: sessionPreparationFixture()})
		if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != scenario.deadline {
			t.Fatalf("batch %d deadline %v, want %d seconds", scenario.batch, job.Spec.ActiveDeadlineSeconds, scenario.deadline)
		}
		containers := append(job.Spec.Template.Spec.InitContainers, job.Spec.Template.Spec.Containers...)
		for _, container := range containers {
			memory := resource.MustParse("512Mi")
			if container.Resources.Requests.Memory().Cmp(memory) != 0 || container.Resources.Limits.Memory().Cmp(memory) != 0 ||
				len(container.Resources.Requests) != 1 || len(container.Resources.Limits) != 1 {
				t.Fatalf("%s resources %+v, want the previous load Job's 512Mi memory request and limit without a CPU limit", container.Name, container.Resources)
			}
		}
	}
}

func TestSessionLoadPrestartTimeoutRetainsWaitingEvidenceAndDeletesJob(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		container string
		waiting   *core.ContainerStateWaiting
		init      bool
	}{
		{name: "Pod not created"},
		{name: "load image pull", container: sessionLoadContainer, waiting: &core.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "dial tcp registry:443: i/o timeout"}},
		{name: "dataset image pull", container: "dataset", init: true, waiting: &core.ContainerStateWaiting{Reason: "ErrImagePull", Message: "registry returned 503 Service Unavailable"}},
		{name: "container creating", container: sessionLoadContainer, waiting: &core.ContainerStateWaiting{Reason: "ContainerCreating", Message: "network sandbox is not ready"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h := newAdmissionHarness(t)
			job := &batch.Job{ObjectMeta: meta.ObjectMeta{Name: "session-b1", Namespace: "chill-system"}}
			if scenario.waiting != nil {
				pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "session-pod", Namespace: job.Namespace, Labels: map[string]string{"job-name": job.Name}}}
				status := core.ContainerStatus{Name: scenario.container, State: core.ContainerState{Waiting: scenario.waiting}}
				if scenario.init {
					pod.Status.InitContainerStatuses = []core.ContainerStatus{status}
					pod.Status.ContainerStatuses = []core.ContainerStatus{{Name: sessionLoadContainer, State: core.ContainerState{Waiting: &core.ContainerStateWaiting{Reason: "PodInitializing"}}}}
				} else {
					pod.Status.ContainerStatuses = []core.ContainerStatus{status}
				}
				if _, err := h.kube.CoreV1().Pods(job.Namespace).Create(context.Background(), pod, meta.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			// The fake API has no garbage collector. Model the background Job
			// deletion removing its never-started Pod.
			h.kube.PrependReactor("delete", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
				if scenario.waiting != nil {
					err := h.kube.Tracker().Delete(core.SchemeGroupVersion.WithResource("pods"), job.Namespace, "session-pod")
					return false, nil, err
				}
				return false, nil, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := h.controller.runSessionLoad(ctx, job, 10*time.Millisecond)
			if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "session-load did not start") || ctx.Err() != nil {
				t.Fatalf("prestart error %v, parent error %v", err, ctx.Err())
			}
			if scenario.waiting != nil && (!strings.Contains(err.Error(), scenario.waiting.Reason) || !strings.Contains(err.Error(), scenario.waiting.Message)) {
				t.Fatalf("timeout lost Pod waiting evidence: %v", err)
			}
			if _, err := h.kube.BatchV1().Jobs(job.Namespace).Get(ctx, job.Name, meta.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatalf("load Job survives timeout: %v", err)
			}
			for _, action := range h.kube.Actions() {
				if deletion, ok := action.(ktesting.DeleteAction); ok && action.GetResource().Resource == "jobs" {
					policy := deletion.GetDeleteOptions().PropagationPolicy
					if policy == nil || *policy != meta.DeletePropagationBackground {
						t.Fatalf("cleanup must also delete load Pod: %+v", deletion.GetDeleteOptions())
					}
				}
			}
		})
	}
}

func TestSessionLoadPrestartBoundEndsWhenLoadContainerRuns(t *testing.T) {
	h := newAdmissionHarness(t)
	job := &batch.Job{ObjectMeta: meta.ObjectMeta{Name: "session-running", Namespace: "chill-system"}}
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "session-pod", Namespace: job.Namespace, Labels: map[string]string{"job-name": job.Name}},
		Status: core.PodStatus{ContainerStatuses: []core.ContainerStatus{{Name: sessionLoadContainer, State: core.ContainerState{Running: &core.ContainerStateRunning{}}}}}}
	if _, err := h.kube.CoreV1().Pods(job.Namespace).Create(context.Background(), pod, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	h.kube.PrependReactor("get", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		if time.Since(start) < 30*time.Millisecond {
			return false, nil, nil
		}
		current := job.DeepCopy()
		current.Status.Conditions = []batch.JobCondition{{Type: batch.JobComplete, Status: core.ConditionTrue}}
		pod.Status.ContainerStatuses[0].State = core.ContainerState{Terminated: &core.ContainerStateTerminated{Message: `{"levels":[{"kind":"saturation","completedBatches":3}]}`}}
		if err := h.kube.Tracker().Update(core.SchemeGroupVersion.WithResource("pods"), pod, job.Namespace); err != nil {
			return true, nil, err
		}
		return true, current, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	table, err := h.controller.runSessionLoad(ctx, job, 10*time.Millisecond)
	if err != nil || len(table.Levels) != 1 || table.Levels[0].CompletedBatches != 3 {
		t.Fatalf("running load was cut off by startup bound: table %+v, error %v", table, err)
	}
}

func TestSessionLoadTerminationBeforeRunningPollStillReturnsTable(t *testing.T) {
	h := newAdmissionHarness(t)
	job := &batch.Job{ObjectMeta: meta.ObjectMeta{Name: "session-finished", Namespace: "chill-system"}}
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "session-pod", Namespace: job.Namespace, Labels: map[string]string{"job-name": job.Name}},
		Status: core.PodStatus{ContainerStatuses: []core.ContainerStatus{{Name: sessionLoadContainer, State: core.ContainerState{Terminated: &core.ContainerStateTerminated{Message: `{"levels":[]}`}}}}}}
	if _, err := h.kube.CoreV1().Pods(job.Namespace).Create(context.Background(), pod, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	reads := 0
	h.kube.PrependReactor("get", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		reads++
		current := job.DeepCopy()
		if reads > 1 {
			current.Status.Conditions = []batch.JobCondition{{Type: batch.JobComplete, Status: core.ConditionTrue}}
		}
		return true, current, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := h.controller.runSessionLoad(ctx, job, 10*time.Millisecond); err != nil {
		t.Fatalf("load finished between polls: %v", err)
	}
}
