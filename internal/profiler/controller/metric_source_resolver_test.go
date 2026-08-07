package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testPowerEndpoint = "http://edge-device-lattepanda.monitoring.svc.cluster.local:9102/metrics"

func TestKubernetesMetricSourceResolverResolvesNodeEntry(t *testing.T) {
	resolver := newMetricSourceResolver(t, validMetricSourceConfigMap())
	resolved, err := resolver.ResolveMetricSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Kind != edgeMetricsKind || resolved.Endpoint != testPowerEndpoint ||
		resolved.PowerMetric != "shelly_power_total_watts" {
		t.Fatalf("unexpected resolved metric source: %#v", resolved)
	}

	again, err := resolver.ResolveMetricSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}
	if again != resolved {
		t.Fatal("identical metric-source entry produced different resolved facts")
	}
}

func TestKubernetesMetricSourceResolverSelectsOnlyRequestedNodeEntry(t *testing.T) {
	base := validMetricSourceConfigMap()
	first := newMetricSourceResolver(t, base.DeepCopy())
	resolved, err := first.ResolveMetricSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}

	unrelated := base.DeepCopy()
	unrelated.Data["metric-sources.yaml"] += `  - node: jetsonx
    kind: edge-metrics
    endpoint: http://edge-device-jetsonx.monitoring.svc.cluster.local:9102/metrics
    powerMetric: shelly_power_total_watts
`
	second, err := newMetricSourceResolver(t, unrelated).ResolveMetricSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}
	if second != resolved {
		t.Fatal("unrelated Node entry changed selected metric-source facts")
	}

	changed := base.DeepCopy()
	changed.Data["metric-sources.yaml"] = strings.Replace(
		changed.Data["metric-sources.yaml"], "edge-device-lattepanda", "edge-device-lattepanda-v2", 1,
	)
	third, err := newMetricSourceResolver(t, changed).ResolveMetricSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}
	if third.Endpoint == resolved.Endpoint {
		t.Fatal("changed selected entry was not observed")
	}
}

func TestKubernetesMetricSourceResolverFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*corev1.ConfigMap)
		match  string
		is     error
	}{
		{
			name:   "Node unavailable",
			mutate: func(config *corev1.ConfigMap) {},
			is:     ErrMetricSourceUnavailable,
		},
		{
			name: "unsupported kind",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], edgeMetricsKind, "Other", 1,
				)
			},
			match: "unsupported metric-source kind",
		},
		{
			name: "wrong metric",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], "shelly_power_total_watts", "other", 1,
				)
			},
			match: "unsupported edge-metrics power metric",
		},
		{
			name: "endpoint credentials",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], "http://", "http://user:pass@", 1,
				)
			},
			match: "without credentials",
		},
		{
			name: "duplicate Node",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] += `  - node: lattepanda
    kind: edge-metrics
    endpoint: http://duplicate.monitoring.svc:9102/metrics
    powerMetric: shelly_power_total_watts
`
			},
			match: "duplicate metric source",
		},
		{
			name: "second YAML document",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] += "---\nschemaVersion: ignored\n"
			},
			match: "exactly one YAML document",
		},
		{
			name: "unknown field",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] += "unknown: value\n"
			},
			match: `unknown field "unknown"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validMetricSourceConfigMap()
			test.mutate(config)
			node := "lattepanda"
			if test.is != nil {
				node = "missing"
			}
			_, err := newMetricSourceResolver(t, config).ResolveMetricSource(context.Background(), node)
			if test.is != nil && !errors.Is(err, test.is) {
				t.Fatalf("expected %v, got %v", test.is, err)
			}
			if test.match != "" && (err == nil || !strings.Contains(err.Error(), test.match)) {
				t.Fatalf("expected error containing %q, got %v", test.match, err)
			}
		})
	}
}

func TestKubernetesMetricSourceResolverPreservesNotFound(t *testing.T) {
	resolver := newMetricSourceResolver(t)
	_, err := resolver.ResolveMetricSource(context.Background(), "lattepanda")
	if err == nil || !apierrors.IsNotFound(err) {
		t.Fatalf("expected ConfigMap NotFound cause, got %v", err)
	}
}

func TestNewKubernetesMetricSourceResolverValidatesDependencies(t *testing.T) {
	if _, err := NewKubernetesMetricSourceResolver(nil, metricSourceResolverOptions()); err == nil {
		t.Fatal("expected nil reader rejection")
	}
	options := metricSourceResolverOptions()
	options.ConfigMapKey = " "
	if _, err := NewKubernetesMetricSourceResolver(metricSourceFakeReader(t), options); err == nil {
		t.Fatal("expected invalid options rejection")
	}
}

func newMetricSourceResolver(t *testing.T, objects ...client.Object) *KubernetesMetricSourceResolver {
	t.Helper()
	resolver, err := NewKubernetesMetricSourceResolver(metricSourceFakeReader(t, objects...), metricSourceResolverOptions())
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func metricSourceFakeReader(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func metricSourceResolverOptions() MetricSourceResolverOptions {
	return MetricSourceResolverOptions{
		Namespace: "chill-system", ConfigMapName: "chill-profiler-facts", ConfigMapKey: "metric-sources.yaml",
	}
}

func validMetricSourceConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "chill-system", Name: "chill-profiler-facts",
			UID: types.UID("config-uid"), ResourceVersion: "30",
		},
		Data: map[string]string{"metric-sources.yaml": `schemaVersion: chill.dacs.io/profiler-metric-sources.v1alpha1
sources:
  - node: lattepanda
    kind: edge-metrics
    endpoint: ` + testPowerEndpoint + `
    powerMetric: shelly_power_total_watts
`},
	}
}
