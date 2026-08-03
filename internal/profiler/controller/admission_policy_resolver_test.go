package controller

import (
	"context"
	"strings"
	"testing"

	profileradmission "github.com/lab-paper-code/chill/internal/profiler/admission"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const controllerPolicyCatalog = `
schemaVersion: chill.dacs.io/profiler-admission-policies.v1alpha1
policies:
  - name: operator-meter-policy
    workloadMode: saturated-serial-fixed-batch
    minimumSamples: 7
    maximumGapSeconds: 2.5
    maximumBoundaryDistanceSeconds: 1.25
    minimumCoverage: 0.97
    allowReceiptTimestamps: true
    rejectConcurrentCHILLWorkloads: true
    rejectExternalWorkloadChanges: true
`

func TestKubernetesAdmissionPolicyResolverFreezesExactConfigMapSource(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "chill-system", Name: "chill-profiler-facts", UID: "facts-uid", ResourceVersion: "19",
		},
		Data: map[string]string{"admission-policies.yaml": controllerPolicyCatalog},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap).Build()
	resolver, err := NewKubernetesAdmissionPolicyResolver(reader, AdmissionPolicyResolverOptions{
		Namespace: "chill-system", ConfigMapName: "chill-profiler-facts", ConfigMapKey: "admission-policies.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}

	frozen, err := resolver.Resolve(context.Background(), "operator-meter-policy")
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Policy.MinimumSamples != 7 || frozen.Policy.MaximumGapSeconds != 2.5 ||
		frozen.Source.Kind != profileradmission.ConfigMapPolicySourceKind || frozen.Source.UID != "facts-uid" ||
		frozen.Source.ResourceVersion != "19" || !strings.HasPrefix(frozen.Source.EntryContentDigest, "sha256:") {
		t.Fatalf("resolved frozen policy = %#v", frozen)
	}
}

func TestKubernetesAdmissionPolicyResolverFailsClosed(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "chill-system", Name: "facts", UID: "facts-uid", ResourceVersion: "1",
		},
		BinaryData: map[string][]byte{"admission-policies.yaml": []byte(controllerPolicyCatalog)},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap).Build()
	resolver, err := NewKubernetesAdmissionPolicyResolver(reader, AdmissionPolicyResolverOptions{
		Namespace: "chill-system", ConfigMapName: "facts", ConfigMapKey: "admission-policies.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), "operator-meter-policy"); err == nil ||
		!strings.Contains(err.Error(), "text data") {
		t.Fatalf("binary policy source error = %v", err)
	}

	missingReader := fake.NewClientBuilder().WithScheme(scheme).Build()
	missing, _ := NewKubernetesAdmissionPolicyResolver(missingReader, AdmissionPolicyResolverOptions{
		Namespace: "chill-system", ConfigMapName: "missing", ConfigMapKey: "admission-policies.yaml",
	})
	if _, err := missing.Resolve(context.Background(), "operator-meter-policy"); err == nil {
		t.Fatal("missing admission-policy ConfigMap was accepted")
	}
}
