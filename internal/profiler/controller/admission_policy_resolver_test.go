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
			Namespace: testSystemNamespace, Name: "chill-profiler-facts", UID: testFactsUID, ResourceVersion: "19",
		},
		Data: map[string]string{"admission-policies.yaml": controllerPolicyCatalog},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap).Build()
	resolver, err := NewKubernetesAdmissionPolicyResolver(reader, AdmissionPolicyResolverOptions{
		Namespace: testSystemNamespace, ConfigMapName: "chill-profiler-facts", ConfigMapKey: "admission-policies.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}

	frozen, err := resolver.Resolve(context.Background(), "operator-meter-policy")
	if err != nil {
		t.Fatal(err)
	}
	source, policy, err := frozen.Records()
	if err != nil {
		t.Fatal(err)
	}
	if policy.MinimumSamples != 7 || policy.MaximumGapSeconds != 2.5 ||
		source.Kind != profileradmission.ConfigMapPolicySourceKind || source.UID != testFactsUID ||
		source.ResourceVersion != "19" {
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
			Namespace: testSystemNamespace, Name: "facts", UID: testFactsUID, ResourceVersion: "1",
		},
		BinaryData: map[string][]byte{"admission-policies.yaml": []byte(controllerPolicyCatalog)},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap).Build()
	resolver, err := NewKubernetesAdmissionPolicyResolver(reader, AdmissionPolicyResolverOptions{
		Namespace: testSystemNamespace, ConfigMapName: "facts", ConfigMapKey: "admission-policies.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), "operator-meter-policy"); err == nil ||
		!strings.Contains(err.Error(), "text data") {
		t.Fatalf("binary policy source error = %v", err)
	} else if reason, terminal := terminalFreezeReason(err); !terminal || reason != freezeReasonAdmissionPolicyInvalid {
		t.Fatalf("binary policy source classification = %q, terminal=%t", reason, terminal)
	}

	malformedConfigMap := configMap.DeepCopy()
	malformedConfigMap.BinaryData = nil
	malformedConfigMap.Data = map[string]string{"admission-policies.yaml": "policies: ["}
	malformed, _ := NewKubernetesAdmissionPolicyResolver(
		fake.NewClientBuilder().WithScheme(scheme).WithObjects(malformedConfigMap).Build(),
		AdmissionPolicyResolverOptions{
			Namespace: testSystemNamespace, ConfigMapName: "facts", ConfigMapKey: "admission-policies.yaml",
		},
	)
	if _, err := malformed.Resolve(context.Background(), "operator-meter-policy"); err == nil {
		t.Fatal("malformed admission-policy catalog was accepted")
	} else if reason, terminal := terminalFreezeReason(err); !terminal || reason != freezeReasonAdmissionPolicyInvalid {
		t.Fatalf("malformed policy classification = %q, terminal=%t", reason, terminal)
	}

	missingReader := fake.NewClientBuilder().WithScheme(scheme).Build()
	missing, _ := NewKubernetesAdmissionPolicyResolver(missingReader, AdmissionPolicyResolverOptions{
		Namespace: testSystemNamespace, ConfigMapName: "missing", ConfigMapKey: "admission-policies.yaml",
	})
	if _, err := missing.Resolve(context.Background(), "operator-meter-policy"); err == nil {
		t.Fatal("missing admission-policy ConfigMap was accepted")
	} else if reason, terminal := terminalFreezeReason(err); terminal {
		t.Fatalf("missing ConfigMap must remain retryable, got terminal reason %q", reason)
	}
}

func TestKubernetesAdmissionPolicyResolverClassifiesUnknownPolicyAsTerminal(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testSystemNamespace, Name: "facts", UID: testFactsUID, ResourceVersion: "1",
		},
		Data: map[string]string{"admission-policies.yaml": controllerPolicyCatalog},
	}
	resolver, err := NewKubernetesAdmissionPolicyResolver(
		fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap).Build(),
		AdmissionPolicyResolverOptions{
			Namespace: testSystemNamespace, ConfigMapName: "facts", ConfigMapKey: "admission-policies.yaml",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(context.Background(), "typo-v9")
	reason, terminal := terminalFreezeReason(err)
	if err == nil || !terminal || reason != freezeReasonAdmissionPolicyInvalid ||
		!strings.Contains(err.Error(), "is not configured") {
		t.Fatalf("unknown policy error = %v, reason=%q terminal=%t", err, reason, terminal)
	}
}
