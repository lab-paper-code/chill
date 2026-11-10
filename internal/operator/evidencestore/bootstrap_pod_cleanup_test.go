package evidencestore

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestReconcileBootstrapJobCleansSucceededPodsOnlyAfterCompletion(t *testing.T) {
	ctx := context.Background()
	scheme := runtimeTestScheme(t)
	system := runtimeTestSystem()
	instance := validInstance()
	job := buildBootstrapJob(instance, validConfig(), testConfigResourceVersion)
	job.UID = types.UID("bootstrap-job-uid")
	job.Status.Succeeded = 1
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionFalse}}
	if err := controllerutil.SetControllerReference(system, job, scheme); err != nil {
		t.Fatal(err)
	}
	owner := *metav1.NewControllerRef(job, batchv1.SchemeGroupVersion.WithKind("Job"))
	foreignOwner := owner
	foreignOwner.UID = "foreign-job-uid"
	wrongName := owner
	wrongName.Name = "another-job"
	wrongKind := owner
	wrongKind.Kind = "ReplicaSet"
	wrongVersion := owner
	wrongVersion.APIVersion = "apps/v1"
	nonController := owner
	nonController.Controller = ptr.To(false)
	cases := []struct {
		name      string
		phase     corev1.PodPhase
		owner     metav1.OwnerReference
		namespace string
		deleted   bool
	}{
		{name: "succeeded", phase: corev1.PodSucceeded, owner: owner, deleted: true},
		{name: "failed", phase: corev1.PodFailed, owner: owner},
		{name: "running", phase: corev1.PodRunning, owner: owner},
		{name: "pending", phase: corev1.PodPending, owner: owner},
		{name: "foreign-uid", phase: corev1.PodSucceeded, owner: foreignOwner},
		{name: "wrong-name", phase: corev1.PodSucceeded, owner: wrongName},
		{name: "wrong-kind", phase: corev1.PodSucceeded, owner: wrongKind},
		{name: "wrong-version", phase: corev1.PodSucceeded, owner: wrongVersion},
		{name: "non-controller", phase: corev1.PodSucceeded, owner: nonController},
		{name: "orphan", phase: corev1.PodSucceeded},
		{name: "other-namespace", phase: corev1.PodSucceeded, owner: owner, namespace: "other"},
	}
	objects := []client.Object{system, job}
	for i := range cases {
		test := &cases[i]
		if test.namespace == "" {
			test.namespace = instance.Namespace
		}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: test.namespace, Name: test.name, UID: types.UID(test.name)},
			Status:     corev1.PodStatus{Phase: test.phase},
		}
		if test.owner.UID != "" {
			pod.OwnerReferences = []metav1.OwnerReference{test.owner}
		}
		objects = append(objects, pod)
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(job).WithObjects(objects...).Build()

	state, requeue, err := reconcileBootstrapJob(ctx, k8sClient, scheme, system, instance, validConfig(), testConfigResourceVersion)
	if err != nil || requeue || state != BootstrapStateRunning {
		t.Fatalf("before completion = %q, %v, %v", state, requeue, err)
	}
	for _, test := range cases {
		key := client.ObjectKey{Namespace: test.namespace, Name: test.name}
		if err := k8sClient.Get(ctx, key, &corev1.Pod{}); err != nil {
			t.Fatalf("Pod %s removed before JobComplete was consumed: %v", key, err)
		}
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job); err != nil {
		t.Fatal(err)
	}
	job.Status.Conditions[0].Status = corev1.ConditionTrue
	if err := k8sClient.Status().Update(ctx, job); err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		state, requeue, err = reconcileBootstrapJob(ctx, k8sClient, scheme, system, instance, validConfig(), testConfigResourceVersion)
		if err != nil || requeue || state != BootstrapStateSucceeded {
			t.Fatalf("completed reconcile %d = %q, %v, %v", attempt, state, requeue, err)
		}
		for _, test := range cases {
			key := client.ObjectKey{Namespace: test.namespace, Name: test.name}
			err := k8sClient.Get(ctx, key, &corev1.Pod{})
			if test.deleted {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("consumed Pod %s still exists: %v", key, err)
				}
			} else if err != nil {
				t.Fatalf("retained Pod %s was removed: %v", key, err)
			}
		}
		retained := &batchv1.Job{}
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(job), retained); err != nil {
			t.Fatalf("bootstrap attestation Job was removed: %v", err)
		}
		if retained.UID != job.UID || observeBootstrapJob(retained) != BootstrapStateSucceeded {
			t.Fatalf("bootstrap attestation changed: %#v", retained)
		}
	}
}

func TestReconcileBootstrapJobDoesNotCleanUnconsumedPods(t *testing.T) {
	for _, name := range []string{"failed", "foreign-job", "stale-contract", "deleting", "missing-job-uid"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtimeTestScheme(t)
			system := runtimeTestSystem()
			instance := validInstance()
			job := buildBootstrapJob(instance, validConfig(), testConfigResourceVersion)
			job.UID = "bootstrap-job-uid"
			job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
			if err := controllerutil.SetControllerReference(system, job, scheme); err != nil {
				t.Fatal(err)
			}
			wantState := BootstrapStateWaiting
			wantRequeue := false
			switch name {
			case "failed":
				job.Status.Conditions[0].Type = batchv1.JobFailed
				wantState = BootstrapStateFailed
			case "foreign-job":
				job.OwnerReferences[0].UID = "foreign-system-uid"
			case "stale-contract":
				job.Spec.Template.Spec.Containers[0].Image = "stale-image"
			case "deleting":
				job.DeletionTimestamp = ptr.To(metav1.Now())
				job.Finalizers = []string{"test.chill.io/retain"}
				wantState = BootstrapStateRunning
				wantRequeue = true
			case "missing-job-uid":
				job.UID = ""
				wantState = BootstrapStateSucceeded
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:       instance.Namespace,
					Name:            "succeeded",
					UID:             "pod-uid",
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(job, batchv1.SchemeGroupVersion.WithKind("Job"))},
				},
				Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
			}
			k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, job, pod).Build()
			state, requeue, err := reconcileBootstrapJob(ctx, k8sClient, scheme, system, instance, validConfig(), testConfigResourceVersion)
			if name == "foreign-job" {
				if err == nil {
					t.Fatal("foreign bootstrap Job did not return ownership error")
				}
			} else if err != nil || state != wantState || requeue != wantRequeue {
				t.Fatalf("reconcileBootstrapJob() = %q, %v, %v", state, requeue, err)
			}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{}); err != nil {
				t.Fatalf("unconsumed Pod was removed: %v", err)
			}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(job), &batchv1.Job{}); err != nil {
				t.Fatalf("bootstrap Job was removed: %v", err)
			}
		})
	}
}
