package controller

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestLoadFirstFailureRetainsRuntimeClassificationBeforeCleanup(t *testing.T) {
	for _, test := range []struct {
		name, stage, failure, class string
	}{
		{"allocation", profilerevidence.StageExecution, profilerevidence.FailureMemoryAllocation, profilerevidence.FailureClassExpectedStop},
		{"completed prefix budget", profilerevidence.StageExecution, profilerevidence.FailureMeasurementBudget, profilerevidence.FailureClassExpectedStop},
		{"completed prefix execution error", profilerevidence.StageExecution, profilerevidence.FailureExecution, profilerevidence.FailureClassFatalFailure},
		{"result integrity", profilerevidence.StageResultValidation, profilerevidence.FailureIntegrity, profilerevidence.FailureClassFatalFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			run := testRun()
			recordRunConfigAndJob(run)
			run.Status.JobCleanup = runJobCleanupPending
			resolved := resolvedRunForPrepared(t, currentPreparedRun(t))
			worker := recordedJob(t, run, resolved, &batchv1.JobStatus{Active: 1})
			load := completedMeasurementLoad(run, worker)
			load.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"}}
			payload, err := json.Marshal(profilerevidence.TerminationMessage{SchemaVersion: profilerevidence.TerminationSchemaVersion, Stage: test.stage, FailureReason: test.failure})
			if err != nil {
				t.Fatal(err)
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker-pod", Namespace: run.Namespace,
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: worker.Name, UID: worker.UID, Controller: ptr.To(true)}}},
				Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: runtimeContainerName,
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 3, Reason: "Error", Message: string(payload)}}}}}}
			kube := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).WithStatusSubresource(run).WithObjects(run, worker, load, pod).Build()
			finalizer, err := NewKubernetesExecutionFinalizer(kube, unusedStoreFactory)
			if err != nil {
				t.Fatal(err)
			}
			r, err := NewRunReconciler(kube, kube, (&recordingRunConfigResolver{}).Resolve, (&recordingExecutionJobs{}).Find, (&recordingExecutionJobs{}).Ensure, finalizer.Finalize, finalizer.RuntimeTermination)
			if err != nil {
				t.Fatal(err)
			}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}
			if _, err := r.Reconcile(ctx, request); err != nil {
				t.Fatal(err)
			}
			actual := &api.ProfilingRun{}
			if err := kube.Get(ctx, request.NamespacedName, actual); err != nil {
				t.Fatal(err)
			}
			termination := actual.Status.RuntimeTermination
			if termination == nil || termination.Stage != test.stage || termination.FailureReason != test.failure || termination.Class != test.class {
				t.Fatalf("load-first failure lost typed runtime evidence: %#v", termination)
			}
			for _, job := range []*batchv1.Job{worker, load} {
				if err := kube.Get(ctx, client.ObjectKeyFromObject(job), &batchv1.Job{}); !apierrors.IsNotFound(err) {
					t.Fatalf("failed load left active Job %s: %v", job.Name, err)
				}
			}
		})
	}
}

func TestLoadFirstFailureWaitsForRuntimeTerminationBeforeCleanup(t *testing.T) {
	ctx := context.Background()
	run := testRun()
	recordRunConfigAndJob(run)
	resolved := resolvedRunForPrepared(t, currentPreparedRun(t))
	worker := recordedJob(t, run, resolved, &batchv1.JobStatus{Active: 1})
	load := completedMeasurementLoad(run, worker)
	load.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
	kube := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).WithStatusSubresource(run).WithObjects(run, worker, load).Build()
	finalizer := &recordingRunEvidenceFinalizer{}
	r, err := NewRunReconciler(kube, kube, (&recordingRunConfigResolver{}).Resolve, (&recordingExecutionJobs{}).Find, (&recordingExecutionJobs{}).Ensure, finalizer.Finalize, finalizer.RuntimeTermination)
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}
	result, err := r.Reconcile(ctx, request)
	if err != nil || result.RequeueAfter <= 0 {
		t.Fatalf("missing runtime termination must retry: result=%+v err=%v", result, err)
	}
	for _, job := range []*batchv1.Job{worker, load} {
		if err := kube.Get(ctx, client.ObjectKeyFromObject(job), &batchv1.Job{}); err != nil {
			t.Fatalf("deleted %s before runtime evidence: %v", job.Name, err)
		}
	}
	actual := &api.ProfilingRun{}
	if err := kube.Get(ctx, request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	if condition := findRunCondition(actual.Status.Conditions, api.ProfilingRunConditionExecutionComplete); condition != nil && condition.Status == metav1.ConditionFalse {
		t.Fatalf("published terminal failure before runtime evidence: %+v", condition)
	}
	finalizer.termination = &api.ProfilingRunRuntimeTermination{Class: profilerevidence.FailureClassExpectedStop, Container: runtimeContainerName, Stage: profilerevidence.StageExecution, FailureReason: profilerevidence.FailureMemoryAllocation}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, request.NamespacedName, actual); err != nil || actual.Status.RuntimeTermination == nil || actual.Status.RuntimeTermination.FailureReason != profilerevidence.FailureMemoryAllocation {
		t.Fatalf("delayed runtime termination not retained: %+v, %v", actual.Status, err)
	}
}

func TestExecutionLostCleansMeasurementLoad(t *testing.T) {
	ctx := context.Background()
	run := testRun()
	recordRunConfigAndJob(run)
	worker := ownedJob(run, nil)
	load := completedMeasurementLoad(run, worker)
	kube := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).WithStatusSubresource(run).WithObjects(run, load).Build()
	finalizer := &recordingRunEvidenceFinalizer{}
	r, err := NewRunReconciler(kube, kube, (&recordingRunConfigResolver{}).Resolve, (&recordingExecutionJobs{}).Find, (&recordingExecutionJobs{}).Ensure, finalizer.Finalize, finalizer.RuntimeTermination)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(load), &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("ExecutionLost leaked load Job: %v", err)
	}
}

func TestMeasurementLoadUsesWorkerAbsoluteDeadlineAndResourceBounds(t *testing.T) {
	run := testRun()
	config := currentPreparedRun(t).RunConfig
	worker := ownedJob(run, nil)
	worker.Spec.ActiveDeadlineSeconds = ptr.To[int64](600)
	worker.CreationTimestamp = metav1.NewTime(time.Now().Add(-2 * time.Minute))
	start := metav1.NewTime(time.Now().Add(-time.Minute))
	worker.Status.StartTime = &start
	pod := &corev1.Pod{Spec: corev1.PodSpec{NodeName: config.TargetNode.Name}, Status: corev1.PodStatus{PodIP: "10.42.1.7"}}
	gateway := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "serving-entrypoint", Image: "gateway:test"}}}}
	load, err := buildMeasurementLoadJob(run, worker, pod, gateway, config, MeasurementLoadOptions{ServiceAccountName: "load"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := start.Add(600 * time.Second).UTC().Format(time.RFC3339Nano)
	if load.Spec.ActiveDeadlineSeconds == nil || *load.Spec.ActiveDeadlineSeconds <= 0 || *load.Spec.ActiveDeadlineSeconds >= *worker.Spec.ActiveDeadlineSeconds {
		t.Error("load restarted the measurement Job relative deadline")
	}
	container := load.Spec.Template.Spec.Containers[0]
	if !slices.Contains(container.Args, "--deadline="+deadline) {
		t.Errorf("load did not inherit worker absolute deadline %s: %v", deadline, container.Args)
	}
	for _, container := range append(load.Spec.Template.Spec.InitContainers, container) {
		for _, resources := range []corev1.ResourceList{container.Resources.Requests, container.Resources.Limits} {
			if _, exists := resources[corev1.ResourceCPU]; exists {
				t.Errorf("load container %s reserves or limits shared CPU: %+v", container.Name, container.Resources)
			}
			if resources.Memory().Cmp(resource.MustParse("512Mi")) != 0 {
				t.Errorf("unbounded load container %s: %+v", container.Name, container.Resources)
			}
		}
	}
	volume := load.Spec.Template.Spec.Volumes[0].EmptyDir
	if volume == nil || volume.SizeLimit == nil || volume.SizeLimit.Cmp(resource.MustParse("1Gi")) != 0 {
		t.Errorf("unbounded dataset volume: %+v", volume)
	}
	if load.Labels["chill.dacs.io/measurement-load"] != config.TargetNode.UID {
		t.Error("load is invisible to terminal-root handback selector")
	}
	gateway.Spec.Containers[0].Name = "unrelated"
	if _, err := buildMeasurementLoadJob(run, worker, pod, gateway, config, MeasurementLoadOptions{ServiceAccountName: "load"}); err == nil {
		t.Fatal("accepted a gateway without the native chill-server image")
	}
	gateway.Spec.Containers[0].Name = "serving-entrypoint"
	worker.Status.StartTime = nil
	if _, err := buildMeasurementLoadJob(run, worker, pod, gateway, config, MeasurementLoadOptions{ServiceAccountName: "load"}); err == nil {
		t.Fatal("invented a deadline without authoritative measurement start")
	}
}

func TestLoadFirstFailureDoesNotCleanUpWhenTerminationReadFails(t *testing.T) {
	ctx := context.Background()
	run := testRun()
	recordRunConfigAndJob(run)
	worker := recordedJob(t, run, resolvedRunForPrepared(t, currentPreparedRun(t)), &batchv1.JobStatus{Active: 1})
	load := completedMeasurementLoad(run, worker)
	load.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
	kube := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).WithStatusSubresource(run).WithObjects(run, worker, load).Build()
	want := errors.New("runtime Pod read failed")
	finalizer := &recordingRunEvidenceFinalizer{}
	r, err := NewRunReconciler(kube, kube, (&recordingRunConfigResolver{}).Resolve, (&recordingExecutionJobs{}).Find, (&recordingExecutionJobs{}).Ensure, finalizer.Finalize,
		func(context.Context, *api.ProfilingRun, *batchv1.Job) (*api.ProfilingRunRuntimeTermination, error) {
			return nil, want
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); !errors.Is(err, want) {
		t.Fatalf("termination read failure lost: %v", err)
	}
	for _, job := range []*batchv1.Job{worker, load} {
		if err := kube.Get(ctx, client.ObjectKeyFromObject(job), &batchv1.Job{}); err != nil {
			t.Fatalf("deleted %s after termination read error: %v", job.Name, err)
		}
	}
}
