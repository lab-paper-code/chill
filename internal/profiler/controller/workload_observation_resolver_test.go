package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/profilingrun"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestKubernetesWorkloadObservationResolverReturnsValidatedInstallationFacts(t *testing.T) {
	config := workloadObservationConfigMap(validWorkloadObservationFacts())
	resolver := newTestWorkloadObservationResolver(t, config)

	resolved, err := resolver.ResolveWorkloadObservation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resolved.Image, "@sha256:") ||
		resolved.Policy.SchemaVersion != profilingrun.AllowedBackgroundPolicySchemaVersion ||
		len(resolved.Policy.Selectors) != 1 {
		t.Fatalf("resolved workload observation = %#v", resolved)
	}
	digest, err := resolved.Policy.Digest()
	if err != nil || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("policy digest = %q, error = %v", digest, err)
	}
}

func TestKubernetesWorkloadObservationResolverRejectsUntrustedFacts(t *testing.T) {
	tests := []struct {
		name  string
		facts string
		match string
	}{
		{name: "mutable image", facts: strings.Replace(
			validWorkloadObservationFacts(), testWorkloadObserverImage, "registry.example/workload-observer:latest", 1,
		), match: "digest-pinned"},
		{name: "unknown field", facts: validWorkloadObservationFacts() + "unknown: true\n", match: "unknown"},
		{name: "duplicate selectors", facts: strings.Replace(
			validWorkloadObservationFacts(),
			"    - namespace: monitoring",
			"    - namespace: monitoring\n      controllerOwner:\n        kind: DaemonSet\n        name: edge-metrics-exporter\n      matchLabels:\n        app.kubernetes.io/component: edge-metrics\n    - namespace: monitoring",
			1,
		), match: "unique"},
		{name: "missing owner", facts: strings.Replace(
			validWorkloadObservationFacts(),
			"      controllerOwner:\n        kind: DaemonSet\n        name: edge-metrics-exporter\n",
			"",
			1,
		), match: "controller owner"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := newTestWorkloadObservationResolver(t, workloadObservationConfigMap(test.facts))
			_, err := resolver.ResolveWorkloadObservation(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("ResolveWorkloadObservation() error = %v", err)
			}
		})
	}
}

func newTestWorkloadObservationResolver(
	t *testing.T,
	config *corev1.ConfigMap,
) *KubernetesWorkloadObservationResolver {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config).Build()
	resolver, err := NewKubernetesWorkloadObservationResolver(reader, WorkloadObservationResolverOptions{
		Namespace: "chill-system", ConfigMapName: "chill-profiler-facts", ConfigMapKey: "workload-observation.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func workloadObservationConfigMap(facts string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "chill-system", Name: "chill-profiler-facts",
			UID: types.UID("facts-uid"), ResourceVersion: "10",
		},
		Data: map[string]string{"workload-observation.yaml": facts},
	}
}

const testWorkloadObserverImage = "registry.example/workload-observer@sha256:" +
	"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func validWorkloadObservationFacts() string {
	return `schemaVersion: chill.dacs.io/profiler-workload-observation.v1alpha1
image: ` + testWorkloadObserverImage + `
allowedBackground:
  schemaVersion: chill.dacs.io/allowed-background-policy.v1alpha1
  selectors:
    - namespace: monitoring
      controllerOwner:
        kind: DaemonSet
        name: edge-metrics-exporter
      matchLabels:
        app.kubernetes.io/component: edge-metrics
`
}
