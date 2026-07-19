package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func TestKubernetesEvidenceStoreResolverResolvesNonSecretDeliveryBinding(t *testing.T) {
	resolver := newEvidenceStoreResolver(t, validEvidenceStoreObjects()...)
	binding, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if binding.Store != "chill-evidence-store" ||
		binding.Endpoint != "http://chill-evidence-store.chill-system.svc:9000" ||
		binding.Bucket != "chill-profiling-evidence" || binding.Region != "us-east-1" ||
		binding.CredentialsSecretName != "chill-evidence-store-credentials" {
		t.Fatalf("binding = %#v", binding)
	}
}

func TestKubernetesEvidenceStoreResolverRejectsUnavailableOrInvalidBinding(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*edgev1alpha1.ChillSystem, *corev1.ConfigMap)
		want   string
	}{
		{
			name: "disabled",
			mutate: func(system *edgev1alpha1.ChillSystem, _ *corev1.ConfigMap) {
				system.Spec.EvidenceStore.Enabled = false
			},
			want: "does not enable",
		},
		{
			name: "namespace drift",
			mutate: func(system *edgev1alpha1.ChillSystem, _ *corev1.ConfigMap) {
				system.Spec.ManagementNamespace = "another-system"
			},
			want: "does not match",
		},
		{
			name: "invalid namespace",
			mutate: func(system *edgev1alpha1.ChillSystem, _ *corev1.ConfigMap) {
				system.Spec.ManagementNamespace = " chill-system "
			},
			want: "management namespace is invalid",
		},
		{
			name: "invalid config reference",
			mutate: func(system *edgev1alpha1.ChillSystem, _ *corev1.ConfigMap) {
				system.Spec.EvidenceStore.ConfigRef.Name = " "
			},
			want: "config reference is invalid",
		},
		{
			name: "missing config key",
			mutate: func(_ *edgev1alpha1.ChillSystem, config *corev1.ConfigMap) {
				delete(config.Data, "config.yaml")
			},
			want: "missing key",
		},
		{
			name: "malformed config",
			mutate: func(_ *edgev1alpha1.ChillSystem, config *corev1.ConfigMap) {
				config.Data["config.yaml"] = "bucket: ["
			},
			want: "decode Evidence Store",
		},
		{
			name: "blank required delivery field",
			mutate: func(_ *edgev1alpha1.ChillSystem, config *corev1.ConfigMap) {
				config.Data["config.yaml"] = "bucket: chill-profiling-evidence\nregion: ' '"
			},
			want: "delivery region is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			objects := validEvidenceStoreObjects()
			system := objects[0].(*edgev1alpha1.ChillSystem)
			config := objects[1].(*corev1.ConfigMap)
			test.mutate(system, config)
			resolver := newEvidenceStoreResolver(t, objects...)
			if _, err := resolver.Resolve(context.Background()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestNewKubernetesEvidenceStoreResolverValidatesDependencies(t *testing.T) {
	if _, err := NewKubernetesEvidenceStoreResolver(nil, EvidenceStoreResolverOptions{}); err == nil {
		t.Fatal("nil reader accepted")
	}
	reader := fake.NewClientBuilder().Build()
	for _, options := range []EvidenceStoreResolverOptions{
		{Namespace: "chill-system"},
		{SystemName: "chill"},
		{SystemName: " chill ", Namespace: "chill-system"},
	} {
		if _, err := NewKubernetesEvidenceStoreResolver(reader, options); err == nil {
			t.Fatalf("invalid options accepted: %#v", options)
		}
	}
}

func newEvidenceStoreResolver(t *testing.T, objects ...client.Object) *KubernetesEvidenceStoreResolver {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	resolver, err := NewKubernetesEvidenceStoreResolver(reader, EvidenceStoreResolverOptions{
		SystemName: "chill",
		Namespace:  "chill-system",
	})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func validEvidenceStoreObjects() []client.Object {
	system := &edgev1alpha1.ChillSystem{ObjectMeta: metav1.ObjectMeta{Name: "chill"}}
	system.Spec.ManagementNamespace = "chill-system"
	system.Spec.EvidenceStore.Enabled = true
	system.Spec.EvidenceStore.ConfigRef.Name = "chill-evidence-store-config"
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Namespace: "chill-system",
		Name:      "chill-evidence-store-config",
	}}
	config.Data = map[string]string{"config.yaml": `
image: registry.example/evidence@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
bucket: chill-profiling-evidence
region: us-east-1
storageSize: 1Gi
`}
	return []client.Object{system, config}
}
