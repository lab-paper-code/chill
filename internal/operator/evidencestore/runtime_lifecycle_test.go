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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func TestApplyRuntimeResourcesCreatesOwnedDesiredState(t *testing.T) {
	ctx := context.Background()
	scheme := runtimeTestScheme(t)
	system := runtimeTestSystem()
	instance := validInstance()
	config := validConfig()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system).Build()

	requeue, err := applyRuntimeResources(ctx, k8sClient, scheme, system, instance, config)
	if err != nil || requeue {
		t.Fatalf("applyRuntimeResources() requeue = %v, error = %v", requeue, err)
	}
	deployment := &appsv1.Deployment{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: instance.Namespace, Name: instance.DeploymentName}, deployment); err != nil {
		t.Fatal(err)
	}
	service := &corev1.Service{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: instance.Namespace, Name: instance.ServiceName}, service); err != nil {
		t.Fatal(err)
	}
	if !metav1.IsControlledBy(deployment, system) || !metav1.IsControlledBy(service, system) {
		t.Fatalf("owner references: Deployment=%#v Service=%#v", deployment.OwnerReferences, service.OwnerReferences)
	}
}

func TestApplyRuntimeResourcesRequeuesAfterImmutableSelectorDrift(t *testing.T) {
	ctx := context.Background()
	scheme := runtimeTestScheme(t)
	system := runtimeTestSystem()
	instance := validInstance()
	deployment := buildDeployment(instance, validConfig())
	deployment.Spec.Selector.MatchLabels["drifted"] = "true"
	if err := controllerutil.SetControllerReference(system, deployment, scheme); err != nil {
		t.Fatal(err)
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, deployment).Build()

	requeue, err := applyRuntimeResources(ctx, k8sClient, scheme, system, instance, validConfig())
	if err != nil || !requeue {
		t.Fatalf("applyRuntimeResources() requeue = %v, error = %v", requeue, err)
	}
	err = k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), &appsv1.Deployment{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("drifted Deployment still exists: %v", err)
	}
	err = k8sClient.Get(ctx, types.NamespacedName{Namespace: instance.Namespace, Name: instance.ServiceName}, &corev1.Service{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("Service was applied before Deployment recreation: %v", err)
	}
}

func TestApplyRuntimeResourcesRejectsForeignSameNamedResource(t *testing.T) {
	for _, kind := range []string{"Deployment", "Service"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtimeTestScheme(t)
			system := runtimeTestSystem()
			instance := validInstance()
			var foreign client.Object
			if kind == "Deployment" {
				foreign = buildDeployment(instance, validConfig())
			} else {
				foreign = buildService(instance)
			}
			k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, foreign).Build()

			_, err := applyRuntimeResources(ctx, k8sClient, scheme, system, instance, validConfig())
			if err == nil || !strings.Contains(err.Error(), "exists without a controller reference") {
				t.Fatalf("applyRuntimeResources() error = %v, want ownership error", err)
			}
			deploymentErr := k8sClient.Get(
				ctx,
				types.NamespacedName{Namespace: instance.Namespace, Name: instance.DeploymentName},
				&appsv1.Deployment{},
			)
			serviceErr := k8sClient.Get(
				ctx,
				types.NamespacedName{Namespace: instance.Namespace, Name: instance.ServiceName},
				&corev1.Service{},
			)
			if kind == "Deployment" {
				if deploymentErr != nil || !apierrors.IsNotFound(serviceErr) {
					t.Fatalf("foreign Deployment state: deployment=%v service=%v", deploymentErr, serviceErr)
				}
			} else if serviceErr != nil || !apierrors.IsNotFound(deploymentErr) {
				t.Fatalf("foreign Service state: deployment=%v service=%v", deploymentErr, serviceErr)
			}
		})
	}
}

func TestDeleteRuntimeResourcesRetainsSecretAndPersistentVolumeClaim(t *testing.T) {
	ctx := context.Background()
	scheme := runtimeTestScheme(t)
	system := runtimeTestSystem()
	instance := validInstance()
	config := validConfig()
	secret, err := newCredentialsSecret(instance, strings.NewReader(strings.Repeat("x", accessKeyBytes+secretKeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	claim := buildPersistentVolumeClaim(instance, config)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, secret, claim).Build()
	if _, err := applyRuntimeResources(ctx, k8sClient, scheme, system, instance, config); err != nil {
		t.Fatal(err)
	}

	if err := deleteRuntimeResources(ctx, k8sClient, system, instance); err != nil {
		t.Fatal(err)
	}
	for _, object := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}} {
		key := types.NamespacedName{Namespace: instance.Namespace, Name: instance.ServiceName}
		if _, ok := object.(*appsv1.Deployment); ok {
			key.Name = instance.DeploymentName
		}
		if err := k8sClient.Get(ctx, key, object); !apierrors.IsNotFound(err) {
			t.Fatalf("replaceable %T still exists: %v", object, err)
		}
	}
	for _, object := range []client.Object{&corev1.Secret{}, &corev1.PersistentVolumeClaim{}} {
		key := types.NamespacedName{Namespace: instance.Namespace, Name: instance.CredentialsSecretName}
		if _, ok := object.(*corev1.PersistentVolumeClaim); ok {
			key.Name = instance.PersistentVolumeClaimName
		}
		if err := k8sClient.Get(ctx, key, object); err != nil {
			t.Fatalf("retained %T missing: %v", object, err)
		}
	}
}

func runtimeTestSystem() *edgev1alpha1.ChillSystem {
	return &edgev1alpha1.ChillSystem{
		ObjectMeta: metav1.ObjectMeta{Name: testSystemName, UID: types.UID("test-system-uid")},
	}
}

func runtimeTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme,
		appsv1.AddToScheme,
		batchv1.AddToScheme,
		edgev1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}
