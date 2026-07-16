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

func TestKubernetesPowerSourceResolverResolvesNodeEntry(t *testing.T) {
	resolver := newPowerSourceResolver(t, validPowerSourceConfigMap())
	resolved, err := resolver.ResolvePowerSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Kind != edgeMetricsKind || resolved.Endpoint != testPowerEndpoint ||
		resolved.Metric != "shelly_power_total_watts" ||
		!strings.HasPrefix(resolved.ConfigurationContentDigest, "sha256:") {
		t.Fatalf("unexpected resolved power source: %#v", resolved)
	}

	again, err := resolver.ResolvePowerSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}
	if again != resolved {
		t.Fatal("identical power-source entry produced different resolved facts")
	}
}

func TestKubernetesPowerSourceResolverDigestTracksSelectedEntryOnly(t *testing.T) {
	base := validPowerSourceConfigMap()
	first := newPowerSourceResolver(t, base.DeepCopy())
	resolved, err := first.ResolvePowerSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}

	unrelated := base.DeepCopy()
	unrelated.Data["power-sources.yaml"] += `  - node: jetsonx
    kind: edge-metrics
    endpoint: http://edge-device-jetsonx.monitoring.svc.cluster.local:9102/metrics
    metric: shelly_power_total_watts
`
	second, err := newPowerSourceResolver(t, unrelated).ResolvePowerSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}
	if second.ConfigurationContentDigest != resolved.ConfigurationContentDigest {
		t.Fatal("unrelated Node entry changed selected power-source identity")
	}

	changed := base.DeepCopy()
	changed.Data["power-sources.yaml"] = strings.Replace(
		changed.Data["power-sources.yaml"], "edge-device-lattepanda", "edge-device-lattepanda-v2", 1,
	)
	third, err := newPowerSourceResolver(t, changed).ResolvePowerSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}
	if third.ConfigurationContentDigest == resolved.ConfigurationContentDigest {
		t.Fatal("changed selected entry retained previous identity")
	}
}

func TestKubernetesPowerSourceResolverFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*corev1.ConfigMap)
		match  string
		is     error
	}{
		{
			name:   "Node unavailable",
			mutate: func(config *corev1.ConfigMap) {},
			is:     ErrPowerSourceUnavailable,
		},
		{
			name: "unsupported kind",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["power-sources.yaml"] = strings.Replace(
					config.Data["power-sources.yaml"], edgeMetricsKind, "Other", 1,
				)
			},
			match: "unsupported power-source kind",
		},
		{
			name: "wrong metric",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["power-sources.yaml"] = strings.Replace(
					config.Data["power-sources.yaml"], "shelly_power_total_watts", "other", 1,
				)
			},
			match: "unsupported edge-metrics metric",
		},
		{
			name: "endpoint credentials",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["power-sources.yaml"] = strings.Replace(
					config.Data["power-sources.yaml"], "http://", "http://user:pass@", 1,
				)
			},
			match: "without credentials",
		},
		{
			name: "duplicate Node",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["power-sources.yaml"] += `  - node: lattepanda
    kind: edge-metrics
    endpoint: http://duplicate.monitoring.svc:9102/metrics
    metric: shelly_power_total_watts
`
			},
			match: "duplicate power source",
		},
		{
			name: "second YAML document",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["power-sources.yaml"] += "---\nschemaVersion: ignored\n"
			},
			match: "exactly one YAML document",
		},
		{
			name: "unknown field",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["power-sources.yaml"] += "unknown: value\n"
			},
			match: `unknown field "unknown"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validPowerSourceConfigMap()
			test.mutate(config)
			node := "lattepanda"
			if test.is != nil {
				node = "missing"
			}
			_, err := newPowerSourceResolver(t, config).ResolvePowerSource(context.Background(), node)
			if test.is != nil && !errors.Is(err, test.is) {
				t.Fatalf("expected %v, got %v", test.is, err)
			}
			if test.match != "" && (err == nil || !strings.Contains(err.Error(), test.match)) {
				t.Fatalf("expected error containing %q, got %v", test.match, err)
			}
		})
	}
}

func TestKubernetesPowerSourceResolverPreservesNotFound(t *testing.T) {
	resolver := newPowerSourceResolver(t)
	_, err := resolver.ResolvePowerSource(context.Background(), "lattepanda")
	if err == nil || !apierrors.IsNotFound(err) {
		t.Fatalf("expected ConfigMap NotFound cause, got %v", err)
	}
}

func TestNewKubernetesPowerSourceResolverValidatesDependencies(t *testing.T) {
	if _, err := NewKubernetesPowerSourceResolver(nil, powerSourceResolverOptions()); err == nil {
		t.Fatal("expected nil reader rejection")
	}
	options := powerSourceResolverOptions()
	options.ConfigMapKey = " "
	if _, err := NewKubernetesPowerSourceResolver(powerSourceFakeReader(t), options); err == nil {
		t.Fatal("expected invalid options rejection")
	}
}

func newPowerSourceResolver(t *testing.T, objects ...client.Object) *KubernetesPowerSourceResolver {
	t.Helper()
	resolver, err := NewKubernetesPowerSourceResolver(powerSourceFakeReader(t, objects...), powerSourceResolverOptions())
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func powerSourceFakeReader(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func powerSourceResolverOptions() PowerSourceResolverOptions {
	return PowerSourceResolverOptions{
		Namespace: "chill-system", ConfigMapName: "chill-profiler-facts", ConfigMapKey: "power-sources.yaml",
	}
}

func validPowerSourceConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "chill-system", Name: "chill-profiler-facts",
			UID: types.UID("config-uid"), ResourceVersion: "30",
		},
		Data: map[string]string{"power-sources.yaml": `schemaVersion: chill.dacs.io/profiler-power-sources.v1alpha1
sources:
  - node: lattepanda
    kind: edge-metrics
    endpoint: ` + testPowerEndpoint + `
    metric: shelly_power_total_watts
`},
	}
}
