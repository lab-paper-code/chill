package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/component"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	"github.com/lab-paper-code/chill/internal/metadata"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestKubernetesExecutionFinalizerObservesExactJobPodAndNodeWorkload(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{batchv1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Namespace: "chill-system", Name: "run-job", UID: "job-uid",
	}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "chill-system", Name: "run-pod", UID: "pod-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr.To(true),
			}},
		},
		Spec: corev1.PodSpec{NodeName: "lattepanda"},
	}
	coResident := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "edge-metric", UID: "metric-uid"},
		Spec:       corev1.PodSpec{NodeName: "lattepanda"},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "lattepanda", UID: "node-uid"}}
	reader := fake.NewClientBuilder().WithScheme(scheme).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(object client.Object) []string {
			return []string{object.(*corev1.Pod).Spec.NodeName}
		}).
		WithObjects(job, pod, coResident, node).
		Build()
	finalizer, err := NewKubernetesExecutionFinalizer(reader, unusedStoreFactory)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	finalizer.now = func() time.Time { return now }
	run := testRun()
	observation, err := finalizer.observe(context.Background(), run, job)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Pod.UID != pod.UID || observation.Node.UID != node.UID ||
		len(observation.NodePods) != 2 || !observation.ObservedAt.Equal(now) {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestKubernetesExecutionFinalizerReadsFailedRuntimeTermination(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "run-job", UID: "job-uid"}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "chill-system", Name: "run-pod", UID: "pod-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr.To(true),
			}},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "metric-observer", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 4}}},
			{Name: runtimeContainerName, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 3, Message: "execute firstfloor measurement: timed out\n",
			}}},
		}},
	}
	run := testRun()
	empty, err := NewKubernetesExecutionFinalizer(fake.NewClientBuilder().WithScheme(scheme).Build(), unusedStoreFactory)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := empty.RuntimeTermination(context.Background(), run, job); err != nil || got != nil {
		t.Fatalf("deleted Pod termination = %#v, %v; want nil", got, err)
	}
	finalizer, err := NewKubernetesExecutionFinalizer(
		fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build(), unusedStoreFactory,
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := finalizer.RuntimeTermination(context.Background(), run, job)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ExitCode != 3 || got.LogTail != "execute firstfloor measurement: timed out\n" {
		t.Fatalf("runtime termination = %#v", got)
	}
}

func TestKubernetesExecutionFinalizerRecordsOOMKilledAndMemoryLimit(t *testing.T) {
	for _, name := range []string{"runtime", "metric-observer", "workload-observer", "artifact"} {
		t.Run(name, func(t *testing.T) {
			scheme := executionJobTestScheme(t)
			run := testRun()
			job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: "run-job", UID: "job-uid"}}
			container := corev1.Container{Name: name, Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
			}}
			status := corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 137, Reason: "OOMKilled",
			}}}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: "run-pod", OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr.To(true),
				}}},
				Spec:   corev1.PodSpec{Containers: []corev1.Container{container}},
				Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{status}},
			}
			if name == "artifact" {
				pod.Spec.InitContainers, pod.Spec.Containers = pod.Spec.Containers, nil
				pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses = pod.Status.ContainerStatuses, nil
			}
			finalizer, err := NewKubernetesExecutionFinalizer(fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build(), unusedStoreFactory)
			if err != nil {
				t.Fatal(err)
			}
			got, err := finalizer.RuntimeTermination(context.Background(), run, job)
			if err != nil || got == nil || got.ExitCode != 137 ||
				!strings.Contains(got.LogTail, name+": OOMKilled") || !strings.Contains(got.LogTail, "memory limit=64Mi (67108864 bytes)") {
				t.Fatalf("OOM termination = %#v, err=%v", got, err)
			}
		})
	}
}

func TestKubernetesExecutionFinalizerLoadsOnlyRequiredCredentials(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "store-credentials"},
		Data: map[string][]byte{
			component.EvidenceStoreAccessKeyKey: []byte("access"),
			component.EvidenceStoreSecretKeyKey: []byte("secret"),
			"unrelated":                         []byte("ignored"),
		},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	finalizer, err := NewKubernetesExecutionFinalizer(reader, unusedStoreFactory)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := storekube.LoadCredentials(context.Background(), finalizer.reader, "chill-system", secret.Name)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccessKeyID != "access" || credentials.SecretAccessKey != "secret" {
		t.Fatalf("credentials = %#v", credentials)
	}
}

func TestObserveActuationRecordsNodeApplyAndGeneration(t *testing.T) {
	t.Parallel()
	run, resolved := testActuatedRunAndResolved(t)
	config := resolved.Prepared.RunConfig
	readyAt := time.Date(2026, 8, 6, 12, 0, 30, 0, time.UTC)
	node := settledNode(config, config.Actuation.PowerMode, readyAt, 5)
	facts, err := observeActuation(run, resolved.Prepared, node)
	if err != nil {
		t.Fatal(err)
	}
	if facts == nil || facts.Run.UID != string(run.UID) || facts.Handoff != nil ||
		facts.Target.PowerMode != config.Actuation.PowerMode || !facts.ReadyAt.Equal(readyAt) ||
		!facts.Target.ObservedAt.Equal(readyAt.Add(-30*time.Second)) ||
		facts.Generation == nil || *facts.Generation != (profilerevidence.PowerModeGeneration{Start: 5, End: 5}) {
		t.Fatalf("actuation facts = %#v", facts)
	}

	node.Annotations[metadata.PowerModeGeneration] = "6"
	if facts, err := observeActuation(run, resolved.Prepared, node); err != nil ||
		*facts.Generation != (profilerevidence.PowerModeGeneration{Start: 5, End: 6}) {
		t.Fatalf("host change facts = %#v, %v", facts, err)
	}

	delete(node.Annotations, metadata.PowerModeTransition)
	if _, err := observeActuation(run, resolved.Prepared, node); err == nil {
		t.Fatal("Node without a settled apply was accepted for finalization")
	}
}

func unusedStoreFactory(storekube.Binding, string, string) (profilerevidence.ObjectStore, error) {
	return nil, nil
}

func TestFinalizedEvidenceObjectKeyIsRunScoped(t *testing.T) {
	t.Parallel()
	want := "profiling-runs/run-uid/execution.json"
	if got := finalizedEvidenceObjectKey(types.UID("run-uid")); got != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
}

func TestKubernetesExecutionFinalizerUsesFrozenJobStoreBinding(t *testing.T) {
	t.Parallel()
	run := testRun()
	prepared := currentPreparedRun(t)
	recordRunConfigAndJob(run)
	binding := testEvidenceStoreBinding()
	job, err := buildExecutionJob(run, resolvedRunForPrepared(t, prepared), binding, testExecutionJobOptions(), executionJobTestScheme(t))
	if err != nil {
		t.Fatal(err)
	}
	job.UID = types.UID(run.Status.Job.UID)
	run.Status.Job.Name = job.Name
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: binding.CredentialsSecretName},
		Data: map[string][]byte{
			component.EvidenceStoreAccessKeyKey: []byte("access"),
			component.EvidenceStoreSecretKeyKey: []byte("secret"),
		},
	}
	want := errors.New("stop before observing native objects")
	finalizer, err := NewKubernetesExecutionFinalizer(
		fake.NewClientBuilder().WithScheme(executionJobTestScheme(t)).WithObjects(secret).Build(),
		func(got storekube.Binding, access, key string) (profilerevidence.ObjectStore, error) {
			if got != binding || access != "access" || key != "secret" {
				t.Fatalf("frozen binding/credentials = %#v, %q, %q", got, access, key)
			}
			return nil, want
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := finalizer.Finalize(context.Background(), run, prepared, job); !errors.Is(err, want) {
		t.Fatalf("Finalize() error = %v", err)
	}
}

func TestKubernetesExecutionFinalizerRequiresSuccessfulOwnedLoadPod(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*batchv1.Job, *corev1.Pod)
		match  string
	}{
		{name: "valid two-container load"},
		{name: "missing Job", mutate: func(job *batchv1.Job, _ *corev1.Pod) { job.Name = "other-load" }, match: "load Job"},
		{name: "foreign Job", mutate: func(job *batchv1.Job, _ *corev1.Pod) { job.OwnerReferences[0].UID = "foreign-run" }, match: "load Job"},
		{name: "wrong origin", mutate: func(job *batchv1.Job, _ *corev1.Pod) { job.Spec.Template.Spec.NodeName = "wrong-node" }, match: "load Job"},
		{name: "failed Job", mutate: func(job *batchv1.Job, _ *corev1.Pod) {
			job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue})
		}, match: "load Pod"},
		{name: "missing Pod", mutate: func(_ *batchv1.Job, pod *corev1.Pod) { pod.OwnerReferences[0].UID = "foreign-job" }, match: "load Pod"},
		{name: "wrong Pod node", mutate: func(_ *batchv1.Job, pod *corev1.Pod) { pod.Spec.NodeName = "wrong-node" }, match: "load Pod"},
		{name: "failed Pod", mutate: func(_ *batchv1.Job, pod *corev1.Pod) { pod.Status.Phase = corev1.PodFailed }, match: "load Pod"},
		{name: "failed dataset", mutate: func(_ *batchv1.Job, pod *corev1.Pod) {
			pod.Status.InitContainerStatuses[0].State.Terminated.ExitCode = 1
		}, match: "load Pod"},
		{name: "failed load", mutate: func(_ *batchv1.Job, pod *corev1.Pod) {
			pod.Status.ContainerStatuses[0].State.Terminated.ExitCode = 1
		}, match: "load Pod"},
		{name: "missing container", mutate: func(_ *batchv1.Job, pod *corev1.Pod) { pod.Status.ContainerStatuses = nil }, match: "load Pod"},
		{name: "wrong dataset image", mutate: func(_ *batchv1.Job, pod *corev1.Pod) { pod.Spec.InitContainers[0].Image = "wrong-image" }, match: "load Pod"},
		{name: "wrong load image", mutate: func(_ *batchv1.Job, pod *corev1.Pod) { pod.Spec.Containers[0].Image = "wrong-image" }, match: "load Pod"},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := testRun()
			prepared := currentPreparedRun(t)
			recordRunConfigAndJob(run)
			binding := testEvidenceStoreBinding()
			job, err := buildExecutionJob(run, resolvedRunForPrepared(t, prepared), binding, testExecutionJobOptions(), executionJobTestScheme(t))
			if err != nil {
				t.Fatal(err)
			}
			job.UID = types.UID(run.Status.Job.UID)
			run.Status.Job.Name = job.Name
			load := completedMeasurementLoad(run, job)
			load.OwnerReferences = append([]metav1.OwnerReference(nil), job.OwnerReferences...)
			completed := metav1.NewTime(time.Date(2026, 11, 13, 4, 1, 0, 0, time.UTC))
			load.Status.CompletionTime = &completed
			load.Spec.Template.Spec.InitContainers = []corev1.Container{{Name: "dataset", Image: prepared.RunConfig.Measurement.Dataset.Image}}
			load.Spec.Template.Spec.Containers = []corev1.Container{{Name: "load", Image: "gateway-image"}}
			status := func(name string) corev1.ContainerStatus {
				return corev1.ContainerStatus{Name: name, ImageID: name + "-image-id", State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						Reason: "Completed", StartedAt: metav1.NewTime(completed.Add(-time.Minute)), FinishedAt: completed,
					},
				}}
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: load.Name + "-pod", UID: "load-pod-uid",
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: load.Name, UID: load.UID, Controller: ptr.To(true)}}},
				Spec: *load.Spec.Template.Spec.DeepCopy(),
				Status: corev1.PodStatus{Phase: corev1.PodSucceeded,
					InitContainerStatuses: []corev1.ContainerStatus{status("dataset")}, ContainerStatuses: []corev1.ContainerStatus{status("load")}},
			}
			if test.mutate != nil {
				test.mutate(load, pod)
			}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: binding.CredentialsSecretName},
				Data:       map[string][]byte{component.EvidenceStoreAccessKeyKey: []byte("access"), component.EvidenceStoreSecretKeyKey: []byte("secret")},
			}
			reader := fake.NewClientBuilder().WithScheme(executionJobTestScheme(t)).WithObjects(secret, load, pod).Build()
			finalizer, err := NewKubernetesExecutionFinalizer(reader,
				func(storekube.Binding, string, string) (profilerevidence.ObjectStore, error) {
					// Native validation must finish before object-store IO.
					return struct{ profilerevidence.ObjectStore }{}, nil
				})
			if err != nil {
				t.Fatal(err)
			}
			_, err = finalizer.Finalize(context.Background(), run, prepared, job)
			if test.match == "" {
				if err == nil || !strings.Contains(err.Error(), "expected one Pod owned by measurement Job, found 0") {
					t.Fatalf("successful load did not reach measurement observation: %v", err)
				}
			} else if !errors.Is(err, profilerevidence.ErrPermanentValidation) || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("invalid load finalization = %v, want permanent %q failure", err, test.match)
			}
		})
	}
}

func TestKubernetesExecutionFinalizerKeepsLoadReadFailureRetryable(t *testing.T) {
	want := errors.New("temporary Kubernetes API transport failure")
	finalizer, err := NewKubernetesExecutionFinalizer(failingJobReader{err: want}, unusedStoreFactory)
	if err != nil {
		t.Fatal(err)
	}
	run := testRun()
	config := currentPreparedRun(t).RunConfig
	err = finalizer.validateMeasurementLoad(context.Background(), run, &batchv1.Job{}, config)
	if !errors.Is(err, want) || errors.Is(err, profilerevidence.ErrPermanentValidation) {
		t.Fatalf("load API failure = %v, want retryable transport failure", err)
	}
}

func TestKubernetesExecutionFinalizerIdleNeedsNoLoadSnapshot(t *testing.T) {
	finalizer, err := NewKubernetesExecutionFinalizer(failingJobReader{err: errors.New("unexpected API read")}, unusedStoreFactory)
	if err != nil {
		t.Fatal(err)
	}
	config := currentPreparedRun(t).RunConfig
	config.Measurement.WorkloadMode = profilingrun.IdleModelLoadedWorkloadMode
	config.Measurement.Dataset = nil
	if err := finalizer.validateMeasurementLoad(context.Background(), testRun(), &batchv1.Job{}, config); err != nil {
		t.Fatal(err)
	}
}
