package kube

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
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
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, _, err := h.controller.runSessionLoad(ctx, job, 10*time.Millisecond, 1<<20)
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
	h.controller.sessionLoadLog = func(context.Context, *core.Pod) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("")), nil
	}
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
	table, _, err := h.controller.runSessionLoad(ctx, job, 10*time.Millisecond, 1<<20)
	if err != nil || len(table.Levels) != 1 || table.Levels[0].CompletedBatches != 3 {
		t.Fatalf("running load was cut off by startup bound: table %+v, error %v", table, err)
	}
}

func TestSessionLoadTerminationBeforeRunningPollStillReturnsTable(t *testing.T) {
	h := newAdmissionHarness(t)
	h.controller.sessionLoadLog = func(context.Context, *core.Pod) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("")), nil
	}
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
	if _, _, err := h.controller.runSessionLoad(ctx, job, 10*time.Millisecond, 1<<20); err != nil {
		t.Fatalf("load finished between polls: %v", err)
	}
}

func TestReadSessionBatchRowsKeepsExecutorClockAndBoundsLog(t *testing.T) {
	const line = `CHILL_SESSION_BATCH {"workloadUID":"session/2/mid","batchID":3,"batchSize":2,"finishedAt":"2026-11-15T00:00:00Z","serviceDurationNanos":1000000,"precedingIdleNanos":7,"latestEnqueuedAt":"2026-11-14T23:59:59Z"}` + "\n"
	rows, err := readSessionBatchRows(strings.NewReader("unrelated log line\n"+line), 1<<20)
	if err != nil || len(rows["session/2/mid"]) != 1 {
		t.Fatalf("rows %+v error %v", rows, err)
	}
	row := rows["session/2/mid"][0]
	if row.BatchID != 3 || row.BatchSize != 2 || row.StartedAt != row.FinishedAt.Add(-time.Millisecond) ||
		row.PrecedingIdleNanos == nil || *row.PrecedingIdleNanos != 7 || row.LatestEnqueuedAt.IsZero() {
		t.Fatalf("executor row changed: %+v", row)
	}
	for _, limit := range []int64{int64(len(line) - 1), int64(len(line))} {
		_, err := readSessionBatchRows(strings.NewReader(line), limit)
		if (err == nil) != (limit == int64(len(line))) {
			t.Fatalf("limit %d error %v", limit, err)
		}
	}
	_, err = readSessionBatchRows(iotest.ErrReader(io.ErrUnexpectedEOF), 1<<20)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read error changed: %v", err)
	}
	_, err = readSessionBatchRows(strings.NewReader(profilederivation.SessionBatchRowPrefix+"{\n"), 1<<20)
	if err == nil || err.Error() != "decode session batch row: unexpected end of JSON input" {
		t.Fatalf("parse error changed: %v", err)
	}
}
