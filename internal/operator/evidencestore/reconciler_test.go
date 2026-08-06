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
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func TestReconcileCreatesRetainedAndReplaceableResourcesInOrder(t *testing.T) {
	ctx := context.Background()
	scheme := runtimeTestScheme(t)
	system := enabledEvidenceStoreSystem()
	configMap := evidenceStoreConfigMap(t, system, validConfig())
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, configMap).Build()
	reconciler := &Reconciler{
		Client:  k8sClient,
		Scheme:  scheme,
		Options: Options{SystemName: system.Name, Namespace: "operator-system"},
	}

	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: system.Name}})
	if err != nil || result.Requeue {
		t.Fatalf("Reconcile() result = %#v, error = %v", result, err)
	}
	instance := resolveInstance(system, reconciler.Options)
	secret := &corev1.Secret{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: instance.Namespace, Name: instance.CredentialsSecretName}, secret); err != nil {
		t.Fatal(err)
	}
	claim := &corev1.PersistentVolumeClaim{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: instance.Namespace, Name: instance.PersistentVolumeClaimName}, claim); err != nil {
		t.Fatal(err)
	}
	if len(secret.OwnerReferences) != 0 || len(claim.OwnerReferences) != 0 {
		t.Fatalf("retained owner references: Secret=%#v PVC=%#v", secret.OwnerReferences, claim.OwnerReferences)
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
		t.Fatalf("runtime owner references: Deployment=%#v Service=%#v", deployment.OwnerReferences, service.OwnerReferences)
	}
	job := &batchv1.Job{}
	err = k8sClient.Get(ctx, client.ObjectKey{Namespace: instance.Namespace, Name: instance.BootstrapJobName}, job)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("bootstrap Job exists before Deployment availability: %v", err)
	}
}

func TestReconcileStopsBeforeMutationWhenConfigIsMissing(t *testing.T) {
	ctx := context.Background()
	scheme := runtimeTestScheme(t)
	system := enabledEvidenceStoreSystem()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system).Build()
	reconciler := &Reconciler{Client: k8sClient, Scheme: scheme, Options: Options{Namespace: "chill-system"}}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: system.Name}}); err == nil {
		t.Fatal("Reconcile() error = nil, want missing ConfigMap error")
	}
	instance := resolveInstance(system, reconciler.Options)
	for _, object := range []client.Object{
		&corev1.Secret{},
		&corev1.PersistentVolumeClaim{},
		&appsv1.Deployment{},
		&corev1.Service{},
		&batchv1.Job{},
	} {
		key := resourceKey(instance, object)
		if err := k8sClient.Get(ctx, key, object); !apierrors.IsNotFound(err) {
			t.Fatalf("%T was mutated before config validation: %v", object, err)
		}
	}
}

func TestReconcileDisableDeletesOnlyReplaceableResources(t *testing.T) {
	ctx := context.Background()
	scheme := runtimeTestScheme(t)
	system := enabledEvidenceStoreSystem()
	instance := resolveInstance(system, Options{Namespace: "chill-system"})
	config := validConfig()
	secret, err := newCredentialsSecret(instance, zeroCredentialsReader())
	if err != nil {
		t.Fatal(err)
	}
	claim := buildPersistentVolumeClaim(instance, config)
	deployment := buildDeployment(instance, config)
	service := buildService(instance)
	job := buildBootstrapJob(instance, config, testConfigResourceVersion)
	for _, object := range []client.Object{deployment, service, job} {
		if err := controllerutil.SetControllerReference(system, object, scheme); err != nil {
			t.Fatal(err)
		}
	}
	system.Spec.EvidenceStore.Enabled = false
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		system, secret, claim, deployment, service, job,
	).Build()
	reconciler := &Reconciler{Client: k8sClient, Scheme: scheme, Options: Options{Namespace: "chill-system"}}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: system.Name}}); err != nil {
		t.Fatal(err)
	}
	for _, object := range []client.Object{deployment, service, job} {
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(object), object); !apierrors.IsNotFound(err) {
			t.Fatalf("replaceable %T still exists: %v", object, err)
		}
	}
	for _, object := range []client.Object{secret, claim} {
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
			t.Fatalf("retained %T missing after disable: %v", object, err)
		}
	}
}

func TestMapResourceToSystemMapsOnlyConfiguredEvidenceStoreResources(t *testing.T) {
	ctx := context.Background()
	scheme := runtimeTestScheme(t)
	system := enabledEvidenceStoreSystem()
	options := Options{SystemName: system.Name, Namespace: "operator-system"}
	instance := resolveInstance(system, options)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system).Build()
	reconciler := &Reconciler{Client: k8sClient, Scheme: scheme, Options: options}

	for _, object := range []client.Object{
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: instance.Namespace, Name: instance.ConfigMapName}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: instance.Namespace, Name: instance.CredentialsSecretName}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: instance.Namespace, Name: instance.PersistentVolumeClaimName}},
	} {
		requests := reconciler.mapResourceToSystem(ctx, object)
		if len(requests) != 1 || requests[0].Name != system.Name {
			t.Fatalf("mapResourceToSystem(%T %s) = %#v", object, object.GetName(), requests)
		}
	}

	for _, object := range []client.Object{
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "another", Name: instance.ConfigMapName}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: instance.Namespace, Name: "unrelated"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: instance.Namespace, Name: instance.CredentialsSecretName}},
	} {
		if requests := reconciler.mapResourceToSystem(ctx, object); len(requests) != 0 {
			t.Fatalf("mapResourceToSystem(%T %s) = %#v, want no request", object, object.GetName(), requests)
		}
	}
}

func enabledEvidenceStoreSystem() *edgev1alpha1.ChillSystem {
	return &edgev1alpha1.ChillSystem{
		ObjectMeta: metav1.ObjectMeta{Name: testSystemName, UID: types.UID("evidence-system-uid")},
		Spec: edgev1alpha1.ChillSystemSpec{
			ManagementNamespace: "chill-system",
			EvidenceStore: edgev1alpha1.ChillEvidenceStoreSpec{
				Enabled:   true,
				ConfigRef: corev1.LocalObjectReference{Name: "research-evidence-store-config"},
			},
		},
	}
}

func evidenceStoreConfigMap(t *testing.T, system *edgev1alpha1.ChillSystem, config Config) *corev1.ConfigMap {
	t.Helper()
	raw, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: system.Spec.ManagementNamespace,
			Name:      system.Spec.EvidenceStore.ConfigRef.Name,
		},
		Data: map[string]string{DefaultConfigKey: string(raw)},
	}
}

func resourceKey(instance Instance, object client.Object) types.NamespacedName {
	key := types.NamespacedName{Namespace: instance.Namespace}
	switch object.(type) {
	case *corev1.Secret:
		key.Name = instance.CredentialsSecretName
	case *corev1.PersistentVolumeClaim:
		key.Name = instance.PersistentVolumeClaimName
	case *appsv1.Deployment:
		key.Name = instance.DeploymentName
	case *corev1.Service:
		key.Name = instance.ServiceName
	case *batchv1.Job:
		key.Name = instance.BootstrapJobName
	}
	return key
}

func zeroCredentialsReader() *strings.Reader {
	return strings.NewReader(strings.Repeat("x", accessKeyBytes+secretKeyBytes))
}
