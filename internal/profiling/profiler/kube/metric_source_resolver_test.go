package kube

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/metricobserver/edgemetrics"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testPowerEndpoint   = "http://central-meter.example.test:18766/prometheus?hostname=node-7"
	testThermalEndpoint = "http://edge-device-lattepanda.monitoring.svc.cluster.local:9102/prometheus"
	testPowerMetric     = "configured_wall_power_watts"
)

func TestKubernetesMetricSourceResolverResolvesAndOwnsCompositeNodeEntry(t *testing.T) {
	resolver := newMetricSourceResolver(t, validMetricSourceConfigMap())
	resolved, err := resolver.ResolveMetricSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}
	assertTestMetricSource(t, resolved)
	resolved.MetricLabels["device_type"] = "caller-mutation"

	again, err := resolver.ResolveMetricSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}
	assertTestMetricSource(t, again)
	if again.MetricLabels["device_type"] != "test_device" {
		t.Fatal("caller mutation changed subsequently resolved labels")
	}
}

func TestKubernetesMetricSourceResolverSelectsOnlyRequestedNodeEntry(t *testing.T) {
	base := validMetricSourceConfigMap()
	resolved, err := newMetricSourceResolver(t, base.DeepCopy()).ResolveMetricSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}

	unrelated := base.DeepCopy()
	unrelated.Data["metric-sources.yaml"] += `  - node: jetsonx
    kind: edge-metrics
    endpoint: http://central-meter.example.test:18766/prometheus?hostname=jetson-xavier
    powerMetric: shelly_power_total_watts
    metricLabels:
      hostname: jetson-xavier
      device_type: jetson_xavier
    thermalEndpoint: http://edge-device-jetsonx.monitoring.svc.cluster.local:9102/metrics
    thermalMetrics:
      observationAvailable: device_thermal_throttle_observation_available
      active: device_thermal_throttle_active
      cpuCoreThrottleEvents: device_thermal_cpu_core_throttle_event_count
      cpuPackageThrottleEvents: device_thermal_cpu_package_throttle_event_count
      latched: device_thermal_throttle_latched
      maximumTemperatureCelsius: device_thermal_max_celsius
`
	second, err := newMetricSourceResolver(t, unrelated).ResolveMetricSource(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}
	assertTestMetricSource(t, second)
	if second.Endpoint != resolved.Endpoint || second.ThermalEndpoint != resolved.ThermalEndpoint {
		t.Fatal("unrelated Node entry changed selected metric-source facts")
	}
}

func TestKubernetesMetricSourceResolverFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*corev1.ConfigMap)
		match  string
		is     error
	}{
		{name: "Node unavailable", mutate: func(*corev1.ConfigMap) {}, is: ErrMetricSourceUnavailable},
		{
			name: "retired predecessor schema",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], metricSourcesSchema,
					"chill.dacs.io/profiler-metric-sources.v1alpha4", 1,
				)
			},
			match: "unsupported Profiler metric-sources schema",
		},
		{
			name: "retired transitionPower field",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], "    thermalEndpoint:",
					"    transitionPower: {}\n    thermalEndpoint:", 1,
				)
			},
			match: `unknown field "transitionPower"`,
		},
		{
			name: "unsupported kind",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], edgemetrics.SourceKind, "Other", 1,
				)
			},
			match: "unsupported metric-source kind",
		},
		{
			name: "invalid power metric name",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], testPowerMetric, "not-valid", 1,
				)
			},
			match: "valid Prometheus metric name",
		},
		{
			name: "empty metric labels",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"],
					"    metricLabels:\n      hostname: node-7\n      device_type: test_device\n", "    metricLabels: {}\n", 1,
				)
			},
			match: "metrics.metricLabels is required",
		},
		{
			name: "invalid metric label name",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], "      hostname: node-7", "      not-valid: node-7", 1,
				)
			},
			match: `metricLabels name "not-valid" is invalid`,
		},
		{
			name: "power endpoint missing hostname query",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], "?hostname=node-7", "", 1,
				)
			},
			match: "exactly one hostname query value",
		},
		{
			name: "power endpoint has another query",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], "?hostname=node-7", "?hostname=node-7&scope=one", 1,
				)
			},
			match: "exactly one hostname query value",
		},
		{
			name: "power endpoint has duplicate hostname query",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], "?hostname=node-7",
					"?hostname=node-7&hostname=node-7", 1,
				)
			},
			match: "exactly one hostname query value",
		},
		{
			name: "power endpoint has empty hostname query",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], "?hostname=node-7", "?hostname=", 1,
				)
			},
			match: "hostname query value is required",
		},
		{
			name: "power endpoint has unclean hostname query",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], "?hostname=node-7", "?hostname=%20node-7", 1,
				)
			},
			match: "without surrounding whitespace",
		},
		{
			name: "power endpoint hostname mismatch",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], "?hostname=node-7", "?hostname=other", 1,
				)
			},
			match: "must match metricLabels.hostname",
		},
		{
			name: "thermal endpoint query",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], testThermalEndpoint, testThermalEndpoint+"?source=local", 1,
				)
			},
			match: "validate thermal endpoint: edge-metrics endpoint must not contain a query",
		},
		{
			name: "missing thermal endpoint",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], "    thermalEndpoint: "+testThermalEndpoint+"\n", "", 1,
				)
			},
			match: "validate thermal endpoint: endpoint is required",
		},
		{
			name: "duplicate typed metric",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] = strings.Replace(
					config.Data["metric-sources.yaml"], "configured_thermal_active", testPowerMetric, 1,
				)
			},
			match: "is duplicated",
		},
		{
			name: "duplicate Node",
			mutate: func(config *corev1.ConfigMap) {
				config.Data["metric-sources.yaml"] += strings.SplitN(
					config.Data["metric-sources.yaml"], "sources:\n", 2,
				)[1]
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
		Data: map[string]string{"metric-sources.yaml": `schemaVersion: chill.dacs.io/profiler-metric-sources.v1alpha5
sources:
  - node: lattepanda
    kind: edge-metrics
    endpoint: ` + testPowerEndpoint + `
    powerMetric: ` + testPowerMetric + `
    metricLabels:
      hostname: node-7
      device_type: test_device
    thermalEndpoint: ` + testThermalEndpoint + `
    thermalMetrics:
      observationAvailable: configured_thermal_observation_available
      active: configured_thermal_active
      cpuCoreThrottleEvents: configured_thermal_cpu_core_events_total
      cpuPackageThrottleEvents: configured_thermal_cpu_package_events_total
      latched: configured_thermal_latched
      maximumTemperatureCelsius: configured_thermal_max_celsius
`},
	}
}

func assertTestMetricSource(t *testing.T, resolved profiler.ResolvedMetricSource) {
	t.Helper()
	if resolved.Kind != edgemetrics.SourceKind || resolved.Endpoint != testPowerEndpoint ||
		resolved.PowerMetric != testPowerMetric || resolved.ThermalEndpoint != testThermalEndpoint ||
		resolved.ThermalMetrics != testThermalMetricContract() || len(resolved.MetricLabels) != 2 ||
		resolved.MetricLabels["hostname"] != "node-7" || resolved.MetricLabels["device_type"] != "test_device" {
		t.Fatalf("unexpected resolved metric source: %#v", resolved)
	}
}

func testThermalMetricContract() profilingrun.ThermalMetricContract {
	return profilingrun.ThermalMetricContract{
		ObservationAvailable:      "configured_thermal_observation_available",
		Active:                    "configured_thermal_active",
		CPUCoreThrottleEvents:     "configured_thermal_cpu_core_events_total",
		CPUPackageThrottleEvents:  "configured_thermal_cpu_package_events_total",
		Latched:                   "configured_thermal_latched",
		MaximumTemperatureCelsius: "configured_thermal_max_celsius",
	}
}
