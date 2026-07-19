package evidencestore

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func TestReconcileBootstrapJobWaitsForAvailableDeployment(t *testing.T) {
	ctx := context.Background()
	scheme := runtimeTestScheme(t)
	system := runtimeTestSystem()
	instance := validInstance()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system).Build()

	state, requeue, err := reconcileBootstrapJob(ctx, k8sClient, scheme, system, instance, validConfig(), testConfigDigest)
	if err != nil || requeue || state != BootstrapStateWaiting {
		t.Fatalf("reconcileBootstrapJob() = %q, %v, %v", state, requeue, err)
	}
	err = k8sClient.Get(ctx, client.ObjectKey{Namespace: instance.Namespace, Name: instance.BootstrapJobName}, &batchv1.Job{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("bootstrap Job exists before Deployment availability: %v", err)
	}
}

func TestReconcileBootstrapJobCreatesAfterDeploymentAvailable(t *testing.T) {
	ctx := context.Background()
	scheme := runtimeTestScheme(t)
	system := runtimeTestSystem()
	instance := validInstance()
	deployment := availableDeployment(t, scheme, system, instance)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, deployment).Build()

	state, requeue, err := reconcileBootstrapJob(ctx, k8sClient, scheme, system, instance, validConfig(), testConfigDigest)
	if err != nil || requeue || state != BootstrapStateRunning {
		t.Fatalf("reconcileBootstrapJob() = %q, %v, %v", state, requeue, err)
	}
	job := &batchv1.Job{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: instance.Namespace, Name: instance.BootstrapJobName}, job); err != nil {
		t.Fatal(err)
	}
	if !metav1.IsControlledBy(job, system) {
		t.Fatalf("bootstrap Job ownerReferences = %#v", job.OwnerReferences)
	}
}

func TestReconcileBootstrapJobKeepsTerminalStateWithoutAutomaticRetry(t *testing.T) {
	for _, test := range []struct {
		name      string
		condition batchv1.JobConditionType
		want      BootstrapState
	}{
		{name: "complete", condition: batchv1.JobComplete, want: BootstrapStateSucceeded},
		{name: "failed", condition: batchv1.JobFailed, want: BootstrapStateFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtimeTestScheme(t)
			system := runtimeTestSystem()
			instance := validInstance()
			job, err := buildBootstrapJob(instance, validConfig(), testConfigDigest)
			if err != nil {
				t.Fatal(err)
			}
			job.Status.Conditions = []batchv1.JobCondition{{Type: test.condition, Status: corev1.ConditionTrue}}
			if err := controllerutil.SetControllerReference(system, job, scheme); err != nil {
				t.Fatal(err)
			}
			k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, job).Build()

			state, requeue, err := reconcileBootstrapJob(ctx, k8sClient, scheme, system, instance, validConfig(), testConfigDigest)
			if err != nil || requeue || state != test.want {
				t.Fatalf("reconcileBootstrapJob() = %q, %v, %v", state, requeue, err)
			}
			retained := &batchv1.Job{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(job), retained); err != nil {
				t.Fatalf("terminal Job was removed: %v", err)
			}
		})
	}
}

func TestReconcileBootstrapJobReplacesStaleInputsOnlyAfterDeploymentAvailable(t *testing.T) {
	ctx := context.Background()
	scheme := runtimeTestScheme(t)
	system := runtimeTestSystem()
	instance := validInstance()
	oldConfig := validConfig()
	job, err := buildBootstrapJob(instance, oldConfig, testConfigDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := controllerutil.SetControllerReference(system, job, scheme); err != nil {
		t.Fatal(err)
	}
	newConfig := oldConfig
	newConfig.Bucket = "updated-evidence-bucket"
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, job).Build()

	state, requeue, err := reconcileBootstrapJob(ctx, k8sClient, scheme, system, instance, newConfig, testConfigDigest)
	if err != nil || requeue || state != BootstrapStateWaiting {
		t.Fatalf("unavailable replacement = %q, %v, %v", state, requeue, err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(job), &batchv1.Job{}); err != nil {
		t.Fatalf("stale Job removed before Deployment became available: %v", err)
	}

	deployment := availableDeployment(t, scheme, system, instance)
	if err := k8sClient.Create(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	state, requeue, err = reconcileBootstrapJob(ctx, k8sClient, scheme, system, instance, newConfig, testConfigDigest)
	if err != nil || !requeue || state != BootstrapStateRunning {
		t.Fatalf("available replacement = %q, %v, %v", state, requeue, err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(job), &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("stale Job still exists: %v", err)
	}
}

func TestReconcileBootstrapJobReplacesStaleConfigAttestation(t *testing.T) {
	ctx := context.Background()
	scheme := runtimeTestScheme(t)
	system := runtimeTestSystem()
	instance := validInstance()
	config := validConfig()
	job, err := buildBootstrapJob(instance, config, testConfigDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := controllerutil.SetControllerReference(system, job, scheme); err != nil {
		t.Fatal(err)
	}
	deployment := availableDeployment(t, scheme, system, instance)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, job, deployment).Build()

	state, requeue, err := reconcileBootstrapJob(
		ctx, k8sClient, scheme, system, instance, config, changedTestConfigDigest,
	)
	if err != nil || !requeue || state != BootstrapStateRunning {
		t.Fatalf("config attestation replacement = %q, %v, %v", state, requeue, err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(job), &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("stale attestation Job still exists: %v", err)
	}
}

func TestReconcileBootstrapJobRejectsForeignSameNamedJob(t *testing.T) {
	ctx := context.Background()
	scheme := runtimeTestScheme(t)
	system := runtimeTestSystem()
	instance := validInstance()
	job, err := buildBootstrapJob(instance, validConfig(), testConfigDigest)
	if err != nil {
		t.Fatal(err)
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, job).Build()

	_, _, err = reconcileBootstrapJob(ctx, k8sClient, scheme, system, instance, validConfig(), testConfigDigest)
	if err == nil || !strings.Contains(err.Error(), "exists without a controller reference") {
		t.Fatalf("reconcileBootstrapJob() error = %v, want ownership error", err)
	}
}

func TestDeleteOwnedBootstrapJobLeavesForeignJob(t *testing.T) {
	ctx := context.Background()
	scheme := runtimeTestScheme(t)
	system := runtimeTestSystem()
	instance := validInstance()
	job, err := buildBootstrapJob(instance, validConfig(), testConfigDigest)
	if err != nil {
		t.Fatal(err)
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, job).Build()

	if err := deleteOwnedBootstrapJob(ctx, k8sClient, system, instance); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(job), &batchv1.Job{}); err != nil {
		t.Fatalf("foreign bootstrap Job was deleted: %v", err)
	}

	if err := controllerutil.SetControllerReference(system, job, scheme); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Update(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := deleteOwnedBootstrapJob(ctx, k8sClient, system, instance); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(job), &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("owned bootstrap Job still exists: %v", err)
	}
}

func availableDeployment(
	t *testing.T,
	scheme *runtime.Scheme,
	system *edgev1alpha1.ChillSystem,
	instance Instance,
) *appsv1.Deployment {
	t.Helper()
	deployment := buildDeployment(instance, validConfig())
	deployment.Generation = 2
	deployment.Status.ObservedGeneration = 2
	deployment.Status.AvailableReplicas = 1
	deployment.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
	}
	if err := controllerutil.SetControllerReference(system, deployment, scheme); err != nil {
		t.Fatal(err)
	}
	return deployment
}
